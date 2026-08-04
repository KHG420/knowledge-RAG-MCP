package knowledge

import (
	"time"
)

// rankedEntry pairs a search entry with its BM25 / hybrid score for sorting.
type rankedEntry struct {
	entry searchEntry
	score float64
}

const dedupJaccardThreshold = 0.6 // Jaccard similarity threshold for snippet dedup (G9)

const rrfK = 60.0 // RRF constant for hybrid search fusion

// SearchLogger is an optional interface for recording search queries and their
// results for telemetry, tuning, and feedback. A nil logger is silently ignored.
type SearchLogger interface {
	LogSearch(entry SearchLogEntry)
}

// SearchLogEntry captures a single search query and its top results.
type SearchLogEntry struct {
	Query      string        `json:"query"`
	HitCount   int           `json:"hit_count"`
	HitIDs     []string      `json:"hit_ids,omitempty"` // returned chunk IDs in ranked order
	TopScores  []float64     `json:"top_scores,omitempty"`
	JudgedHits []string      `json:"judged_hits,omitempty"` // human-annotated relevant chunk IDs
	Filter     *SearchFilter `json:"filter,omitempty"`
	Timestamp  time.Time     `json:"timestamp"`
}

// searchEntry is a unified representation of one chunk during scoring. When the
// index path is used, text is empty until snippet generation; when the fallback
// path is used, text is populated from the chunk file and tokens are computed
// on the fly.
type searchEntry struct {
	docSlug        string
	chunkID        string
	text           string         // chunk content (only populated in fallback or for snippet)
	terms          map[string]int // term frequencies (from index or computed)
	termLen        int            // total token count
	section        string         // from CHUNKS.toml metadata
	offset         int            // from CHUNKS.toml metadata
	sourceType     string         // from meta.json
	title          string         // from meta.json (human-readable document title)
	originalName   string         // from meta.json (original filename)
	pageStart      int            // from CHUNKS.toml (PDF page number, 1-based, 0 = unknown)
	pageEnd        int            // from CHUNKS.toml (PDF page number, 1-based, 0 = unknown)
	vector         []float64      // dense embedding vector (from CHUNKS.toml, if available)
	sectionRole    string         // classified section role (C2), e.g. "abstract", "introduction"
	sectionChunkID string         // G14: parent section chunk ID (e.g. "S00"), for coarse-to-fine search
	isPaper        bool           // G13: true when the parent document is an academic paper
}

