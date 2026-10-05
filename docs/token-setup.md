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

## Prefer a fine-grained token

If your GitLab version offers **fine-grained personal access tokens**, use one:
they let you pick a boundary (selected groups or projects) and per-resource
Create/Read/Update/Delete, which maps almost exactly onto this server's
capabilities.

A good starting point:

| Dimension | Setting |
|---|---|
| Boundary | only the group(s) this server should reach |
| Read | enabled on every resource category |
| Pipelines | Create + Read + Update (add Delete only if `pipelines.delete` is on) |
| Jobs | Read + Update (covers retry / cancel / play) |
| Projects | **Read only** |
| Repositories | **Read only** |
| Issues / Merge requests / Notes / Members | **Read only** |
| CI/CD variables, Secure files, Access tokens, Deploy tokens and keys, Webhooks, Integrations, Runners | **nothing** |

## Classic scopes, if that is all you have

Classic scopes are coarse — there is no "read everything plus write pipelines":

- `read_api` is read-only *everything*, so pipelines cannot be operated.
- `api` is full read **and** write everything, including every CI/CD variable.

So with a classic token, use `api` and understand the consequence: the promise
that pipeline secrets are unreachable then rests on this server's deny list
alone, not on GitLab. The fine-grained token moves that guarantee to the source.

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
