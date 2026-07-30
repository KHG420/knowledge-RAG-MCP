package knowledge

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"knowledge-mcp/internal/logging"
)

// ── Health endpoint ─────────────────────────────────────────────────────────

// handleHealth returns a simple health check response with uptime and version info.
func (s *Store) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeManageJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": "1.0.0",
	})
}

// ── GPU Scheduler status ────────────────────────────────────────────────────

// handleGPUSchedulerStatus returns the current GPU scheduler state.
func (s *Store) handleGPUSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	if s.gpuScheduler == nil {
		writeManageJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "GPU scheduler not configured",
		})
		return
	}

	writeManageJSON(w, http.StatusOK, map[string]any{
		"enabled":        s.gpuScheduler.Enabled(),
		"summary":        s.gpuScheduler.Summary(),
		"embeddingSleepURL": s.gpuScheduler.embeddingSleepURL,
		"rerankerSleepURL":  s.gpuScheduler.rerankerSleepURL,
		"docParserSleepURL": s.gpuScheduler.docParserSleepURL,
		"timeout":        s.gpuScheduler.timeout.String(),
	})
}

// ── Logs API ─────────────────────────────────────────────────────────────────

// handleLogs returns recent log entries from the log file.
// Query params: tail=N (default 100), level=error|warn|info|debug (filter).
func (s *Store) handleLogs(w http.ResponseWriter, r *http.Request) {
	tail := parseIntParam(r, "tail", 100)
	if tail > 1000 {
		tail = 1000
	}
	if tail < 1 {
		tail = 100
	}
	levelFilter := strings.ToLower(r.URL.Query().Get("level"))

	// Read from logger's file if it has one.
	lines, err := readLogTail(s.logger, tail, levelFilter)
	if err != nil {
		writeManageError(w, http.StatusInternalServerError, fmt.Sprintf("failed to read logs: %v", err))
		return
	}

	writeManageJSON(w, http.StatusOK, map[string]any{
		"lines":  lines,
		"tail":   tail,
		"level":  levelFilter,
		"count":  len(lines),
	})
}

// readLogTail reads the last N lines from the logger's file.
func readLogTail(log *logging.Logger, tail int, levelFilter string) ([]string, error) {
	// Logger writes to a file. Read the last N lines.
	logPath := log.Path()
	if logPath == "" {
		return []string{"(log file not configured — logs are written to stderr only)"}, nil
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}

	allLines := strings.Split(string(data), "\n")
	// Filter empty trailing line
	if len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}

	start := 0
	if len(allLines) > tail {
		start = len(allLines) - tail
	}

	var result []string
	for i := start; i < len(allLines); i++ {
		line := allLines[i]
		if levelFilter != "" && !strings.Contains(strings.ToLower(line), "["+levelFilter+"]") {
			continue
		}
		result = append(result, line)
	}

	return result, nil
}

// ── Metrics endpoint ─────────────────────────────────────────────────────────

// metrics counters (atomic for lock-free reads)
var (
	metricsRequests  atomic.Int64
	metricsSearches  atomic.Int64
	metricsUploads   atomic.Int64
	metricsDeletes   atomic.Int64
	metricsStartTime = time.Now()
)

// IncrementSearchCounter increments the search counter.
func IncrementSearchCounter() { metricsSearches.Add(1) }

// IncrementUploadCounter increments the upload counter.
func IncrementUploadCounter() { metricsUploads.Add(1) }

// IncrementDeleteCounter increments the delete counter.
func IncrementDeleteCounter() { metricsDeletes.Add(1) }

// handleMetrics returns Prometheus-compatible metrics or a JSON summary.
func (s *Store) handleMetrics(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")

	if format == "prometheus" {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		uptime := time.Since(metricsStartTime).Seconds()
		fmt.Fprintf(w, "# HELP knowledge_mcp_uptime_seconds Server uptime in seconds\n")
		fmt.Fprintf(w, "# TYPE knowledge_mcp_uptime_seconds gauge\n")
		fmt.Fprintf(w, "knowledge_mcp_uptime_seconds %d\n", int64(uptime))
		fmt.Fprintf(w, "# HELP knowledge_mcp_requests_total Total API requests\n")
		fmt.Fprintf(w, "# TYPE knowledge_mcp_requests_total counter\n")
		fmt.Fprintf(w, "knowledge_mcp_requests_total %d\n", metricsRequests.Load())
		fmt.Fprintf(w, "# HELP knowledge_mcp_searches_total Total search requests\n")
		fmt.Fprintf(w, "# TYPE knowledge_mcp_searches_total counter\n")
		fmt.Fprintf(w, "knowledge_mcp_searches_total %d\n", metricsSearches.Load())
		fmt.Fprintf(w, "# HELP knowledge_mcp_uploads_total Total upload requests\n")
		fmt.Fprintf(w, "# TYPE knowledge_mcp_uploads_total counter\n")
		fmt.Fprintf(w, "knowledge_mcp_uploads_total %d\n", metricsUploads.Load())
		fmt.Fprintf(w, "# HELP knowledge_mcp_deletes_total Total delete requests\n")
		fmt.Fprintf(w, "# TYPE knowledge_mcp_deletes_total counter\n")
		fmt.Fprintf(w, "knowledge_mcp_deletes_total %d\n", metricsDeletes.Load())

		// Memory stats
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Fprintf(w, "# HELP knowledge_mcp_memory_alloc_bytes Allocated memory in bytes\n")
		fmt.Fprintf(w, "# TYPE knowledge_mcp_memory_alloc_bytes gauge\n")
		fmt.Fprintf(w, "knowledge_mcp_memory_alloc_bytes %d\n", m.Alloc)
		fmt.Fprintf(w, "# HELP knowledge_mcp_goroutines Number of goroutines\n")
		fmt.Fprintf(w, "# TYPE knowledge_mcp_goroutines gauge\n")
		fmt.Fprintf(w, "knowledge_mcp_goroutines %d\n", runtime.NumGoroutine())
		return
	}

	// JSON format (default)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	writeManageJSON(w, http.StatusOK, map[string]any{
		"uptimeSeconds":    int64(time.Since(metricsStartTime).Seconds()),
		"totalRequests":    metricsRequests.Load(),
		"totalSearches":    metricsSearches.Load(),
		"totalUploads":     metricsUploads.Load(),
		"totalDeletes":     metricsDeletes.Load(),
		"memoryAllocMB":    float64(m.Alloc) / 1024 / 1024,
		"memoryTotalAllocMB": float64(m.TotalAlloc) / 1024 / 1024,
		"goroutines":       runtime.NumGoroutine(),
		"numCPU":           runtime.NumCPU(),
	})
}

// ── Batch document operations ───────────────────────────────────────────────

// handleBatchDelete deletes multiple documents at once.
func (s *Store) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	var body struct {
		Slugs     []string `json:"slugs"`
		Tombstone bool     `json:"tombstone"`
		Reason    string   `json:"reason,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(body.Slugs) == 0 {
		writeManageError(w, http.StatusBadRequest, "slugs is required")
		return
	}
	if len(body.Slugs) > 100 {
		writeManageError(w, http.StatusBadRequest, "max 100 slugs per batch")
		return
	}

	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}

	deleted := 0
	failed := 0
	var results []map[string]any
	for _, slug := range body.Slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if err := validateComponent(slug); err != nil {
			results = append(results, map[string]any{"slug": slug, "error": err.Error()})
			failed++
			continue
		}
		var err error
		if body.Tombstone {
			err = s.RemoveDocumentTombstone(slug, int64(7*24*time.Hour/time.Second), body.Reason)
		} else {
			err = s.RemoveDocument(slug)
		}
		if err != nil {
			results = append(results, map[string]any{"slug": slug, "error": err.Error()})
			failed++
		} else {
			results = append(results, map[string]any{"slug": slug, "status": "deleted"})
			deleted++
		}
	}

	log.Infof("BatchDelete: kb=%q deleted=%d failed=%d", s.kbName, deleted, failed)
	writeManageJSON(w, http.StatusOK, map[string]any{
		"message": fmt.Sprintf("deleted %d, failed %d", deleted, failed),
		"deleted": deleted,
		"failed":  failed,
		"results": results,
	})
}

// ── Tag management ──────────────────────────────────────────────────────────

// handleDocTagsUpdate updates tags for a document.
func (s *Store) handleDocTagsUpdate(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	slug := r.PathValue("slug")
	if slug == "" {
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := validateComponent(slug); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}

	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	meta, err := s.ReadMeta(slug)
	if err != nil {
		writeManageError(w, http.StatusNotFound, "document not found: "+slug)
		return
	}

	meta.Tags = body.Tags
	if err := s.WriteMeta(slug, meta); err != nil {
		log.Errorf("TagsUpdate: slug=%q failed: %v", slug, err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Infof("TagsUpdate: slug=%q tags=%v", slug, body.Tags)
	writeManageJSON(w, http.StatusOK, map[string]any{
		"message": "tags updated",
		"slug":    slug,
		"tags":    body.Tags,
	})
}

// ── Document chunk preview ──────────────────────────────────────────────────

// handleDocChunks returns all chunk content for a document.
func (s *Store) handleDocChunks(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := validateComponent(slug); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}

	// Get chunk IDs
	ids, err := s.backend.ListChunkIDs(s.kbName, slug)
	if err != nil {
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type chunkPreview struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Index   int    `json:"index"`
	}

	var chunks []chunkPreview
	for _, id := range ids {
		content, err := s.backend.ReadChunk(s.kbName, slug, id)
		if err != nil {
			continue
		}
		// Extract index number from chunk ID (e.g., "003" -> 3)
		idx := 0
		fmt.Sscanf(id, "%d", &idx)
		chunks = append(chunks, chunkPreview{
			ID:      id,
			Content: content,
			Index:   idx,
		})
	}

	writeManageJSON(w, http.StatusOK, map[string]any{
		"slug":        slug,
		"chunkCount":  len(chunks),
		"chunks":      chunks,
	})
}

// ── Document download ───────────────────────────────────────────────────────

// handleDocDownload returns the original file bytes for a document.
func (s *Store) handleDocDownload(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := validateComponent(slug); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}

	meta, err := s.ReadMeta(slug)
	if err != nil {
		writeManageError(w, http.StatusNotFound, "document not found")
		return
	}

	// Try to read the original file
	docDir := s.DocDir(slug)
	originalPath := filepath.Join(docDir, "original")
	if fb, ok := s.backend.(*FileBackend); ok {
		// For file backend, try to read the raw text or find original
		rawPath := filepath.Join(fb.kbDir(s.kbName), slug, "raw.txt")
		data, err := os.ReadFile(rawPath)
		if err == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.txt"`, meta.OriginalName))
			w.Write(data)
			return
		}
	}

	// Fallback: return chunk content as text
	ids, err := s.backend.ListChunkIDs(s.kbName, slug)
	if err != nil {
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var buf bytes.Buffer
	for _, id := range ids {
		content, err := s.backend.ReadChunk(s.kbName, slug, id)
		if err != nil {
			continue
		}
		buf.WriteString(content)
		buf.WriteString("\n\n---\n\n")
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.txt"`, meta.OriginalName))
	w.Write(buf.Bytes())
	_ = originalPath // used for file backend check above
}

// ── Search console ──────────────────────────────────────────────────────────

// handleSearchConsole performs a search and returns raw scores for debugging.
func (s *Store) handleSearchConsole(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	var body struct {
		Query  string `json:"query"`
		Mode   string `json:"mode"`   // bm25, vector, hybrid (overrides runtime setting)
		KBName string `json:"kbName"`
		Limit  int    `json:"limit"`
		Rerank bool   `json:"rerank"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Query == "" {
		writeManageError(w, http.StatusBadRequest, "query is required")
		return
	}
	if body.Limit <= 0 {
		body.Limit = 10
	}
	if body.Limit > 50 {
		body.Limit = 50
	}

	searchStore := s
	if body.KBName != "" {
		searchStore = s.WithKB(body.KBName)
	}

	start := time.Now()

	// Perform search — dispatch based on selected mode.
	var results []SearchHit
	var err error
	switch body.Mode {
	case "bm25":
		results, err = searchStore.SearchBM25(body.Query, body.Limit)
	case "vector":
		results, err = searchStore.SearchVector(body.Query, body.Limit)
	case "hybrid":
		results, err = searchStore.HybridSearch(body.Query, body.Limit)
	default:
		// Default: use Search() when rerank is requested, SearchBM25 otherwise.
		if body.Rerank {
			results, err = searchStore.Search(body.Query, body.Limit)
		} else {
			results, err = searchStore.SearchBM25(body.Query, body.Limit)
		}
	}
	if err != nil {
		log.Errorf("SearchConsole: query=%q failed: %v", body.Query, err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}

	latencyMs := time.Since(start).Milliseconds()

	type debugResult struct {
		Rank        int     `json:"rank"`
		DocSlug     string  `json:"docSlug"`
		ChunkID     string  `json:"chunkId"`
		Score       float64 `json:"score"`
		Snippet     string  `json:"snippet"`
		Section     string  `json:"section,omitempty"`
		SectionRole string  `json:"sectionRole,omitempty"`
		SectionHint string  `json:"sectionHint,omitempty"`
	}

	debugResults := make([]debugResult, len(results))
	for i, r := range results {
		debugResults[i] = debugResult{
			Rank:        i + 1,
			DocSlug:     r.Document.ID,
			ChunkID:     r.Location.ChunkID,
			Score:       r.Score,
			Snippet:     r.Content.Snippet,
			Section:     r.Location.Section,
			SectionRole: r.Content.SectionRole,
			SectionHint: r.SectionHint,
		}
	}

	writeManageJSON(w, http.StatusOK, map[string]any{
		"query":     body.Query,
		"mode":      body.Mode,
		"latencyMs": latencyMs,
		"totalHits": len(debugResults),
		"results":   debugResults,
	})
}

// ── Document replace / re-upload ────────────────────────────────────────────

// handleDocReplace replaces an existing document with a new file.
func (s *Store) handleDocReplace(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	slug := r.PathValue("slug")
	if slug == "" {
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := validateComponent(slug); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}

	// Verify document exists
	_, err := s.ReadMeta(slug)
	if err != nil {
		writeManageError(w, http.StatusNotFound, "document not found: "+slug)
		return
	}

	maxSize := int64(s.GetUploadMaxSizeMB()) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		log.Errorf("DocReplace: parse multipart form failed: %v", err)
		writeManageError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeManageError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	// Remove old document first
	if err := s.RemoveDocument(slug); err != nil {
		log.Errorf("DocReplace: remove old doc %q failed: %v", slug, err)
		writeManageError(w, http.StatusInternalServerError, "failed to remove old document: "+err.Error())
		return
	}

	// Upload new file
	meta, err := saveManageFile(s, file, header.Filename)
	if err != nil {
		log.Errorf("DocReplace: upload new file failed: %v", err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Infof("DocReplace: slug=%q old replaced by new slug=%q", slug, meta.Slug)
	writeManageJSON(w, http.StatusOK, map[string]any{
		"message": "document replaced",
		"oldSlug": slug,
		"newSlug": meta.Slug,
		"name":    meta.OriginalName,
	})
}

// ── Knowledge base export ───────────────────────────────────────────────────

// handleKBExport exports a knowledge base as a ZIP file.
func (s *Store) handleKBExport(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	name := r.PathValue("name")
	if name == "" {
		writeManageError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := validateComponent(name); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid name: "+err.Error())
		return
	}

	// For file backend, zip the KB directory.
	fb, ok := s.backend.(*FileBackend)
	if !ok {
		writeManageError(w, http.StatusBadRequest, "export only supported with file backend")
		return
	}

	kbPath := fb.kbDir(name)
	if _, err := os.Stat(kbPath); os.IsNotExist(err) {
		writeManageError(w, http.StatusNotFound, "knowledge base not found: "+name)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, name))

	zw := zip.NewWriter(w)
	defer zw.Close()

	err := filepath.Walk(kbPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(kbPath, path)
		f, err := zw.Create(rel)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	})

	if err != nil {
		log.Errorf("KBExport: %q failed: %v", name, err)
	}
}

// ── Knowledge base import ───────────────────────────────────────────────────

// handleKBImport imports a knowledge base from a ZIP file.
func (s *Store) handleKBImport(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")

	name := r.URL.Query().Get("name")
	if name == "" {
		writeManageError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := validateComponent(name); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid name: "+err.Error())
		return
	}

	fb, ok := s.backend.(*FileBackend)
	if !ok {
		writeManageError(w, http.StatusBadRequest, "import only supported with file backend")
		return
	}

	maxSize := int64(s.GetUploadMaxSizeMB()) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeManageError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid zip file: "+err.Error())
		return
	}

	kbPath := fb.kbDir(name)
	if err := os.MkdirAll(kbPath, 0o755); err != nil {
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}

	count := 0
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		targetPath := filepath.Join(kbPath, f.Name)
		// Path-traversal guard
		if !strings.HasPrefix(targetPath, kbPath) {
			rc.Close()
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0o755)
			rc.Close()
			continue
		}
		os.MkdirAll(filepath.Dir(targetPath), 0o755)
		dst, err := os.Create(targetPath)
		if err != nil {
			rc.Close()
			continue
		}
		io.Copy(dst, rc)
		dst.Close()
		rc.Close()
		count++
	}

	log.Infof("KBImport: name=%q files=%d", name, count)

	writeManageJSON(w, http.StatusOK, map[string]any{
		"message": "knowledge base imported",
		"name":    name,
		"files":   count,
	})
}

// ── System info ─────────────────────────────────────────────────────────────

// handleSystemInfo returns runtime system information.
func (s *Store) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	writeManageJSON(w, http.StatusOK, map[string]any{
		"goVersion":    runtime.Version(),
		"numCPU":       runtime.NumCPU(),
		"numGoroutine": runtime.NumGoroutine(),
		"memory": map[string]any{
			"allocMB":       float64(m.Alloc) / 1024 / 1024,
			"totalAllocMB":  float64(m.TotalAlloc) / 1024 / 1024,
			"sysMB":         float64(m.Sys) / 1024 / 1024,
			"numGC":         m.NumGC,
			"heapObjects":   m.HeapObjects,
		},
		"uptimeSeconds": int64(time.Since(metricsStartTime).Seconds()),
	})
}
