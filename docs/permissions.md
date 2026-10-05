# Permissions

This server is deliberately narrow. **Pipelines are the only write surface**;
everything else is read-only, and a set of endpoints can never be reached at all.

Five independent layers decide what a call can do. A request must pass every one.

## 1. Who may call the MCP

`internal/auth` — static bearer tokens and/or OIDC access tokens. Only `/mcp` is
gated; `/healthz`, `/readyz` and `/.well-known/oauth-protected-resource` stay
open. See [auth.md](auth.md).

## 2. Which namespaces are reachable

`instance.allowedNamespaces` bounds every project- and group-addressing tool.
Matching is on **path-segment boundaries**, so an entry `team` permits
`team/app` but not `teamwork/app`.

Two consequences worth knowing:

- `projects_list` **filters** its results, so a listing never advertises a
  project the other tools would refuse.
- `search` **requires** a `group` or `project` when the instance is bounded; an
  unscoped search would otherwise reach every namespace the token can see.

An empty allowlist means "anything the token can see" and emits a startup warning.

## 3. What the MCP may do — the capability allowlist

Exactly four capabilities. Each tool declares one, checked before a request is built.

| Capability | Covers |
|---|---|
| `read.core` | projects, groups, members, repository browsing, search, issues, merge requests |
| `read.ci` | pipelines, jobs, job logs, test reports, schedules, and `ci_lint` |
| `pipelines.operate` | pipeline create / update / retry / cancel, job retry / cancel / play, schedule play |
| `pipelines.delete` | `DELETE /projects/:id/pipelines/:id` only |

Two sub-switches narrow the write surface further:

- **`pipelines.allowVariables`** (default `false`) — supplying CI variables when
  starting a pipeline or playing a job. Variables change what the run *does*, so
  this is opt-in.
- **`pipelines.delete`** (default `false`) — deletion needs the **Owner** role at
  GitLab and is **irreversible**: unlike a project, a deleted pipeline has no
  restore endpoint. Leaving it off also lets the service account stay at
  Maintainer. The tool additionally requires `confirmPipelineId` to repeat the
  target id.

**Tools are always registered, even when their capability is off.** A disabled
capability returns a tool *error* naming it:

```
capability "pipelines.delete" is not enabled for GitLab instance "gitlab"
```

A denied tool with an explanation is more debuggable than a silently missing one.

## 4. What GitLab will allow

The access token is the authoritative gate. Give it the least privilege that
covers the capabilities you enabled — see [token-setup.md](token-setup.md).

## 5. What can never be requested — the deny list

`internal/gitlab/denylist.go` lists path patterns the HTTP client refuses to
send, regardless of configuration, capability or token. It is checked
immediately before every request, so no future tool can bypass it.

| Denied | Why |
|---|---|
| `/projects/:id/variables*`, `/groups/:id/variables*`, `/admin/ci/variables*` | CI/CD variables are pipeline secrets |
| `/projects/:id/pipelines/:id/variables` | pipeline variables are pipeline secrets |
| `/projects/:id/secure_files*` | certificates and keystores |
| `/projects/:id/access_tokens*`, `/groups/:id/access_tokens*`, `/personal_access_tokens*` | credential material (the single exception is `…/self`, which reads this server's own token metadata and exposes no secret) |
| `/projects/:id/deploy_tokens*`, `…/deploy_keys*` | long-lived repository credentials |
| `/projects/:id/hooks*`, `/groups/:id/hooks*` | webhook URLs and secret tokens |
| `/projects/:id/integrations*`, `…/services*` | third-party credentials |
| `…/cluster_agents/:id/tokens*`, `…/reset_registration_token`, `…/reset_authentication_token` | runner and agent tokens |
| every job-artifact endpoint | out of scope: this server does no scanning |
| `…/jobs/:id/erase` | destroys artifacts and logs |

Denial is **method-agnostic**: reading a secret is as unacceptable as writing one.

## 6. What the model may see — redaction

Even reachable responses can embed credentials. `internal/gitlab/redact.go`
rewrites every payload before it is rendered:

- JSON keys matching `variable|secret|token|password|passphrase|private_key|credential|authorization|x-api-key` → `<redacted>`.
- Credential-shaped substrings in free text (`glpat-`, `glrt-`, `gldt-`, JWTs,
  PEM private-key headers, AWS key ids) → `<redacted>`.

This covers the two realistic leaks: a pipeline-schedule response carrying a
`variables` array, and a **job log** containing a token an author forgot to mask.
Log redaction is best effort — fix the pipeline's masking if a secret appears.

## 7. Kill switches

- `readOnly: true` at the top level — blocks every mutating tool.
- `instance.readOnly: true` — the same, for one instance.

Both leave reads working, so an operator can freeze mutations without losing
visibility.

## 8. Auditability

`gmcp_operations_total{instance,op,result}` counts every mutation, and each one
is logged with an `initiator` field taken from the verified agent token.

**GitLab attributes every action to the service account behind the token**, so
the MCP's own log is the only place the human identity is recorded. Keep it.

## Explicit non-goals

Never implement these here:

- Repository writes — commits, file changes, branches, tags, merges. **Including
  editing `.gitlab-ci.yml`**: `ci_lint` validates a config the agent holds, it
  never writes one back.
- Project writes — create, update, delete, transfer, archive, visibility.
- Issue and merge-request authoring — create, comment, approve, merge.
- Membership, role, protected-branch or approval-rule changes.
- Any read or write of CI/CD variables, pipeline variables, schedule variables,
  secure files, tokens, deploy keys, webhooks, integrations, runner tokens.
- Group or user CRUD, admin endpoints, `sudo`.
- Scanning of any kind — no secret detection, no vulnerability or dependency
  reporting, no artifact reading. The agent scans its own files; GitLab's CI
  secret detection and security features cover the platform side; `job_log`
  reaches a CI scanner's output when an agent needs it.
- Any filesystem access or transport other than HTTP. The only files this process
  opens are its own config and token files.
- Premium/Ultimate-only APIs. Every endpoint used is Free tier, so the
  deployment cannot break on a licence change.
