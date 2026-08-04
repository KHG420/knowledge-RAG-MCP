package main

import (
	"os"
	"os/signal"
	"syscall"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/manage"
	"knowledge-mcp/internal/logging"
)

func runManage(cfg *config.Config, store *knowledge.Store, logger *logging.Logger, mgmtSrv *manage.Server) {
	log := logger.WithModule("manage")

	managePort := cfg.ManagePort
	if managePort == "" {
		managePort = "8085"
	}

	// Set up signal handling for graceful shutdown.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Infof("received signal %v, shutting down...", sig)
		logger.Close()
		os.Exit(0)
	}()

	log.Infof("management UI starting on %s", formatManageURL(managePort))
	if err := manage.Start(mgmtSrv, managePort); err != nil {
		log.Errorf("management UI error: %v", err)
		os.Exit(1)
	}
}

// --- Tool registration ---

