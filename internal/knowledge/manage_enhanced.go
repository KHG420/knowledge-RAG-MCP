package knowledge

import (
	"archive/zip"
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
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
		"enabled":           s.gpuScheduler.Enabled(),
		"summary":           s.gpuScheduler.Summary(),
		"embeddingSleepURL": s.gpuScheduler.embeddingSleepURL,
		"rerankerSleepURL":  s.gpuScheduler.rerankerSleepURL,
		"docParserSleepURL": s.gpuScheduler.docParserSleepURL,
		"timeout":           s.gpuScheduler.timeout.String(),
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
		"lines": lines,
		"tail":  tail,
		"level": levelFilter,
		"count": len(lines),
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
		"uptimeSeconds":      int64(time.Since(metricsStartTime).Seconds()),
		"totalRequests":      metricsRequests.Load(),
		"totalSearches":      metricsSearches.Load(),
		"totalUploads":       metricsUploads.Load(),
		"totalDeletes":       metricsDeletes.Load(),
		"memoryAllocMB":      float64(m.Alloc) / 1024 / 1024,
		"memoryTotalAllocMB": float64(m.TotalAlloc) / 1024 / 1024,
		"goroutines":         runtime.NumGoroutine(),
		"numCPU":             runtime.NumCPU(),
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
		"slug":       slug,
		"chunkCount": len(chunks),
		"chunks":     chunks,
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

	// Try to read the raw text via backend.
	if rawText, err := s.backend.ReadRawText(s.kbName, slug); err == nil && rawText != "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.txt"`, meta.OriginalName))
		w.Write([]byte(rawText))
		return
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
}

// ── Search console ──────────────────────────────────────────────────────────

// handleSearchConsole performs a search and returns raw scores for debugging.
func (s *Store) handleSearchConsole(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	var body struct {
		Query  string `json:"query"`
		Mode   string `json:"mode"` // bm25, vector, hybrid (overrides runtime setting)
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

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, name))

	mb, ok := s.backend.(*MySQLBackend)
	if !ok {
		writeManageError(w, http.StatusInternalServerError, "export requires MySQL backend")
		return
	}
	s.exportMySQLKB(mb, name, w, log)
}

// exportMySQLKB exports a MySQL-backed KB by building a zip from database records.
func (s *Store) exportMySQLKB(mb *MySQLBackend, name string, w http.ResponseWriter, log *logging.Logger) {
	// Verify KB exists.
	slugs, err := mb.ListDocSlugs(name)
	if err != nil {
		log.Errorf("KBExport MySQL: list docs for %q: %v", name, err)
		writeManageError(w, http.StatusInternalServerError, "failed to list documents")
		return
	}

	zw := zip.NewWriter(w)
	defer zw.Close()

	// ── kb.json ──
	desc, _ := mb.ReadKBDescription(name)
	kbMeta := map[string]string{"name": name, "description": desc}
	if data, err := json.MarshalIndent(kbMeta, "", "  "); err == nil {
		writeZipEntry(zw, "kb.json", data)
	}

	// ── INDEX.md ──
	if indexContent, err := mb.ReadIndex(name); err == nil && indexContent != "" {
		writeZipEntry(zw, "INDEX.md", []byte(indexContent))
	}

	// ── INVERTED.gob ──
	if invIdx, err := mb.ReadInvertedIndex(name); err == nil && invIdx != nil {
		var buf bytes.Buffer
		if gob.NewEncoder(&buf).Encode(invIdx) == nil {
			writeZipEntry(zw, "INVERTED.gob", buf.Bytes())
		}
	}

	// ── LIST_SNAPSHOT.json ──
	if cs, docs, err := mb.ReadSnapshot(name); err == nil && len(docs) > 0 {
		snapshot := map[string]any{
			"checksum":  cs,
			"documents": docs,
		}
		if data, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
			writeZipEntry(zw, "LIST_SNAPSHOT.json", data)
		}
	}

	// ── Per-document entries ──
	for _, slug := range slugs {
		s.exportMySQLDoc(mb, name, slug, zw, log)
	}
}

// exportMySQLDoc writes all files for a single document into the zip.
func (s *Store) exportMySQLDoc(mb *MySQLBackend, kbName, slug string, zw *zip.Writer, log *logging.Logger) {
	// meta.json
	if meta, err := mb.ReadMeta(kbName, slug); err == nil {
		if data, err := json.MarshalIndent(meta, "", "  "); err == nil {
			writeZipEntry(zw, slug+"/meta.json", data)
		}
	}

	// chunks/{id}.md
	if chunkIDs, err := mb.ListChunkIDs(kbName, slug); err == nil {
		for _, cid := range chunkIDs {
			if content, err := mb.ReadChunk(kbName, slug, cid); err == nil {
				writeZipEntry(zw, slug+"/chunks/"+cid+".md", []byte(content))
			}
		}
	}

	// chunks/sections/{id}.md
	if sectionIDs, err := mb.ListSectionChunkIDs(kbName, slug); err == nil {
		for _, sid := range sectionIDs {
			if content, err := mb.ReadSectionChunk(kbName, slug, sid); err == nil {
				writeZipEntry(zw, slug+"/chunks/sections/"+sid+".md", []byte(content))
			}
		}
	}

	// CHUNKS.toml
	if index, err := mb.ReadChunksIndex(kbName, slug); err == nil && index != nil {
		var buf bytes.Buffer
		if toml.NewEncoder(&buf).Encode(index) == nil {
			writeZipEntry(zw, slug+"/CHUNKS.toml", buf.Bytes())
		}
	}

	// MANIFEST.json
	if manifest, err := mb.ReadManifest(kbName, slug); err == nil && manifest != nil {
		if data, err := json.MarshalIndent(manifest, "", "  "); err == nil {
			writeZipEntry(zw, slug+"/MANIFEST.json", data)
		}
	}

	// TASK.json
	if task, err := mb.ReadTaskRecord(kbName, slug); err == nil && task != nil {
		if data, err := json.MarshalIndent(task, "", "  "); err == nil {
			writeZipEntry(zw, slug+"/TASK.json", data)
		}
	}

	// document.md (raw text)
	if rawText, err := mb.ReadRawText(kbName, slug); err == nil && rawText != "" {
		writeZipEntry(zw, slug+"/document.md", []byte(rawText))
	}

	// source{ext}
	if srcData, ext, err := mb.ReadSource(kbName, slug); err == nil && len(srcData) > 0 {
		srcName := "source" + ext
		if ext == "" {
			srcName = "source"
		}
		writeZipEntry(zw, slug+"/"+srcName, srcData)
	}
}

// writeZipEntry is a helper that adds a single file entry to a zip writer.
func writeZipEntry(zw *zip.Writer, name string, data []byte) {
	f, err := zw.Create(name)
	if err != nil {
		return
	}
	f.Write(data)
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

	mb, ok := s.backend.(*MySQLBackend)
	if !ok {
		writeManageError(w, http.StatusInternalServerError, "import requires MySQL backend")
		return
	}
	s.importMySQLKB(mb, name, zr, w, log)
}

// importMySQLKB imports a zip into a MySQL-backed KB.
func (s *Store) importMySQLKB(mb *MySQLBackend, name string, zr *zip.Reader, w http.ResponseWriter, log *logging.Logger) {
	// Collect files by path.
	files := make(map[string]*zip.File)
	slugs := make(map[string]bool)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		files[f.Name] = f

		// Detect document slugs (top-level directories containing a meta.json).
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) == 2 {
			slug := parts[0]
			if strings.HasSuffix(f.Name, "/meta.json") {
				slugs[slug] = true
			}
		}
	}

	// ── Create KB ──
	desc := ""
	if f, ok := files["kb.json"]; ok {
		if data := readZipFile(f); data != nil {
			var kbMeta struct {
				Description string `json:"description"`
			}
			if json.Unmarshal(data, &kbMeta) == nil {
				desc = kbMeta.Description
			}
		}
	}
	if err := mb.CreateKB(name, desc); err != nil {
		log.Errorf("KBImport MySQL: create KB %q: %v", name, err)
	}

	// ── INDEX.md ──
	if f, ok := files["INDEX.md"]; ok {
		if data := readZipFile(f); data != nil {
			mb.WriteIndex(name, string(data))
		}
	}

	// ── INVERTED.gob ──
	if f, ok := files["INVERTED.gob"]; ok {
		if data := readZipFile(f); data != nil {
			var idx InvertedIndex
			if gob.NewDecoder(bytes.NewReader(data)).Decode(&idx) == nil {
				mb.WriteInvertedIndex(name, &idx)
			}
		}
	}

	// ── LIST_SNAPSHOT.json ──
	if f, ok := files["LIST_SNAPSHOT.json"]; ok {
		if data := readZipFile(f); data != nil {
			var snapshot struct {
				Documents []DocumentMeta `json:"documents"`
			}
			if json.Unmarshal(data, &snapshot) == nil && len(snapshot.Documents) > 0 {
				mb.WriteSnapshot(name, snapshot.Documents)
			}
		}
	}

	// ── Per-document imports ──
	count := 0
	for slug := range slugs {
		count += s.importMySQLDoc(mb, name, slug, files, log)
	}

	log.Infof("KBImport MySQL: name=%q docs=%d", name, count)

	writeManageJSON(w, http.StatusOK, map[string]any{
		"message":   "knowledge base imported",
		"name":      name,
		"documents": count,
	})
}

// importMySQLDoc imports all files for a single document from the zip into MySQL.
func (s *Store) importMySQLDoc(mb *MySQLBackend, kbName, slug string, files map[string]*zip.File, log *logging.Logger) int {
	prefix := slug + "/"

	// meta.json — required, use as signal the doc is complete.
	metaData := readZipFile(files[prefix+"meta.json"])
	if metaData == nil {
		return 0
	}
	var meta DocumentMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		log.Warnf("KBImport: skip doc %q: bad meta.json: %v", slug, err)
		return 0
	}
	meta.Slug = slug
	if err := mb.WriteMeta(kbName, slug, &meta); err != nil {
		log.Warnf("KBImport: write meta %q: %v", slug, err)
	}

	// chunks/{id}.md
	for path, f := range files {
		if !strings.HasPrefix(path, prefix+"chunks/") || strings.Contains(path, "/sections/") {
			continue
		}
		name := strings.TrimPrefix(path, prefix+"chunks/")
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		chunkID := strings.TrimSuffix(name, ".md")
		if data := readZipFile(f); data != nil {
			mb.WriteChunk(kbName, slug, chunkID, string(data))
		}
	}

	// chunks/sections/{id}.md
	for path, f := range files {
		if !strings.HasPrefix(path, prefix+"chunks/sections/") {
			continue
		}
		name := strings.TrimPrefix(path, prefix+"chunks/sections/")
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		sectionID := strings.TrimSuffix(name, ".md")
		if data := readZipFile(f); data != nil {
			mb.WriteSectionChunk(kbName, slug, sectionID, string(data))
		}
	}

	// CHUNKS.toml
	if data := readZipFile(files[prefix+"CHUNKS.toml"]); data != nil {
		var index ChunksIndex
		if _, err := toml.Decode(string(data), &index); err == nil {
			mb.WriteChunksIndex(kbName, slug, &index)
		}
	}

	// MANIFEST.json
	if data := readZipFile(files[prefix+"MANIFEST.json"]); data != nil {
		if manifest, err := UnmarshalChunkManifest(data); err == nil {
			mb.WriteManifest(kbName, slug, manifest)
		}
	}

	// TASK.json
	if data := readZipFile(files[prefix+"TASK.json"]); data != nil {
		if task, err := UnmarshalTaskRecord(data); err == nil {
			mb.WriteTaskRecord(kbName, slug, task)
		}
	}

	// document.md
	if data := readZipFile(files[prefix+"document.md"]); data != nil {
		mb.WriteRawText(kbName, slug, string(data))
	}

	// source{ext}
	for path, f := range files {
		if !strings.HasPrefix(path, prefix+"source") {
			continue
		}
		name := strings.TrimPrefix(path, prefix)
		if strings.HasPrefix(name, "source") && name != "source" {
			ext := strings.TrimPrefix(name, "source")
			if data := readZipFile(f); data != nil {
				mb.WriteSource(kbName, slug, data, ext)
			}
			break
		}
	}
	// source (no extension)
	if data := readZipFile(files[prefix+"source"]); data != nil {
		mb.WriteSource(kbName, slug, data, "")
	}

	return 1
}

// readZipFile reads the full content of a zip file entry, or returns nil.
func readZipFile(f *zip.File) []byte {
	if f == nil {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil
	}
	return data
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
			"allocMB":      float64(m.Alloc) / 1024 / 1024,
			"totalAllocMB": float64(m.TotalAlloc) / 1024 / 1024,
			"sysMB":        float64(m.Sys) / 1024 / 1024,
			"numGC":        m.NumGC,
			"heapObjects":  m.HeapObjects,
		},
		"uptimeSeconds": int64(time.Since(metricsStartTime).Seconds()),
	})
}
