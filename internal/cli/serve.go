package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	mcpkg "github.com/sthadka/jai/pkg/mcp"
	synce "github.com/sthadka/jai/pkg/sync"
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

		app := cliFrom(cmd)
		transport, _ := cmd.Flags().GetString("transport")
		port, _ := cmd.Flags().GetInt("port")
		readOnly, _ := cmd.Flags().GetBool("read-only")
		toolsets, _ := cmd.Flags().GetString("toolsets")

		// Override config with CLI flags
		if readOnly {
			app.engine.Config.MCP.ReadOnly = true
		}
		if toolsets != "" {
			app.engine.Config.MCP.Toolsets = strings.Split(toolsets, ",")
		}

		// Discover fields first (non-fatal for serve - will retry in background sync)
		var overrides map[string]string
		if app.engine.Config.Fields.Overrides != nil {
			overrides = app.engine.Config.Fields.Overrides
		}
		if err := app.engine.Sync.DiscoverFields(cmd.Context(), overrides); err != nil {
			fmt.Fprintf(os.Stderr, "warning: field discovery failed (will retry in background): %v\n", err)
		}

		// Start background sync worker
		interval, err := time.ParseDuration(app.engine.Config.Sync.Interval)
		if err != nil {
			interval = 15 * time.Minute
		}
		bgWorker := synce.NewBackgroundWorker(app.engine.Sync, interval)
		bgWorker.Start(cmd.Context())
		defer bgWorker.Stop()

		// Create MCP server
		srv := mcpkg.New(app.engine.Config, app.engine.Store, app.engine.Client, app.engine.Query, app.engine.Sync)

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
