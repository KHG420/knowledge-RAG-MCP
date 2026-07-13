// Package knowledge implements a local, file-based knowledge base that stores
// arbitrary documents as paragraph-level chunks and retrieves them via BM25
// text search. It sits alongside the memory subsystem but is independent: memory
// stores discrete facts as frontmatter .md files indexed by MEMORY.md (which
// loads into the system-prompt prefix), while knowledge stores full documents in
// .reasonix/knowledge/<slug>/chunks/*.md and is queried at runtime via a tool —
// its content NEVER enters the system-prompt prefix, keeping the DeepSeek
// prefix-cache warm regardless of knowledge-base size.
//
// Layout:
//
//	.reasonix/knowledge/
//	├── INDEX.md                   ← document-level index (runtime, not prefix)
//	└── <document-slug>/
//	    ├── meta.json              ← {original_name, source_type, added_at, chunk_count, total_chars}
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
	SourceType   string    `json:"source_type"` // e.g. "pdf", "docx", "md", "txt"
	AddedAt      time.Time `json:"added_at"`
	ChunkCount   int       `json:"chunk_count"`
	TotalChars   int       `json:"total_chars"`
	Title        string    `json:"title,omitempty"`    // extracted paper title (C1)
	Authors      []string  `json:"authors,omitempty"`  // extracted paper authors (C1)
	Abstract     string    `json:"abstract,omitempty"` // extracted abstract text (C1)
	IsPaper      bool      `json:"is_paper,omitempty"` // true when looksLikePaper(text) during upload (G13)
}

// Chunk is a single paragraph-level slice of a document, stored as 000.md etc.
type Chunk struct {
	ID      string // e.g. "005"
	Content string // raw Markdown content of the chunk file
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
}

// SearchHit is one ranked result from a BM25 search over chunks.
type SearchHit struct {
	Score       float64
	DocSlug     string
	ChunkID     string
	Snippet     string // whitespace-compacted excerpt centered on the query
	Section     string // nearest markdown heading above this chunk (from CHUNKS.toml)
	Offset      int    // character offset in the original document text (0-based)
	SectionRole string // classified section role, e.g. "abstract", "introduction" (C2)
	DuplicateOf string // if non-empty, this hit is an approximate duplicate of chunkID (G9)
}

// SearchFilter holds optional filters for narrowing a knowledge base search.
// When a field is the zero value (empty string / 0 / false), the filter is not applied.
// Multiple filters are AND-ed together.
type SearchFilter struct {
	DocSlug    string // if non-empty, only search documents with this exact slug
	SourceType string // if non-empty, only search documents with this source type
	Section    string // if non-empty, only include chunks whose section contains this string (substring match)
	Coarse     bool   // G14: enable coarse-to-fine search (2-phase: section-level then fine-grained)
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
	Vector         []float64  `toml:"vector,omitempty"`
	SectionChunkID string     `toml:"section_chunk_id,omitempty"` // points to the parent section-level chunk (e.g. "S00")
	SectionRole    string     `toml:"section_role,omitempty"`     // classified role: "abstract", "introduction", etc. (C2)
}
