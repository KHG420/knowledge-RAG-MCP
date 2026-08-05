package manage

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// ── Config API ────────────────────────────────────────────────────────────────

// configAPIResponse is the JSON shape returned by GET /api/config.
type configAPIResponse struct {
	// Storage
	DataDir   string `json:"dataDir"`
	DefaultKB string `json:"defaultKB"`

	// Embedder
	EmbedEndpoint string `json:"embedEndpoint"`
	EmbedModel    string `json:"embedModel"`
	EmbedDim      int    `json:"embedDim"`
	EmbedAPIKey   string `json:"embedApiKey,omitempty"` // masked: only shown if set

	// Reranker
	RerankEndpoint       string `json:"rerankEndpoint"`
	RerankModel          string `json:"rerankModel"`
	RerankAPIKey         string `json:"rerankApiKey,omitempty"`
	RerankTimeout        string `json:"rerankTimeout"`
	RerankCandidateLimit int    `json:"rerankCandidateLimit"`

	// GPU Scheduler
	GPUSchedulerEnabled           bool   `json:"gpuSchedulerEnabled"`
	GPUSchedulerTimeout           string `json:"gpuSchedulerTimeout"`
	GPUSchedulerEmbeddingSleepURL string `json:"gpuSchedulerEmbeddingSleepUrl"`
	GPUSchedulerRerankerSleepURL  string `json:"gpuSchedulerRerankerSleepUrl"`
	GPUSchedulerDocParserSleepURL string `json:"gpuSchedulerDocParserSleepUrl"`

	// Doc Parser
	DocParserEndpoint string `json:"docParserEndpoint"`
	DocParserAPIKey   string `json:"docParserApiKey,omitempty"`
	DocParserTimeout  string `json:"docParserTimeout"`

	// Server
	ManagePort   string `json:"managePort"`
	ServePort    string `json:"servePort"`
	ServeBaseURL string `json:"serveBaseUrl"`

	// Logging
	LogFile  string `json:"logFile"`
	LogLevel string `json:"logLevel"`

	// MySQL
	MySQLDSN        string `json:"mysqlDsn,omitempty"`
	MySQLUser       string `json:"mysqlUser,omitempty"`
	MySQLPassword   string `json:"mysqlPassword,omitempty"` // masked
	MySQLHost       string `json:"mysqlHost,omitempty"`
	MySQLPort       string `json:"mysqlPort,omitempty"`
	MySQLDatabase   string `json:"mysqlDatabase,omitempty"`
	MySQLSocketPath string `json:"mysqlSocketPath,omitempty"`

	// Auth
	APIToken string `json:"apiToken,omitempty"` // masked

	// DeepSeek LLM (query rewriting for complex queries)
	DeepSeekEndpoint string `json:"deepseekEndpoint"`
	DeepSeekModel    string `json:"deepseekModel"`
	DeepSeekAPIKey   string `json:"deepseekApiKey,omitempty"` // masked

	// Search
	SearchMode    string  `json:"searchMode"` // bm25, vector, hybrid
	RerankEnabled bool    `json:"rerankEnabled"`
	RRFK          int     `json:"rrfK"`
	AbstractBoost float64 `json:"abstractBoost"`
	BM25K1        float64 `json:"bm25K1"`
	BM25B         float64 `json:"bm25B"`

	// Chunking
	ChunkMinChars          int     `json:"chunkMinChars"`
	ChunkMaxChars          int     `json:"chunkMaxChars"`
	ChunkOverlapChars      int     `json:"chunkOverlapChars"`
	ChunkSemanticThreshold float64 `json:"chunkSemanticThreshold"`

	// Upload
	UploadMaxSizeMB int `json:"uploadMaxSizeMb"`

	// Redis cache
	RedisEnabled  bool   `json:"redisEnabled"`
	RedisAddr     string `json:"redisAddr"`
	RedisPassword string `json:"redisPassword,omitempty"` // masked
	RedisDB       int    `json:"redisDB"`
	RedisPrefix   string `json:"redisPrefix"`
	RedisPoolSize int    `json:"redisPoolSize"`

	// Cache TTLs (seconds)
	CacheQueryTTL  int `json:"cacheQueryTTL"`
	CacheChunkTTL  int `json:"cacheChunkTTL"`
	CacheMetaTTL   int `json:"cacheMetaTTL"`
	CacheIndexTTL  int `json:"cacheIndexTTL"`
	CacheKBListTTL int `json:"cacheKBListTTL"`

	// Read-only metadata
	ConfigPath      string `json:"configPath"`
	RestartRequired bool   `json:"restartRequired,omitempty"` // true when changes pending that need restart
}

// handleConfigGet returns the current runtime configuration.
func (srv *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg := srv.Config()
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	mask := func(v string) string {
		// Always return "***" to avoid leaking whether a key is configured.
		return "***"
	}

	resp := configAPIResponse{
		DataDir:   cfg.DataDir,
		DefaultKB: cfg.DefaultKB,

		EmbedEndpoint: cfg.EmbedEndpoint,
		EmbedModel:    cfg.EmbedModel,
		EmbedDim:      cfg.EmbedDim,
		EmbedAPIKey:   mask(cfg.EmbedAPIKey),

		RerankEndpoint:       cfg.RerankEndpoint,
		RerankModel:          cfg.RerankModel,
		RerankAPIKey:         mask(cfg.RerankAPIKey),
		RerankTimeout:        cfg.RerankTimeout,
		RerankCandidateLimit: cfg.RerankCandidateLimit,

		GPUSchedulerEnabled:           cfg.GPUSchedulerEnabled,
		GPUSchedulerTimeout:           cfg.GPUSchedulerTimeout,
		GPUSchedulerEmbeddingSleepURL: cfg.GPUSchedulerEmbeddingSleepURL,
		GPUSchedulerRerankerSleepURL:  cfg.GPUSchedulerRerankerSleepURL,
		GPUSchedulerDocParserSleepURL: cfg.GPUSchedulerDocParserSleepURL,

		DocParserEndpoint: cfg.DocParserEndpoint,
		DocParserAPIKey:   mask(cfg.DocParserAPIKey),
		DocParserTimeout:  cfg.DocParserTimeout,

		ManagePort:   cfg.ManagePort,
		ServePort:    cfg.ServePort,
		ServeBaseURL: cfg.ServeBaseURL,

		LogFile:  cfg.LogFile,
		LogLevel: cfg.LogLevel,

		MySQLDSN:        mask(cfg.MySQLDSN),
		MySQLUser:       cfg.MySQLUser,
		MySQLPassword:   mask(cfg.MySQLPassword),
		MySQLHost:       cfg.MySQLHost,
		MySQLPort:       cfg.MySQLPort,
		MySQLDatabase:   cfg.MySQLDatabase,
		MySQLSocketPath: cfg.MySQLSocketPath,

		APIToken: mask(cfg.APIToken),

		DeepSeekEndpoint: cfg.DeepSeekEndpoint,
		DeepSeekModel:    cfg.DeepSeekModel,
		DeepSeekAPIKey:   mask(cfg.DeepSeekAPIKey),

		SearchMode:    string(srv.Service().GetSearchMode()),
		RerankEnabled: srv.Service().GetRerankEnabled(),
		RRFK:          srv.Service().GetRRFK(),
		AbstractBoost: cfg.AbstractBoost,
		BM25K1:        srv.Service().GetBM25K1(),
		BM25B:         srv.Service().GetBM25B(),

		ChunkMinChars:          srv.Service().GetChunkMinChars(),
		ChunkMaxChars:          srv.Service().GetChunkMaxChars(),
		ChunkOverlapChars:      srv.Service().GetChunkOverlapChars(),
		ChunkSemanticThreshold: srv.Service().GetChunkSemanticThreshold(),

		UploadMaxSizeMB: srv.Service().GetUploadMaxSizeMB(),

		RedisEnabled:  cfg.RedisEnabled,
		RedisAddr:     cfg.RedisAddr,
		RedisPassword: mask(cfg.RedisPassword),
		RedisDB:       cfg.RedisDB,
		RedisPrefix:   cfg.RedisPrefix,
		RedisPoolSize: cfg.RedisPoolSize,

		CacheQueryTTL:  cfg.CacheQueryTTL,
		CacheChunkTTL:  cfg.CacheChunkTTL,
		CacheMetaTTL:   cfg.CacheMetaTTL,
		CacheIndexTTL:  cfg.CacheIndexTTL,
		CacheKBListTTL: cfg.CacheKBListTTL,

		ConfigPath: srv.ConfigPath(),
	}

	writeJSON(w, http.StatusOK, resp)
}

// configUpdateRequest is the JSON body for PUT /api/config.
// Only the fields that are safe to change at runtime are accepted.
type configUpdateRequest struct {
	// Embedder (hot-reloadable)
	EmbedEndpoint *string `json:"embedEndpoint,omitempty"`
	EmbedModel    *string `json:"embedModel,omitempty"`
	EmbedDim      *int    `json:"embedDim,omitempty"`
	EmbedAPIKey   *string `json:"embedApiKey,omitempty"`

	// Reranker (hot-reloadable)
	RerankEndpoint       *string `json:"rerankEndpoint,omitempty"`
	RerankModel          *string `json:"rerankModel,omitempty"`
	RerankAPIKey         *string `json:"rerankApiKey,omitempty"`
	RerankTimeout        *string `json:"rerankTimeout,omitempty"`
	RerankCandidateLimit *int    `json:"rerankCandidateLimit,omitempty"`

	// GPU Scheduler (hot-reloadable)
	GPUSchedulerEnabled           *bool   `json:"gpuSchedulerEnabled,omitempty"`
	GPUSchedulerTimeout           *string `json:"gpuSchedulerTimeout,omitempty"`
	GPUSchedulerEmbeddingSleepURL *string `json:"gpuSchedulerEmbeddingSleepUrl,omitempty"`
	GPUSchedulerRerankerSleepURL  *string `json:"gpuSchedulerRerankerSleepUrl,omitempty"`
	GPUSchedulerDocParserSleepURL *string `json:"gpuSchedulerDocParserSleepUrl,omitempty"`

	// Doc Parser (hot-reloadable)
	DocParserEndpoint *string `json:"docParserEndpoint,omitempty"`
	DocParserAPIKey   *string `json:"docParserApiKey,omitempty"`
	DocParserTimeout  *string `json:"docParserTimeout,omitempty"`

	// Logging (hot-reloadable)
	LogLevel *string `json:"logLevel,omitempty"`

	// Auth (hot-reloadable)
	APIToken *string `json:"apiToken,omitempty"`

	// DeepSeek LLM (hot-reloadable — rebuilds completer and rewriter)
	DeepSeekEndpoint *string `json:"deepseekEndpoint,omitempty"`
	DeepSeekModel    *string `json:"deepseekModel,omitempty"`
	DeepSeekAPIKey   *string `json:"deepseekApiKey,omitempty"`

	// Search (hot-reloadable)
	SearchMode    *string  `json:"searchMode,omitempty"`
	RerankEnabled *bool    `json:"rerankEnabled,omitempty"`
	RRFK          *int     `json:"rrfK,omitempty"`
	AbstractBoost *float64 `json:"abstractBoost,omitempty"`
	BM25K1        *float64 `json:"bm25K1,omitempty"`
	BM25B         *float64 `json:"bm25B,omitempty"`

	// Chunking (hot-reloadable)
	ChunkMinChars          *int     `json:"chunkMinChars,omitempty"`
	ChunkMaxChars          *int     `json:"chunkMaxChars,omitempty"`
	ChunkOverlapChars      *int     `json:"chunkOverlapChars,omitempty"`
	ChunkSemanticThreshold *float64 `json:"chunkSemanticThreshold,omitempty"`

	// Upload (hot-reloadable)
	UploadMaxSizeMB *int `json:"uploadMaxSizeMb,omitempty"`

	// Cache TTLs (hot-reloadable — updates runtime TTLs without restart)
	CacheQueryTTL  *int `json:"cacheQueryTTL,omitempty"`
	CacheChunkTTL  *int `json:"cacheChunkTTL,omitempty"`
	CacheMetaTTL   *int `json:"cacheMetaTTL,omitempty"`
	CacheIndexTTL  *int `json:"cacheIndexTTL,omitempty"`
	CacheKBListTTL *int `json:"cacheKBListTTL,omitempty"`
}

// handleConfigPut updates runtime configuration.
// Some changes require a restart (ports, data dir, MySQL); those are persisted
// to the config file but require a restart to take effect. Safe changes are
// applied immediately and persisted.
func (srv *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")

	var req configUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	cfg := srv.Config()
	if cfg == nil {
		writeError(w, http.StatusInternalServerError, "no config loaded")
		return
	}

	restartRequired := false
	changes := []string{}

	// ── Hot-reloadable: Embedder ──
	if req.EmbedEndpoint != nil {
		cfg.EmbedEndpoint = *req.EmbedEndpoint
		changes = append(changes, "embedEndpoint")
		srv.reloadEmbedder(cfg)
	}
	if req.EmbedModel != nil {
		cfg.EmbedModel = *req.EmbedModel
		srv.reloadEmbedder(cfg)
	}
	if req.EmbedDim != nil {
		cfg.EmbedDim = *req.EmbedDim
		srv.reloadEmbedder(cfg)
	}
	if req.EmbedAPIKey != nil {
		if *req.EmbedAPIKey != "" && *req.EmbedAPIKey != "***" {
			cfg.EmbedAPIKey = *req.EmbedAPIKey
			srv.reloadEmbedder(cfg)
		}
	}

	// ── Hot-reloadable: Reranker ──
	if req.RerankEndpoint != nil {
		cfg.RerankEndpoint = *req.RerankEndpoint
		changes = append(changes, "rerankEndpoint")
		srv.reloadReranker(cfg)
	}
	if req.RerankModel != nil {
		cfg.RerankModel = *req.RerankModel
		srv.reloadReranker(cfg)
	}
	if req.RerankAPIKey != nil {
		if *req.RerankAPIKey != "" && *req.RerankAPIKey != "***" {
			cfg.RerankAPIKey = *req.RerankAPIKey
			srv.reloadReranker(cfg)
		}
	}
	if req.RerankTimeout != nil {
		cfg.RerankTimeout = *req.RerankTimeout
		srv.reloadReranker(cfg)
	}
	if req.RerankCandidateLimit != nil {
		cfg.RerankCandidateLimit = *req.RerankCandidateLimit
		srv.Service().SetRerankCandidateLimit(*req.RerankCandidateLimit)
		changes = append(changes, "rerankCandidateLimit")
	}

	// ── Hot-reloadable: GPU Scheduler ──
	if req.GPUSchedulerEnabled != nil {
		cfg.GPUSchedulerEnabled = *req.GPUSchedulerEnabled
		changes = append(changes, "gpuSchedulerEnabled")
		srv.reloadGPUScheduler(cfg)
	}
	if req.GPUSchedulerTimeout != nil {
		cfg.GPUSchedulerTimeout = *req.GPUSchedulerTimeout
		srv.reloadGPUScheduler(cfg)
	}
	if req.GPUSchedulerEmbeddingSleepURL != nil {
		cfg.GPUSchedulerEmbeddingSleepURL = *req.GPUSchedulerEmbeddingSleepURL
		srv.reloadGPUScheduler(cfg)
	}
	if req.GPUSchedulerRerankerSleepURL != nil {
		cfg.GPUSchedulerRerankerSleepURL = *req.GPUSchedulerRerankerSleepURL
		srv.reloadGPUScheduler(cfg)
	}
	if req.GPUSchedulerDocParserSleepURL != nil {
		cfg.GPUSchedulerDocParserSleepURL = *req.GPUSchedulerDocParserSleepURL
		srv.reloadGPUScheduler(cfg)
	}

	// ── Hot-reloadable: Doc Parser ──
	if req.DocParserEndpoint != nil {
		cfg.DocParserEndpoint = *req.DocParserEndpoint
		changes = append(changes, "docParserEndpoint")
		srv.reloadDocParser(cfg)
	}
	if req.DocParserAPIKey != nil {
		if *req.DocParserAPIKey != "" && *req.DocParserAPIKey != "***" {
			cfg.DocParserAPIKey = *req.DocParserAPIKey
			srv.reloadDocParser(cfg)
		}
	}
	if req.DocParserTimeout != nil {
		cfg.DocParserTimeout = *req.DocParserTimeout
		srv.reloadDocParser(cfg)
	}

	// ── Hot-reloadable: Log level ──
	if req.LogLevel != nil {
		cfg.LogLevel = *req.LogLevel
		srv.Logger().SetLevel(logging.ParseLevel(*req.LogLevel))
		changes = append(changes, "logLevel")
	}

	// ── Hot-reloadable: API Token ──
	if req.APIToken != nil {
		if *req.APIToken != "" && *req.APIToken != "***" {
			cfg.APIToken = *req.APIToken
			changes = append(changes, "apiToken")
		}
	}

	// ── Hot-reloadable: Search parameters ──
	if req.SearchMode != nil {
		cfg.SearchMode = *req.SearchMode
		srv.Service().SetSearchMode(knowledge.SearchMode(*req.SearchMode))
		changes = append(changes, "searchMode")
	}
	if req.RerankEnabled != nil {
		cfg.RerankEnabled = *req.RerankEnabled
		srv.Service().SetRerankEnabled(*req.RerankEnabled)
		changes = append(changes, "rerankEnabled")
	}
	if req.RRFK != nil {
		cfg.RRFK = *req.RRFK
		srv.Service().SetRRFK(*req.RRFK)
		changes = append(changes, "rrfK")
	}
	if req.AbstractBoost != nil {
		cfg.AbstractBoost = *req.AbstractBoost
		srv.Service().SetAbstractBoost(*req.AbstractBoost)
		changes = append(changes, "abstractBoost")
	}
	if req.BM25K1 != nil {
		cfg.BM25K1 = *req.BM25K1
		srv.Service().SetBM25K1(*req.BM25K1)
		changes = append(changes, "bm25K1")
	}
	if req.BM25B != nil {
		cfg.BM25B = *req.BM25B
		srv.Service().SetBM25B(*req.BM25B)
		changes = append(changes, "bm25B")
	}

	// ── Hot-reloadable: Chunking ──
	if req.ChunkMinChars != nil {
		cfg.ChunkMinChars = *req.ChunkMinChars
		srv.Service().SetChunkMinChars(*req.ChunkMinChars)
		changes = append(changes, "chunkMinChars")
	}
	if req.ChunkMaxChars != nil {
		cfg.ChunkMaxChars = *req.ChunkMaxChars
		srv.Service().SetChunkMaxChars(*req.ChunkMaxChars)
		changes = append(changes, "chunkMaxChars")
	}
	if req.ChunkOverlapChars != nil {
		cfg.ChunkOverlapChars = *req.ChunkOverlapChars
		srv.Service().SetChunkOverlapChars(*req.ChunkOverlapChars)
		changes = append(changes, "chunkOverlapChars")
	}
	if req.ChunkSemanticThreshold != nil {
		cfg.ChunkSemanticThreshold = *req.ChunkSemanticThreshold
		srv.Service().SetChunkSemanticThreshold(*req.ChunkSemanticThreshold)
		changes = append(changes, "chunkSemanticThreshold")
	}

	// ── Hot-reloadable: Upload max size ──
	if req.UploadMaxSizeMB != nil {
		cfg.UploadMaxSizeMB = *req.UploadMaxSizeMB
		srv.Service().SetUploadMaxSizeMB(*req.UploadMaxSizeMB)
		changes = append(changes, "uploadMaxSizeMb")
	}

	// ── Hot-reloadable: Cache TTLs ──
	if req.CacheQueryTTL != nil {
		cfg.CacheQueryTTL = *req.CacheQueryTTL
		srv.Service().SetCacheQueryTTL(time.Duration(*req.CacheQueryTTL) * time.Second)
		changes = append(changes, "cacheQueryTTL")
	}
	if req.CacheChunkTTL != nil {
		cfg.CacheChunkTTL = *req.CacheChunkTTL
		srv.Service().SetCacheChunkTTL(time.Duration(*req.CacheChunkTTL) * time.Second)
		changes = append(changes, "cacheChunkTTL")
	}
	if req.CacheMetaTTL != nil {
		cfg.CacheMetaTTL = *req.CacheMetaTTL
		srv.Service().SetCacheMetaTTL(time.Duration(*req.CacheMetaTTL) * time.Second)
		changes = append(changes, "cacheMetaTTL")
	}
	if req.CacheIndexTTL != nil {
		cfg.CacheIndexTTL = *req.CacheIndexTTL
		srv.Service().SetCacheIndexTTL(time.Duration(*req.CacheIndexTTL) * time.Second)
		changes = append(changes, "cacheIndexTTL")
	}
	if req.CacheKBListTTL != nil {
		cfg.CacheKBListTTL = *req.CacheKBListTTL
		srv.Service().SetCacheKBListTTL(time.Duration(*req.CacheKBListTTL) * time.Second)
		changes = append(changes, "cacheKBListTTL")
	}

	// ── Hot-reloadable: DeepSeek LLM ──
	if req.DeepSeekEndpoint != nil {
		cfg.DeepSeekEndpoint = *req.DeepSeekEndpoint
		changes = append(changes, "deepseekEndpoint")
		srv.reloadDeepSeek(cfg)
	}
	if req.DeepSeekModel != nil {
		cfg.DeepSeekModel = *req.DeepSeekModel
		srv.reloadDeepSeek(cfg)
	}
	if req.DeepSeekAPIKey != nil {
		// Allow empty string to clear the key; skip masked sentinel.
		if *req.DeepSeekAPIKey == "" {
			cfg.DeepSeekAPIKey = ""
			changes = append(changes, "deepseekApiKey")
		} else if *req.DeepSeekAPIKey != "***" {
			cfg.DeepSeekAPIKey = *req.DeepSeekAPIKey
			changes = append(changes, "deepseekApiKey")
		}
		srv.reloadDeepSeek(cfg)
	}

	// ── Persist to config file ──
	configPath := srv.ConfigPath()
	if configPath == "" {
		configPath = filepath.Join(srv.DataDir(), "..", "knowledge-mcp.toml")
	}
	if err := config.Save(configPath, cfg); err != nil {
		log.Errorf("ConfigPut: failed to save config to %s: %v", configPath, err)
		// Changes are still applied in memory even if save fails.
	}

	log.Infof("ConfigPut: updated %v (restartRequired=%v)", changes, restartRequired)
	writeJSON(w, http.StatusOK, map[string]any{
		"message":         "configuration updated",
		"changes":         changes,
		"restartRequired": restartRequired,
	})
}

// ── Hot-reload helpers ──

func (srv *Server) reloadEmbedder(cfg *config.Config) {
	if cfg.EmbedEndpoint == "" {
		return
	}
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
	opts = append(opts, knowledge.WithEmbedLogger(srv.Logger().WithModule("embed")))
	srv.SetEmbedder(knowledge.NewOpenAIEmbedder(opts...))
	srv.Logger().Infof("embedder reloaded: %s (model=%s)", cfg.EmbedEndpoint, model)
}

func (srv *Server) reloadReranker(cfg *config.Config) {
	if cfg.RerankEndpoint == "" {
		return
	}
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
		}
	}
	opts = append(opts, knowledge.WithRerankLogger(srv.Logger().WithModule("rerank")))
	srv.SetReranker(knowledge.NewInfinityReranker(opts...))
	srv.Service().SetRerankCandidateLimit(cfg.RerankCandidateLimit)
	srv.Logger().Infof("reranker reloaded: %s", cfg.RerankEndpoint)
}

func (srv *Server) reloadGPUScheduler(cfg *config.Config) {
	if !cfg.GPUSchedulerEnabled {
		srv.SetGPUScheduler(nil)
		knowledge.SetParserGPUScheduler(nil)
		return
	}
	var opts []knowledge.GPUSchedulerOption
	opts = append(opts, knowledge.WithSchedulerEnabled(true))
	opts = append(opts, knowledge.WithSchedulerLogger(srv.Logger().WithModule("gpu-scheduler")))
	if cfg.GPUSchedulerEmbeddingSleepURL != "" {
		opts = append(opts, knowledge.WithSchedulerEmbeddingSleepURL(cfg.GPUSchedulerEmbeddingSleepURL))
	}
	if cfg.GPUSchedulerRerankerSleepURL != "" {
		opts = append(opts, knowledge.WithSchedulerRerankerSleepURL(cfg.GPUSchedulerRerankerSleepURL))
	}
	if cfg.GPUSchedulerDocParserSleepURL != "" {
		opts = append(opts, knowledge.WithSchedulerDocParserSleepURL(cfg.GPUSchedulerDocParserSleepURL))
	}
	if cfg.GPUSchedulerTimeout != "" {
		if d, err := time.ParseDuration(cfg.GPUSchedulerTimeout); err == nil {
			opts = append(opts, knowledge.WithSchedulerTimeout(d))
		}
	}
	scheduler := knowledge.NewGPUScheduler(opts...)
	srv.SetGPUScheduler(scheduler)
	srv.Logger().Infof("GPU scheduler reloaded: %s", scheduler.Summary())
}

func (srv *Server) reloadDocParser(cfg *config.Config) {
	if cfg.DocParserEndpoint == "" {
		knowledge.SetDocParser(nil)
		return
	}
	var opts []knowledge.HTTPDocParserOption
	opts = append(opts, knowledge.WithParserEndpoint(cfg.DocParserEndpoint))
	if cfg.DocParserAPIKey != "" {
		opts = append(opts, knowledge.WithParserAPIKey(cfg.DocParserAPIKey))
	}
	if cfg.DocParserTimeout != "" {
		if d, err := time.ParseDuration(cfg.DocParserTimeout); err == nil {
			opts = append(opts, knowledge.WithParserTimeout(d))
		}
	}
	opts = append(opts, knowledge.WithParserLogger(srv.Logger().WithModule("doc-parser")))
	knowledge.SetDocParser(knowledge.NewHTTPDocParser(opts...))
	srv.Logger().Infof("doc parser reloaded: %s", cfg.DocParserEndpoint)
}

func (srv *Server) reloadDeepSeek(cfg *config.Config) {
	// If API key is empty, remove the LLM rewriter (fall back to synonym-only).
	if cfg.DeepSeekAPIKey == "" {
		srv.Service().SetLLMRewriter(nil)
		srv.Logger().Infof("deepseek: LLM rewriter removed (no API key)")
		return
	}

	completer := knowledge.NewDeepSeekCompleter(
		cfg.DeepSeekEndpoint,
		cfg.DeepSeekAPIKey,
		cfg.DeepSeekModel,
		knowledge.WithDeepSeekLogger(srv.Logger().WithModule("deepseek")),
	)
	// Default fallback is SynonymRewriter — consistent with main.go behavior.
	llmRewriter := knowledge.NewLLMQueryRewriter(completer)
	srv.Service().SetLLMRewriter(llmRewriter)
	srv.Logger().Infof("deepseek reloaded: endpoint=%s model=%s", cfg.DeepSeekEndpoint, cfg.DeepSeekModel)
}

// makeConfigPtr is a helper for int literal pointers.
func makeConfigPtr[T any](v T) *T { return &v }

// strPtr helper
func strPtr(s string) *string { return &s }

// intPtr helper
func intPtr(i int) *int { return &i }

// floatPtr helper
func floatPtr(f float64) *float64 { return &f }

// boolPtr helper
func boolPtr(b bool) *bool { return &b }

// parseIntParam parses an integer query parameter.
func parseIntParam(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return defaultVal
	}
	return n
}

// ── Tool Descriptions API ──

// toolDescriptionsResponse wraps current custom descriptions plus their
// built-in defaults so the UI can show both side-by-side.
type toolDescriptionsResponse struct {
	Custom   knowledge.ToolDescriptions `json:"custom"`   // user-overridden values (empty = use default)
	Defaults knowledge.ToolDescriptions `json:"defaults"` // hardcoded defaults for reference
}

func getDefaultToolDescriptions() knowledge.ToolDescriptions {
	return knowledge.ToolDescriptions{
		SearchDesc:       knowledge.DefaultSearchDesc,
		SearchKbNameDesc: knowledge.DefaultSearchKbNameDesc,
		ReadDesc:         knowledge.DefaultReadDesc,
		ReadKbNameDesc:   knowledge.DefaultReadKbNameDesc,
		ListDesc:         knowledge.DefaultListDesc,
		ListKBsDesc:      knowledge.DefaultListKBsDesc,
		UploadDesc:       knowledge.DefaultUploadDesc,
		RemoveDesc:       knowledge.DefaultRemoveDesc,
	}
}

func (srv *Server) handleToolDescriptionsGet(w http.ResponseWriter, r *http.Request) {
	resp := toolDescriptionsResponse{
		Custom:   srv.Service().GetToolDescriptions(),
		Defaults: getDefaultToolDescriptions(),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (srv *Server) handleToolDescriptionsPut(w http.ResponseWriter, r *http.Request) {
	var td knowledge.ToolDescriptions
	if err := json.NewDecoder(r.Body).Decode(&td); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Persist to runtime settings.
	srv.Service().SetToolDescriptions(td)

	// Persist to TOML config file.
	cfg := srv.Config()
	if cfg != nil {
		cfg.ToolSearchDesc = td.SearchDesc
		cfg.ToolSearchKbNameDesc = td.SearchKbNameDesc
		cfg.ToolReadDesc = td.ReadDesc
		cfg.ToolReadKbNameDesc = td.ReadKbNameDesc
		cfg.ToolListDesc = td.ListDesc
		cfg.ToolListKBsDesc = td.ListKBsDesc
		cfg.ToolUploadDesc = td.UploadDesc
		cfg.ToolRemoveDesc = td.RemoveDesc

		if srv.ConfigPath() != "" {
			if err := config.Save(srv.ConfigPath(), cfg); err != nil {
				srv.Logger().WithModule("manage").Warnf("tool-descriptions: save config failed: %v", err)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"warning": "工具描述已保存到配置文件。重启 MCP 服务后生效。",
	})
}

// handleRestart triggers a service restart. It returns immediately and then
// spawns a goroutine that waits 500ms before executing the restart — giving
// the HTTP response time to flush.
func (srv *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "正在重启 MCP 服务，请稍候…",
	})

	// Flush the response before we kill ourselves.
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	go func() {
		time.Sleep(500 * time.Millisecond)

		// Try service-manager.sh first (in the same dir as the binary).
		execPath, _ := os.Executable()
		scriptDir := filepath.Dir(execPath)
		script := filepath.Join(scriptDir, "service-manager.sh")

		if _, err := os.Stat(script); err == nil {
			log.Infof("restart: executing %s restart knowledge", script)
			cmd := exec.Command("bash", script, "restart", "knowledge")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				log.Errorf("restart: script failed: %v, falling back to exit", err)
				os.Exit(0)
			}
			return
		}

		log.Infof("restart: no service-manager.sh found, exiting (rely on process manager to restart)")
		os.Exit(0)
	}()
}
