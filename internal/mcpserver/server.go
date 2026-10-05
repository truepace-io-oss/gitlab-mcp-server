// Package mcpserver builds the MCP server: it registers the GitLab tools and
// routes each call to the right instance in the registry.
//
// Tools are registered unconditionally, even when their capability is disabled.
// A denied capability yields a tool *error* naming it, not a missing tool —
// "why can't you do X?" should be answerable, and a silently absent tool is not.
package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/instances"
)

// serverVersion is injected by main via SetVersion.
var serverVersion = "dev"

// SetVersion sets the version advertised by the MCP server.
func SetVersion(v string) {
	if v != "" {
		serverVersion = v
	}
}

// Version reports the advertised server version.
func Version() string { return serverVersion }

// Server holds the shared dependencies for all tool handlers.
type Server struct {
	reg      *instances.Registry
	readOnly bool // global kill-switch, surfaced in messages
}

// New builds a Server from the registry and config.
func New(reg *instances.Registry, cfg *config.Config) *Server {
	return &Server{reg: reg, readOnly: cfg.ReadOnly}
}

// MCPServer constructs an *mcp.Server with all tools registered. The streamable
// HTTP handler calls this (via a closure) per session.
func (s *Server) MCPServer() *mcp.Server {
	m := mcp.NewServer(&mcp.Implementation{
		Name:    "gitlab-mcp",
		Version: serverVersion,
	}, nil)

	s.registerMetaTools(m)
	s.registerReadCoreTools(m)
	s.registerReadCITools(m)
	s.registerPipelineTools(m)
	return m
}
