package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/manage"
	"knowledge-mcp/internal/logging"
)

func streamableHTTPHandler(mcpServer *server.MCPServer, log *logging.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// GET can be used for SSE upgrade (server→client notifications).
			// Without active sessions this is a no-op; clients that need
			// notifications can fall back to the legacy SSE endpoint.
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			// Keep the connection open briefly then close — signals no events.
			w.(http.Flusher).Flush()
			return
		}
		if r.Method == http.MethodDelete {
			// DELETE closes the session (no-op in stateless mode).
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()

		ctx := r.Context()
		response := mcpServer.HandleMessage(ctx, body)

		if response == nil {
			// Notification — accepted, no content.
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

// registerAllTools registers all MCP tool handlers on the given server.
func registerAllTools(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	registerSearch(s, store, logger)
	registerRead(s, store, logger)
	registerListKBs(s, store, logger)
}

// newMCPHandler builds the MCP HTTP mux (legacy SSE + Streamable HTTP on the
// same port) and applies the configured API-token authentication to all of
// them. When APIToken is empty, auth is disabled and behavior is unchanged.
// The returned SSEServer is needed by the caller for graceful shutdown.
func newMCPHandler(cfg *config.Config, mcpServer *server.MCPServer, log *logging.Logger) (http.Handler, *server.SSEServer) {
	// Create the SSE server (legacy transport for backward compatibility).
	sseServer := server.NewSSEServer(mcpServer)
	if cfg.ServeBaseURL != "" {
		sseServer = server.NewSSEServer(mcpServer, server.WithBaseURL(cfg.ServeBaseURL))
	}

	mux := http.NewServeMux()
	mux.Handle("/sse", sseServer)
	mux.Handle("/message", sseServer)
	mux.HandleFunc("/mcp", streamableHTTPHandler(mcpServer, log))

	// The API token protects the MCP transports exactly as it protects the
	// management API. /mcp, /sse and /message all require the same bearer
	// token when configured.
	var handler http.Handler = mux
	if cfg.APIToken != "" {
		log.Infof("MCP endpoints require api_token bearer authentication")
		handler = knowledge.AuthMiddleware(cfg.APIToken)(mux)
	} else {
		log.Infof("api_token not set — MCP endpoints are open (local deployment only)")
	}
	return handler, sseServer
}

// runServe runs the server in long-lived HTTP SSE mode. When mcpOnly is true,
// only the MCP SSE endpoint is started; otherwise the management UI is also
// started in a background goroutine.
func runServe(cfg *config.Config, store *knowledge.Store, logger *logging.Logger, mgmtSrv *manage.Server, mcpOnly bool) {
	log := logger.WithModule("serve")

	s := server.NewMCPServer(
		"knowledge-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	registerAllTools(s, store, logger)

	// Set up signal handling for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if !mcpOnly {
		// Start management UI in the background.
		managePort := cfg.ManagePort
		if managePort == "" {
			managePort = "8085"
		}
		go func() {
			log.Infof("management UI starting on %s", formatManageURL(managePort))
			// Retry up to 3 times on startup failure (port may still be in TIME_WAIT).
			var err error
			for retry := 0; retry < 3; retry++ {
				if retry > 0 {
					time.Sleep(1 * time.Second)
					log.Infof("management UI retry %d/3 on port %s", retry+1, managePort)
				}
				// Use background context for retries, but StartWithContext uses signal ctx for shutdown
				err = manage.StartWithContext(ctx, mgmtSrv, managePort)
				if err == nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				default:
				}
			}
			log.Errorf("management UI failed to start on port %s after 3 attempts: %v", managePort, err)
		}()
	}

	servePort := cfg.ServePort
	if servePort == "" {
		servePort = "8086"
	}

	// Combined handler: SSE (legacy) + Streamable HTTP on the same port,
	// protected by the API token when one is configured.
	mcpHandler, sseServer := newMCPHandler(cfg, s, log)

	httpServer := &http.Server{
		Addr:              ":" + servePort,
		Handler:           mcpHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	httpServer.BaseContext = func(_ net.Listener) context.Context { return ctx }

	go func() {
		sig := <-sigChan
		log.Infof("received signal %v, shutting down...", sig)
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := sseServer.Shutdown(shutdownCtx); err != nil {
			log.Errorf("SSE server shutdown error: %v", err)
		}
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Errorf("HTTP server shutdown error: %v", err)
		}
	}()

	log.Infof("MCP server starting on :%s (SSE + Streamable HTTP, mcpOnly=%v)", servePort, mcpOnly)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Errorf("HTTP server error: %v", err)
		os.Exit(1)
	}
}

// runStdio runs the server in stdio mode (stdin/stdout MCP protocol).
// This is the mode used by Reasonix, Claude Desktop, and other stdio-based MCP hosts.
