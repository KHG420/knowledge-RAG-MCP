package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/logging"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/setup"
)

func main() {
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
	fmt.Fprintf(os.Stderr, "  setup           Interactive configuration\n")
	os.Exit(1)
}

// initStoreAndLogger creates and configures the knowledge store and structured
// logger from the given config. It sets up the embedder, reranker, and GPU
// scheduler as configured. The caller must call logger.Close() when done.
func initStoreAndLogger(cfg *config.Config) (*knowledge.Store, *logging.Logger) {
	dataDir := cfg.DataDir
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

	store := knowledge.NewStore()
	if dataDir != "" {
		store = store.WithDataDir(dataDir)
	}
	store.SetLogger(logger.WithModule("store"))
	if defaultKB != "" {
		store = store.WithKB(defaultKB)
		log.Infof("default KB: %s", defaultKB)
	}
	if err := store.EnsureDir(); err != nil {
		log.Errorf("failed to init data dir: %v", err)
		os.Exit(1)
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

	return store, logger
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
			log.Infof("management UI starting on http://localhost:%s", managePort)
			if err := store.StartManageServer(managePort); err != nil {
				log.Errorf("management UI failed to start on port %s: %v", managePort, err)
			}
		}()
	}

	// Create the SSE server.
	sseServer := server.NewSSEServer(s)
	if cfg.ServeBaseURL != "" {
		sseServer = server.NewSSEServer(s, server.WithBaseURL(cfg.ServeBaseURL))
	}

	servePort := cfg.ServePort
	if servePort == "" {
		servePort = "8086"
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
	}()

	log.Infof("SSE MCP server starting on :%s (mcpOnly=%v)", servePort, mcpOnly)
	if err := sseServer.Start(":" + servePort); err != nil {
		log.Errorf("SSE server error: %v", err)
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

	log.Infof("management UI starting on http://localhost:%s", managePort)
	if err := store.StartManageServer(managePort); err != nil {
		log.Errorf("management UI error: %v", err)
		os.Exit(1)
	}
}

// --- Tool registration ---

func registerSearch(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_search",
		mcp.WithDescription(`BM25/hybrid keyword search across all documents in the knowledge base.

**IMPORTANT — kbName (knowledge base selection)**: Before calling, THINK about which knowledge base (KB) the user's question refers to. Infer the most likely KB from the user's context, workspace, or project context — then pass that KB name in the "kbName" parameter to scope the search and get accurate results. Only omit "kbName" when the user explicitly asks to search across ALL knowledge bases, or when absolutely no single KB can be reasonably inferred.

BEFORE CALLING: you MUST rewrite the user's question into a space-separated string of distinctive keywords and phrases. Do NOT pass the raw question verbatim. Fix typos, resolve pronouns from conversation context, add synonyms and related terms (Chinese + English where applicable).

Examples of required rewriting:
  User: "how to chunk documents?"
    → search_keywords: "chunking text splitting segmentation document chunk longChunk shortChunk overlap"
  User: (after discussing chunking) "它的参数有哪些？"
    → search_keywords: "分块 chunking 参数 longChunk shortChunk overlapChars fragmentThreshold"
  User: "embeding vs retrieval"
    → search_keywords: "embedding vector retrieval search dense sparse BM25 hybrid"`),
		mcp.WithString("search_keywords",
			mcp.Required(),
			mcp.Description("REWRITTEN keyword string (space-separated terms) — NOT the user's raw question. Fix typos, expand context, add synonyms. Use distinctive keywords the documents are likely to contain."),
		),
		mcp.WithString("original_question",
			mcp.Description("The user's original question verbatim, for logging purposes."),
		),
		mcp.WithString("query",
			mcp.Description("DEPRECATED: use search_keywords instead. Fallback for backward compatibility."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum results to return. Default 8, max 20."),
		),
		mcp.WithString("mode",
			mcp.Description("Search mode: 'bm25' (default, keyword) or 'hybrid' (BM25 + embedding). Requires embedder for hybrid."),
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
			mcp.Description("REQUIRED when a specific knowledge base matches the user's question. Before calling, think: which KB does the user's context most likely refer to? Pass that KB name here to scope the search. Omit ONLY when the user explicitly asks to search across all KBs, or when absolutely no KB can be inferred from context."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Prefer search_keywords; fall back to deprecated query param.
		searchKW := getString(req, "search_keywords")
		if searchKW == "" {
			searchKW = getString(req, "query")
		}
		if searchKW == "" {
			return mcp.NewToolResultError("search_keywords is required — rewrite the user's question into distinctive keywords before calling"), nil
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
		searchStore := store
		if kbName != "" {
			searchStore = store.WithKB(kbName)
		}

		var hits []knowledge.SearchHit
		var err error
		switch strings.ToLower(getString(req, "mode")) {
		case "hybrid":
			if kbName != "" {
				hits, err = searchStore.HybridSearch(searchKW, limit, filter)
			} else {
				// HybridSearchAll not implemented; fallback to SearchAll
				hits, err = searchStore.SearchAll(searchKW, limit, filter)
			}
		default:
			if kbName != "" {
				hits, err = searchStore.Search(searchKW, limit, filter)
			} else {
				hits, err = searchStore.SearchAll(searchKW, limit, filter)
			}
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
		}
		tlog := logger.WithModule("tool")
		tlog.Debugf("knowledge_search: query=%q limit=%d kb=%q mode=%q hits=%d", searchKW, limit, kbName, getString(req, "mode"), len(hits))
		if len(hits) == 0 {
			return mcp.NewToolResultText("No matching chunks found."), nil
		}
		data, _ := json.MarshalIndent(hits, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	})
}

func registerRead(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_read",
		mcp.WithDescription(`Read a specific chunk from a document in the knowledge base.

**kbName**: When you have search results, pass the same kbName from the search call to scope the read to the correct KB. If you don't know the KB, you may omit it — the system will search all KBs.

If search results show multiple hits from the same section (SectionHint field is non-empty), consider reading with level=section to get the full section context instead of just the individual chunk.`),
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
			mcp.Description("Pass the same kbName from the search call that produced these results. If you don't know the KB, you may omit it — the system searches all KBs."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docSlug := getString(req, "docSlug")
		chunkID := getString(req, "chunkID")
		if docSlug == "" || chunkID == "" {
			return mcp.NewToolResultError("docSlug and chunkID are required"), nil
		}

		kbName := getString(req, "kbName")

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
			return mcp.NewToolResultText(text), nil
		}

		text, err := tryReadChunk(store, kbName, docSlug, chunkID, ctxCount)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		logger.WithModule("tool").Debugf("knowledge_read: chunk slug=%q chunk=%q ctx=%d kb=%q textlen=%d", docSlug, chunkID, ctxCount, kbName, len(text))
		return mcp.NewToolResultText(text), nil
	})
}

func readSection(store *knowledge.Store, docSlug, chunkID string) (string, error) {
	index, err := store.ReadChunksIndex(docSlug)
	if err != nil || index == nil {
		return "", fmt.Errorf("no index found for document %q", docSlug)
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
	for _, kb := range kbs {
		text, err := store.WithKB(kb).ReadChunkContext(docSlug, chunkID, ctxCount)
		if err == nil {
			return text, nil
		}
	}
	return "", fmt.Errorf("document %q not found in any KB", docSlug)
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
	for _, kb := range kbs {
		text, err := readSection(store.WithKB(kb), docSlug, chunkID)
		if err == nil {
			return text, nil
		}
	}
	return "", fmt.Errorf("document %q not found in any KB", docSlug)
}

func registerListKBs(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_list_kbs",
		mcp.WithDescription(`List all knowledge bases with their descriptions.

Returns the count of knowledge bases and each KB's name and description.
The description is the brief summary provided when the KB was created.
Knowledge bases without a description will show "(no description)".`),
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
		mcp.WithDescription(`List all uploaded documents in the knowledge base.`),
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
		mcp.WithDescription(`Upload a document file or batch-upload a directory into the knowledge base. Supports PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX, MD, TXT.`),
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
		meta, err := uploadStore.UploadDocument(filePath, tags...)
		if err != nil {
			tlog.Errorf("knowledge_upload: file=%q failed: %v", filePath, err)
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
		mcp.WithDescription(`Remove a document and all its chunks from the knowledge base.`),
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

// findConfigPath returns the path to the config file.
// It only looks for knowledge-mcp.toml in the same directory as the executable.
// This is the single source of truth — no fallback to home dir config.
func findConfigPath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "knowledge-mcp.toml")
	}
	return "knowledge-mcp.toml"
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
