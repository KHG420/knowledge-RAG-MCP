// Package knowledge — version management for document indexing.
//
// This file extends the data model with versioning fields that enable:
//   - Detecting whether a document has changed since last index
//   - Tracking which chunking strategy was used
//   - Managing index versions for atomic switch-over
//   - Tombstone-based soft-delete with TTL
package knowledge

import (
	"fmt"
	"time"
)

// ── Extended DocumentMeta fields ──────────────────────────────────────────────

// DocumentVersionInfo holds versioning fields optionally embedded in
// DocumentMeta. When zero-valued, the document was ingested before versioning
// was introduced (legacy mode).
//
// These fields are serialized into meta.json alongside existing fields.
type DocumentVersionInfo struct {
	// DocVersion is a monotonically increasing counter for the document.
	// Starts at 1. Incremented on every re-index.
	DocVersion int `json:"doc_version,omitempty"`

	// SourceHash is the SHA256 hex digest of the original source file bytes.
	// Used to detect whether re-upload of the same path is a no-op.
	SourceHash string `json:"source_hash,omitempty"`

	// TextHash is the SHA256 hex digest of the parsed plain text.
	// Changes when the source file changes OR the parser output changes.
	TextHash string `json:"text_hash,omitempty"`

	// ChunkStrategy is the ChunkingStrategyVersion() that was used to produce
	// the current chunks. When this changes across restarts, a full re-index
	// with a new index version is required.
	ChunkStrategy string `json:"chunk_strategy,omitempty"`

	// IndexVersion is the current active index version for this document.
	// 0 means legacy (no versioning). The search layer reads the version
	// that is marked "active".
	IndexVersion int `json:"index_version,omitempty"`
}

// ── Index version management ──────────────────────────────────────────────────

// IndexVersionState tracks the state of a single index version for a document.
// Multiple versions can exist simultaneously during a rolling upgrade:
//
//	active    — the version that search queries read from
//	building  — a new version being built in the background (not yet active)
//	retired   — a previous version kept for rollback (can be deleted after grace period)
//	tombstone — the document has been soft-deleted; search filters it out
type IndexVersionState string

const (
	IndexStateActive    IndexVersionState = "active"
	IndexStateBuilding  IndexVersionState = "building"
	IndexStateRetired   IndexVersionState = "retired"
	IndexStateTombstone IndexVersionState = "tombstone"
)

// IndexVersionMeta is persisted alongside each version's chunks and index.
// It is stored as INDEX_VERSION.json in the version subdirectory.
type IndexVersionMeta struct {
	DocSlug     string            `json:"doc_slug"`
	Version     int               `json:"version"`     // index version number (distinct from doc version)
	State       IndexVersionState `json:"state"`       // current state of this version
	DocVersion  int               `json:"doc_version"` // DocumentMeta.DocVersion this was built from
	SourceHash  string            `json:"source_hash"`
	TextHash    string            `json:"text_hash"`
	Strategy    string            `json:"strategy"` // ChunkingStrategyVersion()
	ChunkCount  int               `json:"chunk_count"`
	BuiltAt     time.Time         `json:"built_at"`
	ActivatedAt *time.Time        `json:"activated_at,omitempty"` // when this version became active
	RetiredAt   *time.Time        `json:"retired_at,omitempty"`   // when this version was retired
	Checksum    string            `json:"checksum,omitempty"`     // SHA256 of the CHUNKS.toml file
}

// Validate returns an error if required fields are missing or inconsistent.
func (m *IndexVersionMeta) Validate() error {
	if m.DocSlug == "" {
		return fmt.Errorf("index version meta: doc_slug is required")
	}
	if m.Version <= 0 {
		return fmt.Errorf("index version meta: version must be positive")
	}
	if m.State == "" {
		return fmt.Errorf("index version meta: state is required")
	}
	return nil
}

// IsSearchable reports whether this version should be included in search results.
func (m *IndexVersionMeta) IsSearchable() bool {
	return m.State == IndexStateActive
}

// ── Tombstone ─────────────────────────────────────────────────────────────────

// Tombstone records a soft-deleted document. The document's chunks and index
// remain on disk for a configurable TTL, but search queries filter out
// tombstoned documents. After the TTL expires, a background cleaner removes
// the physical files.
type Tombstone struct {
	DocSlug      string    `json:"doc_slug"`
	DeletedAt    time.Time `json:"deleted_at"`
	TTLSeconds   int64     `json:"ttl_seconds"`      // 0 = never auto-clean (manual only)
	Reason       string    `json:"reason,omitempty"` // optional reason for deletion
	DocVersion   int       `json:"doc_version"`      // version at time of deletion
	IndexVersion int       `json:"index_version"`    // index version at time of deletion
}

// Expired reports whether the tombstone's TTL has elapsed and the document
// is ready for physical cleanup.
func (t *Tombstone) Expired(now time.Time) bool {
	if t.TTLSeconds <= 0 {
		return false // never expires
	}
	return now.After(t.DeletedAt.Add(time.Duration(t.TTLSeconds) * time.Second))
}

// DefaultTombstoneTTL is the default TTL for tombstoned documents (7 days).
const DefaultTombstoneTTL = 7 * 24 * 3600
