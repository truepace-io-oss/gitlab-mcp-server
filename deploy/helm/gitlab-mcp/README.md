# gitlab-mcp Helm chart

Deploys the [gitlab-mcp](https://github.com/truepace-io-oss/gitlab-mcp-server)
server: an MCP server that lets an AI agent read a GitLab installation and
operate its pipelines.

The GitLab access token is mounted from a Secret — either one you create, or one
the External Secrets Operator (ESO) pulls from your secret backend (Vault, a
cloud secrets manager, …).

## Install

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

The referenced Secret must contain a key `token` holding the GitLab personal
access token.

## What gets created

| Resource | When |
|---|---|
| `Deployment`, `Service`, `ServiceAccount` | always |
| `ConfigMap` (`config.yaml`) | always — server and instance registry, no secrets |
| `ExternalSecret` (per instance) | `externalSecrets.enabled` and the instance sets `token.esoRef` or `tls.caEsoRef` |
| `ExternalSecret` (per static auth token) | `auth.static` tokens with an `eso.ref` |
| `Ingress` | `ingress.enabled` |
| `ServiceMonitor` | `serviceMonitor.enabled` (requires `metrics.enabled`) |
| Grafana dashboard `ConfigMap` | `grafanaDashboard.enabled` |

## Configuring instances

Each entry in `instances` becomes one GitLab installation the server can reach.

```yaml
config:
  defaultInstance: gitlab

instances:
  - name: gitlab
    url: https://gitlab.example.com
    timeout: "30s"
    readOnly: false
    # Bounds every project- and group-addressing tool. Leaving this empty lets
    # the server reach everything the token can see.
    allowedNamespaces: ["example-group"]
    token:
      # Exactly one of these three:
      esoRef: <secret-backend-key>   # ESO creates the Secret, key `token`
      existingSecret: ""             # a Secret you manage, key `token`
      inline: ""                     # discouraged: not rotatable without a restart
    permissions:
      read:
        core: true                   # projects, repos, search, issues, merge requests
        ci: true                     # pipelines, jobs, job logs, schedules, ci_lint
      pipelines:
        operate: true                # create/update/retry/cancel + job and schedule control
        delete: false                # needs Owner at GitLab and is irreversible
        allowVariables: false        # supplying CI variables injects run behaviour
    pagination: { perPage: 100, maxPages: 10 }
    rateLimit: { minRemaining: 50 }
    # For a self-managed GitLab behind a private CA:
    tls: { caEsoRef: "" }
```

Secrets are mounted at `/etc/gmcp/instances/<name>/{token,ca.crt}` and re-read on
every request, so rotating the Secret needs no restart.

## Agent authentication

Independent of the GitLab token: this decides who may call the MCP.

```yaml
auth:
  enabled: true
  oidc:
    enabled: true
    issuer: "https://auth.example.com/application/o/gitlab-mcp/"
    audience: "gitlab-mcp"                            # expected token `aud`
    resource: "https://gitlab-mcp.example.com/mcp"    # RFC 9728 identifier
    requiredGroups: ["gitlab-mcp-users"]
```

**`resource` is effectively required** when `resourceMetadata` is true: it is the
server's canonical public URL, and it is deliberately *not* the same as
`audience`. An empty value falls back to the audience, which is usually not a URI
— and the server then refuses to start. See the project's `docs/auth.md`.

Static bearer tokens work alongside OIDC:

```yaml
auth:
  enabled: true
  static:
    enabled: true
    tokens:
      - name: ci
        eso:
          ref: <secret-backend-key>   # ESO creates the Secret, key `token`
      # - name: legacy
      #   existingSecret: my-secret   # must contain key `token`
```

## Exposure

The MCP transport has no built-in auth unless you enable it. With `auth.enabled:
false`, only expose the service on an internal ingress or not at all.

The ingress defaults carry the annotations streamable HTTP needs (long timeouts,
no buffering). Metrics are served on a **separate, unauthenticated port** and
must never be routed through the ingress.

## Values

See [values.yaml](values.yaml) — every key is commented.
