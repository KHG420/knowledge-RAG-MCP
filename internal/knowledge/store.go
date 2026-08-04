package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"knowledge-mcp/internal/cache"
	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/logging"
	"knowledge-mcp/internal/knowledge/search/retrieval"
)

const maxTermsPerChunk = 50 // top-N frequent terms retained in CHUNKS.toml

const boundaryMergeN = 5 // G12: number of old tail chunks for incremental boundary merge
const boundaryMergeM = 5 // G12: number of new head chunks for incremental boundary merge

// VectorIndexState holds the per-KB HNSW vector index cache, guarded by its
// own mutex. It is stored as a pointer in Store so value-copies of Store
// (like WithKB) share the same lock and cache.
type VectorIndexState struct {
	mu    sync.RWMutex
	cache map[string]*HNSWIndex
}

// Cache returns the internal cache map (read-only access for search sub-packages).
func (vs *VectorIndexState) Cache() map[string]*HNSWIndex { return vs.cache }

// SetCache replaces the internal cache map.
func (vs *VectorIndexState) SetCache(c map[string]*HNSWIndex) { vs.cache = c }

// Lock acquires a read lock on the state.
func (vs *VectorIndexState) Lock()   { vs.mu.RLock() }

// Unlock releases a read lock.
func (vs *VectorIndexState) Unlock() { vs.mu.RUnlock() }

// WLock acquires a write lock.
func (vs *VectorIndexState) WLock()   { vs.mu.Lock() }

// WUnlock releases a write lock.
func (vs *VectorIndexState) WUnlock() { vs.mu.Unlock() }

// RerankCacheState holds the in-memory rerank result cache. Stored as a pointer
// in Store for the same reason as VectorIndexState.
type RerankCacheState struct {
	mu    sync.RWMutex
	cache map[string][]float64
}

// Cache returns the internal cache map (read-only access).
func (rs *RerankCacheState) Cache() map[string][]float64 { return rs.cache }

// SetCache replaces the internal cache map.
func (rs *RerankCacheState) SetCache(c map[string][]float64) { rs.cache = c }

// Lock acquires a read lock.
func (rs *RerankCacheState) Lock()   { rs.mu.RLock() }

// Unlock releases a read lock.
func (rs *RerankCacheState) Unlock() { rs.mu.RUnlock() }

// WLock acquires a write lock.
func (rs *RerankCacheState) WLock()   { rs.mu.Lock() }

// WUnlock releases a write lock.
func (rs *RerankCacheState) WUnlock() { rs.mu.Unlock() }

// Store manages the knowledge base with a MySQL-backed StorageBackend.
// Use NewStoreWithBackend to create a Store with a MySQL backend.
//
// Store is a Facade that delegates to 6 sub-components (REFACTOR_PLAN Phase 3 ✅).
//   Search   → searchEngine (Searcher)      Chunk I/O → chunkStore (ChunkStore) ✅
//   Ingestion → ingestSvc (Ingester)         Dict      → dictSvc (DictService)  ✅
//   KB admin  → kbAdmin (KBAdmin)            HTTP mgmt → manage.ManageServer
//
// Store methods delegate to the sub-component engines when available (nil-safe),
// with legacy backend paths as fallback. Chunk CRUD (16 methods) and dictionary
// loading are now fully bridged through chunkStore and dictSvc respectively.
//
// LEGACY FIELDS (B-group — retained for ManageService + test fallbacks):
// Fields marked DEPRECATED below are shared references with sub-component engines.
// They cannot be removed until ManageService is extracted into manage/ sub-package
// (Phase 5), which would let the Store drop all infrastructure fields entirely.
// Legacy search_*.go files can be deleted once all tests use engine-injected Stores.
type Store struct {
	// ── Sub-components (REFACTOR_PLAN Phase 3 extraction) ──
	searchEngine Searcher       // retrieval core: BM25, vector, hybrid, rerank
	chunkStore ChunkStore       // chunk I/O: CRUD, manifest, tombstone, staging
	manage   *ManageServer       // web management: config, settings, HTTP handlers
	dictSvc  DictService          // dictionary: synonyms, mining, rewriting
	ingestSvc Ingester            // document ingestion: parse, chunk, upload
	kbAdmin  KBAdmin              // KB lifecycle: CRUD, routing, router-desc sync

	// ── Storage ──
	backend StorageBackend // pluggable storage (MySQLBackend)
	kbName  string         // knowledge base name; empty means flat legacy mode
	dataDir string         // root directory for file-based artifacts

	// ── Query rewriting (legacy — migrating to SearchEngine) ──
	// NOTE: rewriter field removed — it was dead code (written but never read).
	// Store-level synonymRewriter/llmRewriter are used only in the
	// searchEngine==nil fallback path; the engine maintains its own copies.
	synonymRewriter *SynonymRewriter  // DEPRECATED: use searchEngine.SetSynonymRewriter
	llmRewriter     *LLMQueryRewriter // DEPRECATED: use searchEngine.SetLLMRewriter

	// ── Embedding & reranking (DEPRECATED: use searchEngine for search paths) ──
	// These fields are retained for:
	//  1. searchEngine==nil fallback (tests only)
	//  2. ManageService interface methods (EmbedderInfo, RerankerInfo, etc.)
	//  3. Non-search paths (upload, incremental, vector rebuild)
	embedder             Embedder
	reranker             Reranker
	dictRelatedTerms     []string
	rerankCandidateLimit int
	rerankBatchSize      int
	searchLogger         SearchLogger
	AbstractBoost        float64 // G13: multiplier for abstract-section chunks

	// ── KB routing ──
	kbRouter *KBRouter

	// ── GPU scheduler ──
	gpuScheduler *GPUScheduler

	// ── Vector index (DEPRECATED for search: use searchEngine.VectorIndex) ──
	// Retained for ManageService (GetVectorStats, GetVectorIndexInfo, RebuildVectors)
	// and non-search paths (remove.go, store_incremental.go).
	vectorIndex *HNSWIndex
	vecState    *VectorIndexState
	rerankState *RerankCacheState

	// ── Upload ──
	taskManager *UploadTaskManager

	// ── Tombstone ──
	tombstoneManager *TombstoneManager

	// ── Runtime configuration ──
	config     *config.Config
	configPath string
	settings   *storeSettings

	// ── Cache layer (DEPRECATED for search: use searchEngine cacheClient/TTL) ──
	// Retained for ManageService data-plane caching (ReadChunk, ReadMeta, ReadChunksIndex,
	// ListKBs) and for direct cache manipulation via ManageService.
	cacheClient    cache.Cache
	queryCacheTTL  time.Duration
	chunkCacheTTL  time.Duration
	metaCacheTTL   time.Duration
	indexCacheTTL  time.Duration
	kbListCacheTTL time.Duration

	// ── Infrastructure ──
	logger *logging.Logger
	mu     *sync.Mutex
}

// NewStoreWithBackend returns a Store using the given StorageBackend.
// The caller is responsible for calling backend.Init() and backend.Close().
// A default file-system directory is used for file-based artifacts like
// VECTOR.gob and task persistence.
func NewStoreWithBackend(backend StorageBackend) *Store {
	s := defaultSettings()
	homeDir, _ := os.UserHomeDir()
	dataDir := filepath.Join(homeDir, "knowledge_base")
	tasksDir := filepath.Join(dataDir, "tasks")
	st := &Store{
		backend:       backend,
		dataDir:       dataDir,
		AbstractBoost: 1.1,
		logger:        logging.NewNopLogger(),
		mu:            &sync.Mutex{},
		vecState:      &VectorIndexState{},
		rerankState:   &RerankCacheState{},
		settings:      &s,
		taskManager:   NewUploadTaskManager(tasksDir, logging.NewNopLogger()),
	}
	// Wire up ManageServer (Phase 3.4: external assembly via manage.Server).
	st.manage = NewManageServer(nil, "", &s, nil, nil, st.taskManager, st.mu, st.logger)
	return st
}

// SetSearchEngine injects the retrieval engine (Phase 3: external assembly).
// Call from init.go after constructing a search.Engine with the proper
// backend, cache, and state references.
func (s *Store) SetSearchEngine(se Searcher) { s.searchEngine = se }

// SetChunkStore injects the chunk storage engine (Phase 3.3: external assembly).
func (s *Store) SetChunkStore(cs ChunkStore) { s.chunkStore = cs }

// ChunkStore returns the injected chunk storage engine (may be nil for legacy path).
func (s *Store) ChunkStore() ChunkStore { return s.chunkStore }

// SetDictService injects the dictionary service (Phase 3.5: external assembly).
func (s *Store) SetDictService(ds DictService) { s.dictSvc = ds }

// DictService returns the injected dictionary engine.
func (s *Store) DictService() DictService { return s.dictSvc }

// LoadDictionaries delegates to dictSvc when available; falls back to the
// package-level LoadDictionaries + rewriter injection (legacy path).
func (s *Store) LoadDictionaries(dir string) error {
	if s.dictSvc != nil {
		return s.dictSvc.LoadDictionaries(dir)
	}
	entries, err := LoadDictionaries(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	// Populate synonym rewriter (legacy fallback).
	syns := DictToSynonyms(entries)
	if s.synonymRewriter != nil {
		for term, synonyms := range syns {
			for _, syn := range synonyms {
				s.synonymRewriter.AddSynonym(term, syn)
			}
		}
	}
	s.dictRelatedTerms = DictToRelatedTerms(entries)
	s.logger.Infof("dictionaries: loaded %d terms from %s (legacy path)", len(entries), dir)
	return nil
}

// SetIngestService injects the ingestion service (Phase 3.5: external assembly).
func (s *Store) SetIngestService(is Ingester) { s.ingestSvc = is }

// SetKBAdmin injects the KB administration engine (Phase 3: external assembly).
func (s *Store) SetKBAdmin(ka KBAdmin) { s.kbAdmin = ka }

// Mutex returns the shared mutex for use by sub-components.
func (s *Store) Mutex() *sync.Mutex { return s.mu }

// VecState returns the shared vector index state.
func (s *Store) VecState() *VectorIndexState { return s.vecState }

// RerankState returns the shared rerank cache state.
func (s *Store) RerankState() *RerankCacheState { return s.rerankState }

// SetConfig stores a reference to the parsed config for API exposure and
// initializes runtime settings (search mode, chunking, BM25) from the config.
func (s *Store) SetConfig(cfg *config.Config, cfgPath string) {
	s.config = cfg
	s.configPath = cfgPath
	s.settings.applyFromConfig(cfg)
	if cfg != nil && cfg.AbstractBoost > 0 {
		s.AbstractBoost = cfg.AbstractBoost
	}
}

// Config returns the current config reference (may be nil).
func (s *Store) Config() *config.Config { return s.config }

// ConfigPath returns the path to the config file on disk.
func (s *Store) ConfigPath() string { return s.configPath }

// Backend returns the underlying StorageBackend for inspection.
func (s *Store) Backend() StorageBackend { return s.backend }

// Reranker returns the current reranker (may be nil).
func (s *Store) Reranker() Reranker { return s.reranker }

// GPUScheduler returns the GPU scheduler (may be nil).
func (s *Store) GPUScheduler() *GPUScheduler { return s.gpuScheduler }

// KBRouter returns the KB router (may be nil).
func (s *Store) KBRouter() *KBRouter { return s.kbRouter }

// VectorIndexRaw returns the raw vector index for type assertion (may be nil).
func (s *Store) VectorIndexRaw() interface{} { return s.vectorIndex }

// ValidateComponent validates a path component name.
func (s *Store) ValidateComponent(name string) error {
	return validateComponent(name)
}

// WithKBService returns a scoped ManageService for the given KB name.
// This is the ManageService-compatible version of WithKB.
func (s *Store) WithKBService(kbName string) ManageService {
	return s.WithKB(kbName)
}

// SetAbstractBoost sets the abstract chunk score multiplier.
func (s *Store) SetAbstractBoost(v float64) {
	s.AbstractBoost = v
	if s.searchEngine != nil {
		s.searchEngine.SetAbstractBoost(v)
	}
}

// ── ManageService tombstone / manifest / vector wrappers ──

// ListTombstones returns all tombstoned documents for the current KB.
func (s *Store) ListTombstones() ([]Tombstone, error) {
	tm := s.getTombstoneManager()
	all := tm.All()
	result := make([]Tombstone, len(all))
	for i, t := range all {
		result[i] = *t
	}
	return result, nil
}

// RestoreTombstone removes a tombstone and restores the document.
func (s *Store) RestoreTombstone(docSlug string) error {
	tm := s.getTombstoneManager()
	return tm.Remove(docSlug)
}

// ReadManifest reads the chunk manifest for a document.
func (s *Store) ReadManifest(docSlug string) (*ChunkManifest, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ReadManifest(docSlug)
	}
	return s.backend.ReadManifest(s.kbName, docSlug)
}

// WriteManifest writes the chunk manifest for a document, delegating to chunkStore.
func (s *Store) WriteManifest(docSlug string, manifest *ChunkManifest) error {
	if s.chunkStore != nil {
		return s.chunkStore.WriteManifest(docSlug, manifest)
	}
	return s.backend.WriteManifest(s.kbName, docSlug, manifest)
}

// RebuildVectors is an alias for ReEmbedMissingVectors with no slug filter.
func (s *Store) RebuildVectors() (RebuildResult, error) {
	result, err := s.ReEmbedMissingVectors(context.Background(), "", nil)
	if err != nil {
		return RebuildResult{}, err
	}
	return *result, nil
}

// validateComponent rejects path components that contain parent-directory
// references ("..") or absolute paths, preventing path-traversal attacks
// when user-supplied strings are joined into filesystem paths.
func validateComponent(name string) error {
	if name == "" {
		return nil
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid path component %q: must not contain '..'", name)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("invalid path component %q: must not be an absolute path", name)
	}
	return nil
}

// WithKB returns a Store view scoped to the named knowledge base.
// When name is empty, the store operates on the flat knowledge directory (legacy mode).
// The returned Store shares the same embedder, reranker, logger, and other
// configuration but reads/writes from a KB-scoped subdirectory.
func (s *Store) WithKB(name string) *Store {
	if err := validateComponent(name); err != nil {
		s.logger.Warnf("WithKB: %v", err)
		return s // return unscoped store; the caller will fail on subsequent operations
	}
	cp := *s
	cp.kbName = name

	// Check in-memory cache first to avoid repeated disk I/O.
	if s.vecState.Cache() != nil {
		s.vecState.Lock()
		if idx, ok := s.vecState.Cache()[name]; ok {
			s.vecState.Unlock()
			cp.vectorIndex = idx
			return &cp
		}
		s.vecState.Unlock()
	}

	// Try loading the persisted HNSW index for this KB.
	// Each KB has its own VECTOR.gob; if present, use it.
	if idx, err := cp.loadVectorIndex(); err == nil && idx != nil {
		cp.vectorIndex = idx
	} else {
		cp.vectorIndex = nil
	}

	// Store in cache for future WithKB calls.
	if cp.vectorIndex != nil {
		s.vecState.WLock()
		if s.vecState.Cache() == nil {
			s.vecState.SetCache(make(map[string]*HNSWIndex))
		}
		s.vecState.Cache()[name] = cp.vectorIndex
		s.vecState.WUnlock()
	}

	// Keep the ingest engine in sync with the current KB.
	if cp.ingestSvc != nil {
		if eng, ok := cp.ingestSvc.(interface{ SetKBName(string) }); ok {
			eng.SetKBName(name)
		}
	}

	return &cp
}

// SetLogger sets the logger on the Store.
func (s *Store) SetLogger(l *logging.Logger) {
	s.logger = l
	SetParserLogger(l)
}

// SetKBRouter configures the KB router for multi-KB joint retrieval (v4).
// When set, knowledge_research without an explicit kbName will use the router
// to select the best 1–3 KBs instead of searching all KBs blindly.
func (s *Store) SetKBRouter(r *KBRouter) {
	if s.kbAdmin != nil {
		s.kbAdmin.SetKBRouter(r)
		return
	}
	s.kbRouter = r
}

// SyncKBRouterDescs refreshes the KB router's cached name/description list
// from the backend. Call after CreateKB or DeleteKB.
func (s *Store) SyncKBRouterDescs() error {
	if s.kbAdmin != nil {
		s.kbAdmin.SyncKBRouterDescs()
		return nil
	}
	if s.kbRouter == nil {
		return nil
	}
	kbs, err := s.backend.ListKBs()
	if err != nil {
		return err
	}
	descs := make([]KBDesc, len(kbs))
	for i, kb := range kbs {
		descs[i] = KBDesc{Name: kb.Name, Desc: kb.Description}
	}
	s.kbRouter.SetKBDescs(descs)
	return nil
}

// RouteKBs uses the configured KB router to select the best 1–3 KBs for the
// query. Returns nil when no router is configured (caller should fall back to
// cross-KB search).
func (s *Store) RouteKBs(query string) []string {
	if s.kbAdmin != nil {
		return s.kbAdmin.RouteKBs(context.Background(), query)
	}
	if s.kbRouter == nil {
		return nil
	}
	result := s.kbRouter.Route(context.Background(), query, nil)
	if result == nil {
		return nil
	}
	return result.Selected
}

// SetDictionaryRelatedTerms stores related_terms loaded from dictionaries/*.yaml.
// Called once at startup; read-only thereafter.
func (s *Store) SetDictionaryRelatedTerms(terms []string) {
	s.dictRelatedTerms = terms
	if s.searchEngine != nil {
		s.searchEngine.SetDictionaryRelatedTerms(terms)
	}
}

// GetDictionaryRelatedTerms returns the dictionary related_terms for appending
// to search queries.
func (s *Store) GetDictionaryRelatedTerms() []string {
	return s.dictRelatedTerms
}

// SetSynonymRewriter configures the always-on dictionary-based query expander.
// This is used for all query tiers (simple/medium/complex). Unlike llmRewriter,
// the synonym rewriter is cheap and deterministic — it only expands terms that
// exist in the loaded domain dictionaries.
func (s *Store) SetSynonymRewriter(r *SynonymRewriter) {
	s.synonymRewriter = r
	if s.searchEngine != nil {
		s.searchEngine.SetSynonymRewriter(r)
	}
}

// SetLLMRewriter configures the optional LLM-based query rewriter, used only
// for complex queries (triage=TriageComplex). When nil (no DeepSeek API key),
// complex queries fall back to the SynonymRewriter.
func (s *Store) SetLLMRewriter(r *LLMQueryRewriter) {
	s.llmRewriter = r
	if s.searchEngine != nil {
		s.searchEngine.SetLLMRewriter(r)
	}
}

// TaskManager returns the store's UploadTaskManager, creating it lazily if needed.
// Tasks are persisted to a tasks/ subdirectory under the knowledge base directory.
func (s *Store) TaskManager() *UploadTaskManager {
	if s.taskManager == nil {
		// Use the base knowledge directory (not KB-scoped) for task storage.
		base := *s
		base.kbName = ""
		tasksDir := filepath.Join(base.knowledgeDir(), "tasks")
		s.taskManager = NewUploadTaskManager(tasksDir, s.logger)
	}
	return s.taskManager
}

// SetDocParser configures the document parser used by ParseFile for
// non-plain-text formats. When set, the parser is tried before falling
// back to the local tabula library.
func (s *Store) SetDocParser(p DocParser) {
	SetDocParser(p)
}

// SetGPUScheduler configures a GPU scheduler for managing model sleep/wake.
// When set, embedding, reranker, and doc parser operations coordinate to
// share GPU memory.
func (s *Store) SetGPUScheduler(g *GPUScheduler) {
	s.gpuScheduler = g
	if s.searchEngine != nil {
		s.searchEngine.SetGPUScheduler(g)
	}
	SetParserGPUScheduler(g)
}

// SetCache configures the pluggable cache backend and TTL values.
// Pass nil or cache.NoopCache to disable caching.
func (s *Store) SetCache(c cache.Cache, cfg *config.Config) {
	if cache.IsNoop(c) {
		s.cacheClient = nil
		if s.searchEngine != nil {
			s.searchEngine.SetCacheClient(nil)
		}
		return
	}
	s.cacheClient = c
	if s.searchEngine != nil {
		s.searchEngine.SetCacheClient(c)
	}
	if cfg != nil {
		s.queryCacheTTL = time.Duration(cfg.CacheQueryTTL) * time.Second
		s.chunkCacheTTL = time.Duration(cfg.CacheChunkTTL) * time.Second
		s.metaCacheTTL = time.Duration(cfg.CacheMetaTTL) * time.Second
		s.indexCacheTTL = time.Duration(cfg.CacheIndexTTL) * time.Second
		s.kbListCacheTTL = time.Duration(cfg.CacheKBListTTL) * time.Second
		if s.searchEngine != nil {
			s.searchEngine.SetQueryCacheTTL(s.queryCacheTTL)
		}
	}
}

// SetCacheQueryTTL updates the query cache TTL at runtime (hot-reloadable).
func (s *Store) SetCacheQueryTTL(d time.Duration) {
	s.queryCacheTTL = d
	if s.searchEngine != nil {
		s.searchEngine.SetQueryCacheTTL(d)
	}
}

// SetCacheChunkTTL updates the chunk text cache TTL at runtime (hot-reloadable).
func (s *Store) SetCacheChunkTTL(d time.Duration)  { s.chunkCacheTTL = d }

// SetCacheMetaTTL updates the metadata cache TTL at runtime (hot-reloadable).
func (s *Store) SetCacheMetaTTL(d time.Duration)   { s.metaCacheTTL = d }

// SetCacheIndexTTL updates the index cache TTL at runtime (hot-reloadable).
func (s *Store) SetCacheIndexTTL(d time.Duration)  { s.indexCacheTTL = d }

// SetCacheKBListTTL updates the KB list cache TTL at runtime (hot-reloadable).
func (s *Store) SetCacheKBListTTL(d time.Duration) { s.kbListCacheTTL = d }

// cacheEnabled returns true when the cache backend is active.
func (s *Store) cacheEnabled() bool { return s.cacheClient != nil && !cache.IsNoop(s.cacheClient) }

// InvalidateDoc removes all cached entries for a document (chunks, meta, index).
// It also invalidates the query cache for the KB so that any stale search
// results referencing this document are evicted.
func (s *Store) InvalidateDoc(docSlug string) {
	if !s.cacheEnabled() {
		return
	}
	log := s.logger.WithModule("cache")

	// 1) Document-level caches: chunks, meta, index.
	pattern := cache.DocInvalidatePattern(s.kbName, docSlug)
	n, err := s.cacheClient.DeletePattern(context.Background(), pattern)
	if err != nil {
		log.Warnf("invalidate doc %q FAILED: pattern=%q err=%v", docSlug, pattern, err)
		// Continue — query cache invalidation is independent.
	} else if n > 0 {
		log.Infof("invalidate doc %q: deleted %d keys (pattern=%q)", docSlug, n, pattern)
	} else {
		log.Debugf("invalidate doc %q: no keys matched (pattern=%q)", docSlug, pattern)
	}

	// 2) Query-level caches for the entire KB.
	//    Any document mutation can invalidate cached search results, so we
	//    evict all query-cache entries scoped to this KB.
	qpattern := cache.DocQueryInvalidatePattern(s.kbName)
	qn, qerr := s.cacheClient.DeletePattern(context.Background(), qpattern)
	if qerr != nil {
		log.Warnf("invalidate query cache for KB %q FAILED: pattern=%q err=%v", s.kbName, qpattern, qerr)
	} else if qn > 0 {
		log.Infof("invalidate query cache for KB %q: deleted %d keys (pattern=%q)", s.kbName, qn, qpattern)
	} else {
		log.Debugf("invalidate query cache for KB %q: no keys matched (pattern=%q)", s.kbName, qpattern)
	}
}

// InvalidateKBList removes the cached KB list (used after create/delete KB).
func (s *Store) InvalidateKBList() {
	if !s.cacheEnabled() {
		return
	}
	log := s.logger.WithModule("cache")
	if err := s.cacheClient.Delete(context.Background(), cache.KBListKey()); err != nil {
		log.Warnf("invalidate kblist FAILED: err=%v", err)
	} else {
		log.Debugf("invalidate kblist: deleted")
	}
}

// KBInfo holds metadata about a knowledge base.
type KBInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListKBs returns knowledge base names from the backend, with Redis cache.
func (s *Store) ListKBs() ([]string, error) {
	if s.kbAdmin != nil {
		return s.kbAdmin.ListKBs()
	}
	// Try cache first.
	if s.cacheEnabled() {
		key := cache.KBListKey()
		if raw, err := s.cacheClient.Get(context.Background(), key); err == nil && raw != nil {
			var names []string
			if json.Unmarshal(raw, &names) == nil {
				s.logger.WithModule("cache").Debugf("kblist HIT: %d KBs", len(names))
				return names, nil
			}
		}
		s.logger.WithModule("cache").Debugf("kblist MISS")
	}

	kbs, err := s.backend.ListKBs()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(kbs))
	for i, kb := range kbs {
		names[i] = kb.Name
	}

	// Store in cache.
	if s.cacheEnabled() {
		key := cache.KBListKey()
		if raw, jerr := json.Marshal(names); jerr == nil {
			if setErr := s.cacheClient.Set(context.Background(), key, raw, s.kbListCacheTTL); setErr != nil {
				s.logger.WithModule("cache").Warnf("kblist SET failed: err=%v", setErr)
			} else {
				s.logger.WithModule("cache").Debugf("kblist SET: %d KBs ttl=%v", len(names), s.kbListCacheTTL)
			}
		}
	}

	return names, nil
}

// ListKBsInfo delegates to the backend.
func (s *Store) ListKBsInfo() ([]KBInfo, error) {
	if s.kbAdmin != nil {
		return s.kbAdmin.ListKBsInfo()
	}
	return s.backend.ListKBs()
}

// CreateKB delegates to the backend.
func (s *Store) CreateKB(name, description string) error {
	if s.kbAdmin != nil {
		return s.kbAdmin.CreateKB(name, description)
	}
	s.logger.Infof("KB %q: creating", name)
	err := s.backend.CreateKB(name, description)
	if err != nil {
		s.logger.Errorf("KB %q: create failed: %v", name, err)
		return err
	}
	s.InvalidateKBList()
	s.logger.Infof("KB %q: created (description=%q)", name, description)
	return nil
}

// DeleteKB delegates to the backend and invalidates the KB list cache.
func (s *Store) DeleteKB(name string) error {
	if s.kbAdmin != nil {
		return s.kbAdmin.DeleteKB(name)
	}
	s.logger.Infof("KB %q: deleting", name)
	err := s.backend.DeleteKB(name)
	if err != nil {
		s.logger.Errorf("KB %q: delete failed: %v", name, err)
		return err
	}
	s.InvalidateKBList()
	// Also invalidate any query cache entries for this KB.
	if s.cacheEnabled() {
		qpattern := cache.DocQueryInvalidatePattern(name)
		if qn, qerr := s.cacheClient.DeletePattern(context.Background(), qpattern); qerr != nil {
			s.logger.WithModule("cache").Warnf("invalidate query cache for deleted KB %q: %v", name, qerr)
		} else if qn > 0 {
			s.logger.WithModule("cache").Infof("invalidate query cache for deleted KB %q: deleted %d keys", name, qn)
		}
	}
	s.logger.Infof("KB %q: deleted", name)
	return nil
}

// knowledgeDir returns the data directory path for file-based artifacts.
func (s *Store) knowledgeDir() string {
	return s.dataDir
}

// DataDir returns the root data directory (e.g. ~/knowledge_base/).
func (s *Store) DataDir() string {
	return s.dataDir
}

// KBName returns the current knowledge base name, or empty string for
// the legacy flat mode.
func (s *Store) KBName() string {
	return s.kbName
}

// EnsureDir initializes the storage backend.
func (s *Store) EnsureDir() error {
	return s.backend.Init()
}

// ReadIndex delegates to chunkStore when available.
func (s *Store) ReadIndex() (string, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ReadIndex()
	}
	return s.backend.ReadIndex(s.kbName)
}

// WriteIndex delegates to chunkStore when available.
func (s *Store) WriteIndex(content string) error {
	if s.chunkStore != nil {
		return s.chunkStore.WriteIndex(content)
	}
	return s.backend.WriteIndex(s.kbName, content)
}

// WriteMeta delegates to chunkStore when available.
func (s *Store) WriteMeta(slug string, meta DocumentMeta) error {
	if s.chunkStore != nil {
		return s.chunkStore.WriteMeta(slug, &meta)
	}
	return s.backend.WriteMeta(s.kbName, slug, &meta)
}

// ReadMeta delegates to chunkStore when available; falls back to backend+cache.
func (s *Store) ReadMeta(slug string) (DocumentMeta, error) {
	if s.chunkStore != nil {
		meta, err := s.chunkStore.ReadMeta(slug)
		if meta == nil {
			return DocumentMeta{}, err
		}
		return *meta, err
	}

	// Try cache first.
	if s.cacheEnabled() {
		key := cache.MetaKey(s.kbName, slug)
		if raw, err := s.cacheClient.Get(context.Background(), key); err == nil && raw != nil {
			var meta DocumentMeta
			if json.Unmarshal(raw, &meta) == nil {
				s.logger.WithModule("cache").Infof("meta HIT  key=%s slug=%q kb=%q", key, slug, s.kbName)
				return meta, nil
			}
		}
		s.logger.WithModule("cache").Infof("meta MISS key=%s slug=%q kb=%q", key, slug, s.kbName)
	}

	meta, err := s.backend.ReadMeta(s.kbName, slug)
	if err != nil {
		return DocumentMeta{}, err
	}

	// Store in cache.
	if s.cacheEnabled() && meta.OriginalName != "" {
		key := cache.MetaKey(s.kbName, slug)
		if raw, jerr := json.Marshal(meta); jerr == nil {
			if setErr := s.cacheClient.Set(context.Background(), key, raw, s.metaCacheTTL); setErr != nil {
				s.logger.WithModule("cache").Warnf("meta SET failed: key=%s slug=%q err=%v", key, slug, setErr)
			} else {
				s.logger.WithModule("cache").Infof("meta SET  key=%s slug=%q kb=%q ttl=%v size=%d", key, slug, s.kbName, s.metaCacheTTL, len(raw))
			}
		}
	}

	return *meta, nil
}

// WriteChunks delegates to the backend.
func (s *Store) WriteChunks(slug string, chunks []string) error {
	if err := s.backend.DeleteChunks(s.kbName, slug); err != nil {
		return fmt.Errorf("remove old chunks: %w", err)
	}
	for i, content := range chunks {
		chunkID := fmt.Sprintf("%03d", i)
		if err := s.backend.WriteChunk(s.kbName, slug, chunkID, content); err != nil {
			return fmt.Errorf("write chunk %s: %w", chunkID, err)
		}
	}
	return nil
}

// AppendChunks delegates to the backend.
func (s *Store) AppendChunks(slug string, chunks []string) error {
	existing, err := s.backend.ListChunkIDs(s.kbName, slug)
	if err != nil {
		existing = nil
	}
	startID := len(existing)
	for i, content := range chunks {
		chunkID := fmt.Sprintf("%03d", startID+i)
		if err := s.backend.WriteChunk(s.kbName, slug, chunkID, content); err != nil {
			return fmt.Errorf("write chunk %s: %w", chunkID, err)
		}
	}
	return nil
}

// WriteRawText delegates to the backend.
func (s *Store) WriteRawText(slug string, text string) error {
	return s.backend.WriteRawText(s.kbName, slug, text)
}

// AppendChunksIndex reads the existing CHUNKS.toml for a document, appends new
// index entries, and writes the result back. It creates a new index when none
// exists.
func (s *Store) AppendChunksIndex(slug string, newEntries []ChunkIndexEntry) error {
	index, err := s.ReadChunksIndex(slug)
	if err != nil {
		return fmt.Errorf("read existing chunks index: %w", err)
	}
	if index == nil {
		index = &ChunksIndex{
			Slug:       slug,
			ChunkCount: 0,
			Chunks:     nil,
		}
	}
	index.Chunks = append(index.Chunks, newEntries...)
	index.ChunkCount = len(index.Chunks)
	cs, csErr := s.computeChunksChecksum(slug)
	if csErr == nil {
		index.Checksum = cs
	}
	return s.WriteChunksIndex(slug, index)
}

// AppendDocumentText chunks new text and appends it to an existing document.
// It writes new chunk files, updates the search index, and updates meta.json.
// Returns the number of new chunks added.
func (s *Store) AppendDocumentText(slug string, newText string) (int, error) {
	// Verify the document exists.
	meta, err := s.ReadMeta(slug)
	if err != nil {
		return 0, fmt.Errorf("document %q not found: %w", slug, err)
	}

	// Chunk the new text.
	fineChunks, coarseChunks := ChunkTextHierarchical(newText)
	if len(fineChunks) == 0 {
		return 0, nil // nothing to append
	}

	// G12: Incremental boundary merge — when an embedder is configured and
	// there are existing chunks, perform semantic merging at the boundary
	// between old tail and new head chunks.
	oldModified := map[string]string{} // chunkID (e.g. "005") → new content for modified old chunks
	if meta.ChunkCount > 0 && s.embedder != nil {
		n := boundaryMergeN
		m := boundaryMergeM
		if m > len(fineChunks) {
			m = len(fineChunks)
		}

		// Read last N old chunks.
		startOld := meta.ChunkCount - n
		if startOld < 0 {
			startOld = 0
		}
		var oldTail []ChunkWithMeta
		for i := startOld; i < meta.ChunkCount; i++ {
			id := fmt.Sprintf("%03d", i)
			content, readErr := s.ReadChunk(slug, id)
			if readErr != nil {
				continue
			}
			oldTail = append(oldTail, ChunkWithMeta{Content: content})
		}

		if len(oldTail) > 0 && m > 0 {
			newHead := fineChunks[:m]
			boundary := append(oldTail, newHead...)

			merged, mergeErr := MergeSemanticNeighbors(context.Background(), boundary, s.embedder, loadChunkParams().semanticThreshold)
			if mergeErr == nil {
				// Detect changes to old chunks (content modified = absorbed new content).
				for j := 0; j < len(oldTail) && j < len(merged); j++ {
					if merged[j].Content != oldTail[j].Content {
						oldModified[fmt.Sprintf("%03d", startOld+j)] = merged[j].Content
					}
				}

				// Rewrite modified old chunk files.
				for idx, content := range oldModified {
					if writeErr := s.backend.WriteChunk(s.kbName, slug, idx, content); writeErr != nil {
						// Non-fatal: continue with best-effort merge.
						delete(oldModified, idx)
					}
				}

				// Determine which new chunks survived (appear after oldTail in merged output).
				if len(merged) > len(oldTail) {
					survivedNew := merged[len(oldTail):]
					survivedMap := make(map[string]bool, len(survivedNew))
					for _, sc := range survivedNew {
						survivedMap[sc.Content] = true
					}

					// Filter fineChunks to survivors, preserving order.
					filtered := make([]ChunkWithMeta, 0, len(survivedNew))
					for _, fc := range fineChunks {
						if survivedMap[fc.Content] {
							delete(survivedMap, fc.Content)
							filtered = append(filtered, fc)
						}
					}
					fineChunks = filtered

					// If fineChunks changed and coarse chunks exist, rebuild them.
					if len(fineChunks) > 0 && len(coarseChunks) > 0 {
						coarseChunks = rebuildCoarseFromFine(fineChunks)
					}
				} else {
					// All new chunks were absorbed — nothing to append.
					return 0, nil
				}
			}
			// If merge fails, continue with original fineChunks (non-fatal).
		}
	}

	// Step 1: write new chunk files.
	chunks := make([]string, len(fineChunks))
	for i, c := range fineChunks {
		chunks[i] = c.Content
	}
	if err := s.AppendChunks(slug, chunks); err != nil {
		return 0, fmt.Errorf("append chunks: %w", err)
	}

	// Step 2: rewrite section-level chunks.
	if len(coarseChunks) > 0 {
		_ = s.WriteSectionChunks(slug, coarseChunks)
	}

	// Step 3: build index entries — update modified old entries and append new entries.
	// Instead of AppendChunksIndex, we read-modify-write to handle old entry updates.
	index, idxErr := s.ReadChunksIndex(slug)
	if idxErr != nil || index == nil {
		index = &ChunksIndex{
			Slug:       slug,
			ChunkCount: 0,
			Chunks:     nil,
		}
	}

	// Update index entries for modified old chunks.
	if len(oldModified) > 0 {
		entryByID := make(map[string]int)
		for i, e := range index.Chunks {
			entryByID[e.ID] = i
		}
		for chunkID, content := range oldModified {
			tokens := retrieval.Tokens(content)
			tc := retrieval.Counts(tokens)
			replacement := ChunkIndexEntry{
				ID:        chunkID,
				TermCount: len(tokens),
				Terms:     trimTopTerms(tc, maxTermsPerChunk),
			}
			// Preserve original metadata (section, offset, page, vector, etc).
			if pos, ok := entryByID[chunkID]; ok {
				replacement.Section = index.Chunks[pos].Section
				replacement.Offset = index.Chunks[pos].Offset
				replacement.PageStart = index.Chunks[pos].PageStart
				replacement.PageEnd = index.Chunks[pos].PageEnd
				replacement.Vector = index.Chunks[pos].Vector
				replacement.SectionChunkID = index.Chunks[pos].SectionChunkID
				replacement.SectionRole = index.Chunks[pos].SectionRole
				index.Chunks[pos] = replacement
			}
		}
	}

	// Build and append index entries for surviving new chunks.
	for i, c := range fineChunks {
		id := fmt.Sprintf("%03d", meta.ChunkCount+i)
		tokens := retrieval.Tokens(c.Content)
		tc := retrieval.Counts(tokens)
		entry := ChunkIndexEntry{
			ID:          id,
			TermCount:   len(tokens),
			Terms:       trimTopTerms(tc, maxTermsPerChunk),
			Section:     c.Section,
			Offset:      meta.TotalChars + c.Offset,
			PageStart:   c.PageStart,
			PageEnd:     c.PageEnd,
			SectionRole: c.SectionRole,
		}
		if c.SectionID != "" {
			entry.SectionChunkID = c.SectionID
		}
		index.Chunks = append(index.Chunks, entry)
	}
	index.ChunkCount = len(index.Chunks)
	cs, csErr := s.computeChunksChecksum(slug)
	if csErr == nil {
		index.Checksum = cs
	}
	if err := s.WriteChunksIndex(slug, index); err != nil {
		return 0, fmt.Errorf("write chunks index: %w", err)
	}

	// Step 4: update meta.json.
	meta.ChunkCount += len(fineChunks)
	meta.TotalChars += len(newText)
	if err := s.WriteMeta(slug, meta); err != nil {
		return 0, fmt.Errorf("update meta: %w", err)
	}

	return len(fineChunks), nil
}

// rebuildCoarseFromFine rebuilds section-level (coarse) chunks from the given
// fine chunks, grouping by section heading and concatenating content.
// This is used by G12 to regenerate coarse chunks after boundary merge removes
// some fine chunks.
func rebuildCoarseFromFine(fine []ChunkWithMeta) []ChunkWithMeta {
	if len(fine) == 0 {
		return nil
	}
	// Group by section heading.
	sectionGroups := map[string][]ChunkWithMeta{}
	sectionOrder := []string{}
	for _, c := range fine {
		sec := c.Section
		if _, exists := sectionGroups[sec]; !exists {
			sectionOrder = append(sectionOrder, sec)
		}
		sectionGroups[sec] = append(sectionGroups[sec], c)
	}

	var coarse []ChunkWithMeta
	for _, sec := range sectionOrder {
		group := sectionGroups[sec]
		var b strings.Builder
		for j, c := range group {
			if j > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(c.Content)
		}
		coarse = append(coarse, ChunkWithMeta{
			Content:     b.String(),
			Section:     sec,
			Offset:      group[0].Offset,
			SectionID:   sec,
			SectionRole: classifySectionRole(sec),
		})
	}
	return coarse
}

// WriteSectionChunks delegates to the backend.
func (s *Store) WriteSectionChunks(slug string, sections []ChunkWithMeta) error {
	if err := s.backend.DeleteSectionChunks(s.kbName, slug); err != nil {
		return fmt.Errorf("delete old section chunks: %w", err)
	}
	for i, sec := range sections {
		id := fmt.Sprintf("S%02d", i)
		if err := s.backend.WriteSectionChunk(s.kbName, slug, id, sec.Content); err != nil {
			return fmt.Errorf("write section chunk %s: %w", id, err)
		}
	}
	return nil
}

// ReadSectionChunk delegates to chunkStore when available.
func (s *Store) ReadSectionChunk(slug, sectionID string) (string, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ReadSectionChunk(slug, sectionID)
	}
	return s.backend.ReadSectionChunk(s.kbName, slug, sectionID)
}

// ReadChunk delegates to chunkStore when available; falls back to backend+cache.
func (s *Store) ReadChunk(slug, chunkID string) (string, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ReadChunk(slug, chunkID)
	}

	// Legacy path — backend + cache (for tests without chunkStore).
	if s.cacheEnabled() {
		key := cache.ChunkKey(s.kbName, slug, chunkID)
		if raw, err := s.cacheClient.Get(context.Background(), key); err == nil && raw != nil {
			s.logger.WithModule("cache").Infof("chunk HIT  key=%s slug=%q chunk=%s kb=%q size=%d", key, slug, chunkID, s.kbName, len(raw))
			return string(raw), nil
		}
		s.logger.WithModule("cache").Infof("chunk MISS key=%s slug=%q chunk=%s kb=%q", key, slug, chunkID, s.kbName)
	}

	text, err := s.backend.ReadChunk(s.kbName, slug, chunkID)
	if err != nil {
		return "", err
	}

	if s.cacheEnabled() && text != "" {
		key := cache.ChunkKey(s.kbName, slug, chunkID)
		if setErr := s.cacheClient.Set(context.Background(), key, []byte(text), s.chunkCacheTTL); setErr != nil {
			s.logger.WithModule("cache").Warnf("chunk SET failed: key=%s slug=%q chunk=%s err=%v", key, slug, chunkID, setErr)
		} else {
			s.logger.WithModule("cache").Infof("chunk SET  key=%s slug=%q chunk=%s kb=%q ttl=%v size=%d", key, slug, chunkID, s.kbName, s.chunkCacheTTL, len(text))
		}
	}

	return text, nil
}

// ReadRawText delegates to chunkStore when available; falls back to backend.
func (s *Store) ReadRawText(docSlug string) (string, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ReadRawText(docSlug)
	}
	return s.backend.ReadRawText(s.kbName, docSlug)
}

// ListChunks delegates to chunkStore when available; falls back to backend.
func (s *Store) ListChunks(slug string) ([]string, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ListChunks(slug)
	}
	return s.backend.ListChunkIDs(s.kbName, slug)
}

// ListChunkIDs returns chunk IDs for a document (ManageService compatibility).
func (s *Store) ListChunkIDs(slug string) ([]string, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ListChunks(slug)
	}
	return s.backend.ListChunkIDs(s.kbName, slug)
}

// ListSectionChunks delegates to chunkStore when available.
func (s *Store) ListSectionChunks(slug string) ([]string, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ListSectionChunks(slug)
	}
	return s.backend.ListSectionChunkIDs(s.kbName, slug)
}

// SlugFromPath derives a filesystem-safe document slug from a file path.
func SlugFromPath(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	// Replace problematic characters with hyphens.
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	// Collapse consecutive hyphens and trim.
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")
	if name == "" {
		name = "document"
	}
	// Append timestamp suffix for uniqueness.
	suffix := time.Now().Format("20060102-150405.000")
	return name + "-" + suffix
}

// ListDocuments delegates to chunkStore when available.
func (s *Store) ListDocuments() ([]DocumentMeta, error) {
	if s.chunkStore != nil {
		docs, err := s.chunkStore.ListDocuments()
		if err != nil {
			return nil, err
		}
		s.logger.WithModule("store").Debugf("ListDocuments: kb=%q docs=%d", s.kbName, len(docs))
		return docs, nil
	}

	slugs, err := s.backend.ListDocSlugs(s.kbName)
	if err != nil {
		return nil, err
	}
	var docs []DocumentMeta
	for _, slug := range slugs {
		meta, err := s.backend.ReadMeta(s.kbName, slug)
		if err != nil {
			continue
		}
		meta.Slug = slug
		docs = append(docs, *meta)
	}
	s.logger.WithModule("store").Debugf("ListDocuments: kb=%q docs=%d", s.kbName, len(docs))
	return docs, nil
}

// ListDocumentsAll returns metadata for all documents across all knowledge
// bases. When no kbName is set, it traverses every KB subdirectory and
// merges the results. Documents from the current/named KB come first,
// followed by docs from other KBs tagged with their KB name.
func (s *Store) ListDocumentsAll() ([]DocumentMeta, error) {
	kbs, err := s.ListKBs()
	if err != nil {
		return nil, err
	}
	var all []DocumentMeta
	for _, kb := range kbs {
		kbStore := s.WithKB(kb)
		docs, err := kbStore.ListDocuments()
		if err != nil {
			continue
		}
		// Tag each document with its KB name for display.
		for i := range docs {
			docs[i].Tags = append(docs[i].Tags, "kb:"+kb)
		}
		all = append(all, docs...)
	}
	return all, nil
}

// ListChecksum computes a SHA256 checksum over the full list of DocumentMeta
// serialized as JSON. This is used to detect if the knowledge base has changed.
func ListChecksum(docs []DocumentMeta) string {
	h := sha256.New()
	data, _ := json.Marshal(docs)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// ListWithLimit returns up to n documents from the full list.
func (s *Store) ListWithLimit(n int) ([]DocumentMeta, error) {
	docs, err := s.ListDocuments()
	if err != nil {
		return nil, err
	}
	if len(docs) > n {
		docs = docs[:n]
	}
	return docs, nil
}

// Exists delegates to chunkStore when available.
func (s *Store) Exists(slug string) bool {
	if s.chunkStore != nil {
		ok, err := s.chunkStore.Exists(slug)
		return err == nil && ok
	}
	ok, err := s.backend.Exists(s.kbName, slug)
	return err == nil && ok
}

// WriteChunksIndex delegates to the backend and updates the inverted index.
func (s *Store) WriteChunksIndex(slug string, index *ChunksIndex) error {
	if err := s.backend.WriteChunksIndex(s.kbName, slug, index); err != nil {
		return err
	}
	// G7: update the global inverted index via the search engine when available.
	if s.searchEngine != nil {
		_ = s.searchEngine.UpdateInvertedIndex(slug)
	} else {
		_ = s.updateInvertedIndex(slug, index.Chunks)
	}
	return nil
}

// ReadChunksIndex delegates to chunkStore when available.
func (s *Store) ReadChunksIndex(slug string) (*ChunksIndex, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ReadChunksIndex(slug)
	}
	// Try cache first.
	if s.cacheEnabled() {
		key := cache.IndexKey(s.kbName, slug)
		if raw, err := s.cacheClient.Get(context.Background(), key); err == nil && raw != nil {
			var idx ChunksIndex
			if json.Unmarshal(raw, &idx) == nil {
				s.logger.WithModule("cache").Infof("index HIT  key=%s slug=%q kb=%q chunks=%d size=%d", key, slug, s.kbName, len(idx.Chunks), len(raw))
				return &idx, nil
			}
		}
		s.logger.WithModule("cache").Infof("index MISS key=%s slug=%q kb=%q", key, slug, s.kbName)
	}

	idx, err := s.backend.ReadChunksIndex(s.kbName, slug)
	if err != nil || idx == nil {
		return idx, err
	}

	// Store in cache.
	if s.cacheEnabled() {
		key := cache.IndexKey(s.kbName, slug)
		if raw, jerr := json.Marshal(idx); jerr == nil {
			if setErr := s.cacheClient.Set(context.Background(), key, raw, s.indexCacheTTL); setErr != nil {
				s.logger.WithModule("cache").Warnf("index SET failed: key=%s slug=%q err=%v", key, slug, setErr)
			} else {
				s.logger.WithModule("cache").Infof("index SET  key=%s slug=%q kb=%q chunks=%d ttl=%v size=%d", key, slug, s.kbName, len(idx.Chunks), s.indexCacheTTL, len(raw))
			}
		}
	}

	return idx, nil
}

// writeChunksIndexFromMeta builds and persists a ChunksIndex from chunk
// metadata, including pre-computed term frequencies, position info,
// and optionally dense vectors when an embedder is configured.
// It delegates to writeChunksIndexFromMetaWithSections without section data.
func (s *Store) writeChunksIndexFromMeta(slug string, chunks []ChunkWithMeta) error {
	return s.writeChunksIndexFromMetaWithSections(slug, chunks, nil)
}

// writeChunksIndexFromMetaWithSections builds and persists a ChunksIndex from chunk
// metadata, including pre-computed term frequencies, position info,
// and optionally dense vectors when an embedder is configured.
// When sectionChunks is provided, each entry's SectionChunkID is populated
// BuildChunksIndex is the public entry point for building CHUNKS.toml with
// vector embeddings and HNSW index updates. It is exported so the ingest
// engine can call it as a callback during the upload pipeline (Phase 3.5).
//
// Must be called with s.mu held (same goroutine that started the upload).
func (s *Store) BuildChunksIndex(slug string, chunks []ChunkWithMeta, sectionChunks []ChunkWithMeta) error {
	return s.writeChunksIndexFromMetaWithSections(slug, chunks, sectionChunks)
}

// from the chunk's SectionID field.
func (s *Store) writeChunksIndexFromMetaWithSections(slug string, chunks []ChunkWithMeta, sectionChunks []ChunkWithMeta) error {
	index := &ChunksIndex{
		Slug:       slug,
		ChunkCount: len(chunks),
		Chunks:     make([]ChunkIndexEntry, len(chunks)),
	}

	hasEmbedder := s.embedder != nil
	s.logger.Debugf("embed: slug=%q hasEmbedder=%v", slug, hasEmbedder)
	if hasEmbedder {
		s.logger.Debugf("embed: embedder=%T", s.embedder)
		index.HasVectors = true
	}

	// Generate vectors in batch if embedder is available.
	var vectors [][]float32
	if hasEmbedder {
		contents := make([]string, len(chunks))
		for i, c := range chunks {
			contents[i] = c.Content
		}
		var err error
		vectors, err = s.embedder.Embed(context.Background(), contents)
		if err != nil {
			// Non-fatal: continue without vectors.
			s.logger.Warnf("embed: embedding failed for %q: %v", slug, err)
			hasEmbedder = false
			index.VectorDim = 0
			index.HasVectors = false
		} else {
			// Embed succeeded: set dimension from the embedder (which may have
			// auto-detected it from the API response), then validate vectors.
			index.VectorDim = s.embedder.Dim()
			if index.VectorDim <= 0 {
				s.logger.Warnf("embed: embedding returned zero-dimension vectors for %q — disabling vectors", slug)
				hasEmbedder = false
				index.VectorDim = 0
				index.HasVectors = false
			}
		}
	}

	for i, c := range chunks {
		id := fmt.Sprintf("%03d", i)
		tokens := retrieval.Tokens(c.Content)
		tc := retrieval.Counts(tokens)
		entry := ChunkIndexEntry{
			ID:          id,
			TermCount:   len(tokens),
			Terms:       trimTopTerms(tc, maxTermsPerChunk),
			Section:     c.Section,
			Offset:      c.Offset,
			PageStart:   c.PageStart,
			PageEnd:     c.PageEnd,
			SectionRole: c.SectionRole,
		}
		if c.SectionID != "" && sectionChunks != nil {
			entry.SectionChunkID = c.SectionID
		}
		if hasEmbedder && i < len(vectors) && vectors[i] != nil {
			vec64 := make([]float64, len(vectors[i]))
			for j, v := range vectors[i] {
				vec64[j] = float64(v)
			}
			entry.Vector = vec64
		}
		index.Chunks[i] = entry
	}
	cs, csErr := s.computeChunksChecksum(slug)
	if csErr == nil {
		index.Checksum = cs
	}
	if err := s.WriteChunksIndex(slug, index); err != nil {
		return fmt.Errorf("write CHUNKS.toml: %w", err)
	}
	// Update the vector index incrementally (non-fatal).
	// Ensure the vector index exists first — this may trigger a one-time build
	// if this is the first document in the KB. After the incremental update,
	// persist to disk so the index survives restarts.
	if hasEmbedder {
		s.ensureVectorIndexLocked()
		s.updateVectorIndex(slug, index.Chunks)
		if s.vectorIndex != nil {
			if saveErr := s.saveVectorIndex(s.vectorIndex); saveErr != nil {
				s.logger.WithModule("vector").Warnf("save vector index after update: %v", saveErr)
			}
		}
	}
	return nil
}

// ReadChunkContext reads a chunk identified by docSlug and chunkID, optionally
// including up to context adjacent chunks before and after. When context is 0
// it behaves like ReadChunk.
//
// If the document has a CHUNKS.toml with section metadata, adjacent chunks
// under the same section are merged into continuous text with section headers
// (## Section). Otherwise the result is formatted with chunk ID markers as a
// fallback.
func (s *Store) ReadChunkContext(slug, chunkID string, context int) (string, error) {
	if s.chunkStore != nil {
		return s.chunkStore.ReadChunkContext(slug, chunkID, context)
	}

	if context <= 0 {
		return s.ReadChunk(slug, chunkID)
	}

	// Collect all chunk IDs.
	allIDs, err := s.ListChunks(slug)
	if err != nil {
		return "", err
	}

	if len(allIDs) == 0 {
		return "", fmt.Errorf("chunk %q not found in document %q (document has no chunks)", chunkID, slug)
	}

	// Find the position of the target chunk ID in the list.
	targetPos := -1
	for i, cid := range allIDs {
		if cid == chunkID {
			targetPos = i
			break
		}
	}
	if targetPos < 0 {
		return "", fmt.Errorf("chunk %q not found in document %q", chunkID, slug)
	}

	// Determine the context window.
	start := targetPos - context
	if start < 0 {
		start = 0
	}
	end := targetPos + context + 1 // +1 to include the target
	if end > len(allIDs) {
		end = len(allIDs)
	}

	// Try to load section metadata from CHUNKS.toml for richer output.
	sectionByID := map[string]string{}
	hasSections := false
	if index, err := s.ReadChunksIndex(slug); err == nil && index != nil {
		for _, entry := range index.Chunks {
			sectionByID[entry.ID] = entry.Section
			if entry.Section != "" {
				hasSections = true
			}
		}
	}

	var b strings.Builder
	if hasSections {
		// Rich output: merge adjacent chunks under the same section header.
		var lastSection string
		for i := start; i < end; i++ {
			cid := allIDs[i]
			text, err := s.ReadChunk(slug, cid)
			if err != nil {
				continue
			}
			section := sectionByID[cid]
			if section != lastSection {
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				if section != "" {
					b.WriteString("## " + section + "\n")
				}
				lastSection = section
			} else if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(text)
		}
	} else {
		// Fallback: chunk ID markers for documents without section metadata.
		for i := start; i < end; i++ {
			cid := allIDs[i]
			text, err := s.ReadChunk(slug, cid)
			if err != nil {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString(fmt.Sprintf("[%s]\n%s", cid, text))
		}
	}

	if b.Len() == 0 {
		return "", fmt.Errorf("chunk %q not found in document %q", chunkID, slug)
	}
	return b.String(), nil
}

// chunkIDToInt parses a zero-padded chunk ID like "005" to its integer value.
// Deprecated: use with the actual chunk ID list from ListChunks instead.
func chunkIDToInt(chunkID string) int {
	id := 0
	for _, r := range chunkID {
		if r >= '0' && r <= '9' {
			id = id*10 + int(r-'0')
		}
	}
	return id
}

// trimTopTerms keeps only the top n terms with the highest counts, reducing
// the size of the CHUNKS.toml index. Returns a []TermFreq slice sorted by
// count descending. When counts is nil, returns nil.
func trimTopTerms(counts map[string]int, n int) []TermFreq {
	if counts == nil {
		return nil
	}
	if len(counts) == 0 {
		return []TermFreq{}
	}
	type kv struct {
		k string
		v int
	}
	sorted := make([]kv, 0, len(counts))
	for k, v := range counts {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].v > sorted[j].v
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	out := make([]TermFreq, 0, n)
	for _, p := range sorted[:n] {
		out = append(out, TermFreq{Term: p.k, Count: p.v})
	}
	return out
}

// computeChunksChecksum delegates checksum computation to the backend.
func (s *Store) computeChunksChecksum(slug string) (string, error) {
	return s.backend.ComputeChunksChecksum(s.kbName, slug)
}

// ============================================================================
// Vector index — HNSW-based ANN search for dense embeddings
// ============================================================================

// EnsureVectorIndex loads or builds the per-KB HNSW vector index. It is safe to
// call multiple times (idempotent).
func (s *Store) EnsureVectorIndex() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureVectorIndexLocked()
}

// ensureVectorIndexLocked is the internal version of EnsureVectorIndex that
// assumes s.mu is already held. Callers that already hold s.mu (e.g.
// UploadDocumentWithProgress, writeChunksIndexFromMetaWithSections) must use
// this version to avoid a recursive lock.
func (s *Store) ensureVectorIndexLocked() {
	if s.vectorIndex != nil {
		return
	}

	log := s.logger.WithModule("vector")

	// Try loading from persistent cache.
	if idx, err := s.loadVectorIndex(); err == nil && idx != nil {
		s.vectorIndex = idx
		log.Infof("loaded vector index from disk: %d vectors, dim=%d", idx.Len(), idx.Dim())
		return
	} else if err != nil {
		log.Warnf("failed to load vector index from disk, will rebuild: %v", err)
	}

	log.Infof("no cached vector index found, building from scratch...")
	s.buildVectorIndexLocked()
}

// buildVectorIndexLocked rebuilds the HNSW index from all CHUNKS.toml files.
// Must be called with s.mu held. When the build succeeds, the index is
// automatically persisted to VECTOR.gob so subsequent starts can load it
// without a full rebuild.
func (s *Store) buildVectorIndexLocked() {
	log := s.logger.WithModule("vector")

	if s.embedder == nil {
		log.Debugf("buildVectorIndex: no embedder configured, skipping")
		return
	}
	dim := s.embedder.Dim()
	if dim <= 0 {
		log.Debugf("buildVectorIndex: embedder dim=%d, skipping", dim)
		return
	}

	slugs, err := s.backend.ListDocSlugs(s.kbName)
	if err != nil {
		log.Warnf("buildVectorIndex: list docs: %v", err)
		return
	}

	start := time.Now()
	log.Infof("buildVectorIndex: scanning %d documents for vectors (dim=%d)...", len(slugs), dim)

	idx := NewHNSWIndex(dim)
	added := 0
	docsWithVectors := 0
	for i, slug := range slugs {
		index, idxErr := s.ReadChunksIndex(slug)
		if idxErr != nil || index == nil {
			continue
		}
		docAdded := 0
		for _, e := range index.Chunks {
			if len(e.Vector) == dim {
				id := VectorID(slug, e.ID)
				idx.Add(id, e.Vector)
				added++
				docAdded++
			}
		}
		if docAdded > 0 {
			docsWithVectors++
		}
		// Log progress every 50 documents or on the last one.
		if (i+1)%50 == 0 || i == len(slugs)-1 {
			log.Debugf("buildVectorIndex: progress %d/%d docs, %d vectors so far", i+1, len(slugs), added)
		}
	}
	s.vectorIndex = idx
	elapsed := time.Since(start)
	log.Infof("built vector index: %d vectors from %d/%d documents (dim=%d) in %v",
		added, docsWithVectors, len(slugs), dim, elapsed.Round(time.Millisecond))

	// Persist so the next start doesn't need a full rebuild.
	if saveErr := s.saveVectorIndex(idx); saveErr != nil {
		log.Warnf("save vector index after build: %v", saveErr)
	}

	// Update the in-memory vector index cache so future WithKB calls get the rebuilt index.
	if s.vecState.cache != nil && s.kbName != "" {
		s.vecState.mu.Lock()
		s.vecState.cache[s.kbName] = idx
		s.vecState.mu.Unlock()
	}
}

// BuildVectorIndex forces a full rebuild of the HNSW vector index and persists
// it to disk. Call this after bulk-importing documents or when the embedder
// configuration changes.
func (s *Store) BuildVectorIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.logger.WithModule("vector")
	log.Infof("BuildVectorIndex: forcing full rebuild")

	s.vectorIndex = nil
	s.buildVectorIndexLocked()
	if s.vectorIndex != nil {
		stats := s.vectorIndex.Stats()
		log.Infof("BuildVectorIndex: done — %d vectors, %d nodes, maxLevel=%d, avgDegree=%.1f",
			stats.NodeCount, stats.NodeCount, stats.MaxLevel, stats.AvgDegree)
		return s.saveVectorIndex(s.vectorIndex)
	}
	log.Infof("BuildVectorIndex: done — no vectors (embedder not configured?)")
	return nil
}

// updateVectorIndex adds vectors from a single document to the HNSW index and
// removes any previous entries for the same document.
func (s *Store) updateVectorIndex(slug string, entries []ChunkIndexEntry) {
	if s.vectorIndex == nil {
		return
	}

	log := s.logger.WithModule("vector")

	// Remove all old entries for this document before adding new ones.
	// This correctly handles ID format changes (sequential → content-addressed)
	// that happen when a document is re-uploaded via a different code path.
	s.removeDocFromVectorIndex(slug)

	// Add new entries.
	added := 0
	for _, e := range entries {
		if len(e.Vector) > 0 {
			s.vectorIndex.Add(slug+"/"+e.ID, e.Vector)
			added++
		}
	}

	if added > 0 {
		log.Debugf("updateVectorIndex: doc=%s added=%d total=%d",
			slug, added, s.vectorIndex.Len())
	}
}

// removeDocFromVectorIndex removes all vectors belonging to a document.
func (s *Store) removeDocFromVectorIndex(slug string) {
	if s.vectorIndex == nil {
		return
	}
	log := s.logger.WithModule("vector")
	// We don't know the chunk IDs, so iterate through all nodes. This is fine
	// because HNSW Remove is cheap (it only unlinks, no rebalancing).
	prefix := slug + "/"
	removed := 0
	for _, id := range s.vectorIndex.allIDs() {
		if len(id) > len(prefix) && id[:len(prefix)] == prefix {
			if s.vectorIndex.Remove(id) {
				removed++
			}
		}
	}
	if removed > 0 {
		log.Debugf("removeDocFromVectorIndex: doc=%s removed=%d vectors, remaining=%d",
			slug, removed, s.vectorIndex.Len())
	}
}

// vectorIndexPath returns the path to the persisted VECTOR.gob file.
func (s *Store) vectorIndexPath() string {
	if s.dataDir == "" || s.kbName == "" {
		return ""
	}
	return filepath.Join(s.dataDir, s.kbName, "VECTOR.gob")
}

// loadVectorIndex loads the HNSW index from VECTOR.gob.
func (s *Store) loadVectorIndex() (*HNSWIndex, error) {
	p := s.vectorIndexPath()
	if p == "" {
		return nil, nil
	}
	idx, err := LoadHNSWIndex(p)
	if err != nil {
		return nil, err
	}
	if idx != nil {
		s.logger.WithModule("vector").Debugf("loadVectorIndex: path=%s vectors=%d dim=%d", p, idx.Len(), idx.Dim())
	}
	return idx, nil
}

// saveVectorIndex persists the HNSW index to VECTOR.gob.
func (s *Store) saveVectorIndex(idx *HNSWIndex) error {
	p := s.vectorIndexPath()
	if p == "" {
		return nil
	}
	start := time.Now()
	if err := idx.Save(p); err != nil {
		return err
	}
	s.logger.WithModule("vector").Debugf("saveVectorIndex: path=%s vectors=%d elapsed=%v",
		p, idx.Len(), time.Since(start).Round(time.Millisecond))
	return nil
}

// ── Vector statistics & rebuild ────────────────────────────────────────────────

// VectorStats summarises vector coverage in a knowledge base.
type VectorStats struct {
	KBName            string           `json:"kbName"`
	TotalDocs         int              `json:"totalDocs"`
	DocsWithVectors   int              `json:"docsWithVectors"`
	TotalChunks       int              `json:"totalChunks"`
	ChunksWithVectors int              `json:"chunksWithVectors"`
	VectorDim         int              `json:"vectorDim"`
	EmbedderModel     string           `json:"embedderModel,omitempty"`
	Docs              []DocVectorStats `json:"docs,omitempty"`
}

// DocVectorStats holds per-document vector coverage.
type DocVectorStats struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	HasVectors   bool   `json:"hasVectors"`
	VectorDim    int    `json:"vectorDim,omitempty"`
	ChunkCount   int    `json:"chunkCount"`
	VectorChunks int    `json:"vectorChunks"`
	MissingCount int    `json:"missingCount"`
}

// RebuildResult reports the outcome of a vector rebuild operation.
type RebuildResult struct {
	DocsProcessed  int `json:"docsProcessed"`
	ChunksEmbedded int `json:"chunksEmbedded"`
	DocsSkipped    int `json:"docsSkipped"`
}

// GetVectorStats scans chunks_index entries and returns per-document and
// aggregate vector coverage statistics.
func (s *Store) GetVectorStats() (*VectorStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := &VectorStats{KBName: s.kbName}
	if s.embedder != nil {
		stats.EmbedderModel = s.EmbedderInfo()["model"].(string)
		stats.VectorDim = s.embedder.Dim()
	}

	slugs, err := s.backend.ListDocSlugs(s.kbName)
	if err != nil {
		return nil, fmt.Errorf("list slugs: %w", err)
	}
	stats.TotalDocs = len(slugs)

	for _, slug := range slugs {
		index, idxErr := s.backend.ReadChunksIndex(s.kbName, slug)
		if idxErr != nil {
			continue
		}
		if index == nil {
			continue
		}

		meta, _ := s.backend.ReadMeta(s.kbName, slug)
		name := slug
		if meta != nil {
			name = meta.OriginalName
		}

		ds := DocVectorStats{
			Slug:       slug,
			Name:       name,
			HasVectors: index.HasVectors,
			VectorDim:  index.VectorDim,
			ChunkCount: len(index.Chunks),
		}

		if index.HasVectors {
			stats.DocsWithVectors++
			for _, e := range index.Chunks {
				if len(e.Vector) > 0 {
					ds.VectorChunks++
				}
			}
			ds.MissingCount = ds.ChunkCount - ds.VectorChunks
		}

		stats.TotalChunks += ds.ChunkCount
		stats.ChunksWithVectors += ds.VectorChunks
		stats.Docs = append(stats.Docs, ds)
	}

	return stats, nil
}

// GetVectorIndexInfo returns basic information about the in-memory vector index.
func (s *Store) GetVectorIndexInfo() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info := map[string]any{
		"kbName": s.kbName,
	}
	if s.embedder != nil {
		info["embedder"] = s.EmbedderInfo()
	}
	if s.vectorIndex != nil {
		info["index"] = map[string]any{
			"loaded": true,
			"len":    s.vectorIndex.Len(),
		}
	} else {
		info["index"] = map[string]any{
			"loaded": false,
		}
	}
	return info, nil
}

// ReEmbedMissingVectors regenerates embedding vectors for documents that lack
// them. When slug is non-empty, only that document is processed. Progress is
// reported via the callback: func(current, total int, docName string).
func (s *Store) ReEmbedMissingVectors(ctx context.Context, slug string, progress func(int, int, string)) (*RebuildResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.logger.WithModule("vector-rebuild")

	if s.embedder == nil || s.embedder.Dim() <= 0 {
		return nil, fmt.Errorf("embedder not configured")
	}

	var slugs []string
	if slug != "" {
		slugs = []string{slug}
	} else {
		var err error
		slugs, err = s.backend.ListDocSlugs(s.kbName)
		if err != nil {
			return nil, fmt.Errorf("list slugs: %w", err)
		}
	}

	result := &RebuildResult{}
	dim := s.embedder.Dim()

	for si, slug := range slugs {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		index, err := s.backend.ReadChunksIndex(s.kbName, slug)
		if err != nil {
			log.Warnf("skip %q: read chunks_index: %v", slug, err)
			result.DocsSkipped++
			continue
		}
		if index == nil {
			log.Warnf("skip %q: no chunks_index", slug)
			result.DocsSkipped++
			continue
		}

		// Collect chunk texts that need vectors.
		type job struct {
			idx     int
			chunkID string
			text    string
		}
		var jobs []job
		for i, e := range index.Chunks {
			if len(e.Vector) == dim {
				continue // already has a valid vector
			}
			text, readErr := s.backend.ReadChunk(s.kbName, slug, e.ID)
			if readErr != nil {
				log.Warnf("skip chunk %q/%q: %v", slug, e.ID, readErr)
				continue
			}
			jobs = append(jobs, job{idx: i, chunkID: e.ID, text: text})
		}

		if len(jobs) == 0 {
			log.Infof("skip %q: all %d chunks have vectors", slug, len(index.Chunks))
			result.DocsSkipped++
			continue
		}

		log.Infof("embedding %d/%d chunks for %q", len(jobs), len(index.Chunks), slug)

		// Embed in batches (max 20 texts per call — embedder HTTP client has 30s timeout).
		batchSize := 20
		embedded := 0
		for start := 0; start < len(jobs); start += batchSize {
			end := start + batchSize
			if end > len(jobs) {
				end = len(jobs)
			}
			batch := jobs[start:end]

			texts := make([]string, len(batch))
			for i, j := range batch {
				texts[i] = j.text
			}

			vectors, embErr := s.embedder.Embed(ctx, texts)
			if embErr != nil {
				return result, fmt.Errorf("embed %q (batch %d-%d): %w", slug, start, end, embErr)
			}

			for i, j := range batch {
				if i < len(vectors) && len(vectors[i]) == dim {
					// Convert float32 → float64 for ChunkIndexEntry.Vector.
					vec := make([]float64, dim)
					for k, v := range vectors[i] {
						vec[k] = float64(v)
					}
					index.Chunks[j.idx].Vector = vec
					embedded++
				}
			}

			if progress != nil {
				progress(si*len(index.Chunks)+embedded, len(slugs)*len(index.Chunks), slug)
			}
		}

		if embedded > 0 {
			index.HasVectors = true
			index.VectorDim = dim

			if err := s.backend.WriteChunksIndex(s.kbName, slug, index); err != nil {
				return result, fmt.Errorf("write chunks_index for %q: %w", slug, err)
			}

			result.DocsProcessed++
			result.ChunksEmbedded += embedded
			log.Infof("embedded %d vectors for %q", embedded, slug)
		}
	}

	// Build / rebuild the HNSW index once after all embeddings are done.
	// This covers three cases:
	//   - vectors were just embedded  → full rebuild with complete data
	//   - VECTOR.gob was missing      → build from existing vectors in CHUNKS.toml
	//   - both                        → one clean rebuild after embed
	if result.DocsProcessed > 0 || s.vectorIndex == nil {
		s.buildVectorIndexLocked()
	}

	return result, nil
}
