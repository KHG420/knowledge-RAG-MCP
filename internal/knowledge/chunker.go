package knowledge

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	shortChunk               = 200  // chars below this are merged into the preceding chunk
	longChunk                = 2000 // chars above this are re-split on sentence boundaries
	fragmentThreshold        = 60   // chars below this are merged into the preceding chunk after splitLong
	overlapChars             = 200  // chars from the previous chunk tail prepended to each chunk (sentence-aligned)
	defaultSemanticThreshold = 0.75 // cosine similarity threshold for semantic chunk merging
)

// ChunkText splits text into paragraph-level chunks suitable for BM25 retrieval.
//
// Algorithm:
//  1. Scan for markdown headings to track section boundaries.
//  2. Split on "\n\n" to get paragraphs, tracking character offsets.
//  3. Merge short paragraphs (< 200 chars) into the previous chunk.
//  4. Re-split long paragraphs (> 2000 chars) on sentence boundaries
//     (。.！!？?) so no single chunk is overwhelmingly large.
//  5. Merge tiny fragments (< 60 chars) produced by the long split.
//  6. Add ~200-char tail overlap from each previous chunk for context continuity.
//
// Empty input returns nil.
func ChunkText(text string) []ChunkWithMeta {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Build section map: for each character offset, what section are we in.
	sections := buildSectionMap(text)

	// Step 1: split into raw paragraphs with offsets.
	raw := splitParagraphsWithOffset(text)

	// Step 2: merge short paragraphs backward.
	merged := mergeShortWithOffset(raw)

	// Step 3: split long paragraphs on sentence boundaries.
	var out []ChunkWithMeta
	for _, p := range merged {
		sec := sectionAt(sections, p.offset)
		if utf8.RuneCountInString(p.content) > longChunk {
			for _, sub := range splitLong(p.content) {
				out = append(out, ChunkWithMeta{
					Content: sub,
					Section: sec,
					Offset:  p.offset,
				})
			}
		} else {
			out = append(out, ChunkWithMeta{
				Content: p.content,
				Section: sec,
				Offset:  p.offset,
			})
		}
	}
	// Step 4: merge tiny fragments (< 60 chars) produced by splitLong.
	out = mergeFragments(out)
	// Step 5: add contextual overlap between adjacent chunks.
	out = addOverlap(out)
	return out
}

// paraWithOffset is a paragraph with its character offset in the original text.
type paraWithOffset struct {
	content string
	offset  int
}

// buildSectionMap scans text for markdown headings (lines starting with #)
// and returns a map: character offset → section heading text.
// The section for any position is the nearest preceding heading.
func buildSectionMap(text string) []sectionBoundary {
	normalised := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalised, "\n")
	var boundaries []sectionBoundary
	offset := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isHeading(trimmed) {
			boundaries = append(boundaries, sectionBoundary{offset: offset, heading: trimmed})
		}
		offset += len(line) + 1 // +1 for the newline
	}
	return boundaries
}

type sectionBoundary struct {
	offset  int
	heading string
}

// sectionAt returns the section heading for a given character offset.
func sectionAt(boundaries []sectionBoundary, offset int) string {
	sec := ""
	for _, b := range boundaries {
		if b.offset <= offset {
			sec = b.heading
		} else {
			break
		}
	}
	return sec
}

// isHeading reports whether line is a markdown heading (e.g. "# Title", "## Section").
func isHeading(line string) bool {
	if !strings.HasPrefix(line, "#") {
		return false
	}
	// Must have a space after the #s and not be something like "###"
	hashEnd := 0
	for hashEnd < len(line) && line[hashEnd] == '#' {
		hashEnd++
	}
	if hashEnd > 6 {
		return false // too many #s
	}
	return hashEnd < len(line) && line[hashEnd] == ' '
}

// splitParagraphsWithOffset splits text on double-newline boundaries and tracks
// the character offset of each paragraph in the normalised text.
func splitParagraphsWithOffset(text string) []paraWithOffset {
	// Normalise \r\n → \n, then split on \n\n.
	normalised := strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.Split(normalised, "\n\n")
	var out []paraWithOffset
	offset := 0
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			// Find the actual offset of this paragraph in the normalised text.
			idx := strings.Index(normalised[offset:], trimmed)
			if idx >= 0 {
				out = append(out, paraWithOffset{content: trimmed, offset: offset + idx})
			} else {
				out = append(out, paraWithOffset{content: trimmed, offset: offset})
			}
		}
		offset += len(p) + 2 // +2 for the "\n\n" separator
	}
	return out
}

// mergeShortWithOffset merges paragraphs shorter than shortChunk chars into the
// preceding chunk. The first paragraph is never merged "upward".
func mergeShortWithOffset(paras []paraWithOffset) []paraWithOffset {
	if len(paras) <= 1 {
		return paras
	}
	var out []paraWithOffset
	for _, p := range paras {
		if len(out) > 0 && utf8.RuneCountInString(p.content) < shortChunk {
			// Merge into the previous chunk; keep the offset of the first chunk.
			out[len(out)-1].content += "\n\n" + p.content
		} else {
			out = append(out, p)
		}
	}
	return out
}

// mergeFragments merges chunks shorter than fragmentThreshold into the preceding
// chunk. This catches tiny fragments produced by splitLong (e.g. formula lines
// ending with "." that are < 60 chars). The first chunk is never merged "upward".
func mergeFragments(chunks []ChunkWithMeta) []ChunkWithMeta {
	if len(chunks) <= 1 {
		return chunks
	}
	var out []ChunkWithMeta
	for _, c := range chunks {
		if len(out) > 0 && utf8.RuneCountInString(c.Content) < fragmentThreshold {
			// Merge into the previous chunk; keep the offset/section of the first chunk.
			out[len(out)-1].Content += "\n\n" + c.Content
		} else {
			out = append(out, c)
		}
	}
	return out
}

// addOverlap prepends the tail of each previous chunk (~overlapChars runes) to
// the current chunk, providing contextual continuity across chunk boundaries.
// The overlap is truncated at the last sentence boundary within the tail window.
// Offset metadata is NOT modified — it continues to point to the original text position.
func addOverlap(chunks []ChunkWithMeta) []ChunkWithMeta {
	if len(chunks) <= 1 {
		return chunks
	}
	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1].Content
		runes := []rune(prev)
		if len(runes) == 0 {
			continue
		}
		// Determine the overlap start position: take ~overlapChars chars from the tail,
		// then walk forward to find the FIRST sentence boundary so the overlap reads
		// as complete trailing content from the previous chunk.
		start := 0
		if len(runes) > overlapChars {
			start = len(runes) - overlapChars
			// Walk forward from the start of the tail to find the first sentence end.
			tail := runes[start:]
			for j, r := range tail {
				if isSentenceEnd(r) {
					start = start + j + 1 // include the punctuation
					break
				}
			}
		}
		overlap := string(runes[start:])
		if overlap != "" {
			chunks[i].Content = overlap + "\n\n" + chunks[i].Content
		}
	}
	return chunks
}

// MergeSemanticNeighbors merges adjacent chunks whose embedding similarity exceeds
// the given threshold. This prevents semantically related content (e.g. same-topic
// paragraphs) from being split across chunk boundaries.
//
// The first chunk's offset and section are kept when two chunks are merged.
// When embedder is nil or embedding fails, the original chunks are returned unchanged.
// A threshold of 0.75 is a reasonable default (use defaultSemanticThreshold).
func MergeSemanticNeighbors(ctx context.Context, chunks []ChunkWithMeta, embedder Embedder, threshold float64) ([]ChunkWithMeta, error) {
	if len(chunks) <= 1 || embedder == nil {
		return chunks, nil
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}

	vectors, err := embedder.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}

	// Convert []float32 to []float64 for cosine similarity.
	vecs := make([][]float64, len(vectors))
	for i, v := range vectors {
		if v == nil {
			continue
		}
		vecs[i] = make([]float64, len(v))
		for j, f := range v {
			vecs[i][j] = float64(f)
		}
	}

	var out []ChunkWithMeta
	var outOrigIdx []int // tracks the original index for each entry in out

	for i := range chunks {
		if len(out) > 0 && i < len(vecs) && len(vecs[i]) > 0 {
			lastOrig := outOrigIdx[len(outOrigIdx)-1]
			if lastOrig < len(vecs) && vecs[lastOrig] != nil {
				sim := cosineSimilarity(vecs[i], vecs[lastOrig])
				if sim > threshold {
					out[len(out)-1].Content += "\n\n" + chunks[i].Content
					continue
				}
			}
		}
		out = append(out, chunks[i])
		outOrigIdx = append(outOrigIdx, i)
	}
	return out, nil
}

// ChunkTextContent is a convenience wrapper that returns just the content strings,
// for callers that don't need position metadata.
func ChunkTextContent(text string) []string {
	chunks := ChunkText(text)
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Content
	}
	return out
}

// splitLong splits a single long paragraph on sentence boundaries.
// It tries to cut at 。.！!？? and keeps each piece under ~longChunk chars.
func splitLong(text string) []string {
	sentences := splitSentences(text)
	if len(sentences) <= 1 {
		// Could not find sentence boundaries; return as-is.
		return []string{text}
	}

	var out []string
	var buf strings.Builder
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// If adding this sentence would exceed the limit and we already
		// have content, flush the buffer.
		if buf.Len() > 0 && utf8.RuneCountInString(buf.String())+utf8.RuneCountInString(s) > longChunk {
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
		}
		if buf.Len() > 0 {
			buf.WriteString(" ")
		}
		buf.WriteString(s)
	}
	if buf.Len() > 0 {
		out = append(out, strings.TrimSpace(buf.String()))
	}
	return out
}

// splitSentences splits text at sentence-ending punctuation marks
// (。.！!？?) while keeping the punctuation attached to its sentence.
func splitSentences(text string) []string {
	var out []string
	start := 0
	runes := []rune(text)
	for i, r := range runes {
		if isSentenceEnd(r) {
			// Include the punctuation in this sentence.
			out = append(out, string(runes[start:i+1]))
			start = i + 1
		}
	}
	// Remainder after the last punctuation.
	if start < len(runes) {
		rem := strings.TrimSpace(string(runes[start:]))
		if rem != "" {
			out = append(out, rem)
		}
	}
	return out
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '.', '！', '!', '？', '?':
		return true
	}
	return false
}

// classifySectionRole assigns a semantic role to a section heading based on
// its text. It normalises the heading to lowercase, strips heading markers
// and leading numbers, then matches against known paper-section patterns.
// Returns an empty string when the heading is not recognised.
func classifySectionRole(heading string) string {
	// Normalise: strip # markers, trim whitespace, lowercase.
	h := strings.TrimSpace(heading)
	h = strings.TrimLeft(h, "#")
	h = strings.TrimSpace(h)
	h = strings.ToLower(h)

	// Strip leading numbers like "1.", "2.1", "I.", "A.".
	for i, r := range h {
		if r == ' ' || r == '.' || r == ')' {
			if i > 0 {
				h = strings.TrimSpace(h[i+1:])
			}
			break
		}
		if !unicode.IsDigit(r) && r != '.' && r != '(' && r != ')' && r != 'i' && r != 'v' && r != 'x' && r != 'a' && r != 'b' && r != 'c' {
			break
		}
	}

	// Map known patterns.
	switch {
	case strings.Contains(h, "abstract") || strings.Contains(h, "摘要") || strings.Contains(h, "概要"):
		return "abstract"
	case strings.Contains(h, "introduction") || strings.Contains(h, "引言") || strings.Contains(h, "introducing"):
		return "introduction"
	case strings.Contains(h, "related work") || strings.Contains(h, "literature review") || strings.Contains(h, "background") || strings.Contains(h, "相关工作"):
		return "related_work"
	case strings.Contains(h, "method") || strings.Contains(h, "methodology") || strings.Contains(h, "approach") || strings.Contains(h, "proposed") || strings.Contains(h, "方法") || strings.Contains(h, "算法") || strings.Contains(h, "model"):
		return "methodology"
	case strings.Contains(h, "experiment") || strings.Contains(h, "evaluation") || strings.Contains(h, "results") || strings.Contains(h, "实验") || strings.Contains(h, "评估") || strings.Contains(h, "结果"):
		return "experiments"
	case strings.Contains(h, "conclusion") || strings.Contains(h, "discussion") || strings.Contains(h, "future work") || strings.Contains(h, "总结") || strings.Contains(h, "讨论") || strings.Contains(h, "结论"):
		return "conclusion"
	case strings.Contains(h, "reference") || strings.Contains(h, "bibliography") || strings.Contains(h, "参考文献"):
		return "references"
	}
	return ""
}

// ChunkTextHierarchical splits text into fine-grained (chunk-level) and
// coarse-grained (section-level) chunks. Fine chunks are produced by ChunkText;
// coarse chunks are created by concatenating all fine chunks that share the same
// section heading. The SectionID field links fine chunks to their parent coarse chunk.
//
// Returns (fine, coarse). When no section headings are found, a single coarse chunk
// covers the entire document. Each coarse chunk's SectionID reflects its heading.
// SectionRole is populated on both fine and coarse chunks via classifySectionRole.
func ChunkTextHierarchical(text string) (fine []ChunkWithMeta, coarse []ChunkWithMeta) {
	fine = ChunkText(text)
	if len(fine) == 0 {
		return nil, nil
	}

	// Assign section roles to fine chunks based on their section heading.
	for i := range fine {
		fine[i].SectionRole = classifySectionRole(fine[i].Section)
	}

	// Group fine chunks by their section heading.
	sectionGroups := map[string][]ChunkWithMeta{}
	sectionOrder := []string{} // preserve insertion order
	for _, c := range fine {
		sec := c.Section
		if _, exists := sectionGroups[sec]; !exists {
			sectionOrder = append(sectionOrder, sec)
		}
		sectionGroups[sec] = append(sectionGroups[sec], c)
	}

	// Build coarse chunks: one per section, concatenating all fine chunks.
	for _, sec := range sectionOrder {
		group := sectionGroups[sec]
		var b strings.Builder
		for i, c := range group {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(c.Content)
		}
		coarseChunk := ChunkWithMeta{
			Content:     b.String(),
			Section:     sec,
			Offset:      group[0].Offset,
			SectionID:   sec,
			SectionRole: classifySectionRole(sec),
		}
		coarse = append(coarse, coarseChunk)
	}

	// Link each fine chunk to its parent coarse chunk.
	secToCoarseID := map[string]string{}
	for i, sec := range sectionOrder {
		secToCoarseID[sec] = fmt.Sprintf("S%02d", i)
	}
	for i := range fine {
		fine[i].SectionID = secToCoarseID[fine[i].Section]
	}

	return fine, coarse
}
