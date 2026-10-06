# GitLab access token setup

The token is the authoritative gate: the MCP's capability allowlist can only ever
*narrow* what the token permits, never widen it. Give it the least privilege that
covers the capabilities you enable.

## Use a dedicated service account

Create a separate GitLab user for the server rather than using a human account:

- its actions are distinguishable in GitLab's audit trail;
- revoking it does not lock a person out;
- its role caps every token it will ever hold.

Invite it to the groups the server should reach, with the **lowest role that
works**:

| You enabled | Minimum role |
|---|---|
| reads only | **Reporter** (Guest cannot see pipelines on all tiers) |
| `pipelines.operate` | **Developer** — can run, retry and cancel pipelines |
| `pipelines.delete` | **Owner** — GitLab restricts pipeline deletion to Owner |

**Do not grant Owner "just in case."** A token can never exceed its owner's role,
but it always inherits that ceiling, so an Owner account raises the blast radius
of every token it holds. If you do not actually need `pipelines.delete`, leave it
off and keep the account at Developer or Maintainer.

## Exactly what the server calls

This is derived from `internal/gitlab/api.go`, not from guesswork. With the
recommended profile (`read.core`, `read.ci`, `pipelines.operate`;
`pipelines.delete` off, `allowVariables` off) the server issues these requests
and **nothing else**:

**Reads (GET)**

```
/version                                    /projects/:id/repository/tree
/personal_access_tokens/self                /projects/:id/repository/files/:path/raw
/groups        /groups/:id                  /projects/:id/repository/branches
/projects      /projects/:id                /projects/:id/repository/tags
/groups/:id/projects                        /projects/:id/repository/commits[/:sha[/diff]]
/search  /groups/:id/search                 /projects/:id/repository/compare
/projects/:id/search                        /projects/:id/members/all
/issues  /groups/:id/issues                 /groups/:id/members
/projects/:id/issues[/:iid[/notes]]         /projects/:id/pipelines[/:id|/latest]
/merge_requests                             /projects/:id/pipelines/:id/jobs
/groups/:id/merge_requests                  /projects/:id/pipelines/:id/test_report_summary
/projects/:id/merge_requests[/:iid]         /projects/:id/pipeline_schedules
/projects/:id/merge_requests/:iid/diffs     /projects/:id/jobs/:id
/projects/:id/merge_requests/:iid/notes     /projects/:id/jobs/:id/trace
```

**Writes — pipelines only**

```
POST   /projects/:id/ci/lint                     (validation; mutates nothing)
POST   /projects/:id/pipeline                    start a pipeline
POST   /projects/:id/pipelines/:id/retry
POST   /projects/:id/pipelines/:id/cancel
PUT    /projects/:id/pipelines/:id/metadata      set the pipeline name
POST   /projects/:id/jobs/:id/retry
POST   /projects/:id/jobs/:id/cancel
POST   /projects/:id/jobs/:id/play
POST   /projects/:id/pipeline_schedules/:id/play
```

**Only if you enable `pipelines.delete`** (off by default):
`DELETE /projects/:id/pipelines/:id` — this is the one call that requires Owner.

## Classic personal access token

If you are creating an ordinary PAT, tick **exactly one scope**:

| Enabled capabilities | Scope |
|---|---|
| reads only (`read.core`, `read.ci`) | **`read_api`** |
| anything including `pipelines.operate` | **`api`** |

There is no middle ground, and this is worth understanding rather than working
around: `read_api` cannot POST to a pipeline, and `api` is full read **and** write
across everything the token's owner can reach — including every CI/CD variable.

So with a classic `api` token, the promise that pipeline secrets are unreachable
rests on **this server's deny list**, not on GitLab. The deny list is
unconditional and covered by tests that assert no request is ever sent, but it is
one layer instead of two. Prefer a fine-grained token if your instance offers one.

**Do not tick** `write_repository`, `read_registry`, `write_registry`,
`create_runner`, `manage_runner`, `k8s_proxy`, `ai_features`, `sudo` or
`admin_mode`. The server never calls anything they cover.

One optional addition: `read_user`. It is only needed if
`GET /personal_access_tokens/self` is refused on your instance — that call powers
the token name/scope/expiry line in `instances_list`. It degrades gracefully
("could not introspect"), so leave it off unless you want that line.

## Fine-grained personal access token

If your instance offers fine-grained tokens, use one — it turns the guarantee
above from one layer into two. Set the **boundary** to the group(s) the server
should reach, then grant:

| Resource | Grant | Why |
|---|---|---|
| Metadata (Instance) | **Read** | `/version` reachability and version probe |
| Pipelines | **Create, Read, Update** | start, retry, cancel, rename |
| Jobs | **Read, Update** | retry, cancel, play, and read job logs |
| Projects | **Read** | project metadata, `project_resolve` |
| Repositories / Code | **Read** | tree, file contents, branches, tags, commits, compare |
| Issues | **Read** | `issues_list`, `issue_get` |
| Merge requests | **Read** | `mrs_list`, `mr_get`, `mr_diff` |
| Notes / Comments | **Read** | the comment lists on issues and MRs |
| Members | **Read** | `members_list` |
| Groups | **Read** | `groups_list`, `group_get` |
| Pipeline schedules | **Read, Update** | list, and play a schedule |

Optionally grant **Read** for **Personal access token** at the User boundary if
you want `instances_list` to report the token's name, permissions and expiry.
Without it, token introspection degrades gracefully.

Grant **Delete** on Pipelines **only** if you set `pipelines.delete: true`.

Leave these at **nothing** — the server never touches them, and they are the
reason it exists in this shape:

> CI/CD variables · Secure files · Access tokens · Deploy tokens · Deploy keys ·
> Webhooks · Integrations · Runners · Container registry · Packages ·
> Environments (write) · Releases (write) · Protected branches · Members (write) ·
> Repositories (write) · Issues (write) · Merge requests (write) · Groups (write) ·
> Projects (Create / Update / Delete)

## Role

The token can never exceed its owner's role, so the role is a ceiling.

| You enabled | Minimum role |
|---|---|
| reads only | **Reporter** — Guest cannot see pipelines or job logs in private projects |
| `pipelines.operate` | **Developer** in theory, **Maintainer** in practice (see below) |
| `pipelines.delete` | **Owner** |

**Why Maintainer rather than Developer.** Starting a pipeline on a *protected*
branch requires the user to be allowed to push or merge to that branch. If `main`
is protected with "Maintainers only" — the common setup — a Developer-role token
gets a 403 when an agent tries to run a pipeline on `main`. Retry and cancel on
existing pipelines work at Developer; it is specifically *creating* a run on a
protected ref that needs the higher role.

So: **Maintainer** unless you know every ref the agents will target is
unprotected. Still avoid Owner — it buys only pipeline deletion, which is off.

## Verify the token is actually bounded

Run this before deploying. The point is not that the reads work — it is that the
writes and the secret reads **fail**.

```bash
PAT=glpat-…
HOST=https://gitlab.example.com
P=$(python3 -c 'import urllib.parse;print(urllib.parse.quote("example-group/app",safe=""))')

# must succeed
curl -s -o /dev/null -w '%{http_code} version\n'   -H "PRIVATE-TOKEN: $PAT" "$HOST/api/v4/version"
curl -s -o /dev/null -w '%{http_code} project\n'   -H "PRIVATE-TOKEN: $PAT" "$HOST/api/v4/projects/$P"
curl -s -o /dev/null -w '%{http_code} pipelines\n' -H "PRIVATE-TOKEN: $PAT" "$HOST/api/v4/projects/$P/pipelines?per_page=1"
# The repository files API is listed under both `api` and `read_repository` in
# GitLab's own scope table, so prove it rather than assuming: a 403 here means
# you also need read_repository (classic) or Repositories=Read (fine-grained).
curl -s -o /dev/null -w '%{http_code} file read\n' -H "PRIVATE-TOKEN: $PAT" "$HOST/api/v4/projects/$P/repository/files/README.md/raw?ref=HEAD"
# CI lint is a POST but mutates nothing — it is the one write-shaped call the
# read-only profile also needs.
curl -s -o /dev/null -w '%{http_code} ci lint\n' -X POST -H "PRIVATE-TOKEN: $PAT" \
  -H 'Content-Type: application/json' -d '{"content":"stages: [build]\n"}' "$HOST/api/v4/projects/$P/ci/lint"

# must FAIL (403/404). A 200 here means the token is too wide.
curl -s -o /dev/null -w '%{http_code} VARIABLES(want 403)\n' -H "PRIVATE-TOKEN: $PAT" "$HOST/api/v4/projects/$P/variables"
curl -s -o /dev/null -w '%{http_code} HOOKS(want 403)\n'     -H "PRIVATE-TOKEN: $PAT" "$HOST/api/v4/projects/$P/hooks"

# a project outside the intended boundary must also fail
curl -s -o /dev/null -w '%{http_code} OUTSIDE(want 403/404)\n' -H "PRIVATE-TOKEN: $PAT" "$HOST/api/v4/projects/other-group%2Fthing"

# and if pipelines.delete is OFF, deletion must be refused by permission, not by
# a missing target: expect 403, not 404. (A non-existent id makes this safe.)
curl -s -o /dev/null -w '%{http_code} PIPE-DELETE(want 403)\n' -X DELETE \
  -H "PRIVATE-TOKEN: $PAT" "$HOST/api/v4/projects/$P/pipelines/999999999"
```

**Do not deploy a token that can read `/variables`.**

## Rotation

Set an expiry — a year at most. The server re-reads `tokenFile` on **every**
request, so replacing the file (or letting a secrets operator replace it) rotates
the credential with no restart and no dropped requests.

`instances_list` reports the token's name, scopes and expiry, and warns under 14
days. The same information is exported as `gmcp_token_expires_in_seconds`; alert
on it:

```promql
gmcp_token_expires_in_seconds < 14 * 24 * 3600
```
