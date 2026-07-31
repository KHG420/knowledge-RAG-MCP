// Package knowledge — tombstone management for soft-delete.
//
// Tombstones implement safe deletion for knowledge base documents:
//
//  1. On delete request: add a Tombstone record → mark index state as
//     "tombstone" so search filters it out → return success to caller.
//     The document appears deleted immediately from the user's perspective.
//  2. Background cleaner: periodically scans for tombstones whose TTL has
//     expired and physically removes the chunk files, index, and metadata.
//  3. Recovery: if a concurrent upload re-creates a document with the same
//     slug before the tombstone TTL expires, the tombstone is removed and
//     the new version takes over.
//
// This prevents:
//   - "Zombie chunks" — a concurrent search seeing chunks from a deleted doc
//   - Lost deletes — a delete message arriving before a previous upload
//     finishes (tombstone acts as a barrier)
package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TombstoneManager handles tombstone records for the knowledge base.
// It is owned by Store and provides the delete-then-clean lifecycle.
type TombstoneManager struct {
	dir     string // directory where TOMBSTONES.json is stored
	mu      sync.RWMutex
	records map[string]*Tombstone // keyed by doc slug
}

// NewTombstoneManager creates a TombstoneManager backed by the given directory.
// The directory is created if it doesn't exist.
func NewTombstoneManager(dir string) *TombstoneManager {
	return &TombstoneManager{
		dir:     dir,
		records: make(map[string]*Tombstone),
	}
}

// Init loads existing tombstones from disk.
func (tm *TombstoneManager) Init() error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if err := os.MkdirAll(tm.dir, 0755); err != nil {
		return fmt.Errorf("tombstone: create dir %q: %w", tm.dir, err)
	}

	data, err := os.ReadFile(tm.tombstonesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no tombstones yet
		}
		return fmt.Errorf("tombstone: read %q: %w", tm.tombstonesPath(), err)
	}

	var records []*Tombstone
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("tombstone: unmarshal %q: %w", tm.tombstonesPath(), err)
	}

	for _, r := range records {
		if r == nil {
			continue // skip null elements from corrupted JSON
		}
		if r.DocSlug == "" {
			continue // skip entries with empty slug
		}
		tm.records[r.DocSlug] = r
	}
	return nil
}

// Save persists tombstone records to disk. Must be called after mutations.
func (tm *TombstoneManager) Save() error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.saveLocked()
}

// Add creates a tombstone for the given document. Returns an error if a
// tombstone already exists for this slug (caller should use Upsert if
// replacing is intended).
func (tm *TombstoneManager) Add(slug string, docVersion, indexVersion int, ttlSeconds int64, reason string) error {
	if slug == "" {
		return fmt.Errorf("tombstone: slug must not be empty")
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	if _, exists := tm.records[slug]; exists {
		return fmt.Errorf("tombstone %q already exists", slug)
	}

	// Build the record in memory first.
	record := &Tombstone{
		DocSlug:      slug,
		DeletedAt:    time.Now().Truncate(time.Second),
		TTLSeconds:   ttlSeconds,
		Reason:       reason,
		DocVersion:   docVersion,
		IndexVersion: indexVersion,
	}

	// Persist to disk BEFORE updating in-memory state.
	// If this fails, in-memory state remains consistent.
	tm.records[slug] = record // temporarily add for save
	if err := tm.saveLocked(); err != nil {
		delete(tm.records, slug) // rollback on failure
		return fmt.Errorf("tombstone: add %q: %w", slug, err)
	}
	return nil
}

// Remove deletes the tombstone for the given slug (e.g. when a new version
// of the document is uploaded before the tombstone TTL expires).
func (tm *TombstoneManager) Remove(slug string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if _, exists := tm.records[slug]; !exists {
		return nil // idempotent
	}
	// Persist removal to disk first, then update memory.
	backup := tm.records[slug]
	delete(tm.records, slug)
	if err := tm.saveLocked(); err != nil {
		tm.records[slug] = backup // rollback
		return fmt.Errorf("tombstone: remove %q: %w", slug, err)
	}
	return nil
}

// Get returns the tombstone for a slug, or nil if none exists.
func (tm *TombstoneManager) Get(slug string) *Tombstone {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.records[slug]
}

// Exists reports whether a tombstone exists for the slug.
func (tm *TombstoneManager) Exists(slug string) bool {
	return tm.Get(slug) != nil
}

// IsTombstoned reports whether a document is tombstoned (and thus hidden from search).
func (tm *TombstoneManager) IsTombstoned(slug string) bool {
	return tm.Exists(slug)
}

// TombstonedSlugs returns all slugs currently under tombstone.
func (tm *TombstoneManager) TombstonedSlugs() []string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	slugs := make([]string, 0, len(tm.records))
	for slug := range tm.records {
		slugs = append(slugs, slug)
	}
	return slugs
}

// ExpiredSlugs returns slugs whose tombstone TTL has elapsed and are ready
// for physical cleanup.
func (tm *TombstoneManager) ExpiredSlugs(now time.Time) []string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	var slugs []string
	for slug, t := range tm.records {
		if t.Expired(now) {
			slugs = append(slugs, slug)
		}
	}
	return slugs
}

// Cleanup removes tombstones for the given slugs (typically after physical
// files have been removed).
func (tm *TombstoneManager) Cleanup(slugs []string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Backup records before deletion for potential rollback.
	backups := make(map[string]*Tombstone, len(slugs))
	for _, slug := range slugs {
		if r, ok := tm.records[slug]; ok {
			backups[slug] = r
		}
	}

	for _, slug := range slugs {
		delete(tm.records, slug)
	}
	if err := tm.saveLocked(); err != nil {
		// Rollback all deletions.
		for slug, r := range backups {
			tm.records[slug] = r
		}
		return fmt.Errorf("tombstone: cleanup: %w", err)
	}
	return nil
}

// All returns all tombstone records.
func (tm *TombstoneManager) All() []*Tombstone {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	records := make([]*Tombstone, 0, len(tm.records))
	for _, r := range tm.records {
		records = append(records, r)
	}
	return records
}

// Count returns the number of active tombstones.
func (tm *TombstoneManager) Count() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.records)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (tm *TombstoneManager) tombstonesPath() string {
	return filepath.Join(tm.dir, "TOMBSTONES.json")
}

// saveLocked persists records without acquiring the lock (caller must hold tm.mu).
func (tm *TombstoneManager) saveLocked() error {
	records := make([]*Tombstone, 0, len(tm.records))
	for _, r := range tm.records {
		records = append(records, r)
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("tombstone: marshal: %w", err)
	}

	if err := os.WriteFile(tm.tombstonesPath(), data, 0644); err != nil {
		return fmt.Errorf("tombstone: write %q: %w", tm.tombstonesPath(), err)
	}
	return nil
}
