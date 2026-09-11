package knowledge

import (
	"context"
	"time"

	"knowledge-mcp/internal/config"
)

// =============================================================================
// Core interfaces for Store decomposition (REFACTOR_PLAN Phase 3).
//
// All interfaces are defined in the parent package so sub-packages can import
// and implement them without creating import cycles. The Store facade holds
// these interfaces instead of concrete sub-package types — wiring happens in
// init.go (external assembly).
// =============================================================================

// ── Search ──────────────────────────────────────────────────────────────────

// Searcher is the unified retrieval interface. It covers BM25, vector, and
// hybrid search with optional coarse-to-fine filtering. The implementation
// lives in internal/knowledge/search/.
type Searcher interface {
	WithKB(kbName string, chunks ChunkStore) Searcher
	Search(ctx context.Context, question string, limit int, filter SearchFilter) ([]SearchHit, error)
	SearchBM25(ctx context.Context, question string, limit int, filter SearchFilter) ([]SearchHit, error)
	HybridSearch(ctx context.Context, question string, limit int, filter SearchFilter) ([]SearchHit, error)
	SearchVector(ctx context.Context, question string, limit int, filter SearchFilter) ([]SearchHit, error)
	SearchDocuments(ctx context.Context, question string, limit int, filter SearchFilter) ([]DocumentHit, error)
	SearchAll(ctx context.Context, question string, limit int, filter SearchFilter) ([]SearchHit, error)

	// Search mode & settings
	GetSearchMode() string
	SetSearchMode(mode string)
	SetEmbedder(emb Embedder)
	SetReranker(rer Reranker)
	SetRerankCandidateLimit(n int)
	SetRerankBatchSize(n int)
	SetSearchLogger(l SearchLogger)
	SetAbstractBoost(b float64)

	// Dictionary injection (used by query expansion)
	SetDictionaryRelatedTerms(terms []string)
	SetSynonymRewriter(rw *SynonymRewriter)
	SetLLMRewriter(rw *LLMQueryRewriter)

	// BM25 / hybrid tunables
	SetRRFK(k float64)
	GetRRFK() float64
	SetBM25K1(k1 float64)
	GetBM25K1() float64
	SetBM25B(b float64)
	GetBM25B() float64

	// GPU scheduler
	SetGPUScheduler(gs *GPUScheduler)

	// Info
	EmbedderInfo() map[string]any
	RerankerInfo() map[string]any
	RerankCandidateLimit() int

	// Inverted index maintenance
	RebuildInvertedIndex() error
	UpdateInvertedIndex(docSlug string) error

	// Cache coordination (cacheClient is shared; TTLs are per-search config)
	SetCacheClient(client CacheClient)
	SetQueryCacheTTL(d time.Duration)
	InvalidateDoc(docSlug string)
}

// ── Chunk I/O ───────────────────────────────────────────────────────────────

// ChunkStore provides read/write access to chunk storage. The implementation
// lives in internal/knowledge/chunkstore/ and wraps a StorageBackend with
// higher-level operations (manifest, tombstone, checksums, versioning).
type ChunkStore interface {
	// Basic CRUD
	ReadChunk(docSlug, chunkID string) (string, error)
	ReadChunkContext(docSlug, chunkID string, ctxCount int) (string, error)
	ReadChunksIndex(docSlug string) (*ChunksIndex, error)
	ReadSectionChunk(docSlug, sectionID string) (string, error)
	ReadRawText(docSlug string) (string, error)
	ListChunks(docSlug string) ([]string, error)
	ListSectionChunks(docSlug string) ([]string, error)

	// Metadata
	ReadMeta(docSlug string) (*DocumentMeta, error)
	WriteMeta(docSlug string, meta *DocumentMeta) error

	// Document list
	ListDocuments() ([]DocumentMeta, error)
	ListDocumentsAll() ([]DocumentMeta, error)
	ListWithLimit(n int) ([]DocumentMeta, error)
	Exists(docSlug string) (bool, error)

	// INDEX.md
	ReadIndex() (string, error)
	WriteIndex(content string) error

	// Manifest & versioning
	ReadManifest(docSlug string) (*ChunkManifest, error)
	WriteManifest(docSlug string, manifest *ChunkManifest) error
	ActiveVersion(docSlug string) (int, error)

	// Tombstone
	IsTombstoned(docSlug string) bool
	GetTombstone(docSlug string) (*Tombstone, error)
	TombstoneCount() int
	CleanExpiredTombstones() int

	// Checksums
	ComputeChunksChecksum(docSlug string) (string, error)

	// Atomic staging
	PrepareStaging(docSlug string) error
	PromoteStaging(docSlug string, newVersion int) error
	CleanStaging(docSlug string) error

	// KB identity
	KBName() string
	DataDir() string
	WithKB(kbName string) ChunkStore

	// Backend access (for operations that need raw backend calls)
	Backend() StorageBackend
}

// ── Ingest ──────────────────────────────────────────────────────────────────

// Ingester handles document upload: parse → chunk → embed → persist.
// The implementation lives in internal/knowledge/ingest/.
type Ingester interface {
	WithKB(kbName string, chunks ChunkStore, buildChunksIndex func(string, []ChunkWithMeta, []ChunkWithMeta) error) Ingester
	UploadDocument(filePath string, tags ...string) (*DocumentMeta, error)
	UploadDocumentWithProgress(filePath string, tags ...string) (*DocumentMeta, error)
	UploadDirectory(dirPath string, recursive bool) (string, error)

	// Source file management
	CopySource(srcPath, docSlug string) error

	// Task manager access
	TaskManager() *UploadTaskManager
}

// ── KB Administration ───────────────────────────────────────────────────────

// KBAdmin manages knowledge-base lifecycle and routing. The implementation
// lives in internal/knowledge/kb/.
type KBAdmin interface {
	ListKBs() ([]string, error)
	ListKBsInfo() ([]KBInfo, error)
	CreateKB(name, description string) error
	DeleteKB(name string) error

	// Routing
	RouteKBs(ctx context.Context, query string) []string
	SetKBRouter(router *KBRouter)
	SyncKBRouterDescs()
}

// ── Dict ────────────────────────────────────────────────────────────────────

// DictService manages synonym dictionaries, query expansion terms, and
// query rewriting. The implementation lives in internal/knowledge/dict/.
type DictService interface {
	LoadDictionaries(dir string) error
	GenerateDictionary(dir string) error
	RunDictMine(configPath string) error
	RunDictGen(configPath string) error
	GetSynonymRewriter() *SynonymRewriter
	GetLLMRewriter() *LLMQueryRewriter
	GetRelatedTerms() []string
}

// ── Manage ──────────────────────────────────────────────────────────────────

// ManageService is the facade that the web management layer (internal/knowledge/manage/)
// uses to interact with the Store. Defining this interface in the parent package
// avoids a circular dependency (manage/ → knowledge and knowledge → manage/).
//
// Phase 3.4 is complete: 38 HTTP handlers have been migrated to manage/ (6 files)
// and use this interface via manage.Server.
type ManageService interface {
	// ── Document management ──
	ListDocuments() ([]DocumentMeta, error)
	ListDocumentsAll() ([]DocumentMeta, error)
	WithKB(kbName string) *Store
	WithKBService(kbName string) ManageService
	ReadMeta(docSlug string) (DocumentMeta, error)
	WriteMeta(docSlug string, meta DocumentMeta) error
	ReadChunksIndex(docSlug string) (*ChunksIndex, error)
	ReadRawText(docSlug string) (string, error)
	ReadChunk(docSlug, chunkID string) (string, error)
	RemoveDocument(docSlug string) error
	IsTombstoned(docSlug string) bool
	RemoveDocumentTombstone(slug string, ttlSeconds int64, reason string) error
	ListChunkIDs(docSlug string) ([]string, error)

	// ── Search ──
	Search(question string, limit int, filter ...SearchFilter) ([]SearchHit, error)
	SearchBM25(query string, limit int) ([]SearchHit, error)
	SearchVector(query string, limit int) ([]SearchHit, error)
	HybridSearch(question string, limit int, filter ...SearchFilter) ([]SearchHit, error)
	SearchAll(question string, limit int, filter ...SearchFilter) ([]SearchHit, error)

	// ── Upload ──
	UploadDocument(filePath string, tags ...string) (DocumentMeta, error)
	UploadDocumentWithProgress(filePath string, progress ProgressFunc, tags ...string) (DocumentMeta, error)
	TaskManager() *UploadTaskManager

	// ── Tombstone ──
	ListTombstones() ([]Tombstone, error)
	RestoreTombstone(docSlug string) error
	CleanExpiredTombstones() (int, error)

	// ── Reconciliation ──
	Reconcile() (*ReconcileReport, error)

	// ── Manifest ──
	ReadManifest(docSlug string) (*ChunkManifest, error)

	// ── Vector ──
	GetVectorStats() (*VectorStats, error)
	GetVectorIndexInfo() (map[string]any, error)
	RebuildVectors() (RebuildResult, error)
	ReEmbedMissingVectors(ctx context.Context, slug string, progress func(int, int, string)) (*RebuildResult, error)

	// ── Model info ──
	EmbedderInfo() map[string]any
	RerankerInfo() map[string]any
	RerankCandidateLimit() int

	// ── Infrastructure ──
	GPUScheduler() *GPUScheduler
	DataDir() string
	KBName() string
	Backend() StorageBackend
	Config() *config.Config
	ConfigPath() string

	// ── KB Admin ──
	ListKBsInfo() ([]KBInfo, error)
	CreateKB(name, description string) error
	DeleteKB(name string) error

	// ── Settings (search) ──
	GetSearchMode() SearchMode
	SetSearchMode(mode SearchMode)
	GetRerankEnabled() bool
	SetRerankEnabled(v bool)
	GetRRFK() int
	SetRRFK(v int)
	SetAbstractBoost(v float64)
	GetBM25K1() float64
	SetBM25K1(v float64)
	GetBM25B() float64
	SetBM25B(v float64)
	SetRerankCandidateLimit(n int)

	// ── Settings (chunking) ──
	GetChunkMinChars() int
	SetChunkMinChars(v int)
	GetChunkMaxChars() int
	SetChunkMaxChars(v int)
	GetChunkOverlapChars() int
	SetChunkOverlapChars(v int)
	GetChunkSemanticThreshold() float64
	SetChunkSemanticThreshold(v float64)

	// ── Settings (upload) ──
	GetUploadMaxSizeMB() int
	SetUploadMaxSizeMB(v int)

	// ── Settings (cache TTLs) ──
	SetCacheQueryTTL(d time.Duration)
	SetCacheChunkTTL(d time.Duration)
	SetCacheMetaTTL(d time.Duration)
	SetCacheIndexTTL(d time.Duration)
	SetCacheKBListTTL(d time.Duration)

	// ── Tool descriptions ──
	GetToolDescriptions() ToolDescriptions
	SetToolDescriptions(td ToolDescriptions)

	// ── Hot-reload services ──
	SetEmbedder(e Embedder)
	SetReranker(r Reranker)
	SetGPUScheduler(g *GPUScheduler)
	SetLLMRewriter(r *LLMQueryRewriter)
	SetDocParser(p DocParser)

	// ── KB validate ──
	ValidateComponent(name string) error
}

// ── Lightweight aliases to avoid import cycles ──────────────────────────────

// CacheClient mirrors the cache.Cache interface so the knowledge package
// does not need to import internal/cache from sub-packages.
type CacheClient interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
	DeletePattern(ctx context.Context, pattern string) (int64, error)
	Ping(ctx context.Context) error
	Close() error
}
