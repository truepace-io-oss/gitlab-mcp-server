package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/perm"
)

// notThisServer is appended to tool descriptions so the model routes
// infrastructure questions elsewhere instead of guessing with GitLab tools.
const notThisServer = " This server covers GitLab only: for Kubernetes, Argo CD, metrics, logs or alerts use the server dedicated to those."

func (s *Server) registerMetaTools(m *mcp.Server) {
	addTool(m, s, "instances_list",
		"List the GitLab instances this MCP manages, with reachability, GitLab version, the capabilities that are enabled, the access token's name/scopes/expiry and the namespaces each instance is bounded to. Call this first when unsure what the server may do."+notThisServer,
		s.instancesList)
	addTool(m, s, "project_resolve",
		"Resolve a project reference to its canonical GitLab project. Accepts a numeric id, a full path ('group/project'), or a git remote URL (ssh or https) — so an agent can map its own local checkout to the right project. Returns id, full path, default branch, visibility and web URL.",
		s.projectResolve)
}

func (s *Server) instancesList(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	var b strings.Builder
	b.WriteString("Managed GitLab instances:\n")
	def := s.reg.DefaultName()

	for _, in := range s.reg.All() {
		marker := ""
		if in.Name == def {
			marker = " (default)"
		}
		status, err := in.Ping(ctx)
		if err != nil {
			status = "UNREACHABLE: " + err.Error()
		}
		fmt.Fprintf(&b, "- %s%s url=%s readOnly=%t — %s\n", in.Name, marker, in.URL, in.ReadOnly, status)

		caps := in.Guard.EffectiveList()
		names := make([]string, 0, len(caps))
		for _, c := range caps {
			names = append(names, string(c))
		}
		if len(names) == 0 {
			b.WriteString("    capabilities: (none enabled — every tool will refuse)\n")
		} else {
			fmt.Fprintf(&b, "    capabilities: %s\n", strings.Join(names, ", "))
		}
		// Name the disabled ones too: an agent that knows what is off will not
		// keep retrying, and a human can see what to flip.
		var off []string
		for _, c := range perm.All() {
			if !in.Guard.Enabled(c) {
				off = append(off, string(c))
			}
		}
		if len(off) > 0 {
			fmt.Fprintf(&b, "    disabled:     %s\n", strings.Join(off, ", "))
		}
		if in.Guard.AllowVariables() {
			b.WriteString("    CI variables may be supplied when starting pipelines / playing jobs\n")
		} else {
			b.WriteString("    CI variables may NOT be supplied to pipelines or jobs\n")
		}

		if ns := in.Guard.AllowedNamespaces(); len(ns) > 0 {
			fmt.Fprintf(&b, "    namespaces:   %s\n", strings.Join(ns, ", "))
		} else {
			b.WriteString("    namespaces:   (unrestricted — every project the token can see)\n")
		}

		if err == nil {
			if tok, terr := in.TokenInfo(ctx); terr == nil {
				exp := tok.ExpiresAt
				if exp == "" {
					exp = "never"
				}
				fmt.Fprintf(&b, "    token:        name=%q scopes=[%s] expires=%s active=%t\n",
					tok.Name, strings.Join(tok.Scopes, " "), exp, tok.Active)
				if w := tok.Warning(); w != "" {
					fmt.Fprintf(&b, "    %s\n", w)
				}
			} else {
				fmt.Fprintf(&b, "    token:        could not introspect (%s)\n", terr.Error())
			}
		}
	}

	b.WriteString("\nPipelines are the only write surface. Secret-bearing endpoints (CI/CD variables, " +
		"pipeline variables, secure files, tokens, deploy keys, webhooks, integrations) are permanently " +
		"blocked and cannot be enabled by configuration.\n")
	return textResult(b.String()), nil, nil
}

type projectResolveParam struct {
	instanceParam
	Project string `json:"project" jsonschema:"a numeric project id, a full path like 'group/project', or a git remote URL such as git@gitlab.example.com:group/project.git or https://gitlab.example.com/group/project.git"`
}

func (s *Server) projectResolve(ctx context.Context, _ *mcp.CallToolRequest, in projectResolveParam) (*mcp.CallToolResult, any, error) {
	inst, err := s.resolveInstance(in.Instance)
	if err != nil {
		return errorResult(err), nil, nil
	}
	ref := normalizeProjectRef(in.Project)
	p, err := s.resolveProject(ctx, inst, ref)
	if err != nil {
		return errorResult(err), nil, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Project %s\n", p.PathWithNamespace)
	fmt.Fprintf(&b, "  id:             %d\n", p.ID)
	fmt.Fprintf(&b, "  default branch: %s\n", dash(p.DefaultBranch))
	fmt.Fprintf(&b, "  visibility:     %s\n", dash(p.Visibility))
	fmt.Fprintf(&b, "  archived:       %t\n", p.Archived)
	fmt.Fprintf(&b, "  namespace:      %s (%s)\n", dash(p.Namespace.FullPath), dash(p.Namespace.Kind))
	fmt.Fprintf(&b, "  last activity:  %s (%s)\n", dash(p.LastActivityAt), age(p.LastActivityAt))
	if p.WebURL != "" {
		fmt.Fprintf(&b, "  url:            %s\n", p.WebURL)
	}
	if p.Description != "" {
		fmt.Fprintf(&b, "  description:    %s\n", truncate(p.Description, maxMessageLen))
	}
	return textResult(b.String()), nil, nil
}

// normalizeProjectRef accepts a git remote URL as well as a path or id, so an
// agent can hand over whatever `git remote get-url origin` printed.
func normalizeProjectRef(ref string) string {
	r := strings.TrimSpace(ref)
	r = strings.TrimSuffix(r, ".git")

	// scp-style: git@host:group/project
	if i := strings.Index(r, "@"); i >= 0 && !strings.Contains(r, "://") {
		if j := strings.Index(r, ":"); j > i {
			return strings.Trim(r[j+1:], "/")
		}
	}
	// URL form: https://host/group/project or ssh://git@host/group/project
	if i := strings.Index(r, "://"); i >= 0 {
		rest := r[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return strings.Trim(rest[j+1:], "/")
		}
	}
	return strings.Trim(r, "/")
}
