package config

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

// Config holds all configuration for the knowledge-mcp server.
// Fields are populated from (in priority order):
//  1. knowledge-mcp.toml next to the executable (the single config source)
//  2. Environment variables (fallback when no TOML file exists)
//  3. Hard-coded defaults
type Config struct {
	DataDir                       string `toml:"data_dir"`
	DefaultKB                     string `toml:"default_kb"`
	EmbedEndpoint                 string `toml:"embed_endpoint"`
	EmbedModel                    string `toml:"embed_model"`
	EmbedDim                      int    `toml:"embed_dim"`
	EmbedAPIKey                   string `toml:"embed_api_key"`
	RerankEndpoint                string `toml:"rerank_endpoint"`
	RerankModel                   string `toml:"rerank_model"`
	RerankAPIKey                  string `toml:"rerank_api_key"`
	RerankTimeout                 string `toml:"rerank_timeout"`
	RerankCandidateLimit          int    `toml:"rerank_candidate_limit"`
	GPUSchedulerEnabled           bool   `toml:"gpu_scheduler_enabled"`
	GPUSchedulerTimeout           string `toml:"gpu_scheduler_timeout"`
	GPUSchedulerEmbeddingSleepURL string `toml:"gpu_scheduler_embedding_sleep_url"`
	GPUSchedulerRerankerSleepURL  string `toml:"gpu_scheduler_reranker_sleep_url"`
	GPUSchedulerDocParserSleepURL string `toml:"gpu_scheduler_doc_parser_sleep_url"`
	MinerUEnabled                 bool   `toml:"mineru_enabled"`

	// APIToken is an optional Bearer token for management API authentication.
	// When empty, the management API is open (no auth required).
	APIToken string `toml:"api_token"`

	// DeepSeekEndpoint is the base URL for the DeepSeek LLM API, used for
	// LLM-based query rewriting (LLMQueryRewriter). Defaults to the public
	// DeepSeek endpoint when empty.
	DeepSeekEndpoint string `toml:"deepseek_endpoint"`
	// DeepSeekAPIKey is the Bearer token for the DeepSeek API. Leave empty to
	// skip LLM query rewriting (SynonymRewriter-only mode).
	DeepSeekAPIKey string `toml:"deepseek_api_key"`
	// DeepSeekModel selects the DeepSeek model for query rewriting.
	// Default: "deepseek-flash".
	DeepSeekModel string `toml:"deepseek_model"`

	// DocParserEndpoint is the URL of an external HTTP API for document parsing.
	// When set, ParseFile will send documents to this API before falling back
	// to the local tabula parser. Example: "http://localhost:8000/parse"
	DocParserEndpoint string `toml:"doc_parser_endpoint"`
	// DocParserAPIKey is an optional bearer token sent to the parser API.
	DocParserAPIKey string `toml:"doc_parser_api_key"`
	// DocParserTimeout is the HTTP timeout for the parser API (e.g. "600s").
	DocParserTimeout string `toml:"doc_parser_timeout"`

	ManagePort   string `toml:"manage_port"`
	ServePort    string `toml:"serve_port"`
	ServeBaseURL string `toml:"serve_base_url"`
	LogFile      string `toml:"log_file"`
	LogLevel     string `toml:"log_level"`

	// ── Runtime search parameters (hot-reloadable, persisted) ──
	SearchMode    string  `toml:"search_mode"`
	RerankEnabled bool    `toml:"rerank_enabled"`
	RRFK          int     `toml:"rrf_k"`
	BM25K1        float64 `toml:"bm25_k1"`
	BM25B         float64 `toml:"bm25_b"`
	AbstractBoost float64 `toml:"abstract_boost"`

	// ── Runtime chunking parameters (hot-reloadable, persisted) ──
	ChunkMinChars          int     `toml:"chunk_min_chars"`
	ChunkMaxChars          int     `toml:"chunk_max_chars"`
	ChunkOverlapChars      int     `toml:"chunk_overlap_chars"`
	ChunkSemanticThreshold float64 `toml:"chunk_semantic_threshold"`

	// ── Runtime upload parameters ──
	UploadMaxSizeMB int `toml:"upload_max_size_mb"`

	// MySQL backend configuration (required).
	MySQLDSN        string `toml:"mysql_dsn"`
	MySQLUser       string `toml:"mysql_user"`
	MySQLPassword   string `toml:"mysql_password"`
	MySQLHost       string `toml:"mysql_host"`
	MySQLPort       string `toml:"mysql_port"`
	MySQLDatabase   string `toml:"mysql_database"`
	MySQLSocketPath string `toml:"mysql_socket_path"`

	// ── Redis cache configuration ──
	RedisEnabled  bool   `toml:"redis_enabled"`
	RedisAddr     string `toml:"redis_addr"`
	RedisPassword string `toml:"redis_password"`
	RedisDB       int    `toml:"redis_db"`
	RedisPrefix   string `toml:"redis_prefix"`
	RedisPoolSize int    `toml:"redis_pool_size"`

	// ── Cache TTLs (seconds) ──
	CacheQueryTTL  int `toml:"cache_query_ttl"`  // search results, default 300
	CacheChunkTTL  int `toml:"cache_chunk_ttl"`  // chunk text, default 0 (no expiry)
	CacheMetaTTL   int `toml:"cache_meta_ttl"`   // doc metadata, default 0
	CacheIndexTTL  int `toml:"cache_index_ttl"`  // chunks index, default 0
	CacheKBListTTL int `toml:"cache_kblist_ttl"` // KB list, default 60

	// ── MCP Tool Descriptions (customisable via Web UI, effective after restart) ──
	ToolSearchDesc       string `toml:"tool_search_desc"`
	ToolSearchKbNameDesc string `toml:"tool_search_kbname_desc"`
	ToolReadDesc         string `toml:"tool_read_desc"`
	ToolReadKbNameDesc   string `toml:"tool_read_kbname_desc"`
	ToolListDesc         string `toml:"tool_list_desc"`
	ToolListKBsDesc      string `toml:"tool_list_kbs_desc"`
	ToolUploadDesc       string `toml:"tool_upload_desc"`
	ToolRemoveDesc       string `toml:"tool_remove_desc"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		DataDir:                       "",
		DefaultKB:                     "",
		EmbedEndpoint:                 "",
		EmbedModel:                    "bge-m3",
		EmbedDim:                      0,
		EmbedAPIKey:                   "",
		RerankEndpoint:                "",
		RerankModel:                   "gte-multilingual-reranker-base",
		RerankAPIKey:                  "",
		RerankTimeout:                 "30s",
		RerankCandidateLimit:          100,
		GPUSchedulerEnabled:           false,
		GPUSchedulerTimeout:           "30s",
		GPUSchedulerEmbeddingSleepURL: "",
		GPUSchedulerRerankerSleepURL:  "",
		GPUSchedulerDocParserSleepURL: "",
		MinerUEnabled:                 true,
		DocParserEndpoint:             "",
		DocParserAPIKey:               "",
		DocParserTimeout:              "600s",
		DeepSeekEndpoint:             "https://api.deepseek.com/chat/completions",
		DeepSeekAPIKey:               "",
		DeepSeekModel:                "deepseek-flash",
		ManagePort:                    "8085",
		ServePort:                     "8086",
		ServeBaseURL:                  "",
		LogFile:                       "",
		LogLevel:                      "info",

		SearchMode:             "hybrid",
		RerankEnabled:          true,
		RRFK:                   60,
		BM25K1:                 1.2,
		BM25B:                  0.75,
		AbstractBoost:          1.1,
		ChunkMinChars:          200,
		ChunkMaxChars:          2000,
		ChunkOverlapChars:      200,
		ChunkSemanticThreshold: 0.75,
		UploadMaxSizeMB:        500,

		RedisEnabled:   false,
		RedisAddr:      "127.0.0.1:6379",
		RedisPassword:  "",
		RedisDB:        0,
		RedisPrefix:    "kmcp:",
		RedisPoolSize:  10,
		CacheQueryTTL:  300,
		CacheChunkTTL:  0,
		CacheMetaTTL:   0,
		CacheIndexTTL:  0,
		CacheKBListTTL: 60,
	}
}

// Load reads a Config from a TOML file. Returns nil, nil when the file does not
// exist (caller should fall back to env vars or defaults).
func Load(path string) (*Config, error) {
	var cfg Config
	_, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &cfg, nil
}

// Save writes a Config to a TOML file.
func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

// LoadWithEnvFallback loads config from the given TOML path. If the file does
// not exist, it builds a Config from environment variables. Missing env vars
// fall back to DefaultConfig() values.
func LoadWithEnvFallback(path string) *Config {
	cfg, err := Load(path)
	if err == nil && cfg != nil {
		return cfg
	}

	def := DefaultConfig()
	return &Config{
		DataDir:                       envOr("KNOWLEDGE_MCP_DATA_DIR", def.DataDir),
		DefaultKB:                     envOr("KNOWLEDGE_MCP_DEFAULT_KB", def.DefaultKB),
		EmbedEndpoint:                 envOr("EMBED_API_ENDPOINT", def.EmbedEndpoint),
		EmbedModel:                    envOr("EMBED_MODEL", def.EmbedModel),
		EmbedDim:                      envIntOr("EMBED_DIM", def.EmbedDim),
		EmbedAPIKey:                   envOr("EMBED_API_KEY", def.EmbedAPIKey),
		RerankEndpoint:                envOr("RERANK_API_ENDPOINT", def.RerankEndpoint),
		RerankModel:                   envOr("RERANK_MODEL", def.RerankModel),
		RerankAPIKey:                  envOr("RERANK_API_KEY", def.RerankAPIKey),
		RerankTimeout:                 envOr("RERANK_TIMEOUT", def.RerankTimeout),
		RerankCandidateLimit:          envIntOr("RERANK_CANDIDATE_LIMIT", def.RerankCandidateLimit),
		GPUSchedulerEnabled:           os.Getenv("GPU_SCHEDULER_ENABLED") == "true" || os.Getenv("GPU_SCHEDULER_ENABLED") == "1",
		GPUSchedulerTimeout:           envOr("GPU_SCHEDULER_TIMEOUT", def.GPUSchedulerTimeout),
		GPUSchedulerEmbeddingSleepURL: envOr("GPU_SCHEDULER_EMBEDDING_SLEEP_URL", def.GPUSchedulerEmbeddingSleepURL),
		GPUSchedulerRerankerSleepURL:  envOr("GPU_SCHEDULER_RERANKER_SLEEP_URL", def.GPUSchedulerRerankerSleepURL),
		GPUSchedulerDocParserSleepURL: envOr("GPU_SCHEDULER_DOC_PARSER_SLEEP_URL", def.GPUSchedulerDocParserSleepURL),
		MinerUEnabled:                 os.Getenv("MINERU_ENABLED") != "false",
		DocParserEndpoint:             os.Getenv("DOC_PARSER_ENDPOINT"),
		DocParserAPIKey:               os.Getenv("DOC_PARSER_API_KEY"),
		DocParserTimeout:              envOr("DOC_PARSER_TIMEOUT", def.DocParserTimeout),
		DeepSeekEndpoint:              envOr("DEEPSEEK_ENDPOINT", def.DeepSeekEndpoint),
		DeepSeekAPIKey:                envOr("DEEPSEEK_API_KEY", def.DeepSeekAPIKey),
		DeepSeekModel:                 envOr("DEEPSEEK_MODEL", def.DeepSeekModel),
		ManagePort:                    envOr("MANAGE_PORT", def.ManagePort),
		ServePort:                     envOr("KNOWLEDGE_MCP_SERVE_PORT", def.ServePort),
		ServeBaseURL:                  envOr("KNOWLEDGE_MCP_SERVE_BASE_URL", def.ServeBaseURL),
		LogFile:                       envOr("KNOWLEDGE_MCP_LOG_FILE", def.LogFile),
		LogLevel:                      envOr("KNOWLEDGE_MCP_LOG_LEVEL", def.LogLevel),
		MySQLDSN:                      envOr("MYSQL_DSN", ""),
		MySQLUser:                     envOr("MYSQL_USER", ""),
		MySQLPassword:                 os.Getenv("MYSQL_PASSWORD"),
		MySQLHost:                     envOr("MYSQL_HOST", ""),
		MySQLPort:                     envOr("MYSQL_PORT", ""),
		MySQLDatabase:                 envOr("MYSQL_DATABASE", ""),
		MySQLSocketPath:               envOr("MYSQL_SOCKET_PATH", ""),

		RedisEnabled:   os.Getenv("REDIS_ENABLED") == "true" || os.Getenv("REDIS_ENABLED") == "1",
		RedisAddr:      envOr("REDIS_ADDR", def.RedisAddr),
		RedisPassword:  envOr("REDIS_PASSWORD", def.RedisPassword),
		RedisDB:        envIntOr("REDIS_DB", def.RedisDB),
		RedisPrefix:    envOr("REDIS_PREFIX", def.RedisPrefix),
		RedisPoolSize:  envIntOr("REDIS_POOL_SIZE", def.RedisPoolSize),
		CacheQueryTTL:  envIntOr("CACHE_QUERY_TTL", def.CacheQueryTTL),
		CacheChunkTTL:  envIntOr("CACHE_CHUNK_TTL", def.CacheChunkTTL),
		CacheMetaTTL:   envIntOr("CACHE_META_TTL", def.CacheMetaTTL),
		CacheIndexTTL:  envIntOr("CACHE_INDEX_TTL", def.CacheIndexTTL),
		CacheKBListTTL: envIntOr("CACHE_KBLIST_TTL", def.CacheKBListTTL),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
