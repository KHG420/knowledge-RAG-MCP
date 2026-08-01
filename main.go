package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/cache"
	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
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
		store, logger := initStoreAndLogger(cfg)
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

	// Subcommand: setup — interactive configuration.
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		setup.Run()
		os.Exit(0)
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
		store, logger := initStoreAndLogger(cfg)
		defer logger.Close()
		runServe(cfg, store, logger, mcpOnly)
		return
	}

	// Subcommand: stdio — run as a stdio MCP server (for Reasonix/Claude Desktop).
	if len(os.Args) > 1 && os.Args[1] == "stdio" {
		cfg := config.LoadWithEnvFallback(findConfigPath())
		store, logger := initStoreAndLogger(cfg)
		defer logger.Close()
		runStdio(store, logger)
		return
	}

	// Subcommand: manage — start only the web management UI (no MCP server).
	// Use this alongside stdio mode to manage documents via browser.
	if len(os.Args) > 1 && os.Args[1] == "manage" {
		cfg := config.LoadWithEnvFallback(findConfigPath())
		store, logger := initStoreAndLogger(cfg)
		defer logger.Close()
		runManage(cfg, store, logger)
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
func initStoreAndLogger(cfg *config.Config) (*knowledge.Store, *logging.Logger) {
	defaultKB := cfg.DefaultKB

	logPath := cfg.LogFile
	if logPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			logPath = filepath.Join(home, ".knowledge-mcp", "knowledge-mcp.log")
		} else {
			logPath = "/tmp/knowledge-mcp.log"
		}
	}
	logLevel := logging.ParseLevel(cfg.LogLevel)
	logger, err := logging.NewLogger(logPath, logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logger at %s: %v\n", logPath, err)
		os.Exit(1)
	}
	log := logger.WithModule("startup")
	log.Infof("log file: %s level=%s", logPath, []string{"debug", "info"}[logLevel])

	// Create the MySQL storage backend (required).
	if cfg.MySQLDSN == "" && cfg.MySQLHost == "" && cfg.MySQLSocketPath == "" {
		fmt.Fprintf(os.Stderr, "MySQL configuration is required. Set mysql_dsn, mysql_host, or mysql_socket_path in config.\n")
		os.Exit(1)
	}

	mysqlCfg := knowledge.MySQLBackendConfig{
		DSN:        cfg.MySQLDSN,
		User:       cfg.MySQLUser,
		Password:   cfg.MySQLPassword,
		Host:       cfg.MySQLHost,
		Port:       cfg.MySQLPort,
		Database:   cfg.MySQLDatabase,
		SocketPath: cfg.MySQLSocketPath,
	}
	// Apply defaults for empty fields.
	if mysqlCfg.User == "" {
		mysqlCfg.User = "root"
	}
	if mysqlCfg.Host == "" && mysqlCfg.SocketPath == "" {
		mysqlCfg.Host = "127.0.0.1"
	}
	if mysqlCfg.Port == "" && mysqlCfg.Host != "" {
		mysqlCfg.Port = "3306"
	}
	if mysqlCfg.Database == "" {
		mysqlCfg.Database = "knowledge_rag"
	}

	backend, err := knowledge.NewMySQLBackend(mysqlCfg)
	if err != nil {
		log.Errorf("failed to connect to MySQL: %v", err)
		os.Exit(1)
	}
	store := knowledge.NewStoreWithBackend(backend)
	log.Infof("MySQL backend: %s/%s", mysqlCfg.Host, mysqlCfg.Database)
	store.SetLogger(logger.WithModule("store"))
	store.SetConfig(cfg, findConfigPath())
	if defaultKB != "" {
		store = store.WithKB(defaultKB)
		log.Infof("default KB: %s", defaultKB)
	}
	if err := store.EnsureDir(); err != nil {
		log.Errorf("failed to init data dir: %v", err)
		os.Exit(1)
	}

	// --- v4: Load domain dictionaries for query expansion ---
	synonymRewriter := knowledge.NewSynonymRewriter()
	dictDir := filepath.Join(filepath.Dir(findConfigPath()), "dictionaries")
	if _, err := os.Stat(dictDir); err == nil {
		entries, loadErr := knowledge.LoadDictionaries(dictDir)
		if loadErr != nil {
			log.Warnf("dictionary load failed: %v", loadErr)
		} else if len(entries) > 0 {
			syns := knowledge.DictToSynonyms(entries)
			for term, synonyms := range syns {
				for _, syn := range synonyms {
					synonymRewriter.AddSynonym(term, syn)
				}
			}
			store.SetDictionaryRelatedTerms(knowledge.DictToRelatedTerms(entries))
			log.Infof("dictionaries: loaded %d terms from %s", len(entries), dictDir)
		}
	}

	// Always register the synonym rewriter for fast, deterministic expansion.
	store.SetSynonymRewriter(synonymRewriter)

	// When a DeepSeek API key is configured, also register an LLM rewriter
	// reserved for complex queries only (triage=TriageComplex). Simple and
	// medium queries always use the synonym rewriter regardless of API key.
	if cfg.DeepSeekAPIKey != "" {
		llmCompleter := knowledge.NewDeepSeekCompleter(
			cfg.DeepSeekEndpoint,
			cfg.DeepSeekAPIKey,
			cfg.DeepSeekModel,
			knowledge.WithDeepSeekLogger(logger.WithModule("deepseek")),
		)
		llmRewriter := knowledge.NewLLMQueryRewriter(llmCompleter).
			WithFallback(synonymRewriter)
		store.SetLLMRewriter(llmRewriter)
		log.Infof("query rewriter: LLM (deepseek model=%s) reserved for complex queries; "+
			"simple/medium use synonym rewriter (%d terms)",
			cfg.DeepSeekModel, synonymRewriter.SynonymCount())
	} else {
		log.Infof("query rewriter: synonym-only (%d terms, DEEPSEEK_API_KEY not set)",
			synonymRewriter.SynonymCount())
	}

	// --- Optional: vector embedder (OpenAI-compatible API, e.g. Ollama) ---
	if cfg.EmbedEndpoint != "" {
		log.Infof("EMBED_API_ENDPOINT=%q EMBED_MODEL=%q EMBED_DIM=%d",
			cfg.EmbedEndpoint, cfg.EmbedModel, cfg.EmbedDim)
		opts := []knowledge.OpenAIEmbedderOption{knowledge.WithEndpointURL(cfg.EmbedEndpoint)}
		if cfg.EmbedAPIKey != "" {
			opts = append(opts, knowledge.WithAPIKey(cfg.EmbedAPIKey))
		}
		model := cfg.EmbedModel
		if model == "" {
			model = "bge-m3"
		}
		opts = append(opts, knowledge.WithModel(model))
		if cfg.EmbedDim > 0 {
			opts = append(opts, knowledge.WithDim(cfg.EmbedDim))
		}
		opts = append(opts, knowledge.WithEmbedLogger(logger.WithModule("embed")))
		store.SetEmbedder(knowledge.NewOpenAIEmbedder(opts...))
		log.Infof("embedder: %s (model=%s)", cfg.EmbedEndpoint, model)

		// v4: KB Router for intelligent multi-KB routing (same embedder instance).
		kbRouter := knowledge.NewKBRouter(store.Embedder())
		store.SetKBRouter(kbRouter)
		if err := store.SyncKBRouterDescs(); err != nil {
			log.Warnf("kb router: failed to sync KB descriptions: %v", err)
		} else {
			log.Infof("kb router: enabled (top-K multi-KB routing)")
		}
	} else {
		log.Infof("embedder not configured (EMBED_API_ENDPOINT empty)")
	}

	// --- Optional: cross-encoder reranker (Infinity/Cohere-compatible API) ---
	if cfg.RerankEndpoint != "" {
		opts := []knowledge.InfinityRerankerOption{knowledge.WithRerankEndpointURL(cfg.RerankEndpoint)}
		if cfg.RerankAPIKey != "" {
			opts = append(opts, knowledge.WithRerankAPIKey(cfg.RerankAPIKey))
		}
		if cfg.RerankModel != "" {
			opts = append(opts, knowledge.WithRerankModel(cfg.RerankModel))
		}
		if cfg.RerankTimeout != "" {
			if d, err := time.ParseDuration(cfg.RerankTimeout); err == nil {
				opts = append(opts, knowledge.WithRerankTimeout(d))
				log.Infof("rerank timeout: %s", d)
			}
		}
		opts = append(opts, knowledge.WithRerankLogger(logger.WithModule("rerank")))
		store.SetReranker(knowledge.NewInfinityReranker(opts...))
		log.Infof("reranker: %s", cfg.RerankEndpoint)
	} else {
		log.Infof("reranker not configured (RERANK_API_ENDPOINT empty)")
	}

	// --- Optional: rerank candidate limit (default 100) ---
	if cfg.RerankCandidateLimit > 0 {
		store.SetRerankCandidateLimit(cfg.RerankCandidateLimit)
		log.Infof("rerank candidate limit: %d", cfg.RerankCandidateLimit)
	}

	// --- Optional: document parsing API (HTTP) ---
	// When an endpoint is configured, the external API is tried first when
	// parsing non-plain-text documents (PDF, DOCX, etc.), with the local
	// tabula library as fallback.
	if cfg.DocParserEndpoint != "" {
		var parserOpts []knowledge.HTTPDocParserOption
		parserOpts = append(parserOpts, knowledge.WithParserEndpoint(cfg.DocParserEndpoint))
		if cfg.DocParserAPIKey != "" {
			parserOpts = append(parserOpts, knowledge.WithParserAPIKey(cfg.DocParserAPIKey))
		}
		if cfg.DocParserTimeout != "" {
			if d, err := time.ParseDuration(cfg.DocParserTimeout); err == nil {
				parserOpts = append(parserOpts, knowledge.WithParserTimeout(d))
			}
		}
		parserOpts = append(parserOpts, knowledge.WithParserLogger(logger.WithModule("doc-parser")))
		httpParser := knowledge.NewHTTPDocParser(parserOpts...)
		knowledge.SetDocParser(httpParser)
		log.Infof("HTTP doc parser configured: %s", cfg.DocParserEndpoint)
	} else {
		log.Infof("HTTP doc parser not configured (DOC_PARSER_ENDPOINT empty); using local tabula only")
	}

	// Legacy: MinerU CLI support (deprecated — kept for backward compatibility).
	knowledge.SetMinerUEnabled(cfg.MinerUEnabled)
	if cfg.MinerUEnabled {
		log.Warnf("MinerU (magic-pdf) CLI support has been removed. " +
			"Set DOC_PARSER_ENDPOINT to use an external document parsing API instead.")
	}

	// --- Optional: GPU scheduler for model sleep/wake coordination ---
	if cfg.GPUSchedulerEnabled {
		var schedOpts []knowledge.GPUSchedulerOption
		schedOpts = append(schedOpts, knowledge.WithSchedulerEnabled(true))
		schedOpts = append(schedOpts, knowledge.WithSchedulerLogger(logger.WithModule("gpu-scheduler")))
		if cfg.GPUSchedulerEmbeddingSleepURL != "" {
			schedOpts = append(schedOpts, knowledge.WithSchedulerEmbeddingSleepURL(cfg.GPUSchedulerEmbeddingSleepURL))
		}
		if cfg.GPUSchedulerRerankerSleepURL != "" {
			schedOpts = append(schedOpts, knowledge.WithSchedulerRerankerSleepURL(cfg.GPUSchedulerRerankerSleepURL))
		}
		if cfg.GPUSchedulerDocParserSleepURL != "" {
			schedOpts = append(schedOpts, knowledge.WithSchedulerDocParserSleepURL(cfg.GPUSchedulerDocParserSleepURL))
		}
		if cfg.GPUSchedulerTimeout != "" {
			if d, err := time.ParseDuration(cfg.GPUSchedulerTimeout); err == nil {
				schedOpts = append(schedOpts, knowledge.WithSchedulerTimeout(d))
			}
		}
		scheduler := knowledge.NewGPUScheduler(schedOpts...)
		store.SetGPUScheduler(scheduler)
		log.Infof("GPU scheduler enabled: timeout=%s [%s]",
			cfg.GPUSchedulerTimeout, scheduler.Summary())
		// Probe endpoint connectivity (non-fatal: warn and continue).
		if summary, err := scheduler.Probe(context.Background()); err != nil {
			log.Warnf("GPU scheduler: some endpoints unreachable: %s (continuing without GPU coordination)", summary)
		} else {
			log.Infof("GPU scheduler: endpoints reachable — %s", summary)
		}
	}

	// --- Optional: Redis cache backend ---
	if cfg.RedisEnabled {
		redisCfg := cache.RedisConfig{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       cfg.RedisDB,
			Prefix:   cfg.RedisPrefix,
			PoolSize: cfg.RedisPoolSize,
		}
		if redisCache, rerr := cache.NewRedisCache(redisCfg); rerr != nil {
			log.Warnf("Redis cache: %v — caching disabled", rerr)
		} else {
			store.SetCache(redisCache, cfg)
			log.Infof("Redis cache: connected to %s (db=%d prefix=%s)", cfg.RedisAddr, cfg.RedisDB, cfg.RedisPrefix)
		}
	} else {
		log.Infof("Redis cache: not configured (redis_enabled=false)")
	}

	// --- Search log: record queries for synonym mining ---
	searchLogger := knowledge.NewFileSearchLoggerFromStore(store)
	store.SetSearchLogger(searchLogger)
	log.Infof("search log: enabled (%s)", filepath.Join(store.DataDir(), ".searchlog.jsonl"))

	return store, logger
}

// streamableHTTPHandler returns an HTTP handler for the MCP Streamable HTTP
// transport (POST /mcp). It processes JSON-RPC requests from the body and
// returns the JSON-RPC response directly. This is the modern alternative to
// the legacy SSE transport and is compatible with clients that require
// type="http" (Streamable HTTP).
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

// runServe runs the server in long-lived HTTP SSE mode. When mcpOnly is true,
// only the MCP SSE endpoint is started; otherwise the management UI is also
// started in a background goroutine.
func runServe(cfg *config.Config, store *knowledge.Store, logger *logging.Logger, mcpOnly bool) {
	log := logger.WithModule("serve")

	s := server.NewMCPServer(
		"knowledge-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	registerAllTools(s, store, logger)

	if !mcpOnly {
		// Start management UI in the background.
		managePort := cfg.ManagePort
		if managePort == "" {
			managePort = "8085"
		}
		go func() {
			log.Infof("management UI starting on %s", formatManageURL(managePort))
			if err := store.StartManageServer(managePort); err != nil {
				log.Errorf("management UI failed to start on port %s: %v", managePort, err)
			}
		}()
	}

	// Create the SSE server (legacy transport for backward compatibility).
	sseServer := server.NewSSEServer(s)
	if cfg.ServeBaseURL != "" {
		sseServer = server.NewSSEServer(s, server.WithBaseURL(cfg.ServeBaseURL))
	}

	servePort := cfg.ServePort
	if servePort == "" {
		servePort = "8086"
	}

	// Combined mux: SSE (legacy) + Streamable HTTP on the same port.
	mux := http.NewServeMux()
	mux.Handle("/sse", sseServer)
	mux.Handle("/message", sseServer)
	mux.HandleFunc("/mcp", streamableHTTPHandler(s, log))

	httpServer := &http.Server{
		Addr:    ":" + servePort,
		Handler: mux,
	}

	// Set up signal handling for graceful shutdown.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
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
func runManage(cfg *config.Config, store *knowledge.Store, logger *logging.Logger) {
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
	if err := store.StartManageServer(managePort); err != nil {
		log.Errorf("management UI error: %v", err)
		os.Exit(1)
	}
}

// --- Tool registration ---

func registerSearch(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_research",
		mcp.WithDescription(store.ToolSearchDesc()),
		mcp.WithString("question",
			mcp.Required(),
			mcp.Description("The research question or search query. For simple factual questions, pass the user's original question directly. For complex research tasks, you may decompose into focused queries targeting different aspects. Use natural language or keyword strings as appropriate."),
		),
		mcp.WithString("search_keywords",
			mcp.Description("DEPRECATED: use 'question' instead. Space-separated keyword string. Only for backward compatibility with older Agent versions."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum results to return. Default 8, max 20."),
		),
		mcp.WithString("mode",
			mcp.Description("DEPRECATED: the system auto-selects the best retrieval strategy. Accepted for backward compatibility only."),
			mcp.Enum("bm25", "hybrid"),
		),
		mcp.WithString("sourceType",
			mcp.Description("Filter by source type, e.g. 'pdf', 'md', 'txt'."),
		),
		mcp.WithString("section",
			mcp.Description("Filter chunks whose section heading contains this substring."),
		),
		mcp.WithString("tags",
			mcp.Description("Comma-separated tags. Only documents matching at least one tag are returned."),
		),
		mcp.WithString("addedAfter",
			mcp.Description("ISO 8601 date (e.g. '2026-07-01' or '2026-07-01T00:00:00Z'). Only docs added at or after this time."),
		),
		mcp.WithString("addedBefore",
			mcp.Description("ISO 8601 date (e.g. '2026-07-31' or '2026-07-31T23:59:59Z'). Only docs added at or before this time."),
		),
		mcp.WithBoolean("coarse",
			mcp.Description("Enable coarse-to-fine 2-phase search: first score sections, then only search within top-3 sections."),
		),
		mcp.WithString("kbName",
			mcp.Description(store.ToolSearchKbNameDesc()),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// question takes priority; fall back to search_keywords → query (deprecated).
		searchKW := getString(req, "question")
		if searchKW == "" {
			searchKW = getString(req, "search_keywords")
		}
		if searchKW == "" {
			searchKW = getString(req, "query") // truly ancient fallback
		}
		if searchKW == "" {
			return mcp.NewToolResultError("question is required — pass the research question or search query"), nil
		}

		limit := 8
		if v, ok := req.Params.Arguments["limit"].(float64); ok && v > 0 {
			limit = int(v)
		}
		if limit > 20 {
			limit = 20
		}

		filter := knowledge.SearchFilter{
			SourceType:  getString(req, "sourceType"),
			Section:     getString(req, "section"),
			Tags:        parseTags(getString(req, "tags")),
			AddedAfter:  parseTime(getString(req, "addedAfter")),
			AddedBefore: parseTime(getString(req, "addedBefore")),
			Coarse:      getBool(req, "coarse"),
		}

		kbName := getString(req, "kbName")
		if kbName != "" && strings.Contains(kbName, "..") {
			return mcp.NewToolResultError(fmt.Sprintf("invalid kbName %q: must not contain '..'", kbName)), nil
		}

		// v4: When kbName is empty, use KB Router to select best 1–3 KBs.
		var routedKBs []string
		if kbName == "" {
			routedKBs = store.RouteKBs(searchKW)
		}

		var hits []knowledge.SearchHit
		var err error

		// v4: Default to hybrid unless explicitly set to "bm25" for backward compat.
		useMode := strings.ToLower(getString(req, "mode"))
		isHybrid := useMode != "bm25" // default: hybrid

		if kbName != "" {
			searchStore := store.WithKB(kbName)
			if isHybrid {
				hits, err = searchStore.HybridSearch(searchKW, limit, filter)
			} else {
				hits, err = searchStore.Search(searchKW, limit, filter)
			}
		} else if len(routedKBs) > 0 {
			routedMode := "bm25"
			if isHybrid {
				routedMode = "hybrid"
			}
			hits, err = searchMultiKB(store, searchKW, limit, filter, routedMode, routedKBs)
		} else {
			if isHybrid {
				hits, err = store.SearchAll(searchKW, limit, filter)
			} else {
				hits, err = store.SearchAll(searchKW, limit, filter)
			}
		}

		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
		}
		tlog := logger.WithModule("tool")
		tlog.Debugf("knowledge_research: query=%q limit=%d kb=%q routed=%v mode=%q hybrid=%v hits=%d", searchKW, limit, kbName, routedKBs, useMode, isHybrid, len(hits))
		if len(hits) == 0 {
			return mcp.NewToolResultText("No matching chunks found."), nil
		}
		data, _ := json.MarshalIndent(hits, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	})
}

func registerRead(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_read",
		mcp.WithDescription(store.ToolReadDesc()),
		mcp.WithString("docSlug",
			mcp.Required(),
			mcp.Description("Document slug (from list/search results)."),
		),
		mcp.WithString("chunkID",
			mcp.Required(),
			mcp.Description("Chunk identifier (e.g. '005'). From search results."),
		),
		mcp.WithNumber("context",
			mcp.Description("Number of adjacent chunks to include before and after. Default 0, max 5."),
		),
		mcp.WithString("level",
			mcp.Description("Read granularity: 'chunk' (default) or 'section'."),
			mcp.Enum("chunk", "section"),
		),
		mcp.WithString("kbName",
			mcp.Description(store.ToolReadKbNameDesc()),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docSlug := getString(req, "docSlug")
		chunkID := getString(req, "chunkID")
		if docSlug == "" || chunkID == "" {
			return mcp.NewToolResultError("docSlug and chunkID are required"), nil
		}

		// Path-traversal guard: reject ".." in user-supplied path components.
		for _, v := range []string{docSlug, chunkID} {
			if strings.Contains(v, "..") {
				return mcp.NewToolResultError(fmt.Sprintf("invalid parameter %q: must not contain '..'", v)), nil
			}
		}

		kbName := getString(req, "kbName")
		if kbName != "" && strings.Contains(kbName, "..") {
			return mcp.NewToolResultError(fmt.Sprintf("invalid kbName %q: must not contain '..'", kbName)), nil
		}

		ctxCount := 0
		if v, ok := req.Params.Arguments["context"].(float64); ok {
			ctxCount = int(v)
			if ctxCount < 0 {
				ctxCount = 0
			}
			if ctxCount > 5 {
				ctxCount = 5
			}
		}

		if strings.ToLower(getString(req, "level")) == "section" {
			text, err := tryReadSection(store, kbName, docSlug, chunkID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			logger.WithModule("tool").Debugf("knowledge_read: section slug=%q chunk=%q kb=%q ok", docSlug, chunkID, kbName)
			evidence, _ := buildEvidenceJSON(store, kbName, docSlug, chunkID, text)
			return mcp.NewToolResultText(evidence), nil
		}

		text, err := tryReadChunk(store, kbName, docSlug, chunkID, ctxCount)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		logger.WithModule("tool").Debugf("knowledge_read: chunk slug=%q chunk=%q ctx=%d kb=%q textlen=%d", docSlug, chunkID, ctxCount, kbName, len(text))
		evidence, _ := buildEvidenceJSON(store, kbName, docSlug, chunkID, text)
		return mcp.NewToolResultText(evidence), nil
	})
}

func readSection(store *knowledge.Store, docSlug, chunkID string) (string, error) {
	index, err := store.ReadChunksIndex(docSlug)
	if err != nil {
		// Index corrupt — try plain chunk read as fallback.
		if text, chunkErr := store.ReadChunk(docSlug, chunkID); chunkErr == nil {
			return text, nil
		}
		return "", fmt.Errorf("index corrupted for document %q and chunk read also failed: %w", docSlug, err)
	}
	if index == nil {
		// ChunksIndex missing — try plain chunk read as fallback.
		if text, chunkErr := store.ReadChunk(docSlug, chunkID); chunkErr == nil {
			return text, nil
		}
		return "", fmt.Errorf(
			"chunks-index and chunk both missing for document %q chunk %q — "+
				"the document may have been deleted or is not fully indexed; "+
				"try re-searching to get fresh results",
			docSlug, chunkID,
		)
	}
	for _, entry := range index.Chunks {
		if entry.ID == chunkID && entry.SectionChunkID != "" {
			return store.ReadSectionChunk(docSlug, entry.SectionChunkID)
		}
	}
	return store.ReadChunk(docSlug, chunkID)
}

// tryReadChunk reads a chunk from a specific KB, or searches across all KBs
// when kbName is empty.
func tryReadChunk(store *knowledge.Store, kbName, docSlug, chunkID string, ctxCount int) (string, error) {
	if kbName != "" {
		return store.WithKB(kbName).ReadChunkContext(docSlug, chunkID, ctxCount)
	}
	kbs, err := store.ListKBs()
	if err != nil {
		return "", err
	}
	var lastErr error
	for _, kb := range kbs {
		text, err := store.WithKB(kb).ReadChunkContext(docSlug, chunkID, ctxCount)
		if err == nil {
			return text, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", fmt.Errorf("chunk %q not found in document %q across %d KBs: %w — the document may have been deleted or re-indexed; try re-searching", chunkID, docSlug, len(kbs), lastErr)
	}
	return "", fmt.Errorf("document %q not found in any of %d KBs — try re-searching to get current results", docSlug, len(kbs))
}

// tryReadSection reads a section chunk from a specific KB, or searches across
// all KBs when kbName is empty.
func tryReadSection(store *knowledge.Store, kbName, docSlug, chunkID string) (string, error) {
	if kbName != "" {
		return readSection(store.WithKB(kbName), docSlug, chunkID)
	}
	kbs, err := store.ListKBs()
	if err != nil {
		return "", err
	}
	var lastErr error
	for _, kb := range kbs {
		text, err := readSection(store.WithKB(kb), docSlug, chunkID)
		if err == nil {
			return text, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", fmt.Errorf("section read failed for document %q across %d KBs: %w — try reading at chunk level or re-searching", docSlug, len(kbs), lastErr)
	}
	return "", fmt.Errorf("document %q not found in any of %d KBs — try re-searching to get current results", docSlug, len(kbs))
}

// buildEvidenceJSON constructs an EvidenceChunk JSON string that wraps the
// content with full source attribution so the LLM never loses context.
func buildEvidenceJSON(store *knowledge.Store, kbName, docSlug, chunkID, text string) (string, error) {
	// Resolve the correct store view.
	s := store
	if kbName != "" {
		s = store.WithKB(kbName)
	} else {
		// Try to find the document across all KBs to get metadata.
		kbs, err := store.ListKBs()
		if err == nil {
			for _, kb := range kbs {
				if _, metaErr := store.WithKB(kb).ReadMeta(docSlug); metaErr == nil {
					s = store.WithKB(kb)
					break
				}
			}
		}
	}

	meta, metaErr := s.ReadMeta(docSlug)
	if metaErr != nil {
		// Degrade gracefully: return content without metadata.
		feats := knowledge.ExtractEvidenceFeatures(text)
		data, _ := json.MarshalIndent(knowledge.EvidenceChunk{
			Document:   knowledge.DocumentInfo{ID: docSlug},
			Location:   knowledge.LocationInfo{ChunkID: chunkID},
			Content:    text,
			CitationID: fmt.Sprintf("%s_%s", docSlug, chunkID),
			Evidence: knowledge.EvidenceMeta{
				SourceConfidence: "exact_section",
				AnswerRelevance:  knowledge.ClassifyAnswerRelevance(feats),
				Completeness:     knowledge.ClassifyCompleteness(feats),
			},
		}, "", "  ")
		return string(data), nil
	}

	// Try to get section/offset/page from the chunks index.
	sec := ""
	off := 0
	ps := 0
	pe := 0
	if index, idxErr := s.ReadChunksIndex(docSlug); idxErr == nil && index != nil {
		for _, entry := range index.Chunks {
			if entry.ID == chunkID {
				sec = entry.Section
				off = entry.Offset
				ps = entry.PageStart
				pe = entry.PageEnd
				break
			}
		}
	}

	evidence := knowledge.EvidenceChunk{
		Document: knowledge.DocumentInfo{
			ID:           meta.Slug,
			Title:        meta.Title,
			OriginalName: meta.OriginalName,
			Type:         meta.SourceType,
		},
		Location: knowledge.LocationInfo{
			ChunkID:   chunkID,
			Section:   sec,
			Offset:    off,
			PageStart: ps,
			PageEnd:   pe,
		},
		Content:    text,
		CitationID: fmt.Sprintf("%s_%s", docSlug, chunkID),
	}

	// v4: Feature-based evidence quality signals (pure rules, 0 extra cost).
	feats := knowledge.ExtractEvidenceFeatures(text)
	evidence.Evidence = knowledge.EvidenceMeta{
		SourceConfidence: "exact_section", // read path always targets an exact section/chunk
		AnswerRelevance:  knowledge.ClassifyAnswerRelevance(feats),
		Completeness:     knowledge.ClassifyCompleteness(feats),
	}

	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// searchMultiKB searches across multiple routed KBs and merges results,
// picking top-N per KB then re-ranking globally by score (v4 multi-KB).
func searchMultiKB(store *knowledge.Store, query string, limit int, filter knowledge.SearchFilter, mode string, kbNames []string) ([]knowledge.SearchHit, error) {
	perKB := limit
	if len(kbNames) > 1 {
		// Distribute limit across KBs; ensure at least 3 per KB.
		perKB = limit / len(kbNames)
		if perKB < 3 {
			perKB = 3
		}
	}

	var allHits []knowledge.SearchHit
	for _, kb := range kbNames {
		kbStore := store.WithKB(kb)
		var hits []knowledge.SearchHit
		var err error
		switch mode {
		case "hybrid":
			hits, err = kbStore.HybridSearch(query, perKB, filter)
		default:
			hits, err = kbStore.Search(query, perKB, filter)
		}
		if err != nil {
			// Log and continue — one KB failure shouldn't block others.
			continue
		}
		allHits = append(allHits, hits...)
	}

	// Sort merged results by score descending and truncate to limit.
	sort.Slice(allHits, func(i, j int) bool {
		return allHits[i].Score > allHits[j].Score
	})
	if len(allHits) > limit {
		allHits = allHits[:limit]
	}
	return allHits, nil
}

func registerListKBs(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_list_kbs",
		mcp.WithDescription(store.ToolListKBsDesc()),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kbs, err := store.ListKBsInfo()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list KBs failed: %v", err)), nil
		}
		type kbEntry struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		entries := make([]kbEntry, len(kbs))
		for i, kb := range kbs {
			desc := kb.Description
			if desc == "" {
				desc = "(no description)"
			}
			entries[i] = kbEntry{Name: kb.Name, Description: desc}
		}
		result := map[string]any{
			"count":          len(entries),
			"knowledgeBases": entries,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		logger.WithModule("tool").Debugf("knowledge_list_kbs: count=%d", len(entries))
		return mcp.NewToolResultText(string(data)), nil
	})
}

func registerList(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_list",
		mcp.WithDescription(store.ToolListDesc()),
		mcp.WithString("kbName",
			mcp.Description("Optional knowledge base name. When set, list only documents in that KB. When omitted, list all KBs."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kbName := getString(req, "kbName")
		var display, full []knowledge.DocumentMeta
		var err error
		if kbName != "" {
			display, full, err = store.WithKB(kbName).ListPreview(10)
		} else {
			display, full, err = store.ListPreviewAll(10)
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(display) == 0 {
			return mcp.NewToolResultText("Knowledge base is empty."), nil
		}
		logger.WithModule("tool").Debugf("knowledge_list: kb=%q total=%d displayed=%d", kbName, len(full), len(display))

		// Notify the user if there are more docs than shown.
		var msg string
		if len(full) > 10 {
			msg = fmt.Sprintf("Showing %d of %d documents. Full list saved to snapshot file.\n\n", len(display), len(full))
		}

		data, _ := json.MarshalIndent(display, "", "  ")
		return mcp.NewToolResultText(msg + string(data)), nil
	})
}

func registerUpload(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_upload",
		mcp.WithDescription(store.ToolUploadDesc()),
		mcp.WithString("filePath",
			mcp.Description("Path to a single document file. Mutually exclusive with 'directory'."),
		),
		mcp.WithString("directory",
			mcp.Description("Directory path for batch upload. Mutually exclusive with 'filePath'."),
		),
		mcp.WithBoolean("recursive",
			mcp.Description("When true, recursively walk subdirectories (for batch upload)."),
		),
		mcp.WithString("tags",
			mcp.Description("Comma-separated tags to assign to the uploaded document(s)."),
		),
		mcp.WithString("kbName",
			mcp.Description("Knowledge base name. Required when no default KB is configured via KNOWLEDGE_MCP_DEFAULT_KB."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath := getString(req, "filePath")
		directory := getString(req, "directory")
		tags := parseTags(getString(req, "tags"))
		recursive := false
		if v, ok := req.Params.Arguments["recursive"].(bool); ok {
			recursive = v
		}

		kbName := getString(req, "kbName")
		if kbName == "" {
			kbs, listErr := store.ListKBs()
			if listErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("list KBs failed: %v", listErr)), nil
			}
			if len(kbs) == 0 {
				return mcp.NewToolResultError("No knowledge base exists. Create one first via the management UI."), nil
			}
			return mcp.NewToolResultError("kbName is required when no default KB is configured. Specify which knowledge base to upload to."), nil
		}
		uploadStore := store.WithKB(kbName)
		tlog := logger.WithModule("tool")

		if directory != "" {
			if filePath != "" {
				return mcp.NewToolResultError("filePath and directory are mutually exclusive"), nil
			}
			if strings.Contains(directory, "..") {
				return mcp.NewToolResultError("invalid directory path"), nil
			}
			tlog.Debugf("knowledge_upload: directory=%q recursive=%v kb=%q", directory, recursive, kbName)
			summary, err := uploadStore.UploadDirectory(directory, recursive, tags...)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("batch upload failed: %v", err)), nil
			}
			return mcp.NewToolResultText(summary), nil
		}

		if filePath == "" {
			return mcp.NewToolResultError("filePath or directory is required for upload"), nil
		}
		if strings.Contains(filePath, "..") {
			return mcp.NewToolResultError("invalid file path"), nil
		}
		meta, err := uploadStore.UploadDocument(filePath, tags...)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("upload failed: %v", err)), nil
		}
		tlog.Debugf("knowledge_upload: file=%q slug=%q chunks=%d chars=%d", filePath, meta.Slug, meta.ChunkCount, meta.TotalChars)
		return mcp.NewToolResultText(
			fmt.Sprintf("Document uploaded: %s (%d chunks, %d chars)",
				meta.OriginalName, meta.ChunkCount, meta.TotalChars),
		), nil
	})
}

func registerRemove(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_remove",
		mcp.WithDescription(store.ToolRemoveDesc()),
		mcp.WithString("docSlug",
			mcp.Required(),
			mcp.Description("Document slug to remove (from list results)."),
		),
		mcp.WithString("kbName",
			mcp.Description("Optional knowledge base name. When set, remove from that KB. When omitted, remove from all KBs."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docSlug := getString(req, "docSlug")
		if docSlug == "" {
			return mcp.NewToolResultError("docSlug is required for remove"), nil
		}

		if strings.Contains(docSlug, "..") {
			return mcp.NewToolResultError("invalid docSlug"), nil
		}

		kbName := getString(req, "kbName")
		tlog := logger.WithModule("tool")
		tlog.Debugf("knowledge_remove: slug=%q kb=%q", docSlug, kbName)
		if kbName != "" {
			if err := store.WithKB(kbName).RemoveDocument(docSlug); err != nil {
				tlog.Errorf("knowledge_remove: slug=%q kb=%q failed: %v", docSlug, kbName, err)
				return mcp.NewToolResultError(fmt.Sprintf("remove failed: %v", err)), nil
			}
		} else {
			// Try to remove from all KBs
			kbs, err := store.ListKBs()
			if err != nil {
				tlog.Errorf("knowledge_remove: list KBs failed: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("list KBs failed: %v", err)), nil
			}
			removed := false
			for _, kb := range kbs {
				if err := store.WithKB(kb).RemoveDocument(docSlug); err == nil {
					removed = true
					break
				}
			}
			if !removed {
				return mcp.NewToolResultError(fmt.Sprintf("document %q not found in any KB", docSlug)), nil
			}
		}
		tlog.Debugf("knowledge_remove: slug=%q done", docSlug)
		return mcp.NewToolResultText(fmt.Sprintf("Document %q removed.", docSlug)), nil
	})
}

// --- helpers ---

func getString(req mcp.CallToolRequest, key string) string {
	v, _ := req.Params.Arguments[key].(string)
	return v
}

func getBool(req mcp.CallToolRequest, key string) bool {
	v, _ := req.Params.Arguments[key].(bool)
	return v
}

// parseTags splits a comma-separated tag string, trims whitespace,
// and filters out empty strings.
func parseTags(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// localIP returns the preferred outbound LAN IP (e.g. 192.168.x.x).
// Falls back to "localhost" if no suitable non-loopback IPv4 address is found.
func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "localhost"
}

// formatManageURL returns a human-readable startup message showing both
// localhost and LAN addresses the management UI is reachable at.
func formatManageURL(port string) string {
	ip := localIP()
	if ip == "localhost" {
		return fmt.Sprintf("http://localhost:%s", port)
	}
	return fmt.Sprintf("http://localhost:%s  (LAN: http://%s:%s)", port, ip, port)
}

// findConfigPath returns the path to the config file.
// It first checks the executable directory; if no config exists there (e.g. go run),
// it falls back to knowledge-mcp.toml in the current working directory.
func findConfigPath() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "knowledge-mcp.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// go run / dev mode: use CWD
	return filepath.Join(".", "knowledge-mcp.toml")
}

// parseTime parses an ISO 8601 date string, supporting both date-only
// ("2006-01-02") and full RFC 3339 ("2006-01-02T15:04:05Z07:00") formats.
// Returns the zero time on empty input or parse failure.
func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t
	}
	return time.Time{}
}

// ── dict subcommand implementations ──

// runDictMine reads the search log JSONL file and prints discovered synonym
// candidates for human review before adding them to the domain dictionary.
func runDictMine(store *knowledge.Store) {
	logPath := filepath.Join(store.DataDir(), ".searchlog.jsonl")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Search log not found: %s\n", logPath)
		fmt.Fprintf(os.Stderr, "The log is created automatically after the server runs and handles queries.\n")
		fmt.Fprintf(os.Stderr, "Start the server with: knowledge-mcp serve\n")
		os.Exit(1)
	}

	fmt.Printf("Mining synonyms from: %s\n\n", logPath)
	candidates, err := knowledge.MineSynonymsFromLog(logPath, 0.01, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Mine failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(knowledge.FormatSynonymCandidates(candidates))
	if len(candidates) > 0 {
		fmt.Println("\n👉 Review the candidates above and manually add verified pairs to dictionaries/*.yaml.")
		fmt.Println("   Then restart the server to pick up the updated dictionary.")
	}
}

// runDictGen uses the LLM to batch-extract domain terminology from knowledge
// base chunks and writes a candidate YAML dictionary file for human review.
func runDictGen(cfg *config.Config, store *knowledge.Store) {
	if cfg.DeepSeekAPIKey == "" {
		fmt.Fprintf(os.Stderr, "Error: DEEPSEEK_API_KEY not configured.\n")
		fmt.Fprintf(os.Stderr, "Set it in knowledge-mcp.toml or via the DEEPSEEK_API_KEY environment variable.\n")
		os.Exit(1)
	}

	// Collect chunk texts from the knowledge base.
	kbName := store.KBName()
	if kbName == "" {
		fmt.Fprintf(os.Stderr, "No knowledge base selected. Set default_kb in knowledge-mcp.toml.\n")
		os.Exit(1)
	}

	fmt.Printf("Collecting chunks from KB: %s ...\n", kbName)

	// Use the existing SearchAll to get representative chunks across the KB.
	hits, err := store.SearchAll("", 200) // empty query = get recent/representative chunks
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to collect chunks: %v\n", err)
		os.Exit(1)
	}

	var chunkTexts []string
	for _, h := range hits {
		text := h.Content.Snippet
		if len(text) > 50 {
			chunkTexts = append(chunkTexts, text)
		}
	}

	if len(chunkTexts) == 0 {
		fmt.Fprintf(os.Stderr, "No chunks found in KB %q. Upload documents first.\n", kbName)
		os.Exit(1)
	}

	fmt.Printf("Found %d chunks. Calling LLM to extract domain terms...\n", len(chunkTexts))

	completer := knowledge.NewDeepSeekCompleter(
		cfg.DeepSeekEndpoint,
		cfg.DeepSeekAPIKey,
		cfg.DeepSeekModel,
	)
	results, err := knowledge.GenerateDictionaryFromChunks(completer, chunkTexts, 20, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Dictionary generation failed: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No domain terms extracted. Try with more or different chunks.")
		return
	}

	yaml := knowledge.FormatDictAsYAML(results)

	// Write to dictionaries/ directory next to the config.
	dictDir := filepath.Join(filepath.Dir(findConfigPath()), "dictionaries")
	if err := os.MkdirAll(dictDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create dictionaries dir: %v\n", err)
		os.Exit(1)
	}

	outPath := filepath.Join(dictDir, kbName+"_generated.yaml")
	if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write dictionary: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n✅ Generated %d domain terms → %s\n", len(results), outPath)
	fmt.Println("👉 Review the file, edit/remove entries, then restart the server to apply.")
	fmt.Printf("   cat %s\n", outPath)
}
