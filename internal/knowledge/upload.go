package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProgressEvent describes a single stage of document upload processing.
type ProgressEvent struct {
	Stage  string `json:"stage"`
	Status string `json:"status"` // "started" | "done" | "error"
	Detail string `json:"detail,omitempty"`
}

// ProgressFunc is a callback invoked during UploadDocumentWithProgress.
// It receives ProgressEvent values describing each processing stage.
type ProgressFunc func(ProgressEvent)

// Stage constants used by UploadDocumentWithProgress.
const (
	StageParsing  = "parsing"
	StageChunking = "chunking"
	StageMerging  = "merging"
	StageMetadata = "metadata"
	StageWriting  = "writing"
	StageIndexing = "indexing"
	StageComplete = "complete"
)

// UploadDocument ingests a file into the knowledge base.
// It is a convenience wrapper around UploadDocumentWithProgress with no progress callback.
func (s *Store) UploadDocument(path string, tags ...string) (DocumentMeta, error) {
	return s.UploadDocumentWithProgress(path, nil, tags...)
}

// UploadDocumentWithProgress ingests a file into the knowledge base and calls
// progress for each processing stage:
//
//  1. parsing   — ParseFile extracts the full text.
//  2. chunking  — ChunkText splits it into paragraph-level chunks.
//  3. merging   — Optional semantic merging (skipped without embedder).
//  4. metadata  — Slug, paper metadata extraction.
//  5. writing   — Persist chunks, section chunks, and meta.json.
//  6. indexing  — Write CHUNKS.toml search index with vector embeddings.
//  7. complete  — Copy source, write raw text, update INDEX.md.
func (s *Store) UploadDocumentWithProgress(path string, progress ProgressFunc, tags ...string) (DocumentMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.logger.WithModule("upload")
	log.Infof("UploadDocument path=%q kb=%q", path, s.kbName)
	start := time.Now()

	emit := func(stage, status, detail string) {
		if progress != nil {
			progress(ProgressEvent{Stage: stage, Status: status, Detail: detail})
		}
	}

	// Step 1: parse.
	emit(StageParsing, "started", "")
	text, err := ParseFile(path)
	if err != nil {
		log.Errorf("UploadDocument %q: parse failed: %v", path, err)
		emit(StageParsing, "error", err.Error())
		return DocumentMeta{}, fmt.Errorf("upload: parse: %w", err)
	}
	emit(StageParsing, "done", fmt.Sprintf("%d 字符", len(text)))

	// Step 2: hierarchical chunking (fine + coarse section-level chunks).
	emit(StageChunking, "started", "")
	fineChunks, coarseChunks := ChunkTextHierarchical(text)
	if len(fineChunks) == 0 {
		log.Errorf("UploadDocument %q: no chunks produced", path)
		emit(StageChunking, "error", "文档未产生任何分块")
		return DocumentMeta{}, fmt.Errorf("upload: document produced no chunks (empty after parsing)")
	}
	log.Debugf("chunked %d chars → %d fine + %d coarse chunks", len(text), len(fineChunks), len(coarseChunks))
	emit(StageChunking, "done", fmt.Sprintf("%d 个段落分块", len(fineChunks)))

	// Step 2a: optional semantic merging to combine topically adjacent chunks.
	if s.embedder != nil {
		emit(StageMerging, "started", "")
		// Coordinate GPU: sleep reranker, wake embedding.
		var restoreEmbed func()
		if s.gpuScheduler != nil {
			restoreEmbed = s.gpuScheduler.PrepareForEmbedding()
		}
		if merged, err := MergeSemanticNeighbors(context.Background(), fineChunks, s.embedder, chunkSemanticThreshold); err == nil && len(merged) > 0 {
			fineChunks = merged
			// Regenerate coarse chunks to reflect the merged fine chunks.
			_, coarseChunks = ChunkTextHierarchical(text)
		}
		if restoreEmbed != nil {
			restoreEmbed()
		}
		emit(StageMerging, "done", fmt.Sprintf("%d 个分块（合并后）", len(fineChunks)))
	}

	// Extract content strings for writing chunk files.
	chunks := make([]string, len(fineChunks))
	for i, c := range fineChunks {
		chunks[i] = c.Content
	}

	// Step 3: derive slug and metadata.
	emit(StageMetadata, "started", "")
	slug := SlugFromPath(path)
	sourceType := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")

	meta := DocumentMeta{
		Slug:         slug,
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
	emit(StageMetadata, "done", slug)

	// Step 4: persist chunks, section chunks, and metadata.
	emit(StageWriting, "started", "")
	if err := s.WriteChunks(slug, chunks); err != nil {
		emit(StageWriting, "error", err.Error())
		return DocumentMeta{}, fmt.Errorf("upload: write chunks: %w", err)
	}
	if len(coarseChunks) > 0 {
		if err := s.WriteSectionChunks(slug, coarseChunks); err != nil {
			// Non-fatal: sections are a convenience, not essential.
			_ = err
		}
	}
	if err := s.WriteMeta(slug, meta); err != nil {
		emit(StageWriting, "error", err.Error())
		return DocumentMeta{}, fmt.Errorf("upload: write meta: %w", err)
	}
	emit(StageWriting, "done", fmt.Sprintf("%d 分块已写入", len(chunks)))

	// Step 5: write CHUNKS.toml search index with section chunk links.
	emit(StageIndexing, "started", "")
	if err := s.writeChunksIndexFromMetaWithSections(slug, fineChunks, coarseChunks); err != nil {
		log.Warnf("writeChunksIndexFromMetaWithSections for %q: %v", slug, err)
	}

	// Step 6: optionally copy source file for traceability.
	if err := s.copySource(path, slug); err != nil {
		// Non-fatal: the document is already ingested.
		_ = err
	}

	// Step 7: write the full raw markdown text as document.md for reference.
	if err := s.WriteRawText(slug, text); err != nil {
		// Non-fatal: the document is already ingested.
		_ = err
	}

	// Step 8: update INDEX.md.
	if err := s.updateIndex(slug, meta); err != nil {
		// Non-fatal: re-index can be rebuilt.
		_ = err
	}
	emit(StageIndexing, "done", "搜索索引已建立")

	log.Infof("UploadDocument %q done: slug=%q chunks=%d chars=%d in %v", path, slug, meta.ChunkCount, meta.TotalChars, time.Since(start))
	emit(StageComplete, "done", slug)
	return meta, nil
}

// copySource stores the original file via the backend.
func (s *Store) copySource(srcPath, slug string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	ext := filepath.Ext(srcPath)
	return s.backend.WriteSource(s.kbName, slug, data, ext)
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
// Returns a summary string for the caller.
func (s *Store) UploadDirectory(dir string, recursive bool, tags ...string) (string, error) {
	s.logger.WithModule("upload").Infof("UploadDirectory dir=%q recursive=%v kb=%q", dir, recursive, s.kbName)
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
