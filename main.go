package main

import (
	"fmt"
	"os"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/setup"
)

func main() {
	// Subcommand: dict — domain dictionary management tools.
	if len(os.Args) > 1 && os.Args[1] == "dict" {
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: knowledge-mcp dict <mine|gen>\n\n")
			fmt.Fprintf(os.Stderr, "Subcommands:\n")
			fmt.Fprintf(os.Stderr, "  dict mine      Mine synonym candidates from search logs\n")
			fmt.Fprintf(os.Stderr, "  dict gen       Generate domain dictionary from KB chunks (needs DEEPSEEK_API_KEY)\n")
			os.Exit(1)
		}
		cfg := config.LoadWithEnvFallback(findConfigPath())
		store, logger, _ := initStoreAndLogger(cfg)
		defer logger.Close()

		switch os.Args[2] {
		case "mine":
			runDictMine(store)
		case "gen":
			runDictGen(cfg, store)
		default:
			fmt.Fprintf(os.Stderr, "Unknown dict subcommand: %s\n", os.Args[2])
			os.Exit(1)
		}
		return
	}

	// Subcommand: serve (or server) — run as a long-lived HTTP SSE server.
	if len(os.Args) > 1 && (os.Args[1] == "serve" || os.Args[1] == "server") {
		mcpOnly := false
		for _, a := range os.Args[2:] {
			if a == "--mcp" {
				mcpOnly = true
				break
			}
		}
		cfg := config.LoadWithEnvFallback(findConfigPath())
		store, logger, mgmtSrv := initStoreAndLogger(cfg)
		defer logger.Close()
		runServe(cfg, store, logger, mgmtSrv, mcpOnly)
		return
	}

	// Subcommand: setup — interactive configuration wizard. The wizard reads
	// from stdin and writes knowledge-mcp.toml next to the executable (or the
	// current directory) only after the user confirms the summary. It never
	// touches MySQL or any other service.
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		setup.Run()
		return
	}

	// Subcommand: stdio — run as a stdio MCP server (for Reasonix/Claude Desktop).
	if len(os.Args) > 1 && os.Args[1] == "stdio" {
		cfg := config.LoadWithEnvFallback(findConfigPath())
		store, logger, _ := initStoreAndLogger(cfg)
		defer logger.Close()
		runStdio(store, logger)
		return
	}

	// Subcommand: manage — start only the web management UI (no MCP server).
	// Use this alongside stdio mode to manage documents via browser.
	if len(os.Args) > 1 && os.Args[1] == "manage" {
		cfg := config.LoadWithEnvFallback(findConfigPath())
		store, logger, mgmtSrv := initStoreAndLogger(cfg)
		defer logger.Close()
		runManage(cfg, store, logger, mgmtSrv)
		return
	}

	// No subcommand: show usage.
	fmt.Fprintf(os.Stderr, "Usage: knowledge-mcp <command>\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  serve           Start HTTP SSE MCP server\n")
	fmt.Fprintf(os.Stderr, "  server          (alias for serve)\n")
	fmt.Fprintf(os.Stderr, "  stdio           Start stdio MCP server (for Reasonix/Claude Desktop)\n")
	fmt.Fprintf(os.Stderr, "  manage          Start web management UI only (run alongside stdio)\n")
	fmt.Fprintf(os.Stderr, "  dict mine       Mine synonym candidates from search logs\n")
	fmt.Fprintf(os.Stderr, "  dict gen        Generate domain dictionary from KB chunks (LLM)\n")
	fmt.Fprintf(os.Stderr, "  setup           Interactive configuration\n")
	os.Exit(1)
}

// initStoreAndLogger creates and configures the knowledge store and structured
// logger from the given config. It sets up the embedder, reranker, and GPU
// scheduler as configured. The caller must call logger.Close() when done.
