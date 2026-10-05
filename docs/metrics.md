# Metrics

Prometheus metrics are served on a **separate, unauthenticated port** (default
`:9091`, path `/metrics`; set `metricsAddr: "off"` to disable). Never place it
behind the MCP auth or a public ingress.

Labels are deliberately low-cardinality. There is no project path, namespace,
user or API path anywhere — those are unbounded and would blow up the series
count.

| Metric | Type | Labels |
|---|---|---|
| `gmcp_tool_calls_total` | counter | `tool`, `mcp_instance`, `result` |
| `gmcp_tool_call_duration_seconds` | histogram | `tool`, `mcp_instance` |
| `gmcp_auth_requests_total` | counter | `method` (`static\|oidc\|none`), `result` (`allow\|deny`) |
| `gmcp_instance_up` | gauge | `mcp_instance` |
| `gmcp_writes_blocked_total` | counter | `mcp_instance`, `reason` |
| `gmcp_operations_total` | counter | `mcp_instance`, `op`, `result` |
| `gmcp_gitlab_requests_total` | counter | `mcp_instance`, `method`, `code` |
| `gmcp_gitlab_request_duration_seconds` | histogram | `mcp_instance`, `method` |
| `gmcp_gitlab_rate_limit_remaining` | gauge | `mcp_instance` |
| `gmcp_token_expires_in_seconds` | gauge | `mcp_instance` |
| `gmcp_build_info` | gauge | `version`, `goversion` |

The label is `mcp_instance`, not `instance`, because Prometheus overwrites its
own `instance` target label.

## Bounded label values

`result` on tool calls: `ok`, `error`, `forbidden`, `blocked`, `not_found`,
`rate_limited`, `conflict`.

`reason` on blocked writes: `global_readonly`, `instance_readonly`,
`capability_denied`, `namespace_denied`, `deny_listed`, `rate_limited`,
`confirmation_missing`.

`op` on operations: `pipeline_create`, `pipeline_retry`, `pipeline_cancel`,
`pipeline_update`, `pipeline_delete`, `job_retry`, `job_cancel`, `job_play`,
`schedule_play`.

## Queries worth having

Tool error rate:

```promql
sum(rate(gmcp_tool_calls_total{result!="ok"}[5m])) by (tool, result)
```

Mutations, by kind — this is the audit view:

```promql
sum(increase(gmcp_operations_total[24h])) by (op, result)
```

Guards firing. A sustained rate means an agent is repeatedly attempting something
it cannot do, which is usually a prompt or configuration problem:

```promql
sum(rate(gmcp_writes_blocked_total[15m])) by (reason)
```

Authentication denials — a spike is worth looking at:

```promql
sum(rate(gmcp_auth_requests_total{result="deny"}[5m]))
```

## Alerts worth having

```yaml
- alert: GitLabMCPInstanceDown
  expr: gmcp_instance_up == 0
  for: 10m
  annotations:
    summary: "gitlab-mcp cannot reach GitLab instance {{ $labels.mcp_instance }}"

- alert: GitLabMCPTokenExpiringSoon
  expr: gmcp_token_expires_in_seconds < 14 * 24 * 3600
  for: 1h
  annotations:
    summary: "The GitLab token for {{ $labels.mcp_instance }} expires in under 14 days"

- alert: GitLabMCPRateLimitLow
  expr: gmcp_gitlab_rate_limit_remaining < 100
  for: 5m
  annotations:
    summary: "GitLab API budget for {{ $labels.mcp_instance }} is nearly exhausted"
```

The token-expiry alert is the one that will actually save an outage: a silently
expired token turns every tool into a 401.
