package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	mcpkg "github.com/sthadka/jai/internal/mcp"
	synce "github.com/sthadka/jai/internal/sync"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP server for AI agent integration",
	Long: `Start an MCP (Model Context Protocol) server that exposes jai functionality
to AI agents like Claude Code. Supports stdio, HTTP, and SSE transports.

The server runs background sync on a configurable interval (default: 15m).

Examples:
  jai serve                           # stdio transport (default)
  jai serve --transport http          # HTTP transport on :8947
  jai serve --port 9000               # HTTP on custom port
  jai serve --read-only               # block all write operations
  jai serve --toolsets read,schema    # enable only read and schema tools
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		transport, _ := cmd.Flags().GetString("transport")
		port, _ := cmd.Flags().GetInt("port")
		readOnly, _ := cmd.Flags().GetBool("read-only")
		toolsets, _ := cmd.Flags().GetString("toolsets")

		// Override config with CLI flags
		if readOnly {
			g.cfg.MCP.ReadOnly = true
		}
		if toolsets != "" {
			g.cfg.MCP.Toolsets = strings.Split(toolsets, ",")
		}

		// Discover fields first
		var overrides map[string]string
		if g.cfg.Fields.Overrides != nil {
			overrides = g.cfg.Fields.Overrides
		}
		if err := g.sync.DiscoverFields(cmd.Context(), overrides); err != nil {
			return fmt.Errorf("discovering fields: %w", err)
		}

		// Start background sync worker
		interval, err := time.ParseDuration(g.cfg.Sync.Interval)
		if err != nil {
			interval = 15 * time.Minute
		}
		bgWorker := synce.NewBackgroundWorker(g.sync, interval)
		bgWorker.Start(cmd.Context())
		defer bgWorker.Stop()

		// Create MCP server
		srv := mcpkg.New(g.cfg, g.db, g.jira, g.query, g.sync)

		ctx := cmd.Context()
		switch transport {
		case "stdio", "":
			return srv.ServeStdio(ctx)
		case "http":
			return srv.ServeHTTP(ctx, fmt.Sprintf(":%d", port))
		case "sse":
			return srv.ServeSSE(ctx, fmt.Sprintf(":%d", port))
		default:
			return fmt.Errorf("unknown transport: %s (valid: stdio, http, sse)", transport)
		}
	},
}

func init() {
	serveCmd.Flags().String("transport", "stdio", "MCP transport: stdio, http, sse")
	serveCmd.Flags().Int("port", 8947, "HTTP/SSE port")
	serveCmd.Flags().Bool("read-only", false, "Block write operations")
	serveCmd.Flags().String("toolsets", "", "Comma-separated toolsets to enable (read,schema,write,sync,config)")
	rootCmd.AddCommand(serveCmd)
}
