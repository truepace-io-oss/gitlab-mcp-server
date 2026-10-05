package mcpserver

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/metrics"
)

// addTool registers a tool wrapped with metrics: it times the call, resolves the
// target instance for the label, classifies the result, and records both a
// counter and a latency observation.
func addTool[In any](m *mcp.Server, s *Server, name, description string, h mcp.ToolHandlerFor[In, any]) {
	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		res, out, err := h(ctx, req, in)
		metrics.RecordTool(name, s.instanceLabel(in), classifyResult(res, err), time.Since(start))
		return res, out, err
	}
	mcp.AddTool(m, &mcp.Tool{Name: name, Description: description}, wrapped)
}

// instanceLabel returns the resolved instance name for a tool input (the default
// when the caller omitted it; "-" for tools with no instance argument).
func (s *Server) instanceLabel(in any) string {
	c, ok := in.(interface{ metricInstance() string })
	if !ok {
		return "-"
	}
	if name := c.metricInstance(); name != "" {
		return name
	}
	return s.reg.DefaultName()
}

// classifyResult maps a tool outcome to a bounded result label. The strings come
// from the error messages this server and GitLab produce, so the set stays small.
func classifyResult(res *mcp.CallToolResult, err error) string {
	if err != nil {
		return "error"
	}
	if res == nil || !res.IsError {
		return "ok"
	}
	low := strings.ToLower(resultText(res))
	switch {
	case strings.Contains(low, "is not enabled for gitlab instance"),
		strings.Contains(low, "outside the allowed namespaces"),
		strings.Contains(low, "permanently blocked by this server"),
		strings.Contains(low, "read-only"),
		strings.Contains(low, "readonly"):
		return "blocked"
	case strings.Contains(low, "rate limit"), strings.Contains(low, "429"):
		return "rate_limited"
	case strings.Contains(low, "returned 403"), strings.Contains(low, "returned 401"),
		strings.Contains(low, "forbidden"), strings.Contains(low, "unauthorized"):
		return "forbidden"
	case strings.Contains(low, "returned 404"), strings.Contains(low, "not found"):
		return "not_found"
	case strings.Contains(low, "returned 409"), strings.Contains(low, "already"):
		return "conflict"
	default:
		return "error"
	}
}

// resultText concatenates the text content of a result (for classification).
func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
