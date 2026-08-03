package main

import (
	"os"

	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

func runStdio(store *knowledge.Store, logger *logging.Logger) {
	log := logger.WithModule("stdio")
	log.Infof("starting in stdio MCP mode")

	s := server.NewMCPServer(
		"knowledge-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	registerAllTools(s, store, logger)

	if err := server.ServeStdio(s); err != nil {
		log.Errorf("stdio server error: %v", err)
		os.Exit(1)
	}
}

// runManage starts only the web management UI, without any MCP server.
// This can run alongside stdio mode (used by Reasonix etc.) to let users
// upload/delete documents via the browser.
