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
	// Delegate to ingest engine when available (Phase 3.5).
	if s.ingestSvc != nil {
		meta, err := s.ingestSvc.UploadDocument(path, tags...)
		if err != nil {
			// The engine returns the generated metadata alongside a
			// post-slug persistence error so callers can clean up the
			// partial document. Never dereference a nil pointer here.
			if meta != nil {
				return *meta, err
			}
			return DocumentMeta{}, err
		}
		// A nil-error result with no metadata (or an empty slug) is not a
		// usable upload. Treating it as success would let a replacement
		// caller delete the original document while persisting nothing new.
		if meta == nil {
			return DocumentMeta{}, fmt.Errorf("upload: ingest engine reported success without document metadata")
		}
		if meta.Slug == "" {
			return DocumentMeta{}, fmt.Errorf("upload: ingest engine reported success with empty document slug")
		}
		return *meta, nil
	}
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
	text, err := ParseFile(context.Background(), path)
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
		if merged, err := MergeSemanticNeighbors(context.Background(), fineChunks, s.embedder, loadChunkParams().semanticThreshold); err == nil && len(merged) > 0 {
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
		// Chunk files may have been partially replaced: evict stale cached
		// views of this document before surfacing the failure.
		s.InvalidateDoc(slug)
		emit(StageWriting, "error", err.Error())
		// Return the partially written metadata so a replacement caller can
		// remove the slug it just created without touching the old document.
		return meta, fmt.Errorf("upload: write chunks: %w", err)
	}
	if len(coarseChunks) > 0 {
		if err := s.WriteSectionChunks(slug, coarseChunks); err != nil {
			// Non-fatal: sections are a convenience, not essential.
			log.Warnf("WriteSectionChunks %q: %v", slug, err)
		}
	}
	if err := s.WriteMeta(slug, meta); err != nil {
		s.InvalidateDoc(slug)
		emit(StageWriting, "error", err.Error())
		return meta, fmt.Errorf("upload: write meta: %w", err)
	}
	emit(StageWriting, "done", fmt.Sprintf("%d 分块已写入", len(chunks)))

	// Step 5: write CHUNKS.toml search index with section chunk links.
	// A failure here means the document would be stored but not searchable,
	// so it is fatal rather than a warning.
	emit(StageIndexing, "started", "")
	if err := s.writeChunksIndexFromMetaWithSections(slug, fineChunks, coarseChunks); err != nil {
		s.InvalidateDoc(slug)
		emit(StageIndexing, "error", err.Error())
		return meta, fmt.Errorf("upload: build search index: %w", err)
	}

	// Step 6: copy the original source file for traceability. Losing the
	// source means the ingestion cannot be audited, so this is fatal.
	if err := s.copySource(path, slug); err != nil {
		s.InvalidateDoc(slug)
		emit(StageWriting, "error", fmt.Sprintf("persist source: %v", err))
		return meta, fmt.Errorf("upload: persist source: %w", err)
	}

	// Step 7: write the full raw markdown text. Like the source file, this is
	// part of what makes the ingestion auditable, so failures are fatal.
	if err := s.WriteRawText(slug, text); err != nil {
		s.InvalidateDoc(slug)
		emit(StageWriting, "error", fmt.Sprintf("persist raw text: %v", err))
		return meta, fmt.Errorf("upload: persist raw text: %w", err)
	}

	// Step 8: update INDEX.md. This is a convenience summary that can be
	// rebuilt from meta.json, so a failure stays a warning.
	if err := s.updateIndex(slug, meta); err != nil {
		log.Warnf("updateIndex %q: %v", slug, err)
	}
	emit(StageIndexing, "done", "搜索索引已建立")

	// Invalidate caches for this document so that any stale query results
	// referencing old chunk IDs are evicted before the next search.
	s.InvalidateDoc(slug)

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
	// Delegate to ingest engine when available (Phase 3.5).
	if s.ingestSvc != nil {
		return s.ingestSvc.UploadDirectory(dir, recursive)
	}
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
