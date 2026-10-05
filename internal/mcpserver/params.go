package mcpserver

import (
	"context"
	"fmt"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/gitlab"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/instances"
)

// Shared input structs for the tools. The jsonschema tag is surfaced to the LLM
// as the parameter documentation, so it carries the operational nuance.

type instanceParam struct {
	Instance string `json:"instance,omitempty" jsonschema:"the configured GitLab instance to target; defaults to the server's default instance when omitted"`
}

// metricInstance reports the requested instance for metric labels; promoted to
// every input struct that embeds instanceParam.
func (p instanceParam) metricInstance() string { return p.Instance }

type projectParam struct {
	instanceParam
	Project string `json:"project" jsonschema:"the GitLab project: either its numeric id or its full path such as 'group/subgroup/project'"`
}

type limitParam struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of results (default 50, maximum 200)"`
}

// resolvedLimit clamps a caller-supplied limit into a sane range.
func (p limitParam) resolvedLimit() int {
	switch {
	case p.Limit <= 0:
		return 50
	case p.Limit > 200:
		return 200
	default:
		return p.Limit
	}
}

// resolveInstance picks the target instance from the argument or the default.
func (s *Server) resolveInstance(name string) (*instances.Instance, error) {
	return s.reg.Get(name)
}

// resolveProject turns a caller-supplied project reference into its canonical
// full path and applies the namespace guard. Every project-addressing tool goes
// through this, so the allowlist cannot be bypassed by passing a numeric id.
func (s *Server) resolveProject(ctx context.Context, in *instances.Instance, ref string) (*gitlab.Project, error) {
	if ref == "" {
		return nil, fmt.Errorf("project is required: pass a numeric id or a full path like 'group/project'")
	}
	p, err := in.Client.Project(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := in.Guard.AllowNamespace(p.PathWithNamespace); err != nil {
		return nil, err
	}
	return p, nil
}

// resolveGroup applies the namespace guard to a group reference.
func (s *Server) resolveGroup(ctx context.Context, in *instances.Instance, ref string) (*gitlab.Group, error) {
	g, err := in.Client.Group(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := in.Guard.AllowNamespace(g.FullPath); err != nil {
		return nil, err
	}
	return g, nil
}

// initiator returns the human the MCP is acting for, taken from the verified
// agent token. GitLab attributes every action to the service account behind the
// PAT, so this log line is the only place the real identity is recorded.
func initiator(ctx context.Context) string {
	if info := sdkauth.TokenInfoFromContext(ctx); info != nil && info.UserID != "" {
		return "gitlab-mcp:" + info.UserID
	}
	return "gitlab-mcp"
}
