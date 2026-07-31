package knowledge

import (
	"sync"

	"knowledge-mcp/internal/config"
)

// SearchMode controls the retrieval strategy for searches.
type SearchMode string

const (
	SearchModeBM25   SearchMode = "bm25"
	SearchModeVector SearchMode = "vector"
	SearchModeHybrid SearchMode = "hybrid"
)

// Runtime settings that can be changed via the config API without restart.
type storeSettings struct {
	mu sync.RWMutex

	// Search settings
	searchMode    SearchMode // bm25, vector, hybrid
	rerankEnabled bool
	rrfK          int     // RRF fusion constant k
	bm25K1        float64 // BM25 term frequency saturation
	bm25B         float64 // BM25 length normalization

	// Chunking settings
	chunkMinChars          int
	chunkMaxChars          int
	chunkOverlapChars      int
	chunkSemanticThreshold float64

	// Upload
	uploadMaxSizeMB int

	// MCP Tool Descriptions (customised via Web UI)
	toolDescs ToolDescriptions
}

func defaultSettings() storeSettings {
	return storeSettings{
		searchMode:             SearchModeHybrid,
		rerankEnabled:          true,
		rrfK:                   60,
		bm25K1:                 1.2,
		bm25B:                  0.75,
		chunkMinChars:          200,
		chunkMaxChars:          2000,
		chunkOverlapChars:      200,
		chunkSemanticThreshold: 0.75,
		uploadMaxSizeMB:        500,
	}
}

// ── Search getters/setters ──

func (s *Store) GetSearchMode() SearchMode {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.searchMode
}

func (s *Store) SetSearchMode(mode SearchMode) {
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.searchMode = mode
}

func (s *Store) GetRerankEnabled() bool {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.rerankEnabled
}

func (s *Store) SetRerankEnabled(v bool) {
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.rerankEnabled = v
}

func (s *Store) GetRRFK() int {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.rrfK
}

func (s *Store) SetRRFK(v int) {
	if v < 1 {
		v = 60
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.rrfK = v
}

func (s *Store) GetBM25K1() float64 {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.bm25K1
}

func (s *Store) SetBM25K1(v float64) {
	if v < 0 {
		v = 1.2
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.bm25K1 = v
}

func (s *Store) GetBM25B() float64 {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.bm25B
}

func (s *Store) SetBM25B(v float64) {
	if v < 0 || v > 1 {
		v = 0.75
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.bm25B = v
}

// ── Chunking getters/setters ──

func (s *Store) GetChunkMinChars() int {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.chunkMinChars
}

func (s *Store) SetChunkMinChars(v int) {
	if v < 50 {
		v = 200
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.chunkMinChars = v
	SetChunkParams(v, s.settings.chunkMaxChars, s.settings.chunkOverlapChars, s.settings.chunkSemanticThreshold)
}

func (s *Store) GetChunkMaxChars() int {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.chunkMaxChars
}

func (s *Store) SetChunkMaxChars(v int) {
	if v < 500 {
		v = 2000
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.chunkMaxChars = v
	SetChunkParams(s.settings.chunkMinChars, v, s.settings.chunkOverlapChars, s.settings.chunkSemanticThreshold)
}

func (s *Store) GetChunkOverlapChars() int {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.chunkOverlapChars
}

func (s *Store) SetChunkOverlapChars(v int) {
	if v < 0 {
		v = 200
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.chunkOverlapChars = v
	SetChunkParams(s.settings.chunkMinChars, s.settings.chunkMaxChars, v, s.settings.chunkSemanticThreshold)
}

func (s *Store) GetChunkSemanticThreshold() float64 {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.chunkSemanticThreshold
}

func (s *Store) SetChunkSemanticThreshold(v float64) {
	if v < 0 || v > 1 {
		v = 0.75
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.chunkSemanticThreshold = v
	SetChunkParams(s.settings.chunkMinChars, s.settings.chunkMaxChars, s.settings.chunkOverlapChars, v)
}

// ── Upload getter/setter ──

func (s *Store) GetUploadMaxSizeMB() int {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.uploadMaxSizeMB
}

func (s *Store) SetUploadMaxSizeMB(v int) {
	if v < 1 {
		v = 500
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	s.settings.uploadMaxSizeMB = v
}

// applyFromConfig loads runtime settings from a Config, falling back to defaults
// when the config value is the zero value (for backward compat with older toml files).
func (st *storeSettings) applyFromConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	if cfg.SearchMode != "" {
		st.searchMode = SearchMode(cfg.SearchMode)
	}
	if cfg.RerankEnabled {
		st.rerankEnabled = cfg.RerankEnabled
	}
	if cfg.RRFK > 0 {
		st.rrfK = cfg.RRFK
	}
	if cfg.BM25K1 > 0 {
		st.bm25K1 = cfg.BM25K1
	}
	if cfg.BM25B > 0 {
		st.bm25B = cfg.BM25B
	}
	if cfg.ChunkMinChars > 0 {
		st.chunkMinChars = cfg.ChunkMinChars
	}
	if cfg.ChunkMaxChars > 0 {
		st.chunkMaxChars = cfg.ChunkMaxChars
	}
	if cfg.ChunkOverlapChars > 0 {
		st.chunkOverlapChars = cfg.ChunkOverlapChars
	}
	if cfg.ChunkSemanticThreshold > 0 {
		st.chunkSemanticThreshold = cfg.ChunkSemanticThreshold
	}
	if cfg.UploadMaxSizeMB > 0 {
		st.uploadMaxSizeMB = cfg.UploadMaxSizeMB
	}

	// Tool descriptions: only override when a custom value is provided.
	if cfg.ToolSearchDesc != "" {
		st.toolDescs.SearchDesc = cfg.ToolSearchDesc
	}
	if cfg.ToolSearchKbNameDesc != "" {
		st.toolDescs.SearchKbNameDesc = cfg.ToolSearchKbNameDesc
	}
	if cfg.ToolReadDesc != "" {
		st.toolDescs.ReadDesc = cfg.ToolReadDesc
	}
	if cfg.ToolReadKbNameDesc != "" {
		st.toolDescs.ReadKbNameDesc = cfg.ToolReadKbNameDesc
	}
	if cfg.ToolListDesc != "" {
		st.toolDescs.ListDesc = cfg.ToolListDesc
	}
	if cfg.ToolListKBsDesc != "" {
		st.toolDescs.ListKBsDesc = cfg.ToolListKBsDesc
	}
	if cfg.ToolUploadDesc != "" {
		st.toolDescs.UploadDesc = cfg.ToolUploadDesc
	}
	if cfg.ToolRemoveDesc != "" {
		st.toolDescs.RemoveDesc = cfg.ToolRemoveDesc
	}
}

// ── Tool Descriptions ──

// ToolDescriptions holds customisable MCP tool descriptions.
// Each field maps to a specific description slot on an MCP tool.
// An empty string means "use the hardcoded default".
type ToolDescriptions struct {
	SearchDesc       string `json:"SearchDesc"`        // knowledge_search top-level description
	SearchKbNameDesc string `json:"SearchKbNameDesc"`  // knowledge_search kbName parameter description
	ReadDesc         string `json:"ReadDesc"`          // knowledge_read top-level description
	ReadKbNameDesc   string `json:"ReadKbNameDesc"`    // knowledge_read kbName parameter description
	ListDesc         string `json:"ListDesc"`          // knowledge_list top-level description
	ListKBsDesc      string `json:"ListKBsDesc"`       // knowledge_list_kbs top-level description
	UploadDesc       string `json:"UploadDesc"`        // knowledge_upload top-level description
	RemoveDesc       string `json:"RemoveDesc"`        // knowledge_remove top-level description
}

// GetToolDescriptions returns a copy of the current tool descriptions.
func (s *Store) GetToolDescriptions() ToolDescriptions {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.toolDescs
}

// SetToolDescriptions replaces all tool descriptions and persists to the config file.
func (s *Store) SetToolDescriptions(td ToolDescriptions) {
	s.settings.mu.Lock()
	s.settings.toolDescs = td
	s.settings.mu.Unlock()
}

// ── Tool description getters (custom → default fallback) ──

func (s *Store) ToolSearchDesc() string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	if s.settings.toolDescs.SearchDesc != "" {
		return s.settings.toolDescs.SearchDesc
	}
	return DefaultSearchDesc
}

func (s *Store) ToolSearchKbNameDesc() string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	if s.settings.toolDescs.SearchKbNameDesc != "" {
		return s.settings.toolDescs.SearchKbNameDesc
	}
	return DefaultSearchKbNameDesc
}

func (s *Store) ToolReadDesc() string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	if s.settings.toolDescs.ReadDesc != "" {
		return s.settings.toolDescs.ReadDesc
	}
	return DefaultReadDesc
}

func (s *Store) ToolReadKbNameDesc() string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	if s.settings.toolDescs.ReadKbNameDesc != "" {
		return s.settings.toolDescs.ReadKbNameDesc
	}
	return DefaultReadKbNameDesc
}

func (s *Store) ToolListDesc() string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	if s.settings.toolDescs.ListDesc != "" {
		return s.settings.toolDescs.ListDesc
	}
	return DefaultListDesc
}

func (s *Store) ToolListKBsDesc() string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	if s.settings.toolDescs.ListKBsDesc != "" {
		return s.settings.toolDescs.ListKBsDesc
	}
	return DefaultListKBsDesc
}

func (s *Store) ToolUploadDesc() string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	if s.settings.toolDescs.UploadDesc != "" {
		return s.settings.toolDescs.UploadDesc
	}
	return DefaultUploadDesc
}

func (s *Store) ToolRemoveDesc() string {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	if s.settings.toolDescs.RemoveDesc != "" {
		return s.settings.toolDescs.RemoveDesc
	}
	return DefaultRemoveDesc
}
