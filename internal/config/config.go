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
	DataDir              string `toml:"data_dir"`
	DefaultKB            string `toml:"default_kb"`
	EmbedEndpoint        string `toml:"embed_endpoint"`
	EmbedModel           string `toml:"embed_model"`
	EmbedDim             int    `toml:"embed_dim"`
	EmbedAPIKey          string `toml:"embed_api_key"`
	RerankEndpoint       string `toml:"rerank_endpoint"`
	RerankModel          string `toml:"rerank_model"`
	RerankAPIKey         string `toml:"rerank_api_key"`
	RerankTimeout        string `toml:"rerank_timeout"`
	RerankCandidateLimit int    `toml:"rerank_candidate_limit"`
	GPUSchedulerEnabled           bool   `toml:"gpu_scheduler_enabled"`
	GPUSchedulerTimeout           string `toml:"gpu_scheduler_timeout"`
	GPUSchedulerEmbeddingSleepURL string `toml:"gpu_scheduler_embedding_sleep_url"`
	GPUSchedulerRerankerSleepURL  string `toml:"gpu_scheduler_reranker_sleep_url"`
	MinerUEnabled                bool   `toml:"mineru_enabled"`

	// DocParserEndpoint is the URL of an external HTTP API for document parsing.
	// When set, ParseFile will send documents to this API before falling back
	// to the local tabula parser. Example: "http://localhost:8000/parse"
	DocParserEndpoint string `toml:"doc_parser_endpoint"`
	// DocParserAPIKey is an optional bearer token sent to the parser API.
	DocParserAPIKey string `toml:"doc_parser_api_key"`
	// DocParserTimeout is the HTTP timeout for the parser API (e.g. "120s").
	DocParserTimeout string `toml:"doc_parser_timeout"`

	ManagePort                    string `toml:"manage_port"`
	ServePort            string `toml:"serve_port"`
	ServeBaseURL         string `toml:"serve_base_url"`
	LogFile              string `toml:"log_file"`
	LogLevel             string `toml:"log_level"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		DataDir:              "",
		DefaultKB:            "",
		EmbedEndpoint:        "",
		EmbedModel:           "bge-m3",
		EmbedDim:             0,
		EmbedAPIKey:          "",
		RerankEndpoint:       "",
		RerankModel:          "gte-multilingual-reranker-base",
		RerankAPIKey:         "",
		RerankTimeout:        "30s",
		RerankCandidateLimit: 100,
		GPUSchedulerEnabled:           false,
		GPUSchedulerTimeout:           "30s",
		GPUSchedulerEmbeddingSleepURL: "",
		GPUSchedulerRerankerSleepURL:  "",
		MinerUEnabled:         true,
		DocParserEndpoint:     "",
		DocParserAPIKey:       "",
		DocParserTimeout:      "120s",
		ManagePort:            "8085",
		ServePort:            "8086",
		ServeBaseURL:         "",
		LogFile:              "",
		LogLevel:             "info",
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
		DataDir:             envOr("KNOWLEDGE_MCP_DATA_DIR", def.DataDir),
		DefaultKB:           envOr("KNOWLEDGE_MCP_DEFAULT_KB", def.DefaultKB),
		EmbedEndpoint:       envOr("EMBED_API_ENDPOINT", def.EmbedEndpoint),
		EmbedModel:          envOr("EMBED_MODEL", def.EmbedModel),
		EmbedDim:            envIntOr("EMBED_DIM", def.EmbedDim),
		EmbedAPIKey:         envOr("EMBED_API_KEY", def.EmbedAPIKey),
		RerankEndpoint:      envOr("RERANK_API_ENDPOINT", def.RerankEndpoint),
		RerankModel:         envOr("RERANK_MODEL", def.RerankModel),
		RerankAPIKey:        envOr("RERANK_API_KEY", def.RerankAPIKey),
		RerankTimeout:       envOr("RERANK_TIMEOUT", def.RerankTimeout),
		RerankCandidateLimit: envIntOr("RERANK_CANDIDATE_LIMIT", def.RerankCandidateLimit),
		GPUSchedulerEnabled:            os.Getenv("GPU_SCHEDULER_ENABLED") == "true" || os.Getenv("GPU_SCHEDULER_ENABLED") == "1",
		GPUSchedulerTimeout:            envOr("GPU_SCHEDULER_TIMEOUT", def.GPUSchedulerTimeout),
		GPUSchedulerEmbeddingSleepURL:  envOr("GPU_SCHEDULER_EMBEDDING_SLEEP_URL", def.GPUSchedulerEmbeddingSleepURL),
		GPUSchedulerRerankerSleepURL:   envOr("GPU_SCHEDULER_RERANKER_SLEEP_URL", def.GPUSchedulerRerankerSleepURL),
		MinerUEnabled:                os.Getenv("MINERU_ENABLED") != "false",
		DocParserEndpoint:            os.Getenv("DOC_PARSER_ENDPOINT"),
		DocParserAPIKey:              os.Getenv("DOC_PARSER_API_KEY"),
		DocParserTimeout:             envOr("DOC_PARSER_TIMEOUT", def.DocParserTimeout),
		ManagePort:                   envOr("MANAGE_PORT", def.ManagePort),
		ServePort:           envOr("KNOWLEDGE_MCP_SERVE_PORT", def.ServePort),
		ServeBaseURL:        envOr("KNOWLEDGE_MCP_SERVE_BASE_URL", def.ServeBaseURL),
		LogFile:             envOr("KNOWLEDGE_MCP_LOG_FILE", def.LogFile),
		LogLevel:            envOr("KNOWLEDGE_MCP_LOG_LEVEL", def.LogLevel),
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
