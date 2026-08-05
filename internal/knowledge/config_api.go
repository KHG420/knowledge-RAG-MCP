package knowledge

import (
	"knowledge-mcp/internal/config"
)

// ── Config API types (shared between manage/ handlers_config.go and tests) ──

// configAPIResponse is the JSON shape returned by GET /api/config.
type configAPIResponse struct {
	DataDir   string `json:"dataDir"`
	DefaultKB string `json:"defaultKB"`

	EmbedEndpoint string `json:"embedEndpoint"`
	EmbedModel    string `json:"embedModel"`
	EmbedDim      int    `json:"embedDim"`
	EmbedAPIKey   string `json:"embedApiKey,omitempty"`

	RerankEndpoint       string `json:"rerankEndpoint"`
	RerankModel          string `json:"rerankModel"`
	RerankAPIKey         string `json:"rerankApiKey,omitempty"`
	RerankTimeout        string `json:"rerankTimeout"`
	RerankCandidateLimit int    `json:"rerankCandidateLimit"`

	GPUSchedulerEnabled           bool   `json:"gpuSchedulerEnabled"`
	GPUSchedulerTimeout           string `json:"gpuSchedulerTimeout"`
	GPUSchedulerEmbeddingSleepURL string `json:"gpuSchedulerEmbeddingSleepUrl"`
	GPUSchedulerRerankerSleepURL  string `json:"gpuSchedulerRerankerSleepUrl"`
	GPUSchedulerDocParserSleepURL string `json:"gpuSchedulerDocParserSleepUrl"`

	DocParserEndpoint string `json:"docParserEndpoint"`
	DocParserAPIKey   string `json:"docParserApiKey,omitempty"`
	DocParserTimeout  string `json:"docParserTimeout"`

	ManagePort   string `json:"managePort"`
	ServePort    string `json:"servePort"`
	ServeBaseURL string `json:"serveBaseUrl"`

	LogFile  string `json:"logFile"`
	LogLevel string `json:"logLevel"`

	MySQLDSN        string `json:"mysqlDsn,omitempty"`
	MySQLUser       string `json:"mysqlUser,omitempty"`
	MySQLHost       string `json:"mysqlHost,omitempty"`
	MySQLPort       string `json:"mysqlPort,omitempty"`
	MySQLDatabase   string `json:"mysqlDatabase,omitempty"`
	MySQLSocketPath string `json:"mysqlSocketPath,omitempty"`

	APIToken string `json:"apiToken,omitempty"`

	DeepSeekEndpoint string `json:"deepseekEndpoint"`
	DeepSeekModel    string `json:"deepseekModel"`
	DeepSeekAPIKey   string `json:"deepseekApiKey,omitempty"`

	SearchMode    string  `json:"searchMode"`
	RerankEnabled bool    `json:"rerankEnabled"`
	RRFK          int     `json:"rrfK"`
	AbstractBoost float64 `json:"abstractBoost"`
	BM25K1        float64 `json:"bm25K1"`
	BM25B         float64 `json:"bm25B"`

	ChunkMinChars          int     `json:"chunkMinChars"`
	ChunkMaxChars          int     `json:"chunkMaxChars"`
	ChunkOverlapChars      int     `json:"chunkOverlapChars"`
	ChunkSemanticThreshold float64 `json:"chunkSemanticThreshold"`

	UploadMaxSizeMB int `json:"uploadMaxSizeMb"`

	RedisEnabled  bool   `json:"redisEnabled"`
	RedisAddr     string `json:"redisAddr"`
	RedisPassword string `json:"redisPassword,omitempty"`
	RedisDB       int    `json:"redisDB"`
	RedisPrefix   string `json:"redisPrefix"`
	RedisPoolSize int    `json:"redisPoolSize"`

	CacheQueryTTL  int `json:"cacheQueryTTL"`
	CacheChunkTTL  int `json:"cacheChunkTTL"`
	CacheMetaTTL   int `json:"cacheMetaTTL"`
	CacheIndexTTL  int `json:"cacheIndexTTL"`
	CacheKBListTTL int `json:"cacheKBListTTL"`

	ConfigPath      string `json:"configPath"`
	RestartRequired bool   `json:"restartRequired,omitempty"`
}

// configUpdateRequest is the JSON body for PUT /api/config.
type configUpdateRequest struct {
	DataDir   *string `json:"dataDir,omitempty"`
	DefaultKB *string `json:"defaultKB,omitempty"`

	EmbedEndpoint *string `json:"embedEndpoint,omitempty"`
	EmbedModel    *string `json:"embedModel,omitempty"`
	EmbedDim      *int    `json:"embedDim,omitempty"`
	EmbedAPIKey   *string `json:"embedApiKey,omitempty"`

	RerankEndpoint       *string `json:"rerankEndpoint,omitempty"`
	RerankModel          *string `json:"rerankModel,omitempty"`
	RerankAPIKey         *string `json:"rerankApiKey,omitempty"`
	RerankTimeout        *string `json:"rerankTimeout,omitempty"`
	RerankCandidateLimit *int    `json:"rerankCandidateLimit,omitempty"`

	DocParserEndpoint *string `json:"docParserEndpoint,omitempty"`
	DocParserAPIKey   *string `json:"docParserApiKey,omitempty"`
	DocParserTimeout  *string `json:"docParserTimeout,omitempty"`

	GPUSchedulerEnabled           *bool   `json:"gpuSchedulerEnabled,omitempty"`
	GPUSchedulerTimeout           *string `json:"gpuSchedulerTimeout,omitempty"`
	GPUSchedulerEmbeddingSleepURL *string `json:"gpuSchedulerEmbeddingSleepUrl,omitempty"`
	GPUSchedulerRerankerSleepURL  *string `json:"gpuSchedulerRerankerSleepUrl,omitempty"`
	GPUSchedulerDocParserSleepURL *string `json:"gpuSchedulerDocParserSleepUrl,omitempty"`

	ManagePort   *string `json:"managePort,omitempty"`
	ServePort    *string `json:"servePort,omitempty"`
	ServeBaseURL *string `json:"serveBaseUrl,omitempty"`

	LogFile  *string `json:"logFile,omitempty"`
	LogLevel *string `json:"logLevel,omitempty"`

	SearchMode    *string  `json:"searchMode,omitempty"`
	RerankEnabled *bool    `json:"rerankEnabled,omitempty"`
	RRFK          *int     `json:"rrfK,omitempty"`
	AbstractBoost *float64 `json:"abstractBoost,omitempty"`
	BM25K1        *float64 `json:"bm25K1,omitempty"`
	BM25B         *float64 `json:"bm25B,omitempty"`

	ChunkMinChars          *int     `json:"chunkMinChars,omitempty"`
	ChunkMaxChars          *int     `json:"chunkMaxChars,omitempty"`
	ChunkOverlapChars      *int     `json:"chunkOverlapChars,omitempty"`
	ChunkSemanticThreshold *float64 `json:"chunkSemanticThreshold,omitempty"`

	UploadMaxSizeMB *int `json:"uploadMaxSizeMb,omitempty"`

	DeepSeekEndpoint *string `json:"deepseekEndpoint,omitempty"`
	DeepSeekModel    *string `json:"deepseekModel,omitempty"`
	DeepSeekAPIKey   *string `json:"deepseekApiKey,omitempty"`

	RedisEnabled  *bool   `json:"redisEnabled,omitempty"`
	RedisAddr     *string `json:"redisAddr,omitempty"`
	RedisPassword *string `json:"redisPassword,omitempty"`
	RedisDB       *int    `json:"redisDB,omitempty"`
	RedisPrefix   *string `json:"redisPrefix,omitempty"`
	RedisPoolSize *int    `json:"redisPoolSize,omitempty"`

	CacheQueryTTL  *int `json:"cacheQueryTTL,omitempty"`
	CacheChunkTTL  *int `json:"cacheChunkTTL,omitempty"`
	CacheMetaTTL   *int `json:"cacheMetaTTL,omitempty"`
	CacheIndexTTL  *int `json:"cacheIndexTTL,omitempty"`
	CacheKBListTTL *int `json:"cacheKBListTTL,omitempty"`

	MySQLDSN        *string `json:"mysqlDsn,omitempty"`
	MySQLUser       *string `json:"mysqlUser,omitempty"`
	MySQLHost       *string `json:"mysqlHost,omitempty"`
	MySQLPort       *string `json:"mysqlPort,omitempty"`
	MySQLDatabase   *string `json:"mysqlDatabase,omitempty"`
	MySQLSocketPath *string `json:"mysqlSocketPath,omitempty"`

	APIToken *string `json:"apiToken,omitempty"`
}

// ── reloadDeepSeek (retained for tests) ──────────────────────────────────────

func (s *Store) reloadDeepSeek(cfg *config.Config) {
	if cfg.DeepSeekAPIKey == "" {
		s.SetLLMRewriter(nil)
		if s.logger != nil {
			s.logger.Infof("deepseek: LLM rewriter removed (no API key)")
		}
		return
	}

	completer := NewDeepSeekCompleter(
		cfg.DeepSeekEndpoint,
		cfg.DeepSeekAPIKey,
		cfg.DeepSeekModel,
		WithDeepSeekLogger(s.logger.WithModule("deepseek")),
	)
	llmRewriter := NewLLMQueryRewriter(completer)
	s.SetLLMRewriter(llmRewriter)
	if s.logger != nil {
		s.logger.Infof("deepseek reloaded: endpoint=%s model=%s", cfg.DeepSeekEndpoint, cfg.DeepSeekModel)
	}
}

// ── Type helpers ─────────────────────────────────────────────────────────────

func makeConfigPtr[T any](v T) *T { return &v }
func strPtr(s string) *string    { return &s }
func intPtr(i int) *int          { return &i }
func floatPtr(f float64) *float64 { return &f }
func boolPtr(b bool) *bool       { return &b }

// toolDescriptionsResponse wraps current custom descriptions plus their
// built-in defaults so the UI can show both side-by-side.
type toolDescriptionsResponse struct {
	Custom   ToolDescriptions `json:"custom"`
	Defaults ToolDescriptions `json:"defaults"`
}

func getDefaultToolDescriptions() ToolDescriptions {
	return ToolDescriptions{
		SearchDesc:       DefaultSearchDesc,
		SearchKbNameDesc: DefaultSearchKbNameDesc,
		ReadDesc:         DefaultReadDesc,
		ReadKbNameDesc:   DefaultReadKbNameDesc,
		ListDesc:         DefaultListDesc,
		ListKBsDesc:      DefaultListKBsDesc,
		UploadDesc:       DefaultUploadDesc,
		RemoveDesc:       DefaultRemoveDesc,
	}
}
