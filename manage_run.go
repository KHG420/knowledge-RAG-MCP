package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Infof("received signal %v, shutting down...", sig)
		cancel()
	}()

	// Start the management server in a separate goroutine so we can wait for signals.
	errCh := make(chan error, 1)
	go func() {
		errCh <- manage.Start(mgmtSrv, managePort)
	}()

	log.Infof("management UI starting on %s", formatManageURL(managePort))
	select {
	case err := <-errCh:
		if err != nil {
			log.Errorf("management UI error: %v", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		log.Infof("shutdown signal received, exiting")
		// Give manage.Start a moment to clean up its goroutines
		time.Sleep(100 * time.Millisecond)
	}
	logger.Close()
}

// --- Tool registration ---

