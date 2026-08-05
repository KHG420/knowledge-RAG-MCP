// Package chunkstore implements chunk-level persistence: CRUD, indexes,
// manifests, tombstones, checksums, versioning, and atomic staging.
// It is extracted from the Store God Object (REFACTOR_PLAN Phase 3.3).
//
// Engine implements knowledge.ChunkStore and wraps a knowledge.StorageBackend
// with higher-level operations.
package chunkstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// Engine manages chunk-level persistence: CRUD, indexes, manifests,
// tombstones, checksums, versioning, and atomic staging.
//
// It implements knowledge.ChunkStore.
type Engine struct {
	// ── Storage backend ──
	backend knowledge.StorageBackend

	// ── KB identity ──
	kbName  string
	dataDir string

	// ── Tombstone ──
	tombstoneManager *knowledge.TombstoneManager

	// ── Cache layer ──
	cacheClient    knowledge.CacheClient
	chunkCacheTTL  time.Duration
	metaCacheTTL   time.Duration
	indexCacheTTL  time.Duration
	kbListCacheTTL time.Duration

	// ── Infrastructure ──
	logger *logging.Logger
	mu     *sync.Mutex
}

// New creates an Engine with the given backend and KB identity.
func New(backend knowledge.StorageBackend, kbName, dataDir string, mu *sync.Mutex, logger *logging.Logger) *Engine {
	return &Engine{
		backend:        backend,
		kbName:         kbName,
		dataDir:        dataDir,
		logger:         logger,
		mu:             mu,
		chunkCacheTTL:  5 * time.Minute,
		metaCacheTTL:   5 * time.Minute,
		indexCacheTTL:  5 * time.Minute,
		kbListCacheTTL: 1 * time.Minute,
	}
}

// ── KB identity ─────────────────────────────────────────────────────────────

func (e *Engine) KBName() string                   { return e.kbName }
func (e *Engine) DataDir() string                  { return e.dataDir }
func (e *Engine) Backend() knowledge.StorageBackend { return e.backend }
func (e *Engine) SetKBName(name string)            { e.kbName = name }
func (e *Engine) SetDataDir(dir string)            { e.dataDir = dir }

// ── Tombstone ────────────────────────────────────────────────────────────────

func (e *Engine) TombstoneManager() *knowledge.TombstoneManager { return e.tombstoneManager }
func (e *Engine) SetTombstoneManager(tm *knowledge.TombstoneManager) {
	e.tombstoneManager = tm
}

// ── Logger ───────────────────────────────────────────────────────────────────

func (e *Engine) Logger() *logging.Logger     { return e.logger }
func (e *Engine) SetLogger(l *logging.Logger) { e.logger = l }

// ── Mutex ────────────────────────────────────────────────────────────────────

func (e *Engine) Mutex() *sync.Mutex { return e.mu }

// ── Cache wiring ────────────────────────────────────────────────────────────

// SetCacheClient configures the cache backend.
func (e *Engine) SetCacheClient(client knowledge.CacheClient) { e.cacheClient = client }

// SetCacheTTLs configures the cache TTLs for chunks, metadata, and indexes.
func (e *Engine) SetCacheTTLs(chunkTTL, metaTTL, indexTTL, kbListTTL time.Duration) {
	e.chunkCacheTTL = chunkTTL
	e.metaCacheTTL = metaTTL
	e.indexCacheTTL = indexTTL
	e.kbListCacheTTL = kbListTTL
}

// cacheEnabled reports whether the cache layer is available.
func (e *Engine) cacheEnabled() bool { return e.cacheClient != nil }

// NOTE: All cache operations (Get/Set/Delete/DeletePattern) in this file
// intentionally use context.Background() because cache reads and writes
// are best-effort and insensitive to request cancellation — a cancelled
// search should not prevent the cache from being populated for future
// requests, and a stale cache entry is harmless.

// =============================================================================
// Chunk CRUD — migrated from Store
// =============================================================================

// ReadChunk reads a single chunk by doc slug and chunk ID, with cache layer.
func (e *Engine) ReadChunk(slug, chunkID string) (string, error) {
	if e.cacheEnabled() {
		key := chunkCacheKey(e.kbName, slug, chunkID)
		if raw, err := e.cacheClient.Get(context.Background(), key); err == nil && raw != nil {
			e.logger.WithModule("cache").Infof("chunk HIT  key=%s slug=%q chunk=%s kb=%q size=%d", key, slug, chunkID, e.kbName, len(raw))
			return string(raw), nil
		}
		e.logger.WithModule("cache").Infof("chunk MISS key=%s slug=%q chunk=%s kb=%q", key, slug, chunkID, e.kbName)
	}

	text, err := e.backend.ReadChunk(e.kbName, slug, chunkID)
	if err != nil {
		return "", err
	}

	if e.cacheEnabled() && text != "" {
		key := chunkCacheKey(e.kbName, slug, chunkID)
		if setErr := e.cacheClient.Set(context.Background(), key, []byte(text), e.chunkCacheTTL); setErr != nil {
			e.logger.WithModule("cache").Warnf("chunk SET failed: key=%s slug=%q chunk=%s err=%v", key, slug, chunkID, setErr)
		} else {
			e.logger.WithModule("cache").Infof("chunk SET  key=%s slug=%q chunk=%s kb=%q ttl=%v size=%d", key, slug, chunkID, e.kbName, e.chunkCacheTTL, len(text))
		}
	}

	return text, nil
}

// ReadChunkContext reads a chunk with optional context chunks before and after.
func (e *Engine) ReadChunkContext(slug, chunkID string, context int) (string, error) {
	if context <= 0 {
		return e.ReadChunk(slug, chunkID)
	}

	allIDs, err := e.ListChunks(slug)
	if err != nil {
		return "", err
	}

	if len(allIDs) == 0 {
		return "", fmt.Errorf("chunk %q not found in document %q (document has no chunks)", chunkID, slug)
	}

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

	start := targetPos - context
	if start < 0 {
		start = 0
	}
	end := targetPos + context + 1
	if end > len(allIDs) {
		end = len(allIDs)
	}

	sectionByID := map[string]string{}
	hasSections := false
	if index, err := e.ReadChunksIndex(slug); err == nil && index != nil {
		for _, entry := range index.Chunks {
			sectionByID[entry.ID] = entry.Section
			if entry.Section != "" {
				hasSections = true
			}
		}
	}

	var b strings.Builder
	if hasSections {
		var lastSection string
		for i := start; i < end; i++ {
			cid := allIDs[i]
			text, err := e.ReadChunk(slug, cid)
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
		for i := start; i < end; i++ {
			cid := allIDs[i]
			text, err := e.ReadChunk(slug, cid)
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

// ReadChunksIndex reads the per-document chunk index (CHUNKS.toml), with cache.
func (e *Engine) ReadChunksIndex(slug string) (*knowledge.ChunksIndex, error) {
	if e.cacheEnabled() {
		key := indexCacheKey(e.kbName, slug)
		if raw, err := e.cacheClient.Get(context.Background(), key); err == nil && raw != nil {
			var idx knowledge.ChunksIndex
			if json.Unmarshal(raw, &idx) == nil {
				e.logger.WithModule("cache").Infof("index HIT  key=%s slug=%q kb=%q chunks=%d size=%d", key, slug, e.kbName, len(idx.Chunks), len(raw))
				return &idx, nil
			}
		}
		e.logger.WithModule("cache").Infof("index MISS key=%s slug=%q kb=%q", key, slug, e.kbName)
	}

	idx, err := e.backend.ReadChunksIndex(e.kbName, slug)
	if err != nil || idx == nil {
		return idx, err
	}

	if e.cacheEnabled() {
		key := indexCacheKey(e.kbName, slug)
		if raw, jerr := json.Marshal(idx); jerr == nil {
			if setErr := e.cacheClient.Set(context.Background(), key, raw, e.indexCacheTTL); setErr != nil {
				e.logger.WithModule("cache").Warnf("index SET failed: key=%s slug=%q err=%v", key, slug, setErr)
			} else {
				e.logger.WithModule("cache").Infof("index SET  key=%s slug=%q kb=%q chunks=%d ttl=%v size=%d", key, slug, e.kbName, len(idx.Chunks), e.indexCacheTTL, len(raw))
			}
		}
	}

	return idx, nil
}

// ReadSectionChunk reads a section-level chunk.
func (e *Engine) ReadSectionChunk(slug, sectionID string) (string, error) {
	return e.backend.ReadSectionChunk(e.kbName, slug, sectionID)
}

// ReadRawText reads the raw (pre-chunked) content of a document.
func (e *Engine) ReadRawText(slug string) (string, error) {
	return e.backend.ReadRawText(e.kbName, slug)
}

// ListChunks returns all chunk IDs for a document.
func (e *Engine) ListChunks(slug string) ([]string, error) {
	return e.backend.ListChunkIDs(e.kbName, slug)
}

// ListSectionChunks returns all section chunk IDs for a document.
func (e *Engine) ListSectionChunks(slug string) ([]string, error) {
	return e.backend.ListSectionChunkIDs(e.kbName, slug)
}

// ── Metadata ─────────────────────────────────────────────────────────────────

// ReadMeta reads document metadata (meta.json), with cache.
func (e *Engine) ReadMeta(slug string) (*knowledge.DocumentMeta, error) {
	if e.cacheEnabled() {
		key := metaCacheKey(e.kbName, slug)
		if raw, err := e.cacheClient.Get(context.Background(), key); err == nil && raw != nil {
			var meta knowledge.DocumentMeta
			if json.Unmarshal(raw, &meta) == nil {
				e.logger.WithModule("cache").Infof("meta HIT  key=%s slug=%q kb=%q", key, slug, e.kbName)
				return &meta, nil
			}
		}
		e.logger.WithModule("cache").Infof("meta MISS key=%s slug=%q kb=%q", key, slug, e.kbName)
	}

	meta, err := e.backend.ReadMeta(e.kbName, slug)
	if err != nil {
		return nil, err
	}

	if e.cacheEnabled() && meta != nil && meta.OriginalName != "" {
		key := metaCacheKey(e.kbName, slug)
		if raw, jerr := json.Marshal(meta); jerr == nil {
			if setErr := e.cacheClient.Set(context.Background(), key, raw, e.metaCacheTTL); setErr != nil {
				e.logger.WithModule("cache").Warnf("meta SET failed: key=%s slug=%q err=%v", key, slug, setErr)
			} else {
				e.logger.WithModule("cache").Infof("meta SET  key=%s slug=%q kb=%q ttl=%v size=%d", key, slug, e.kbName, e.metaCacheTTL, len(raw))
			}
		}
	}

	return meta, nil
}

// WriteMeta writes document metadata to the backend.
func (e *Engine) WriteMeta(slug string, meta *knowledge.DocumentMeta) error {
	return e.backend.WriteMeta(e.kbName, slug, meta)
}

// ── Document listing ─────────────────────────────────────────────────────────

// ListDocuments returns metadata for all documents in the current KB.
func (e *Engine) ListDocuments() ([]knowledge.DocumentMeta, error) {
	slugs, err := e.backend.ListDocSlugs(e.kbName)
	if err != nil {
		return nil, err
	}
	var docs []knowledge.DocumentMeta
	for _, slug := range slugs {
		meta, metaErr := e.backend.ReadMeta(e.kbName, slug)
		if metaErr != nil || meta == nil {
			continue
		}
		meta.Slug = slug
		docs = append(docs, *meta)
	}
	e.logger.WithModule("store").Debugf("ListDocuments: kb=%q docs=%d", e.kbName, len(docs))
	return docs, nil
}

// ListDocumentsAll returns metadata for all documents across all KBs.
func (e *Engine) ListDocumentsAll() ([]knowledge.DocumentMeta, error) {
	kbs, err := e.ListKBs()
	if err != nil {
		return nil, err
	}
	var all []knowledge.DocumentMeta
	for _, kb := range kbs {
		kbEngine := e.WithKB(kb)
		docs, listErr := kbEngine.ListDocuments()
		if listErr != nil {
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

// ListWithLimit returns at most n document metadata entries.
func (e *Engine) ListWithLimit(n int) ([]knowledge.DocumentMeta, error) {
	docs, err := e.ListDocuments()
	if err != nil {
		return nil, err
	}
	if len(docs) > n {
		docs = docs[:n]
	}
	return docs, nil
}

// Exists reports whether a document exists.
func (e *Engine) Exists(slug string) (bool, error) {
	ok, err := e.backend.Exists(e.kbName, slug)
	return err == nil && ok, nil
}

// ── INDEX.md ─────────────────────────────────────────────────────────────────

// ReadIndex reads the knowledge base INDEX.md file.
func (e *Engine) ReadIndex() (string, error) {
	return e.backend.ReadIndex(e.kbName)
}

// WriteIndex writes the knowledge base INDEX.md file.
func (e *Engine) WriteIndex(content string) error {
	return e.backend.WriteIndex(e.kbName, content)
}

// ── KB listing (internal helper) ─────────────────────────────────────────────

// ListKBs returns the names of all knowledge bases.
func (e *Engine) ListKBs() ([]string, error) {
	kbInfos, err := e.backend.ListKBs()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(kbInfos))
	for i, info := range kbInfos {
		names[i] = info.Name
	}
	return names, nil
}

// ── Manifest & versioning (delegated to backend) ────────────────────────────

func (e *Engine) ReadManifest(slug string) (*knowledge.ChunkManifest, error) {
	return e.backend.ReadManifest(e.kbName, slug)
}

func (e *Engine) WriteManifest(slug string, manifest *knowledge.ChunkManifest) error {
	return e.backend.WriteManifest(e.kbName, slug, manifest)
}

func (e *Engine) ActiveVersion(slug string) (int, error) {
	return e.backend.ActiveVersion(e.kbName, slug)
}

// ── Tombstone ────────────────────────────────────────────────────────────────

func (e *Engine) IsTombstoned(slug string) bool {
	if e.tombstoneManager == nil {
		return false
	}
	return e.tombstoneManager.IsTombstoned(slug)
}

func (e *Engine) GetTombstone(slug string) (*knowledge.Tombstone, error) {
	if e.tombstoneManager == nil {
		return nil, nil
	}
	return e.tombstoneManager.Get(slug), nil
}

func (e *Engine) TombstoneCount() int {
	if e.tombstoneManager == nil {
		return 0
	}
	return e.tombstoneManager.Count()
}

func (e *Engine) CleanExpiredTombstones() int {
	if e.tombstoneManager == nil {
		return 0
	}
	expired := e.tombstoneManager.ExpiredSlugs(time.Now())
	if len(expired) == 0 {
		return 0
	}
	_ = e.tombstoneManager.Cleanup(expired)
	return len(expired)
}

// ── Checksums ────────────────────────────────────────────────────────────────

func (e *Engine) ComputeChunksChecksum(slug string) (string, error) {
	return e.backend.ComputeChunksChecksum(e.kbName, slug)
}

// ── Atomic staging (delegated to backend) ────────────────────────────────────

func (e *Engine) PrepareStaging(slug string) error {
	return e.backend.PrepareStaging(e.kbName, slug)
}

func (e *Engine) PromoteStaging(slug string, newVersion int) error {
	return e.backend.PromoteStaging(e.kbName, slug, newVersion)
}

func (e *Engine) CleanStaging(slug string) error {
	return e.backend.CleanStaging(e.kbName, slug)
}

// ── WithKB ───────────────────────────────────────────────────────────────────

// WithKB returns a new Engine scoped to the given KB name, sharing the same
// backend, cache, and logger.
func (e *Engine) WithKB(kbName string) knowledge.ChunkStore {
	clone := &Engine{
		backend:          e.backend,
		kbName:           kbName,
		dataDir:          e.dataDir,
		tombstoneManager: e.tombstoneManager,
		cacheClient:      e.cacheClient,
		chunkCacheTTL:    e.chunkCacheTTL,
		metaCacheTTL:     e.metaCacheTTL,
		indexCacheTTL:    e.indexCacheTTL,
		kbListCacheTTL:   e.kbListCacheTTL,
		logger:           e.logger,
		mu:               e.mu,
	}
	return clone
}

// ── Cache key helpers ────────────────────────────────────────────────────────

func chunkCacheKey(kbName, slug, chunkID string) string {
	return fmt.Sprintf("chunk:%s:%s:%s", kbName, slug, chunkID)
}

func metaCacheKey(kbName, slug string) string {
	return fmt.Sprintf("meta:%s:%s", kbName, slug)
}

func indexCacheKey(kbName, slug string) string {
	return fmt.Sprintf("index:%s:%s", kbName, slug)
}

func kbListCacheKey(kbName string) string {
	return fmt.Sprintf("kblist:%s", kbName)
}

// Ensure knowledge.ChunkStore interface is satisfied.
var _ knowledge.ChunkStore = (*Engine)(nil)
