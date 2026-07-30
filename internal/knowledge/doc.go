// Package knowledge implements a local, file-based knowledge base that stores
// arbitrary documents as paragraph-level chunks and retrieves them via BM25
// text search. It sits alongside the memory subsystem but is independent: memory
// stores discrete facts as frontmatter .md files indexed by MEMORY.md (which
// loads into the system-prompt prefix), while knowledge stores full documents in
// ~/knowledge_base/<slug>/chunks/*.md and is queried at runtime via a tool —
// its content NEVER enters the system-prompt prefix, keeping the DeepSeek
// prefix-cache warm regardless of knowledge-base size.
//
// Layout:
//
//	~/knowledge_base/<kb-name>/
//	├── INDEX.md                   ← document-level index (runtime, not prefix)
//	└── <document-slug>/
//	    ├── meta.json              ← {original_name, source_type, added_at, chunk_count, total_chars}
//	    ├── document.md            ← full raw markdown from external API parser
//	    ├── CHUNKS.toml            ← pre-computed term frequencies per chunk (search index)
//	    ├── source.<ext>           ← original file (preserved for audit)
//	    └── chunks/
//	        ├── 000.md
//	        ├── 001.md
//	        └── ...
//
// Document parsing uses tsawler/tabula (MIT, pure Go) for PDF/DOCX/ODT/EPUB/
// HTML/XLSX/PPTX/MD/TXT; chunking splits on paragraph boundaries with
// short-chunk merging and long-chunk sentence-boundary re-splitting; search
// reuses the internal/retrieval BM25 engine already in use by history/memory.
package knowledge

import "time"

// DocumentMeta is the per-document metadata persisted in meta.json.
type DocumentMeta struct {
	OriginalName string    `json:"original_name"`
	Slug         string    `json:"slug"`          // unique identifier, e.g. "2501-05366v1-20260713-094555"
	SourceType   string    `json:"source_type"`   // e.g. "pdf", "docx", "md", "txt"
	AddedAt      time.Time `json:"added_at"`
	ChunkCount   int       `json:"chunk_count"`
	TotalChars   int       `json:"total_chars"`
	Title        string    `json:"title,omitempty"`    // extracted paper title (C1)
	Authors      []string  `json:"authors,omitempty"`  // extracted paper authors (C1)
	Abstract     string    `json:"abstract,omitempty"` // extracted abstract text (C1)
	IsPaper      bool      `json:"is_paper,omitempty"` // true when looksLikePaper(text) during upload (G13)
	Tags         []string  `json:"tags,omitempty"`     // user-assigned labels for filtering (G15)
}

// DocumentInfo carries human-readable document identity for attribution.
type DocumentInfo struct {
	ID           string `json:"id"`                      // internal slug
	Title        string `json:"title,omitempty"`         // human-readable title (e.g. paper title)
	OriginalName string `json:"original_name,omitempty"` // original filename (e.g. "paper.pdf")
	Type         string `json:"type,omitempty"`          // source type (e.g. "pdf", "md")
}

// LocationInfo pinpoints where a chunk sits inside its source document.
type LocationInfo struct {
	ChunkID   string `json:"chunk_id"`            // e.g. "005"
	Section   string `json:"section,omitempty"`   // nearest markdown heading
	Offset    int    `json:"offset,omitempty"`    // character offset in the original text (0-based)
	PageStart int    `json:"page_start,omitempty"` // PDF page number (future; requires PDF parser support)
	PageEnd   int    `json:"page_end,omitempty"`   // PDF page number (future; requires PDF parser support)
}

// HitContent wraps the textual content of a search hit.
type HitContent struct {
	Snippet     string `json:"snippet"`                // whitespace-compacted excerpt centered on the query
	SectionRole string `json:"section_role,omitempty"` // e.g. "abstract", "introduction"
}

// EvidenceChunk is a full read result with complete source attribution —
// returned by knowledge_read so the LLM never loses track of where text came from.
type EvidenceChunk struct {
	Document   DocumentInfo `json:"document"`
	Location   LocationInfo `json:"location"`
	Content    string       `json:"content"`
	CitationID string       `json:"citation_id"` // stable reference, e.g. "{slug}_{chunkID}"
}

// ChunkWithMeta is a chunk bundled with position metadata: which section of the
// document it belongs to, its character offset in the original full text, and
// an optional section role (e.g. "abstract", "introduction") for search weighting.
type ChunkWithMeta struct {
	Content     string // chunk text
	Section     string // nearest markdown heading above this chunk, e.g. "## 安装"
	Offset      int    // character offset in the original document text (0-based)
	SectionID   string // section identifier for grouping coarse-level chunks (e.g. "# Introduction")
	SectionRole string // classified role: "abstract", "introduction", etc. (C2)
	PageStart   int    // PDF page number where this chunk starts (1-based, 0 = unknown)
	PageEnd     int    // PDF page number where this chunk ends (1-based, 0 = unknown)
}

// SearchHit is one ranked result from a search over chunks. It carries
// enough source attribution (document identity + location + stable citation id)
// that an LLM can correctly attribute every piece of evidence.
type SearchHit struct {
	Score      float64      `json:"score"`
	Document   DocumentInfo `json:"document"`
	Location   LocationInfo `json:"location"`
	Content    HitContent   `json:"content"`
	CitationID string       `json:"citation_id,omitempty"` // "{slug}_{chunkID}"

	// DuplicateOf is non-empty when this hit is an approximate duplicate of another chunkID (G9).
	DuplicateOf string `json:"duplicate_of,omitempty"`

	// SectionHint is non-empty when multiple chunks from the same section
	// appear in the search results. It suggests reading the full section for
	// complete context.
	SectionHint string `json:"section_hint,omitempty"`
}

// SearchFilter holds optional filters for narrowing a knowledge base search.
// When a field is the zero value (empty slice/string/zero time/false), the filter is not applied.
// Multiple filters are AND-ed together.
type SearchFilter struct {
	DocSlug     string    // if non-empty, only search documents with this exact slug
	SourceType  string    // if non-empty, only search documents with this source type
	Section     string    // if non-empty, only include chunks whose section contains this string (substring match)
	Tags        []string  // if non-empty, only include docs that have at least one matching tag
	AddedAfter  time.Time // if non-zero, only include docs added at or after this time
	AddedBefore time.Time // if non-zero, only include docs added at or before this time
	Coarse      bool      // G14: enable coarse-to-fine search (2-phase: section-level then fine-grained)
}

// DocumentHit is a ranked document result from SearchDocuments. It groups
// chunk-level results by document using MaxP (maximum chunk score per doc).
type DocumentHit struct {
	Score     float64 // MaxP score (highest chunk score for this doc)
	DocSlug   string
	DocMeta   DocumentMeta // full metadata from meta.json
	TopChunks []SearchHit  // top-3 representative chunks, sorted by score descending
}

// termFreq is a single term-count pair for TOML serialization. Using a struct
// array instead of map[string]int reduces CHUNKS.toml size by ~60%.
type termFreq struct {
	Term  string `toml:"term"`
	Count int    `toml:"count"`
}

// termFreqsToMap converts a []termFreq slice back to a map[string]int for the
// search pipeline (which uses maps for O(1) term lookup).
func termFreqsToMap(freqs []termFreq) map[string]int {
	m := make(map[string]int, len(freqs))
	for _, tf := range freqs {
		m[tf.Term] = tf.Count
	}
	return m
}

// ChunksIndex is the per-document search index persisted in CHUNKS.toml. It
// stores pre-computed term frequencies for every chunk so Search can score
// documents without re-reading and re-tokenising every chunk file.
// When an embedder is configured, it also stores dense vector representations
// for hybrid BM25 + embedding search.
type ChunksIndex struct {
	Slug       string            `toml:"slug"`
	ChunkCount int               `toml:"chunk_count"`
	VectorDim  int               `toml:"vector_dim,omitempty"`
	HasVectors bool              `toml:"has_vectors,omitempty"`
	Checksum   string            `toml:"checksum,omitempty"` // SHA256 of chunks/*.md files (G10)
	Chunks     []ChunkIndexEntry `toml:"chunks"`
}

// ChunkIndexEntry holds the pre-computed term frequencies, position
// metadata, and optional dense vector for one chunk.
type ChunkIndexEntry struct {
	ID             string     `toml:"id"`
	TermCount      int        `toml:"term_count"`
	Terms          []termFreq `toml:"terms"`
	Section        string     `toml:"section"`
	Offset         int        `toml:"offset"`
	PageStart      int        `toml:"page_start,omitempty"` // PDF page number (1-based, 0 = unknown)
	PageEnd        int        `toml:"page_end,omitempty"`   // PDF page number (1-based, 0 = unknown)
	Vector         []float64  `toml:"vector,omitempty"`
	SectionChunkID string     `toml:"section_chunk_id,omitempty"` // points to the parent section-level chunk (e.g. "S00")
	SectionRole    string     `toml:"section_role,omitempty"`     // classified role: "abstract", "introduction", etc. (C2)
}
