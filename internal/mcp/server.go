package mcp

import (
	"context"
	"os"

	"github.com/mark3labs/mcp-go/server"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/query"
	synce "github.com/sthadka/jai/internal/sync"
)

// Server wraps an MCP server with jai-specific handlers.
type Server struct {
	cfg      *config.Config
	db       *db.DB
	jira     *jira.Client
	query    *query.Engine
	sync     *synce.Engine
	toolsets *ToolsetRegistry
	mcpSrv   *server.MCPServer
}

// New creates a new MCP server with all tools, resources, and prompts registered.
func New(cfg *config.Config, database *db.DB, jiraClient *jira.Client, queryEngine *query.Engine, syncEngine *synce.Engine) *Server {
	s := &Server{
		cfg:   cfg,
		db:    database,
		jira:  jiraClient,
		query: queryEngine,
		sync:  syncEngine,
	}
	s.toolsets = NewToolsetRegistry(cfg.MCP.Toolsets, cfg.MCP.ReadOnly)
	s.mcpSrv = s.buildServer()
	return s
}

// buildServer creates the MCP server and registers all capabilities.
func (s *Server) buildServer() *server.MCPServer {
	srv := server.NewMCPServer("jai", "4.0.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, true),
		server.WithPromptCapabilities(true),
	)

	// Register tools (implemented in separate tool files)
	registerReadTools(s, srv)
	registerSchemaTools(s, srv)
	registerWriteTools(s, srv)
	registerSyncTools(s, srv)
	registerConfigTools(s, srv)

	// Register resources and prompts (stubs for now)
	s.registerResources(srv)
	s.registerPrompts(srv)

	return srv
}

// ServeStdio starts the MCP server using stdio transport.
func (s *Server) ServeStdio(ctx context.Context) error {
	stdio := server.NewStdioServer(s.mcpSrv)
	return stdio.Listen(ctx, os.Stdin, os.Stdout)
}

// ServeHTTP starts the MCP server using streamable HTTP transport.
func (s *Server) ServeHTTP(ctx context.Context, addr string) error {
	httpSrv := server.NewStreamableHTTPServer(s.mcpSrv)
	return httpSrv.Start(addr)
}

// ServeSSE starts the MCP server using SSE transport.
func (s *Server) ServeSSE(ctx context.Context, addr string) error {
	sseSrv := server.NewSSEServer(s.mcpSrv)
	return sseSrv.Start(addr)
}

// Stub functions for tool registration - implemented in separate files
func registerWriteTools(s *Server, srv *server.MCPServer) {
	// TODO: implement in tools_write.go
}

func registerSyncTools(s *Server, srv *server.MCPServer) {
	// TODO: implement in tools_sync.go
}

func registerConfigTools(s *Server, srv *server.MCPServer) {
	// TODO: implement in tools_config.go
}

// registerPrompts stub - will be implemented in prompts.go
func (s *Server) registerPrompts(srv *server.MCPServer) {
	// TODO: implement in prompts.go
}

