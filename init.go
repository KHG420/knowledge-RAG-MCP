package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"knowledge-mcp/internal/cache"
	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/chunkstore"
	"knowledge-mcp/internal/knowledge/dict"
	"knowledge-mcp/internal/knowledge/ingest"
	"knowledge-mcp/internal/knowledge/kb"
	"knowledge-mcp/internal/knowledge/manage"
	"knowledge-mcp/internal/knowledge/search"
	"knowledge-mcp/internal/logging"
)

func initStoreAndLogger(cfg *config.Config) (*knowledge.Store, *logging.Logger, *manage.Server) {
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
	var levelName string
	switch logLevel {
	case logging.DEBUG:
		levelName = "debug"
	default:
		levelName = "info"
	}
	log.Infof("log file: %s level=%s", logPath, levelName)

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

	// Wire up SearchEngine (Phase 3: external assembly).
	eng := search.New(store.Mutex(), logger.WithModule("search"))
	eng.SetBackend(store.Backend())
	eng.SetVecState(store.VecState())
	eng.SetRerankState(store.RerankState())
	store.SetSearchEngine(eng)

	// Wire up ChunkStoreEngine (Phase 3.3: chunk I/O extraction).
	chunkEng := chunkstore.New(store.Backend(), "", store.DataDir(), store.Mutex(), logger.WithModule("chunkstore"))
	store.SetChunkStore(chunkEng)
	// Also wire the chunk store into the search engine (required for BM25 retrieval).
	eng.SetChunkStore(store.ChunkStore())

	// Wire up DictService + IngestService (Phase 3.5).
	dictEng := dict.New(store.Mutex(), logger.WithModule("dict"))
	dictEng.SetDataDir(store.DataDir())
	store.SetDictService(dictEng)

	// Ingest engine starts with minimal deps; heavy deps (backend, embedder,
	// gpuScheduler, cacheClient, chunkStore) are injected via setters below
	// after they are configured. The buildChunksIndex callback connects the
	// ingest pipeline back to Store's HNSW vector index management.
	ingestEng := ingest.NewSimple(store.TaskManager(), nil, store.Mutex(), logger.WithModule("ingest"))
	ingestEng.SetBackend(store.Backend())
	ingestEng.SetChunkStore(store.ChunkStore())
	ingestEng.SetBuildChunksIndex(store.BuildChunksIndex)
	store.SetIngestService(ingestEng)

	// Wire up KBAdmin (Phase 3: KB lifecycle + routing).
	kbEng := kb.New(store.Backend(), store.Mutex(), logger.WithModule("kb"))
	store.SetKBAdmin(kbEng)

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
		ingestEng.SetEmbedder(store.Embedder())
		ingestEng.SetKBName(defaultKB)
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
		ingestEng.SetGPUScheduler(scheduler)
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
			ingestEng.SetCacheClient(redisCache)
			log.Infof("Redis cache: connected to %s (db=%d prefix=%s)", cfg.RedisAddr, cfg.RedisDB, cfg.RedisPrefix)
		}
	} else {
		log.Infof("Redis cache: not configured (redis_enabled=false)")
	}

	// --- Search log: record queries for synonym mining ---
	searchLogger := knowledge.NewFileSearchLoggerFromStore(store)
	store.SetSearchLogger(searchLogger)
	log.Infof("search log: enabled (%s)", filepath.Join(store.DataDir(), ".searchlog.jsonl"))

	// --- Wire up ManageServer (Phase 3.4: external assembly) ---
	cfgPath := findConfigPath()
	mgmtSrv := manage.New(
		store,                     // ManageService
		cfg, cfgPath,              // config
		store.Backend(),           // backend
		store.Embedder(),          // embedder
		store.Reranker(),          // reranker
		store.VectorIndexRaw(),    // vectorIndex
		store.KBName(),            // kbName
		store.DataDir(),           // dataDir
		store.GPUScheduler(),      // gpuScheduler
		store.KBRouter(),          // kbRouter
		store.TaskManager(),       // taskManager
		store.Mutex(),             // mu
		logger.WithModule("manage"), // logger
	)
	log.Infof("manage server: wired (port=%s)", cfg.ManagePort)

	return store, logger, mgmtSrv
}

// streamableHTTPHandler returns an HTTP handler for the MCP Streamable HTTP
// transport (POST /mcp). It processes JSON-RPC requests from the body and
// returns the JSON-RPC response directly. This is the modern alternative to
// the legacy SSE transport and is compatible with clients that require
// type="http" (Streamable HTTP).
