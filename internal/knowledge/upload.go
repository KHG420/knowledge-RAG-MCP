package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UploadDocument ingests a file into the knowledge base:
//  1. ParseFile extracts the full text.
//  2. ChunkText splits it into paragraph-level chunks with position metadata.
//  3. Chunks are written as NNN.md under .reasonix/knowledge/<slug>/chunks/.
//  4. Metadata is written to meta.json.
//  5. CHUNKS.toml search index is written with position metadata.
//  6. INDEX.md is updated with a link to the new document.
//
// The original file is NOT copied into the knowledge base by this method; the
// caller is responsible for preserving source.<ext> if desired. Returns the
// generated slug and the metadata written.
func (s *Store) UploadDocument(path string, tags ...string) (DocumentMeta, error) {
	// Step 1: parse.
	text, err := ParseFile(path)
	if err != nil {
		return DocumentMeta{}, fmt.Errorf("upload: parse: %w", err)
	}

	// Step 2: hierarchical chunking (fine + coarse section-level chunks).
	fineChunks, coarseChunks := ChunkTextHierarchical(text)
	if len(fineChunks) == 0 {
		return DocumentMeta{}, fmt.Errorf("upload: document produced no chunks (empty after parsing)")
	}

	// Step 2a: optional semantic merging to combine topically adjacent chunks.
	if s.embedder != nil {
		if merged, err := MergeSemanticNeighbors(context.Background(), fineChunks, s.embedder, defaultSemanticThreshold); err == nil && len(merged) > 0 {
			fineChunks = merged
			// Regenerate coarse chunks to reflect the merged fine chunks.
			_, coarseChunks = ChunkTextHierarchical(text)
		}
	}

	// Extract content strings for writing chunk files.
	chunks := make([]string, len(fineChunks))
	for i, c := range fineChunks {
		chunks[i] = c.Content
	}

	// Step 3: derive slug and metadata.
	slug := SlugFromPath(path)
	sourceType := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")

	meta := DocumentMeta{
		OriginalName: filepath.Base(path),
		SourceType:   sourceType,
		AddedAt:      time.Now().Truncate(time.Second),
		ChunkCount:   len(chunks),
		TotalChars:   len(text),
		Tags:         tags,
	}

	// C1: attempt paper metadata extraction for paper-like documents.
	if looksLikePaper(text) {
		title, authors, abstract := ExtractPaperMeta(text)
		meta.Title = title
		meta.Authors = authors
		meta.Abstract = abstract
		meta.IsPaper = true
	}

	// Step 4: persist chunks, section chunks, and metadata.
	if err := s.WriteChunks(slug, chunks); err != nil {
		return DocumentMeta{}, fmt.Errorf("upload: write chunks: %w", err)
	}
	if len(coarseChunks) > 0 {
		if err := s.WriteSectionChunks(slug, coarseChunks); err != nil {
			// Non-fatal: sections are a convenience, not essential.
			_ = err
		}
	}
	if err := s.WriteMeta(slug, meta); err != nil {
		return DocumentMeta{}, fmt.Errorf("upload: write meta: %w", err)
	}

	// Step 5: write CHUNKS.toml search index with section chunk links.
	if err := s.writeChunksIndexFromMetaWithSections(slug, fineChunks, coarseChunks); err != nil {
		// Non-fatal: the index can be rebuilt from chunk files.
		_ = err
	}

	// Step 6: optionally copy source file for traceability.
	if err := s.copySource(path, slug); err != nil {
		// Non-fatal: the document is already ingested.
		_ = err
	}

	// Step 7: update INDEX.md.
	if err := s.updateIndex(slug, meta); err != nil {
		// Non-fatal: re-index can be rebuilt.
		_ = err
	}

	return meta, nil
}

// copySource copies the original file into the document directory as source.<ext>.
func (s *Store) copySource(srcPath, slug string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	ext := filepath.Ext(srcPath)
	dest := filepath.Join(s.DocDir(slug), "source"+ext)
	if err := os.MkdirAll(s.DocDir(slug), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o644)
}

// updateIndex appends a line to INDEX.md for the newly uploaded document.
func (s *Store) updateIndex(slug string, meta DocumentMeta) error {
	existing, _ := s.ReadIndex()
	line := fmt.Sprintf("- [%s](%s/meta.json) — %d chunks, %s\n",
		meta.OriginalName, slug, meta.ChunkCount, meta.AddedAt.Format(time.RFC3339))
	return s.WriteIndex(existing + line)
}

// UploadDirectory ingests all supported document files under dir. When recursive
// is true it walks subdirectories; otherwise it scans only the top level.
// Returns a summary string for the caller (e.g. "Uploaded 5 documents (3 pdf, 2 md), 0 failures").
func (s *Store) UploadDirectory(dir string, recursive bool, tags ...string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("access directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", dir)
	}

	type result struct {
		path string
		ext  string
		err  error
	}
	var results []result

	walkFn := func(path string, d os.FileInfo, err error) error {
		if err != nil {
			return err // skip inaccessible files
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		// Only ingest files with a parseable extension.
		switch ext {
		case ".md", ".txt", ".pdf", ".docx", ".odt", ".epub", ".html", ".xlsx", ".pptx":
			// supported
		default:
			return nil
		}
		_, uploadErr := s.UploadDocument(path, tags...)
		results = append(results, result{path: path, ext: ext, err: uploadErr})
		return nil // never abort the walk for individual file failures
	}

	if recursive {
		err = filepath.Walk(dir, walkFn)
	} else {
		err = filepath.Walk(dir, func(path string, d os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			// Skip subdirectories when not recursive.
			if d.IsDir() && path != dir {
				return filepath.SkipDir
			}
			return walkFn(path, d, err)
		})
	}
	if err != nil {
		return "", fmt.Errorf("walk directory %q: %w", dir, err)
	}

	if len(results) == 0 {
		return "No supported documents found.", nil
	}

	// Count successes and failures.
	extCount := map[string]int{}
	successCount := 0
	failCount := 0
	var failDetails []string
	for _, r := range results {
		extCount[r.ext]++
		if r.err != nil {
			failCount++
			failDetails = append(failDetails, fmt.Sprintf("  - %s: %v", r.path, r.err))
		} else {
			successCount++
		}
	}

	// Build extension summary.
	var extParts []string
	for _, ext := range []string{".md", ".txt", ".pdf", ".docx", ".odt", ".epub", ".html", ".xlsx", ".pptx"} {
		if n := extCount[ext]; n > 0 {
			extParts = append(extParts, fmt.Sprintf("%d %s", n, strings.TrimPrefix(ext, ".")))
		}
	}
	extSummary := strings.Join(extParts, ", ")

	summary := fmt.Sprintf("Uploaded %d documents (%s), %d failures", successCount, extSummary, failCount)
	if failCount > 0 {
		summary += "\n\nFailures:\n" + strings.Join(failDetails, "\n")
	}
	return summary, nil
}
