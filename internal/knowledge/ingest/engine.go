// Package ingest manages document ingestion: parsing, chunking, uploading,
// and upload task management. Extracted from Store (REFACTOR_PLAN Phase 3.5).
//
// Engine implements knowledge.Ingester. It performs the full upload pipeline:
// parse → chunk → semantic merge → metadata → persist → index → complete.
//
// Dependency injection (Phase 3.5 completion):
// Engine requires backend, embedder, GPU scheduler, cache client, and chunk
// store to be injected before UploadDocument methods can produce results.
// The vector index building step is provided as a callback since HNSW
// management remains in the Store during the transition period.
package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// Engine manages document ingestion: parsing, chunking, uploading.
// It coordinates upload tasks and GPU scheduling for external doc parsers.
//
// All Upload methods (UploadDocument, UploadDocumentWithProgress, UploadDirectory,
// CopySource) require the infrastructure dependencies to be injected. Without
// them they return descriptive errors.
type Engine struct {
	// ── Core infrastructure (Phase 3.5: injected) ──
	backend      knowledge.StorageBackend
	embedder     knowledge.Embedder
	gpuScheduler *knowledge.GPUScheduler
	cacheClient  knowledge.CacheClient
	chunkStore   knowledge.ChunkStore // for ReadIndex/WriteIndex/WriteMeta when available

	// ── KB identity ──
	kbName  string
	dataDir string

	// ── Vector index building callback (provided by Store during transition) ──
	// Called after chunks are written to build CHUNKS.toml with embeddings + HNSW.
	// When nil, the index-building step is skipped (chunks are still persisted).
	buildChunksIndex func(slug string, chunks []knowledge.ChunkWithMeta, sectionChunks []knowledge.ChunkWithMeta) error

	// ── Upload task management ──
	taskManager *knowledge.UploadTaskManager

	// ── Infrastructure ──
	logger *logging.Logger
	mu     *sync.Mutex
}

// New creates an Ingest Engine with all dependencies for the full upload pipeline.
func New(
	backend knowledge.StorageBackend,
	embedder knowledge.Embedder,
	gpuSched *knowledge.GPUScheduler,
	cacheClient knowledge.CacheClient,
	chunkStore knowledge.ChunkStore,
	kbName, dataDir string,
	buildChunksIndex func(slug string, chunks []knowledge.ChunkWithMeta, sectionChunks []knowledge.ChunkWithMeta) error,
	taskMgr *knowledge.UploadTaskManager,
	mu *sync.Mutex,
	logger *logging.Logger,
) *Engine {
	return &Engine{
		backend:          backend,
		embedder:         embedder,
		gpuScheduler:     gpuSched,
		cacheClient:      cacheClient,
		chunkStore:       chunkStore,
		kbName:           kbName,
		dataDir:          dataDir,
		buildChunksIndex: buildChunksIndex,
		taskManager:      taskMgr,
		mu:               mu,
		logger:           logger,
	}
}

// NewSimple creates an Ingest Engine with minimal dependencies (suitable for
// test environments where no upload happens). Use New() for production.
func NewSimple(
	taskMgr *knowledge.UploadTaskManager,
	gpuSched *knowledge.GPUScheduler,
	mu *sync.Mutex,
	logger *logging.Logger,
) *Engine {
	return &Engine{
		taskManager:  taskMgr,
		gpuScheduler: gpuSched,
		logger:       logger,
		mu:           mu,
	}
}

// ── Dependency setters (Phase 3.5) ────────────────────────────────────────────

func (e *Engine) SetBackend(b knowledge.StorageBackend)                     { e.backend = b }
func (e *Engine) SetEmbedder(emb knowledge.Embedder)                        { e.embedder = emb }
func (e *Engine) SetGPUScheduler(gs *knowledge.GPUScheduler)                { e.gpuScheduler = gs }
func (e *Engine) SetCacheClient(c knowledge.CacheClient)                    { e.cacheClient = c }
func (e *Engine) SetChunkStore(cs knowledge.ChunkStore)                     { e.chunkStore = cs }
func (e *Engine) SetKBName(name string)                                     { e.kbName = name }
func (e *Engine) SetDataDir(dir string)                                     { e.dataDir = dir }
func (e *Engine) SetBuildChunksIndex(fn func(string, []knowledge.ChunkWithMeta, []knowledge.ChunkWithMeta) error) { e.buildChunksIndex = fn }

// ── Basic accessors ───────────────────────────────────────────────────────────

func (e *Engine) TaskManager() *knowledge.UploadTaskManager  { return e.taskManager }
func (e *Engine) SetTaskManager(tm *knowledge.UploadTaskManager) { e.taskManager = tm }
func (e *Engine) GPUScheduler() *knowledge.GPUScheduler       { return e.gpuScheduler }
func (e *Engine) Logger() *logging.Logger                     { return e.logger }
func (e *Engine) SetLogger(l *logging.Logger)                 { e.logger = l }
func (e *Engine) Mutex() *sync.Mutex                          { return e.mu }
func (e *Engine) Backend() knowledge.StorageBackend           { return e.backend }

// ── Ingester interface implementation ─────────────────────────────────────────

// UploadDocument ingests a file into the knowledge base. It is a convenience
// wrapper around UploadDocumentWithProgress with no progress callback.
func (e *Engine) UploadDocument(filePath string, tags ...string) (*knowledge.DocumentMeta, error) {
	meta, err := e.uploadDocumentWithProgress(filePath, nil, tags...)
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

// UploadDocumentWithProgress ingests a file with progress reporting.
func (e *Engine) UploadDocumentWithProgress(filePath string, tags ...string) (*knowledge.DocumentMeta, error) {
	meta, err := e.uploadDocumentWithProgress(filePath, nil, tags...)
	if err != nil {
		return nil, err
	}
	return &meta, nil
}

// uploadDocumentWithProgress is the shared implementation for UploadDocument
// and UploadDocumentWithProgress. It mirrors Store.UploadDocumentWithProgress
// using injected dependencies.
func (e *Engine) uploadDocumentWithProgress(path string, progress knowledge.ProgressFunc, tags ...string) (knowledge.DocumentMeta, error) {
	if e.mu != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
	}
	if e.backend == nil {
		return knowledge.DocumentMeta{}, fmt.Errorf("ingest: backend not injected — call SetBackend or use New()")
	}

	log := e.logger
	if log != nil {
		log = log.WithModule("upload")
		log.Infof("UploadDocument path=%q kb=%q", path, e.kbName)
	}
	start := time.Now()

	emit := func(stage, status, detail string) {
		if progress != nil {
			progress(knowledge.ProgressEvent{Stage: stage, Status: status, Detail: detail})
		}
	}

	// Step 1: parse.
	emit(knowledge.StageParsing, "started", "")
	text, err := knowledge.ParseFile(path)
	if err != nil {
		if log != nil {
			log.Errorf("UploadDocument %q: parse failed: %v", path, err)
		}
		emit(knowledge.StageParsing, "error", err.Error())
		return knowledge.DocumentMeta{}, fmt.Errorf("upload: parse: %w", err)
	}
	emit(knowledge.StageParsing, "done", fmt.Sprintf("%d 字符", len(text)))

	// Step 2: hierarchical chunking.
	emit(knowledge.StageChunking, "started", "")
	fineChunks, coarseChunks := knowledge.ChunkTextHierarchical(text)
	if len(fineChunks) == 0 {
		emit(knowledge.StageChunking, "error", "文档未产生任何分块")
		return knowledge.DocumentMeta{}, fmt.Errorf("upload: document produced no chunks (empty after parsing)")
	}
	if log != nil {
		log.Debugf("chunked %d chars → %d fine + %d coarse chunks", len(text), len(fineChunks), len(coarseChunks))
	}
	emit(knowledge.StageChunking, "done", fmt.Sprintf("%d 个段落分块", len(fineChunks)))

	// Step 2a: optional semantic merging.
	if e.embedder != nil {
		emit(knowledge.StageMerging, "started", "")
		var restoreEmbed func()
		if e.gpuScheduler != nil {
			restoreEmbed = e.gpuScheduler.PrepareForEmbedding()
		}
		// Use default semantic threshold from the knowledge package.
		if merged, mergeErr := knowledge.MergeSemanticNeighbors(context.Background(), fineChunks, e.embedder, 0.7); mergeErr == nil && len(merged) > 0 {
			fineChunks = merged
			_, coarseChunks = knowledge.ChunkTextHierarchical(text)
		}
		if restoreEmbed != nil {
			restoreEmbed()
		}
		emit(knowledge.StageMerging, "done", fmt.Sprintf("%d 个分块（合并后）", len(fineChunks)))
	}

	chunks := make([]string, len(fineChunks))
	for i, c := range fineChunks {
		chunks[i] = c.Content
	}

	// Step 3: derive slug and metadata.
	emit(knowledge.StageMetadata, "started", "")
	slug := knowledge.SlugFromPath(path)
	sourceType := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")

	meta := knowledge.DocumentMeta{
		Slug:         slug,
		OriginalName: filepath.Base(path),
		SourceType:   sourceType,
		AddedAt:      time.Now().Truncate(time.Second),
		ChunkCount:   len(chunks),
		TotalChars:   len(text),
		Tags:         tags,
	}

	// Attempt paper metadata extraction (ExtractPaperMeta handles the check internally).
	title, authors, abstract := knowledge.ExtractPaperMeta(text)
	if title != "" || len(authors) > 0 || abstract != "" {
		meta.Title = title
		meta.Authors = authors
		meta.Abstract = abstract
		meta.IsPaper = true
	}
	emit(knowledge.StageMetadata, "done", slug)

	// Step 4: persist chunks, section chunks, and metadata.
	emit(knowledge.StageWriting, "started", "")
	if err := e.writeChunks(slug, chunks); err != nil {
		emit(knowledge.StageWriting, "error", err.Error())
		return knowledge.DocumentMeta{}, fmt.Errorf("upload: write chunks: %w", err)
	}
	if len(coarseChunks) > 0 {
		_ = e.writeSectionChunks(slug, coarseChunks) // non-fatal
	}
	if err := e.writeMeta(slug, &meta); err != nil {
		emit(knowledge.StageWriting, "error", err.Error())
		return knowledge.DocumentMeta{}, fmt.Errorf("upload: write meta: %w", err)
	}
	emit(knowledge.StageWriting, "done", fmt.Sprintf("%d 分块已写入", len(chunks)))

	// Step 5: write CHUNKS.toml search index with section chunk links.
	emit(knowledge.StageIndexing, "started", "")
	if e.buildChunksIndex != nil {
		if idxErr := e.buildChunksIndex(slug, fineChunks, coarseChunks); idxErr != nil && log != nil {
			log.Warnf("buildChunksIndex for %q: %v", slug, idxErr)
		}
	}

	// Step 6: optionally copy source file for traceability.
	_ = e.copySource(path, slug) // non-fatal

	// Step 7: write the full raw markdown text.
	_ = e.backend.WriteRawText(e.kbName, slug, text) // non-fatal

	// Step 8: update INDEX.md.
	_ = e.updateIndex(slug, meta) // non-fatal

	emit(knowledge.StageIndexing, "done", "搜索索引已建立")

	// Invalidate caches.
	e.invalidateDoc(slug)

	if log != nil {
		log.Infof("UploadDocument %q done: slug=%q chunks=%d chars=%d in %v", path, slug, meta.ChunkCount, meta.TotalChars, time.Since(start))
	}
	emit(knowledge.StageComplete, "done", slug)
	return meta, nil
}

// ── Internal helpers (mirroring Store's private methods) ──────────────────────

func (e *Engine) writeChunks(slug string, chunks []string) error {
	if err := e.backend.DeleteChunks(e.kbName, slug); err != nil {
		return fmt.Errorf("remove old chunks: %w", err)
	}
	for i, content := range chunks {
		chunkID := fmt.Sprintf("%03d", i)
		if err := e.backend.WriteChunk(e.kbName, slug, chunkID, content); err != nil {
			return fmt.Errorf("write chunk %s: %w", chunkID, err)
		}
	}
	return nil
}

func (e *Engine) writeSectionChunks(slug string, sections []knowledge.ChunkWithMeta) error {
	if err := e.backend.DeleteSectionChunks(e.kbName, slug); err != nil {
		return fmt.Errorf("delete old section chunks: %w", err)
	}
	for i, sec := range sections {
		id := fmt.Sprintf("S%02d", i)
		if err := e.backend.WriteSectionChunk(e.kbName, slug, id, sec.Content); err != nil {
			return fmt.Errorf("write section chunk %s: %w", id, err)
		}
	}
	return nil
}

func (e *Engine) writeMeta(slug string, meta *knowledge.DocumentMeta) error {
	if e.chunkStore != nil {
		return e.chunkStore.WriteMeta(slug, meta)
	}
	// Fallback: direct backend write.
	return e.backend.WriteMeta(e.kbName, slug, meta)
}

func (e *Engine) copySource(srcPath, slug string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	ext := filepath.Ext(srcPath)
	return e.backend.WriteSource(e.kbName, slug, data, ext)
}

func (e *Engine) updateIndex(slug string, meta knowledge.DocumentMeta) error {
	var existing string
	if e.chunkStore != nil {
		existing, _ = e.chunkStore.ReadIndex()
	} else {
		existing, _ = e.backend.ReadIndex(e.kbName)
	}
	line := fmt.Sprintf("- [%s](%s/meta.json) — %d chunks, %s\n",
		meta.OriginalName, slug, meta.ChunkCount, meta.AddedAt.Format(time.RFC3339))
	if e.chunkStore != nil {
		return e.chunkStore.WriteIndex(existing + line)
	}
	return e.backend.WriteIndex(e.kbName, existing+line)
}

func (e *Engine) invalidateDoc(docSlug string) {
	if e.cacheClient == nil {
		return
	}

	// We need the cache key patterns. These use package-level functions from the cache package.
	// For now, use the backend to access the raw cache operations.
	// The patterns are: "chunk:<kb>:<slug>:*" and "query:<kb>:*"
	prefix := e.kbName
	if prefix == "" {
		prefix = "default"
	}

	// Chunk-level pattern: chunk:<kb>:<slug>:*
	chunkPattern := fmt.Sprintf("chunk:%s:%s:*", prefix, docSlug)
	if _, err := e.cacheClient.DeletePattern(context.Background(), chunkPattern); err != nil {
		if e.logger != nil {
			e.logger.WithModule("cache").Warnf("invalidate doc %q FAILED: pattern=%q err=%v", docSlug, chunkPattern, err)
		}
	}

	// Query-level pattern: query:<kb>:*
	queryPattern := fmt.Sprintf("query:%s:*", prefix)
	if _, err := e.cacheClient.DeletePattern(context.Background(), queryPattern); err != nil {
		if e.logger != nil {
			e.logger.WithModule("cache").Warnf("invalidate query cache for KB %q FAILED: pattern=%q err=%v", prefix, queryPattern, err)
		}
	}
}

// ── UploadDirectory ───────────────────────────────────────────────────────────

// UploadDirectory ingests all supported document files under dir. When recursive
// is true it walks subdirectories; otherwise it scans only the top level.
func (e *Engine) UploadDirectory(dirPath string, recursive bool) (string, error) {
	if e.backend == nil {
		return "", fmt.Errorf("ingest: backend not injected — call SetBackend or use New()")
	}
	if e.logger != nil {
		e.logger.WithModule("upload").Infof("UploadDirectory dir=%q recursive=%v kb=%q", dirPath, recursive, e.kbName)
	}

	info, err := os.Stat(dirPath)
	if err != nil {
		return "", fmt.Errorf("access directory %q: %w", dirPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", dirPath)
	}

	type result struct {
		path string
		ext  string
		err  error
	}
	var results []result

	walkFn := func(path string, d os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".md", ".txt", ".pdf", ".docx", ".odt", ".epub", ".html", ".xlsx", ".pptx":
		default:
			return nil
		}
		_, uploadErr := e.UploadDocument(path)
		results = append(results, result{path: path, ext: ext, err: uploadErr})
		return nil
	}

	if recursive {
		err = filepath.Walk(dirPath, walkFn)
	} else {
		err = filepath.Walk(dirPath, func(path string, d os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && path != dirPath {
				return filepath.SkipDir
			}
			return walkFn(path, d, err)
		})
	}
	if err != nil {
		return "", fmt.Errorf("walk directory %q: %w", dirPath, err)
	}

	if len(results) == 0 {
		return "No supported documents found.", nil
	}

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

// ── CopySource ────────────────────────────────────────────────────────────────

// CopySource stores the original source file via the backend.
func (e *Engine) CopySource(srcPath, docSlug string) error {
	if e.backend == nil {
		return fmt.Errorf("ingest: backend not injected")
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	ext := filepath.Ext(srcPath)
	return e.backend.WriteSource(e.kbName, docSlug, data, ext)
}

// Ensure knowledge.Ingester interface is satisfied.
var _ knowledge.Ingester = (*Engine)(nil)
