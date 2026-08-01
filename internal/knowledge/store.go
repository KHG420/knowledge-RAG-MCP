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
	"knowledge-mcp/internal/retrieval"
)

const maxTermsPerChunk = 50 // top-N frequent terms retained in CHUNKS.toml

const boundaryMergeN = 5 // G12: number of old tail chunks for incremental boundary merge
const boundaryMergeM = 5 // G12: number of new head chunks for incremental boundary merge

// Store manages the knowledge base with a MySQL-backed StorageBackend.
// Use NewStoreWithBackend to create a Store with a MySQL backend.
type Store struct {
	backend              StorageBackend // pluggable storage (MySQLBackend)
	kbName               string         // knowledge base name; empty means flat legacy mode (no subdirectory)
	dataDir              string         // root directory for file-based artifacts (VECTOR.gob, tasks, etc.)
	rewriter             QueryRewriter         // active rewriter (kept for backward compat)
	synonymRewriter      *SynonymRewriter      // always-on dictionary-based expansion
	llmRewriter          *LLMQueryRewriter     // optional LLM rewriter for complex queries only
	embedder             Embedder
	reranker             Reranker
	kbRouter             *KBRouter // v4: KB router for multi-KB retrieval
	gpuScheduler         *GPUScheduler
	dictRelatedTerms     []string // v4: related_terms from dictionaries/*.yaml
	rerankCandidateLimit int // max candidates fed to reranker (default 100)
	rerankBatchSize      int // max documents per reranker request (default 20)
	searchLogger         SearchLogger
	AbstractBoost        float64 // G13: multiplier for abstract-section chunks in papers (default 1.1)
	logger               *logging.Logger
	mu                   *sync.Mutex
	taskManager          *UploadTaskManager
	vectorIndex          *HNSWIndex        // per-KB vector index for fast ANN search
	vectorIndexCache     map[string]*HNSWIndex // in-memory cache of loaded vector indexes by kbName
	vectorIndexCacheMu   sync.RWMutex          // guards vectorIndexCache reads/writes
	rerankCache          map[string][]float64  // in-memory cache of rerankTop scores (key: query+texts hash)
	rerankCacheMu        sync.RWMutex          // guards rerankCache reads/writes
	tombstoneManager     *TombstoneManager // cached tombstone manager (lazy init)

	// Runtime configuration
	config     *config.Config // reference to loaded configuration for API exposure
	configPath string         // path to the config file on disk
	settings   *storeSettings // hot-reloadable runtime settings (pointer to avoid lock copy)

	// ── Cache layer ──
	cacheClient    cache.Cache // pluggable cache backend (nil or NoopCache = disabled)
	queryCacheTTL  time.Duration
	chunkCacheTTL  time.Duration
	metaCacheTTL   time.Duration
	indexCacheTTL  time.Duration
	kbListCacheTTL time.Duration
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
	return &Store{
		backend:       backend,
		dataDir:       dataDir,
		AbstractBoost: 1.1,
		logger:        logging.NewNopLogger(),
		mu:            &sync.Mutex{},
		settings:      &s,
		taskManager:   NewUploadTaskManager(tasksDir, logging.NewNopLogger()),
	}
}

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
	if s.vectorIndexCache != nil {
		s.vectorIndexCacheMu.RLock()
		if idx, ok := s.vectorIndexCache[name]; ok {
			s.vectorIndexCacheMu.RUnlock()
			cp.vectorIndex = idx
			return &cp
		}
		s.vectorIndexCacheMu.RUnlock()
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
		s.vectorIndexCacheMu.Lock()
		if s.vectorIndexCache == nil {
			s.vectorIndexCache = make(map[string]*HNSWIndex)
		}
		s.vectorIndexCache[name] = cp.vectorIndex
		s.vectorIndexCacheMu.Unlock()
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
	s.kbRouter = r
}

// SyncKBRouterDescs refreshes the KB router's cached name/description list
// from the backend. Call after CreateKB or DeleteKB.
func (s *Store) SyncKBRouterDescs() error {
	if s.kbRouter == nil {
		return nil
	}
	kbs, err := s.backend.ListKBs()
	if err != nil {
		return err
	}
	descs := make([]kbDesc, len(kbs))
	for i, kb := range kbs {
		descs[i] = kbDesc{Name: kb.Name, Desc: kb.Description}
	}
	s.kbRouter.SetKBDescs(descs)
	return nil
}

// RouteKBs uses the configured KB router to select the best 1–3 KBs for the
// query. Returns nil when no router is configured (caller should fall back to
// cross-KB search).
func (s *Store) RouteKBs(query string) []string {
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
	// Also set as the default rewriter for backward compatibility.
	s.rewriter = r
}

// SetLLMRewriter configures the optional LLM-based query rewriter, used only
// for complex queries (triage=TriageComplex). When nil (no DeepSeek API key),
// complex queries fall back to the SynonymRewriter.
func (s *Store) SetLLMRewriter(r *LLMQueryRewriter) {
	s.llmRewriter = r
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
	SetParserGPUScheduler(g)
}

// SetCache configures the pluggable cache backend and TTL values.
// Pass nil or cache.NoopCache to disable caching.
func (s *Store) SetCache(c cache.Cache, cfg *config.Config) {
	if cache.IsNoop(c) {
		s.cacheClient = nil
		return
	}
	s.cacheClient = c
	if cfg != nil {
		s.queryCacheTTL = time.Duration(cfg.CacheQueryTTL) * time.Second
		s.chunkCacheTTL = time.Duration(cfg.CacheChunkTTL) * time.Second
		s.metaCacheTTL = time.Duration(cfg.CacheMetaTTL) * time.Second
		s.indexCacheTTL = time.Duration(cfg.CacheIndexTTL) * time.Second
		s.kbListCacheTTL = time.Duration(cfg.CacheKBListTTL) * time.Second
	}
}

// cacheEnabled returns true when the cache backend is active.
func (s *Store) cacheEnabled() bool { return s.cacheClient != nil && !cache.IsNoop(s.cacheClient) }

// InvalidateDoc removes all cached entries for a document (chunks, meta, index).
func (s *Store) InvalidateDoc(docSlug string) {
	if !s.cacheEnabled() {
		return
	}
	pattern := cache.DocInvalidatePattern(s.kbName, docSlug)
	log := s.logger.WithModule("cache")
	n, err := s.cacheClient.DeletePattern(context.Background(), pattern)
	if err != nil {
		log.Warnf("invalidate doc %q FAILED: pattern=%q err=%v", docSlug, pattern, err)
		return
	}
	if n > 0 {
		log.Infof("invalidate doc %q: deleted %d keys (pattern=%q)", docSlug, n, pattern)
	} else {
		log.Debugf("invalidate doc %q: no keys matched (pattern=%q)", docSlug, pattern)
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
	return s.backend.ListKBs()
}

// CreateKB delegates to the backend.
func (s *Store) CreateKB(name, description string) error {
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

// DeleteKB delegates to the backend.
func (s *Store) DeleteKB(name string) error {
	s.logger.Infof("KB %q: deleting", name)
	err := s.backend.DeleteKB(name)
	if err != nil {
		s.logger.Errorf("KB %q: delete failed: %v", name, err)
		return err
	}
	s.InvalidateKBList()
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

// ReadIndex delegates to the backend.
func (s *Store) ReadIndex() (string, error) {
	return s.backend.ReadIndex(s.kbName)
}

// WriteIndex delegates to the backend.
func (s *Store) WriteIndex(content string) error {
	return s.backend.WriteIndex(s.kbName, content)
}

// WriteMeta delegates to the backend.
func (s *Store) WriteMeta(slug string, meta DocumentMeta) error {
	return s.backend.WriteMeta(s.kbName, slug, &meta)
}

// ReadMeta delegates to the backend, with cache layer.
func (s *Store) ReadMeta(slug string) (DocumentMeta, error) {
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

			merged, mergeErr := MergeSemanticNeighbors(context.Background(), boundary, s.embedder, chunkSemanticThreshold)
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

// ReadSectionChunk delegates to the backend.
func (s *Store) ReadSectionChunk(slug, sectionID string) (string, error) {
	return s.backend.ReadSectionChunk(s.kbName, slug, sectionID)
}

// ReadChunk delegates to the backend, with cache layer.
func (s *Store) ReadChunk(slug, chunkID string) (string, error) {
	// Try cache first.
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

	// Store in cache.
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

// ListChunks delegates to the backend.
func (s *Store) ListChunks(slug string) ([]string, error) {
	return s.backend.ListChunkIDs(s.kbName, slug)
}

// ListSectionChunks delegates to the backend.
func (s *Store) ListSectionChunks(slug string) ([]string, error) {
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

// ListDocuments delegates to the backend.
func (s *Store) ListDocuments() ([]DocumentMeta, error) {
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

// ListPreviewAll returns up to n documents across all knowledge bases for
// display, merging results from every KB. The display slice is capped at n.
func (s *Store) ListPreviewAll(n int) (display []DocumentMeta, full []DocumentMeta, err error) {
	full, err = s.ListDocumentsAll()
	if err != nil {
		return nil, nil, err
	}
	if len(full) > n {
		display = full[:n]
	} else {
		display = full
	}
	return display, full, nil
}

// SnapshotPath kept for backward compatibility.
func (s *Store) SnapshotPath() string {
	return ""
}

// ListChecksum computes a SHA256 checksum over the full list of DocumentMeta
// serialized as JSON. This is used to detect if the knowledge base has changed.
func ListChecksum(docs []DocumentMeta) string {
	h := sha256.New()
	data, _ := json.Marshal(docs)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// WriteListSnapshot delegates to the backend.
func (s *Store) WriteListSnapshot(docs []DocumentMeta) error {
	return s.backend.WriteSnapshot(s.kbName, docs)
}

// ReadListSnapshot delegates to the backend.
func (s *Store) ReadListSnapshot() (checksum string, docs []DocumentMeta, err error) {
	return s.backend.ReadSnapshot(s.kbName)
}

// ListWithSnapshot returns the full document list using backend snapshots.
func (s *Store) ListWithSnapshot() ([]DocumentMeta, error) {
	docs, err := s.ListDocuments()
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return docs, nil
	}
	cs := ListChecksum(docs)
	savedCS, _, _ := s.backend.ReadSnapshot(s.kbName)
	if savedCS != cs {
		_ = s.backend.WriteSnapshot(s.kbName, docs)
	}
	return docs, nil
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

// Exists delegates to the backend.
func (s *Store) Exists(slug string) bool {
	ok, err := s.backend.Exists(s.kbName, slug)
	return err == nil && ok
}

// WriteChunksIndex delegates to the backend and updates the inverted index.
func (s *Store) WriteChunksIndex(slug string, index *ChunksIndex) error {
	if err := s.backend.WriteChunksIndex(s.kbName, slug, index); err != nil {
		return err
	}
	// G7: update the global inverted index. Non-fatal.
	if err := s.updateInvertedIndex(slug, index.Chunks); err != nil {
		// Non-fatal: inverted index update failure doesn't block search.
	}
	return nil
}

// ReadChunksIndex delegates to the backend, with cache layer.
func (s *Store) ReadChunksIndex(slug string) (*ChunksIndex, error) {
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
// the size of the CHUNKS.toml index. Returns a []termFreq slice sorted by
// count descending. When counts is nil, returns nil.
func trimTopTerms(counts map[string]int, n int) []termFreq {
	if counts == nil {
		return nil
	}
	if len(counts) == 0 {
		return []termFreq{}
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
	out := make([]termFreq, 0, n)
	for _, p := range sorted[:n] {
		out = append(out, termFreq{Term: p.k, Count: p.v})
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
				id := slug + "/" + e.ID
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
	if s.vectorIndexCache != nil && s.kbName != "" {
		s.vectorIndexCacheMu.Lock()
		s.vectorIndexCache[s.kbName] = idx
		s.vectorIndexCacheMu.Unlock()
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
