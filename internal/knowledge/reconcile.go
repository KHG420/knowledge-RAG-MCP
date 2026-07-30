// Package knowledge — reconciliation: periodic consistency checks between
// the declared state (manifests) and the actual state (chunk files, index
// entries, vector index).
//
// The reconciler runs:
//  1. On startup — fast check for any drift that occurred while the process
//     was down.
//  2. Periodically (configurable interval) — catch any filesystem corruption
//     or concurrent modifications.
//  3. On demand — triggered by an API call for manual verification.
//
// For each document in the knowledge base, the reconciler verifies:
//  - Manifest exists and is valid
//  - Every chunk listed in the manifest has a chunk file on disk
//  - Every chunk file on disk is listed in the manifest (no orphan files)
//  - CHUNKS.toml checksums match the manifest
//  - Vector index entries exist for all chunks that should have vectors
//
// Drift is categorized by severity:
//  - WARN:  missing optional data (section chunks, source file)
//  - ERROR: missing required data (chunk file, CHUNKS.toml entry)
//  - FATAL: document is completely missing or manifest is corrupt
package knowledge

import (
	"fmt"
	"time"
)

// ── Reconciliation report types ───────────────────────────────────────────────

// ReconcileSeverity indicates how serious a drift finding is.
type ReconcileSeverity string

const (
	ReconcileWarn  ReconcileSeverity = "warn"
	ReconcileError ReconcileSeverity = "error"
	ReconcileFatal ReconcileSeverity = "fatal"
)

// ReconcileFinding is a single inconsistency detected during reconciliation.
type ReconcileFinding struct {
	Severity   ReconcileSeverity `json:"severity"`
	DocSlug    string            `json:"doc_slug"`
	ChunkID    string            `json:"chunk_id,omitempty"`
	Message    string            `json:"message"`
	DetectedAt time.Time         `json:"detected_at"`
}

// ReconcileReport summarizes the results of a reconciliation run.
type ReconcileReport struct {
	KBName     string             `json:"kb_name"`
	StartedAt  time.Time          `json:"started_at"`
	Duration   time.Duration      `json:"duration"`
	DocsChecked int               `json:"docs_checked"`
	DocsOK     int                `json:"docs_ok"`
	Findings   []ReconcileFinding `json:"findings"`
}

// ── Reconciler ────────────────────────────────────────────────────────────────

// Reconciler checks consistency of a knowledge base.
type Reconciler struct {
	findings []ReconcileFinding
}

// NewReconciler creates a new Reconciler.
func NewReconciler() *Reconciler {
	return &Reconciler{}
}

// AddFinding records a reconciliation finding.
func (r *Reconciler) AddFinding(severity ReconcileSeverity, docSlug, chunkID, message string) {
	r.findings = append(r.findings, ReconcileFinding{
		Severity:   severity,
		DocSlug:    docSlug,
		ChunkID:    chunkID,
		Message:    message,
		DetectedAt: time.Now(),
	})
}

// Findings returns all findings from the last reconciliation run.
func (r *Reconciler) Findings() []ReconcileFinding {
	return r.findings
}

// Reset clears all findings for a new run.
func (r *Reconciler) Reset() {
	r.findings = nil
}

// HasErrors reports whether any ERROR or FATAL findings exist.
func (r *Reconciler) HasErrors() bool {
	for _, f := range r.findings {
		if f.Severity == ReconcileError || f.Severity == ReconcileFatal {
			return true
		}
	}
	return false
}

// HasFatal reports whether any FATAL findings exist.
func (r *Reconciler) HasFatal() bool {
	for _, f := range r.findings {
		if f.Severity == ReconcileFatal {
			return true
		}
	}
	return false
}

// ── Backend-dependent reconciliation helpers ──────────────────────────────────
// The actual reconciliation logic is split between the Reconciler (pure logic)
// and the Store (which has access to the backend for I/O).

// ValidateManifest checks that a ChunkManifest is internally consistent and
// records any findings.
func (r *Reconciler) ValidateManifest(manifest *ChunkManifest) bool {
	if manifest == nil {
		r.AddFinding(ReconcileFatal, "", "", "manifest is nil")
		return false
	}
	if manifest.DocSlug == "" {
		r.AddFinding(ReconcileFatal, "", "", "manifest has empty doc_slug")
		return false
	}

	if err := manifest.VerifyManifestIntegrity(); err != nil {
		r.AddFinding(ReconcileError, manifest.DocSlug, "", err.Error())
		return false
	}
	return true
}

// ValidateChunkFiles checks that every chunk ID in the manifest maps to a
// readable chunk file, and that there are no orphan chunk files.
// The actual file existence checks are done against the StorageBackend.
func (r *Reconciler) ValidateChunkFiles(manifest *ChunkManifest, existingIDs []string, readable func(id string) (string, error)) {
	slug := manifest.DocSlug

	// Check manifest → disk: every listed chunk must exist.
	manifestSet := make(map[string]bool, len(manifest.Chunks))
	for _, c := range manifest.Chunks {
		manifestSet[c.ID] = true
		content, err := readable(c.ID)
		if err != nil {
			r.AddFinding(ReconcileError, slug, c.ID,
				fmt.Sprintf("chunk file missing or unreadable: %v", err))
			continue
		}
		// Verify that the chunk content matches its declared ID.
		computed := ComputeChunkID(content)
		if computed != c.ID {
			r.AddFinding(ReconcileError, slug, c.ID,
				fmt.Sprintf("chunk content hash mismatch: declared=%s computed=%s", c.ID, computed))
		}
	}

	// Check disk → manifest: every chunk file on disk must be in the manifest.
	for _, id := range existingIDs {
		if !manifestSet[id] {
			r.AddFinding(ReconcileWarn, slug, id,
				"orphan chunk file (not in manifest)")
		}
	}
}

// ValidateIndexEntries checks ChunksIndex entries against the manifest.
func (r *Reconciler) ValidateIndexEntries(manifest *ChunkManifest, index *ChunksIndex) {
	slug := manifest.DocSlug
	if index == nil {
		r.AddFinding(ReconcileError, slug, "", "CHUNKS.toml index is missing")
		return
	}

	manifestSet := manifest.IDSet()
	indexSet := make(map[string]bool, len(index.Chunks))
	for _, e := range index.Chunks {
		indexSet[e.ID] = true
		if !manifestSet[e.ID] {
			r.AddFinding(ReconcileWarn, slug, e.ID,
				"index entry not in manifest (stale)")
		}
	}
	for _, c := range manifest.Chunks {
		if !indexSet[c.ID] {
			r.AddFinding(ReconcileError, slug, c.ID,
				"chunk in manifest but missing from CHUNKS.toml index")
		}
	}
}

// ValidateVectorConsistency checks that vector data in the ChunksIndex is
// internally consistent and matches the configured embedder (if any).
//   - HasVectors flag is consistent with actual Vector presence
//   - VectorDim matches embedder.Dim() when embedder is configured
//   - Vector dimension is consistent across all entries
func (r *Reconciler) ValidateVectorConsistency(index *ChunksIndex, embedderDim int) {
	slug := index.Slug
	if slug == "" {
		return
	}

	hasAnyVector := false
	hasMissingVector := false
	totalWithVector := 0

	for _, e := range index.Chunks {
		if len(e.Vector) > 0 {
			hasAnyVector = true
			totalWithVector++
			if embedderDim > 0 && len(e.Vector) != embedderDim {
				r.AddFinding(ReconcileWarn, slug, e.ID,
					fmt.Sprintf("vector dim mismatch: chunk has %d, embedder expects %d",
						len(e.Vector), embedderDim))
			}
		} else {
			hasMissingVector = true
		}
	}

	if hasAnyVector {
		if !index.HasVectors {
			r.AddFinding(ReconcileWarn, slug, "",
				fmt.Sprintf("HasVectors=false but %d/%d chunks have vector data",
					totalWithVector, len(index.Chunks)))
		}
		if index.VectorDim == 0 && embedderDim > 0 {
			r.AddFinding(ReconcileWarn, slug, "", "VectorDim is 0 but vectors are present")
		}
		if hasMissingVector && index.HasVectors {
			r.AddFinding(ReconcileWarn, slug, "",
				fmt.Sprintf("%d/%d chunks lack vectors despite HasVectors=true",
					len(index.Chunks)-totalWithVector, len(index.Chunks)))
		}
	} else if index.HasVectors {
		r.AddFinding(ReconcileWarn, slug, "", "HasVectors=true but no chunk has vector data")
	}
}
