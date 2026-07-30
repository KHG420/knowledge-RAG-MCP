// Package knowledge — chunk manifest: a durable, versioned record of every
// chunk that belongs to a document at a given point in time.
//
// The manifest is the source of truth for what the index *should* contain.
// It enables:
//
//  1. Incremental diff — comparing an old manifest to a new one reveals
//     which chunks were added, removed, or stayed the same.
//  2. Idempotent rebuild — re-processing a document with the same source
//     hash and chunking strategy produces the same manifest; unchanged
//     chunks can skip re-embedding.
//  3. Reconciliation — the reconciler walks all manifests and verifies
//     that every listed chunk file / index entry actually exists.
//
// Storage: MANIFEST.json in the document directory, next to meta.json.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ChunkManifest is the authoritative, versioned list of chunks for a document.
// It is persisted as MANIFEST.json alongside meta.json.
type ChunkManifest struct {
	DocSlug     string `json:"doc_slug"`     // document slug (unique identifier)
	Version     int    `json:"version"`       // monotonically increasing manifest version
	SourceHash  string `json:"source_hash"`   // SHA256 of the original source file
	TextHash    string `json:"text_hash"`     // SHA256 of the parsed plain text
	Strategy    string `json:"strategy"`      // ChunkingStrategyVersion() that produced this manifest
	ChunkCount  int    `json:"chunk_count"`   // total number of fine-grained chunks
	SectionCount int   `json:"section_count"` // total number of section-level chunks
	CreatedAt   time.Time `json:"created_at"` // when this manifest version was created
	Chunks      []ChunkManifestEntry `json:"chunks"`       // ordered fine-grained chunk entries
	Sections    []ChunkManifestEntry `json:"sections"`     // ordered section-level chunk entries
}

// ChunkManifestEntry describes one chunk in the manifest.
type ChunkManifestEntry struct {
	ID          string `json:"id"`            // content-based chunk ID (e.g. "C3f2a8b1c0d1")
	LegacyID    string `json:"legacy_id,omitempty"` // legacy sequential ID for backward compat
	Section     string `json:"section,omitempty"`   // nearest markdown heading
	Offset      int    `json:"offset"`              // character offset in original text
	SectionID   string `json:"section_id,omitempty"` // parent section chunk ID
	SectionRole string `json:"section_role,omitempty"` // classified role
	CharCount   int    `json:"char_count"`          // character count (for filtering)
}

// NewChunkManifest creates a new manifest for the given document and chunk
// metadata. The version starts at 1.
func NewChunkManifest(docSlug, sourceHash, textHash string, fineChunks, coarseChunks []ChunkWithMeta) *ChunkManifest {
	m := &ChunkManifest{
		DocSlug:    docSlug,
		Version:    1,
		SourceHash: sourceHash,
		TextHash:   textHash,
		Strategy:   ChunkingStrategyVersion(),
		CreatedAt:  time.Now().Truncate(time.Second),
		ChunkCount: len(fineChunks),
		SectionCount: len(coarseChunks),
	}

	m.Chunks = make([]ChunkManifestEntry, len(fineChunks))
	for i, c := range fineChunks {
		m.Chunks[i] = ChunkManifestEntry{
			ID:          ComputeChunkID(c.Content),
			LegacyID:    fmt.Sprintf("%03d", i),
			Section:     c.Section,
			Offset:      c.Offset,
			SectionID:   c.SectionID,
			SectionRole: c.SectionRole,
			CharCount:   len([]rune(c.Content)),
		}
	}

	m.Sections = make([]ChunkManifestEntry, len(coarseChunks))
	for i, c := range coarseChunks {
		m.Sections[i] = ChunkManifestEntry{
			ID:          ComputeChunkID(c.Content),
			LegacyID:    fmt.Sprintf("S%02d", i),
			Section:     c.Section,
			Offset:      c.Offset,
			SectionID:   c.SectionID,
			SectionRole: c.SectionRole,
			CharCount:   len([]rune(c.Content)),
		}
	}
	return m
}

// NextVersion returns a copy of the manifest with Version incremented and
// CreatedAt updated. Use when rebuilding an existing document.
func (m *ChunkManifest) NextVersion(sourceHash, textHash string, fineChunks, coarseChunks []ChunkWithMeta) *ChunkManifest {
	next := NewChunkManifest(m.DocSlug, sourceHash, textHash, fineChunks, coarseChunks)
	next.Version = m.Version + 1
	return next
}

// IDs returns the ordered list of chunk IDs from this manifest.
// Returns nil when m is nil.
func (m *ChunkManifest) IDs() []string {
	if m == nil {
		return nil
	}
	ids := make([]string, len(m.Chunks))
	for i, c := range m.Chunks {
		ids[i] = c.ID
	}
	return ids
}

// SectionIDs returns the ordered list of section chunk IDs from this manifest.
func (m *ChunkManifest) SectionIDs() []string {
	ids := make([]string, len(m.Sections))
	for i, s := range m.Sections {
		ids[i] = s.ID
	}
	return ids
}

// IDSet returns the set of chunk IDs as a map for O(1) membership checks.
// Returns nil when m is nil.
func (m *ChunkManifest) IDSet() map[string]bool {
	if m == nil {
		return nil
	}
	set := make(map[string]bool, len(m.Chunks))
	for _, c := range m.Chunks {
		set[c.ID] = true
	}
	return set
}

// Diff computes the difference between two manifests (old → new). Returns
// chunks that were added, removed, and unchanged. Unchanged chunks can skip
// re-embedding and re-indexing.
type ManifestDiff struct {
	Added     []ChunkManifestEntry // chunks present in new but not in old
	Removed   []ChunkManifestEntry // chunks present in old but not in new
	Unchanged []ChunkManifestEntry // chunks with the same ID in both
}

// DiffManifests compares old and new manifests. When old is nil, all new chunks
// are reported as Added. When new is nil, all old chunks are reported as Removed.
func DiffManifests(old, new *ChunkManifest) ManifestDiff {
	var d ManifestDiff

	if old == nil && new == nil {
		return d
	}
	if old == nil {
		d.Added = append([]ChunkManifestEntry{}, new.Chunks...)
		return d
	}
	if new == nil {
		d.Removed = append([]ChunkManifestEntry{}, old.Chunks...)
		return d
	}

	oldSet := old.IDSet()
	newSet := new.IDSet()

	for _, c := range new.Chunks {
		if oldSet[c.ID] {
			d.Unchanged = append(d.Unchanged, c)
		} else {
			d.Added = append(d.Added, c)
		}
	}
	for _, c := range old.Chunks {
		if !newSet[c.ID] {
			d.Removed = append(d.Removed, c)
		}
	}
	return d
}

// Changed reports whether any chunk content changed between the two manifests.
func (d ManifestDiff) Changed() bool {
	return len(d.Added) > 0 || len(d.Removed) > 0
}

// AddedCount returns the number of added chunks.
func (d ManifestDiff) AddedCount() int { return len(d.Added) }

// RemovedCount returns the number of removed chunks.
func (d ManifestDiff) RemovedCount() int { return len(d.Removed) }

// UnchangedCount returns the number of unchanged chunks.
func (d ManifestDiff) UnchangedCount() int { return len(d.Unchanged) }

// MarshalJSON serializes the manifest to indented JSON bytes.
func (m *ChunkManifest) MarshalJSON() ([]byte, error) {
	type Alias ChunkManifest
	return json.MarshalIndent((*Alias)(m), "", "  ")
}

// UnmarshalChunkManifest deserializes a ChunkManifest from JSON bytes.
func UnmarshalChunkManifest(data []byte) (*ChunkManifest, error) {
	var m ChunkManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// VerifyManifestIntegrity checks that the manifest's ID list is self-consistent:
// - ChunkCount matches len(Chunks)
// - all IDs in Chunks and Sections are unique and non-empty
func (m *ChunkManifest) VerifyManifestIntegrity() error {
	if m == nil {
		return fmt.Errorf("manifest is nil")
	}
	if m.ChunkCount != len(m.Chunks) {
		return fmt.Errorf("manifest chunk_count=%d but len(chunks)=%d", m.ChunkCount, len(m.Chunks))
	}
	if m.SectionCount != len(m.Sections) {
		return fmt.Errorf("manifest section_count=%d but len(sections)=%d", m.SectionCount, len(m.Sections))
	}

	seen := make(map[string]bool, len(m.Chunks)+len(m.Sections))
	for _, c := range m.Chunks {
		if c.ID == "" {
			return fmt.Errorf("manifest contains empty chunk ID")
		}
		if seen[c.ID] {
			return fmt.Errorf("manifest contains duplicate chunk ID %q", c.ID)
		}
		seen[c.ID] = true
	}
	for _, s := range m.Sections {
		if s.ID == "" {
			return fmt.Errorf("manifest contains empty section ID")
		}
		if seen[s.ID] {
			return fmt.Errorf("manifest contains duplicate section ID %q", s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}

// ManifestFilename is the conventional name for the manifest file.
const ManifestFilename = "MANIFEST.json"

// sortedManifestEntrySlice attaches the methods of sort.Interface to []ChunkManifestEntry.
type sortedManifestEntrySlice []ChunkManifestEntry

func (s sortedManifestEntrySlice) Len() int           { return len(s) }
func (s sortedManifestEntrySlice) Less(i, j int) bool  { return s[i].ID < s[j].ID }
func (s sortedManifestEntrySlice) Swap(i, j int)       { s[i], s[j] = s[j], s[i] }

// SortManifestEntries returns a sorted copy of the entries (by ID).
func SortManifestEntries(entries []ChunkManifestEntry) []ChunkManifestEntry {
	sorted := make([]ChunkManifestEntry, len(entries))
	copy(sorted, entries)
	sort.Sort(sortedManifestEntrySlice(sorted))
	return sorted
}

// TextHash computes the SHA256 hex digest of parsed text.
func TextHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}
