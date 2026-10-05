# gitlab-mcp

An [MCP](https://modelcontextprotocol.io) server that lets an AI agent **read a
GitLab installation and operate its pipelines**.

The scope is deliberate: **pipelines are the only write surface.** Projects,
repositories, issues and merge requests are read-only, and the endpoints that
carry credentials — CI/CD variables, pipeline variables, secure files, access and
deploy tokens, webhooks, integrations, runner tokens — are blocked in code and
cannot be enabled by configuration.

## What it can do

**Read (`read.core`)** — groups, projects, repository tree and files, branches,
tags, commits, diffs, ref comparison, code and issue search, issues, merge
requests and their diffs, members.

**Read CI (`read.ci`)** — pipelines, jobs, job logs, test reports, schedules,
waiting for a pipeline to finish, and `ci_lint`.

**Operate pipelines (`pipelines.operate`)** — create, retry, cancel, rename;
retry, cancel and play individual jobs; run a schedule immediately.

**Delete a pipeline (`pipelines.delete`)** — off by default. It needs the Owner
role at GitLab and is irreversible.

`ci_lint` deserves a word: it validates an **uncommitted** `.gitlab-ci.yml` in
real project context, with GitLab resolving `include:`, project variables and
`extends:` server-side. That cannot be reproduced locally, which is why it is the
one tool that accepts file content.

## What it will not do

No repository writes. No project create/update/delete. No issue or merge-request
authoring. No membership or permission changes. No access to any secret-bearing
endpoint. No scanning of any kind. No filesystem access, and no transport other
than HTTP. Every endpoint it uses is GitLab Free tier.

See [docs/permissions.md](docs/permissions.md) for the full enforcement model.

## Quick start

```bash
# Smallest possible run: read-only against gitlab.com, no config file.
GMCP_TOKEN=glpat-… go run .

# Or with a config file:
cp examples/config.yaml config.yaml   # then edit it
go run . --config config.yaml
```

Add it to an MCP client (see [examples/mcp.claude.json](examples/mcp.claude.json)):

```json
{
  "mcpServers": {
    "gitlab": {
      "type": "http",
      "url": "https://gitlab-mcp.example.com/mcp"
    }
  }
}
```

Then ask the agent to call `instances_list` first — it reports what the server
can reach, which capabilities are on, and when the token expires.

## Configuration

`examples/config.yaml` is commented end to end. The shape:

```yaml
readOnly: false            # global kill-switch for every mutating tool
defaultInstance: gitlab
instances:
  - name: gitlab
    url: https://gitlab.example.com
    tokenFile: /etc/gmcp/instances/gitlab/token   # re-read per request
    allowedNamespaces: ["example-group"]          # bounds every tool
    permissions:
      read:      { core: true, ci: true }
      pipelines: { operate: true, delete: false, allowVariables: false }
auth:
  enabled: false           # see docs/auth.md before exposing this
```

Environment overrides: `GMCP_CONFIG`, `GMCP_LISTEN_ADDR`, `GMCP_METRICS_ADDR`,
`GMCP_LOG_LEVEL`, `GMCP_READ_ONLY`, `GMCP_DEFAULT_INSTANCE`, `GMCP_AUTH_ENABLED`,
`GMCP_AUTH_STATIC_TOKEN`, `GMCP_AUTH_OIDC_ISSUER`, `GMCP_AUTH_OIDC_AUDIENCE`,
`GMCP_AUTH_OIDC_RESOURCE`, and `GMCP_TOKEN` / `GMCP_URL` for the zero-config case.

## Endpoints

| Path | Purpose |
|---|---|
| `/mcp` | the MCP streamable-HTTP transport (the only authenticated path) |
| `/healthz` | liveness; always 200 |
| `/readyz` | readiness; probes the default instance |
| `/.well-known/oauth-protected-resource` | RFC 9728 metadata, when OIDC is on |
| `:9091/metrics` | Prometheus, on a **separate unauthenticated port** |

## Deploying

A Helm chart lives in [`deploy/helm/gitlab-mcp`](deploy/helm/gitlab-mcp). It
mounts the GitLab token from a Secret — optionally managed by the External
Secrets Operator — runs as nonroot with a read-only root filesystem, and can
ship a ServiceMonitor and a Grafana dashboard.

```bash
helm install gitlab-mcp deploy/helm/gitlab-mcp \
  --set 'instances[0].name=gitlab' \
  --set 'instances[0].url=https://gitlab.example.com' \
  --set 'instances[0].token.existingSecret=gitlab-mcp-token' \
  --set 'instances[0].allowedNamespaces[0]=example-group' \
  --set 'instances[0].permissions.read.core=true' \
  --set 'instances[0].permissions.read.ci=true' \
  --set 'instances[0].permissions.pipelines.operate=true' \
  --set 'config.defaultInstance=gitlab'
```

## Documentation

| Document | Contents |
|---|---|
| [docs/permissions.md](docs/permissions.md) | the five enforcement layers, the deny list, redaction, non-goals |
| [docs/token-setup.md](docs/token-setup.md) | choosing the GitLab token's scope and role, plus a verification script |
| [docs/auth.md](docs/auth.md) | agent authentication; why `audience` and `resource` differ |
| [docs/architecture.md](docs/architecture.md) | the request path and package layout |
| [docs/metrics.md](docs/metrics.md) | every metric, with queries and alerts |

## Development

```bash
make build        # bin/gitlab-mcp
make test         # unit tests, no GitLab needed
make test-e2e     # the real server + a fake GitLab, over the real transport
make lint         # go vet + gofmt
make helm-lint    # helm lint
```

The end-to-end tests drive the real MCP server through the official MCP client
against a fake GitLab that records every request. That recorder is what makes the
security tests meaningful: they assert the bytes never left the process, not
merely that a tool returned an error.

## License

Apache 2.0 — see [LICENSE](LICENSE).
