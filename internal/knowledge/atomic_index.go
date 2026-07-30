// Package knowledge — atomic index switching for zero-downtime index updates.
//
// When a document is re-indexed (due to content change or chunking strategy
// change), the new index is built in a staging location. Once all checks pass,
// the staging index is atomically promoted to "active" and the previous index
// is retired (kept for rollback).
//
// This ensures:
//  1. No partial index is ever visible to search queries.
//  2. A failed build leaves the existing active index intact.
//  3. Rollback is a single state transition (retired → active).
//
// Layout (per document):
//
//	<doc-dir>/
//	  chunks/           ← active chunks (legacy, index version 0)
//	  CHUNKS.toml       ← active index
//	  MANIFEST.json      ← active manifest
//	  .staging/          ← staging area for next version
//	    chunks/
//	    CHUNKS.toml
//	    MANIFEST.json
//	  versions/          ← retired versions kept for rollback
//	    v1/
//	      chunks/
//	      CHUNKS.toml
//	      MANIFEST.json
//	      INDEX_VERSION.json
package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
)

// ── Atomic index operations — filesystem backend ──────────────────────────────

// AtomicIndexBuilder manages building a new index version without disrupting
// the active index. After all chunks and the index are written to staging,
// PromoteAtomically swaps staging into the active position.
type AtomicIndexBuilder struct {
	docDir      string // e.g. ~/knowledge_base/<kb>/<slug>/
	stagingDir  string // e.g. <docDir>/.staging/
	versionsDir string // e.g. <docDir>/versions/
}

// NewAtomicIndexBuilder creates a builder for the given document directory.
func NewAtomicIndexBuilder(docDir string) *AtomicIndexBuilder {
	return &AtomicIndexBuilder{
		docDir:      docDir,
		stagingDir:  filepath.Join(docDir, ".staging"),
		versionsDir: filepath.Join(docDir, "versions"),
	}
}

// PrepareStaging cleans and prepares the staging directory for a new build.
// It removes any leftover staging data from a previous failed build.
func (b *AtomicIndexBuilder) PrepareStaging() error {
	// Remove any leftover staging.
	if err := os.RemoveAll(b.stagingDir); err != nil {
		return fmt.Errorf("atomic: clean staging: %w", err)
	}
	// Create fresh staging directories.
	if err := os.MkdirAll(filepath.Join(b.stagingDir, "chunks"), 0755); err != nil {
		return fmt.Errorf("atomic: create staging chunks dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(b.stagingDir, "chunks", "sections"), 0755); err != nil {
		return fmt.Errorf("atomic: create staging sections dir: %w", err)
	}
	return nil
}

// StagingChunkPath returns the path for a chunk file in the staging area.
func (b *AtomicIndexBuilder) StagingChunkPath(chunkID string) string {
	return filepath.Join(b.stagingDir, "chunks", chunkID+".md")
}

// StagingSectionPath returns the path for a section chunk file in the staging area.
func (b *AtomicIndexBuilder) StagingSectionPath(sectionID string) string {
	return filepath.Join(b.stagingDir, "chunks", "sections", sectionID+".md")
}

// StagingChunksIndexPath returns the path for CHUNKS.toml in the staging area.
func (b *AtomicIndexBuilder) StagingChunksIndexPath() string {
	return filepath.Join(b.stagingDir, "CHUNKS.toml")
}

// StagingManifestPath returns the path for MANIFEST.json in the staging area.
func (b *AtomicIndexBuilder) StagingManifestPath() string {
	return filepath.Join(b.stagingDir, "MANIFEST.json")
}

// StagingDir returns the staging directory path.
func (b *AtomicIndexBuilder) StagingDir() string {
	return b.stagingDir
}

// PromoteAtomically swaps the staging index into the active position.
//
// Steps:
//  1. If an active index exists, move it to versions/v<N>/ (retire).
//  2. Move staging → active position (rename is atomic on most filesystems).
//  3. Clean up empty staging directory.
//
// On failure, the active index is left intact.
func (b *AtomicIndexBuilder) PromoteAtomically(newVersion int) error {
	// Step 1: retire current active version (if any).
	activeChunksDir := filepath.Join(b.docDir, "chunks")
	activeIndexPath := filepath.Join(b.docDir, "CHUNKS.toml")
	activeManifestPath := filepath.Join(b.docDir, "MANIFEST.json")

	retireDir := filepath.Join(b.versionsDir, fmt.Sprintf("v%d", newVersion-1))

	if _, err := os.Stat(activeChunksDir); err == nil {
		// Active version exists — retire it.
		if err := os.MkdirAll(retireDir, 0755); err != nil {
			return fmt.Errorf("atomic: create retire dir: %w", err)
		}
		// Move chunks directory.
		if err := os.Rename(activeChunksDir, filepath.Join(retireDir, "chunks")); err != nil {
			return fmt.Errorf("atomic: retire chunks: %w", err)
		}
		// Move CHUNKS.toml.
		if _, err := os.Stat(activeIndexPath); err == nil {
			if err := os.Rename(activeIndexPath, filepath.Join(retireDir, "CHUNKS.toml")); err != nil {
				return fmt.Errorf("atomic: retire index: %w", err)
			}
		}
		// Move MANIFEST.json.
		if _, err := os.Stat(activeManifestPath); err == nil {
			if err := os.Rename(activeManifestPath, filepath.Join(retireDir, "MANIFEST.json")); err != nil {
				return fmt.Errorf("atomic: retire manifest: %w", err)
			}
		}
	}

	// Step 2: promote staging → active.
	stagingChunksDir := filepath.Join(b.stagingDir, "chunks")
	stagingIndexPath := filepath.Join(b.stagingDir, "CHUNKS.toml")
	stagingManifestPath := filepath.Join(b.stagingDir, "MANIFEST.json")

	if err := os.Rename(stagingChunksDir, activeChunksDir); err != nil {
		return fmt.Errorf("atomic: promote chunks: %w", err)
	}
	if _, err := os.Stat(stagingIndexPath); err == nil {
		if err := os.Rename(stagingIndexPath, activeIndexPath); err != nil {
			return fmt.Errorf("atomic: promote index: %w", err)
		}
	}
	if _, err := os.Stat(stagingManifestPath); err == nil {
		if err := os.Rename(stagingManifestPath, activeManifestPath); err != nil {
			return fmt.Errorf("atomic: promote manifest: %w", err)
		}
	}

	// Step 3: clean up staging directory.
	_ = os.RemoveAll(b.stagingDir)

	return nil
}

// Rollback reverts a promote by restoring the previous version from retire dir.
func (b *AtomicIndexBuilder) Rollback(currentVersion int) error {
	if currentVersion <= 1 {
		return fmt.Errorf("atomic: no previous version to rollback to")
	}

	retireDir := filepath.Join(b.versionsDir, fmt.Sprintf("v%d", currentVersion-1))
	if _, err := os.Stat(retireDir); os.IsNotExist(err) {
		return fmt.Errorf("atomic: retired version v%d not found", currentVersion-1)
	}

	// Verify retire directory has the required content before deleting active.
	retireChunks := filepath.Join(retireDir, "chunks")
	if _, err := os.Stat(retireChunks); os.IsNotExist(err) {
		return fmt.Errorf("atomic: retired version v%d has no chunks directory — rollback unsafe", currentVersion-1)
	}

	// Move current active to a temporary backup first.
	activeChunksDir := filepath.Join(b.docDir, "chunks")
	activeIndexPath := filepath.Join(b.docDir, "CHUNKS.toml")
	activeManifestPath := filepath.Join(b.docDir, "MANIFEST.json")
	backupDir := filepath.Join(b.docDir, ".rollback-backup")

	// Clean any leftover backup.
	_ = os.RemoveAll(backupDir)

	var movedActive bool
	if _, err := os.Stat(activeChunksDir); err == nil {
		if err := os.MkdirAll(backupDir, 0755); err != nil {
			return fmt.Errorf("atomic: create backup dir: %w", err)
		}
		if err := os.Rename(activeChunksDir, filepath.Join(backupDir, "chunks")); err != nil {
			return fmt.Errorf("atomic: backup active chunks: %w", err)
		}
		movedActive = true
	}
	if _, err := os.Stat(activeIndexPath); err == nil {
		_ = os.Rename(activeIndexPath, filepath.Join(backupDir, "CHUNKS.toml"))
	}
	if _, err := os.Stat(activeManifestPath); err == nil {
		_ = os.Rename(activeManifestPath, filepath.Join(backupDir, "MANIFEST.json"))
	}

	// Restore from retire dir.
	if err := os.Rename(retireChunks, activeChunksDir); err != nil {
		// Restore backup.
		if movedActive {
			_ = os.Rename(filepath.Join(backupDir, "chunks"), activeChunksDir)
		}
		return fmt.Errorf("atomic: rollback chunks: %w", err)
	}
	if _, err := os.Stat(filepath.Join(retireDir, "CHUNKS.toml")); err == nil {
		if err := os.Rename(filepath.Join(retireDir, "CHUNKS.toml"), activeIndexPath); err != nil {
			_ = os.Rename(filepath.Join(backupDir, "CHUNKS.toml"), activeIndexPath)
			return fmt.Errorf("atomic: rollback index: %w", err)
		}
	}
	if _, err := os.Stat(filepath.Join(retireDir, "MANIFEST.json")); err == nil {
		if err := os.Rename(filepath.Join(retireDir, "MANIFEST.json"), activeManifestPath); err != nil {
			_ = os.Rename(filepath.Join(backupDir, "MANIFEST.json"), activeManifestPath)
			return fmt.Errorf("atomic: rollback manifest: %w", err)
		}
	}

	// Clean up retire dir and backup.
	_ = os.RemoveAll(retireDir)
	_ = os.RemoveAll(backupDir)
	return nil
}

// CleanRetired removes retired versions older than keepVersions.
func (b *AtomicIndexBuilder) CleanRetired(keepVersions int) error {
	if keepVersions <= 0 {
		return nil
	}

	entries, err := os.ReadDir(b.versionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("atomic: read versions dir: %w", err)
	}

	// Collect version numbers from directory names like "v1", "v2", etc.
	var versions []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(e.Name(), "v%d", &v); err == nil && v > 0 {
			versions = append(versions, v)
		}
	}

	// Keep only the highest keepVersions versions.
	if len(versions) <= keepVersions {
		return nil
	}

	// Sort descending (simple bubble — versions list is small).
	for i := 0; i < len(versions); i++ {
		for j := i + 1; j < len(versions); j++ {
			if versions[i] < versions[j] {
				versions[i], versions[j] = versions[j], versions[i]
			}
		}
	}

	// Remove oldest versions.
	for _, v := range versions[keepVersions:] {
		dir := filepath.Join(b.versionsDir, fmt.Sprintf("v%d", v))
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("atomic: clean retired v%d: %w", v, err)
		}
	}
	return nil
}

// ActiveVersion returns the current active index version based on directory state.
// Returns 0 if no versioning is in use (legacy layout).
func (b *AtomicIndexBuilder) ActiveVersion() int {
	// Check if versions dir exists and count entries.
	entries, err := os.ReadDir(b.versionsDir)
	if err != nil {
		return 0
	}
	maxV := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(e.Name(), "v%d", &v); err == nil && v > maxV {
			maxV = v
		}
	}
	return maxV
}
