// Package knowledge implements a local knowledge base with pluggable storage backends.
//
// StorageBackend abstracts all persistent storage operations so the knowledge base
// is backed by a MySQL-compatible database (MySQLBackend). The Store struct
// delegates all I/O to its configured backend.
package knowledge

// StorageBackend defines the interface for all persistent storage operations.
// Implementations must be safe for concurrent use (the Store holds a single backend
// shared across all KB views).
type StorageBackend interface {
	// ── Lifecycle ────────────────────────────────────────────────────────────
	// Init prepares the backend for use (creates directories, tables, etc.).
	// It is called once at startup.
	Init() error

	// Close releases any resources held by the backend (database connections, etc.).
	Close() error

	// ── Knowledge Base operations ────────────────────────────────────────────
	ListKBs() ([]KBInfo, error)
	CreateKB(name, description string) error
	DeleteKB(name string) error

	// ── Document metadata ────────────────────────────────────────────────────
	ReadMeta(kbName, slug string) (*DocumentMeta, error)
	WriteMeta(kbName, slug string, meta *DocumentMeta) error
	Exists(kbName, slug string) (bool, error)
	ListDocSlugs(kbName string) ([]string, error)
	RemoveDocument(kbName, slug string) error

	// ── Fine-grained chunks ──────────────────────────────────────────────────
	ReadChunk(kbName, slug, chunkID string) (string, error)
	WriteChunk(kbName, slug, chunkID, content string) error
	ListChunkIDs(kbName, slug string) ([]string, error)
	DeleteChunks(kbName, slug string) error

	// ── Section-level chunks (coarse) ────────────────────────────────────────
	ReadSectionChunk(kbName, slug, sectionID string) (string, error)
	WriteSectionChunk(kbName, slug, sectionID, content string) error
	ListSectionChunkIDs(kbName, slug string) ([]string, error)
	DeleteSectionChunks(kbName, slug string) error

	// ── Search index (per-document CHUNKS.toml equivalent) ──────────────────
	ReadChunksIndex(kbName, slug string) (*ChunksIndex, error)
	WriteChunksIndex(kbName, slug string, index *ChunksIndex) error

	// ── Global inverted index ────────────────────────────────────────────────
	ReadInvertedIndex(kbName string) (*InvertedIndex, error)
	WriteInvertedIndex(kbName string, idx *InvertedIndex) error
	// DeleteInvertedDocEntries removes all inverted index entries for a document.
	// Used for incremental updates: delete old entries, then upsert new ones.
	DeleteInvertedDocEntries(kbName, docSlug string) error
	// UpsertInvertedEntries inserts or updates a batch of inverted index entries.
	// Used for incremental updates after per-document CHUNKS.toml writes.
	UpsertInvertedEntries(kbName string, entries []InvertedEntry) error

	// ── Full raw text & source file ──────────────────────────────────────────
	WriteRawText(kbName, slug, text string) error
	ReadRawText(kbName, slug string) (string, error)
	WriteSource(kbName, slug string, data []byte, ext string) error

	// ── INDEX.md (KB-level document index) ───────────────────────────────────
	ReadIndex(kbName string) (string, error)
	WriteIndex(kbName, content string) error

	// ── List snapshot cache ──────────────────────────────────────────────────
	ReadSnapshot(kbName string) (checksum string, docs []DocumentMeta, err error)
	WriteSnapshot(kbName string, docs []DocumentMeta) error

	// ── Chunk checksum verification ──────────────────────────────────────────
	ComputeChunksChecksum(kbName, slug string) (string, error)

	// ── Chunk manifest (versioned chunk list, MANIFEST.json) ──────────────────
	ReadManifest(kbName, slug string) (*ChunkManifest, error)
	WriteManifest(kbName, slug string, manifest *ChunkManifest) error

	// ── Task state persistence (TASK.json for state machine) ──────────────────
	ReadTaskRecord(kbName, slug string) (*TaskRecord, error)
	WriteTaskRecord(kbName, slug string, task *TaskRecord) error
	DeleteTaskRecord(kbName, slug string) error

	// ── Atomic index staging support ──────────────────────────────────────────
	// PrepareStaging prepares the staging area for a new index version build.
	PrepareStaging(kbName, slug string) error
	// PromoteStaging atomically promotes the staging index to active.
	PromoteStaging(kbName, slug string, newVersion int) error
	// CleanStaging removes leftover staging data after a failed build.
	CleanStaging(kbName, slug string) error
	// ActiveVersion returns the current active index version for a document.
	ActiveVersion(kbName, slug string) (int, error)
}
