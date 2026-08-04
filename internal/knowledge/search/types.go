// Package search — internal types used during the retrieval pipeline.
// These mirror the unexported types in the parent knowledge package
// so the search engine can operate without needing Store internals.
package search

// ── Scoring types ───────────────────────────────────────────────────────────

// searchEntry is a unified representation of one chunk during scoring.
type searchEntry struct {
	docSlug        string
	chunkID        string
	text           string
	terms          map[string]int
	termLen        int
	section        string
	offset         int
	sourceType     string
	title          string
	originalName   string
	pageStart      int
	pageEnd        int
	vector         []float64
	sectionRole    string
	sectionChunkID string
	isPaper        bool
}

// rankedEntry pairs a search entry with its score for sorting.
type rankedEntry struct {
	entry searchEntry
	score float64
}

// ── Constants ───────────────────────────────────────────────────────────────

const dedupJaccardThreshold = 0.6
const rrfK = 60.0
