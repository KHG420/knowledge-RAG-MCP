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

import (
	"strings"
	"time"
)

// DocumentMeta is the per-document metadata persisted in meta.json.
type DocumentMeta struct {
	OriginalName string    `json:"original_name"`
	Slug         string    `json:"slug"`        // unique identifier, e.g. "2501-05366v1-20260713-094555"
	SourceType   string    `json:"source_type"` // e.g. "pdf", "docx", "md", "txt"
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
	ChunkID   string `json:"chunk_id"`             // e.g. "005"
	Section   string `json:"section,omitempty"`    // nearest markdown heading
	Offset    int    `json:"offset,omitempty"`     // character offset in the original text (0-based)
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
	CitationID string       `json:"citation_id"`        // stable reference, e.g. "{slug}_{chunkID}"
	Evidence   EvidenceMeta `json:"evidence,omitempty"` // v4: feature-based evidence quality metadata
}

// EvidenceMeta records lightweight evidence-quality signals computed from the
// chunk text with pure text rules (no LLM, no embedding).
type EvidenceMeta struct {
	SourceConfidence string `json:"source_confidence"` // "exact_section" | "related_section" | "semantic_match"
	AnswerRelevance  string `json:"answer_relevance"`  // "high" | "medium" | "low"
	Completeness     string `json:"completeness"`      // "complete" | "partial" | "context_only"
}

// evidenceFeatures captures structural signals extracted from a chunk of text.
// All fields are determined by lightweight string matching — zero extra API calls.
type evidenceFeatures struct {
	hasDefinition   bool // "是"、"定义"、"指"、"refers to"、"defined as"
	hasMechanism    bool // "因为"、"由于"、"导致"、"because"、"机理"
	hasQuantitative bool // digit count ≥3 or unit symbols present
	hasConclusion   bool // "因此"、"结果表明"、"thus"、"therefore"
}

// ExtractEvidenceFeatures scans text and returns structural content signals.
func ExtractEvidenceFeatures(text string) evidenceFeatures {
	lower := strings.ToLower(text)
	digitCount := 0
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digitCount++
		}
	}
	return evidenceFeatures{
		hasDefinition: strings.Contains(lower, "是") ||
			strings.Contains(lower, "定义") ||
			strings.Contains(lower, "指") ||
			strings.Contains(lower, "refers to") ||
			strings.Contains(lower, "defined as") ||
			strings.Contains(lower, "称为") ||
			strings.Contains(lower, "即"),
		hasMechanism: strings.Contains(lower, "因为") ||
			strings.Contains(lower, "由于") ||
			strings.Contains(lower, "导致") ||
			strings.Contains(lower, "because") ||
			strings.Contains(lower, "机理") ||
			strings.Contains(lower, "机制") ||
			strings.Contains(lower, "原理") ||
			strings.Contains(lower, "due to") ||
			strings.Contains(lower, "caused by"),
		hasQuantitative: digitCount >= 3 ||
			strings.Contains(lower, "m/s") ||
			strings.Contains(lower, "deg") ||
			strings.Contains(lower, "n·m") ||
			strings.Contains(lower, "rad/s") ||
			strings.Contains(lower, "mm") ||
			strings.Contains(lower, "kg") ||
			strings.Contains(lower, "kn") ||
			strings.Contains(lower, "%") ||
			strings.Contains(lower, "°"),
		hasConclusion: strings.Contains(lower, "因此") ||
			strings.Contains(lower, "结果表明") ||
			strings.Contains(lower, "thus") ||
			strings.Contains(lower, "therefore") ||
			strings.Contains(lower, "综上") ||
			strings.Contains(lower, "conclusion") ||
			strings.Contains(lower, "最后"),
	}
}

// ClassifyCompleteness maps evidenceFeatures to a completeness label.
//
//	"complete"      — has definition AND (mechanism OR quantitative data)
//	"partial"       — has definition OR mechanism
//	"context_only"  — none of the above (pure descriptive text)
func ClassifyCompleteness(feats evidenceFeatures) string {
	if feats.hasDefinition && (feats.hasMechanism || feats.hasQuantitative) {
		return "complete"
	}
	if feats.hasDefinition || feats.hasMechanism {
		return "partial"
	}
	return "context_only"
}

// ClassifyAnswerRelevance estimates how directly the chunk answers a question
// based on the presence of structural evidence signals.
func ClassifyAnswerRelevance(feats evidenceFeatures) string {
	count := 0
	if feats.hasDefinition {
		count++
	}
	if feats.hasMechanism {
		count++
	}
	if feats.hasQuantitative {
		count++
	}
	if feats.hasConclusion {
		count++
	}
	switch {
	case count >= 3:
		return "high"
	case count >= 2:
		return "medium"
	default:
		return "low"
	}
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
type TermFreq struct {
	Term  string `toml:"term"`
	Count int    `toml:"count"`
}

// TermFreqsToMap converts a []TermFreq slice back to a map[string]int for the
// search pipeline (which uses maps for O(1) term lookup).
func TermFreqsToMap(freqs []TermFreq) map[string]int {
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
	Terms          []TermFreq `toml:"terms"`
	Section        string     `toml:"section"`
	Offset         int        `toml:"offset"`
	PageStart      int        `toml:"page_start,omitempty"` // PDF page number (1-based, 0 = unknown)
	PageEnd        int        `toml:"page_end,omitempty"`   // PDF page number (1-based, 0 = unknown)
	Vector         []float64  `toml:"vector,omitempty"`
	SectionChunkID string     `toml:"section_chunk_id,omitempty"` // points to the parent section-level chunk (e.g. "S00")
	SectionRole    string     `toml:"section_role,omitempty"`     // classified role: "abstract", "introduction", etc. (C2)
}
