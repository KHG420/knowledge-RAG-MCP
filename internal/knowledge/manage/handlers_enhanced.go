// Package manage — enhanced management HTTP handlers migrated from
// manage_enhanced.go (originally part of package knowledge on Store).
package manage

import (
	"archive/zip"
	"bytes"
	"context"
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
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// ── Health endpoint ─────────────────────────────────────────────────────────

// handleHealth returns a simple health check response with uptime and version info.
func (srv *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	result := map[string]any{
		"status":  "ok",
		"version": "1.0.0",
	}

	// Report MySQL backend connectivity if available.
	if mb, ok := srv.Backend().(*knowledge.MySQLBackend); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := mb.Health(ctx); err != nil {
			result["mysql"] = map[string]any{"status": "unhealthy", "error": err.Error()}
			result["status"] = "degraded"
		} else {
			result["mysql"] = map[string]any{"status": "healthy"}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// ── GPU Scheduler status ────────────────────────────────────────────────────

// handleGPUSchedulerStatus returns the current GPU scheduler state.
func (srv *Server) handleGPUSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	if srv.GPUScheduler() == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "GPU scheduler not configured",
		})
		return
	}

	gs := srv.GPUScheduler()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": gs.Enabled(),
		"summary": gs.Summary(),
	})
}

// ── Logs API ─────────────────────────────────────────────────────────────────

// handleLogs returns recent log entries from the log file.
// Query params: tail=N (default 100), level=error|warn|info|debug (filter).
func (srv *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	tail := ParseIntParam(r, "tail", 100)
	if tail > 1000 {
		tail = 1000
	}
	if tail < 1 {
		tail = 100
	}
	levelFilter := strings.ToLower(r.URL.Query().Get("level"))

	// Read from logger's file if it has one.
	lines, err := readLogTail(srv.Logger(), tail, levelFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to read logs: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
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

// IncrementRequestCounter increments the request counter (called from middleware).
func IncrementRequestCounter() { metricsRequests.Add(1) }

// IncrementSearchCounter increments the search counter.
func IncrementSearchCounter() { metricsSearches.Add(1) }

// IncrementUploadCounter increments the upload counter.
func IncrementUploadCounter() { metricsUploads.Add(1) }

// IncrementDeleteCounter increments the delete counter.
func IncrementDeleteCounter() { metricsDeletes.Add(1) }

// handleMetrics returns Prometheus-compatible metrics or a JSON summary.
func (srv *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, map[string]any{
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
func (srv *Server) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	var body struct {
		Slugs     []string `json:"slugs"`
		Tombstone bool     `json:"tombstone"`
		Reason    string   `json:"reason,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(body.Slugs) == 0 {
		writeError(w, http.StatusBadRequest, "slugs is required")
		return
	}
	if len(body.Slugs) > 100 {
		writeError(w, http.StatusBadRequest, "max 100 slugs per batch")
		return
	}

	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	deleted := 0
	failed := 0
	var results []map[string]any
	for _, slug := range body.Slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if err := svc.ValidateComponent(slug); err != nil {
			results = append(results, map[string]any{"slug": slug, "error": err.Error()})
			failed++
			continue
		}
		var err error
		if body.Tombstone {
			err = svc.RemoveDocumentTombstone(slug, int64(7*24*time.Hour/time.Second), body.Reason)
		} else {
			err = svc.RemoveDocument(slug)
		}
		if err != nil {
			results = append(results, map[string]any{"slug": slug, "error": err.Error()})
			failed++
		} else {
			results = append(results, map[string]any{"slug": slug, "status": "deleted"})
			deleted++
		}
	}

	log.Infof("BatchDelete: kb=%q deleted=%d failed=%d", svc.KBName(), deleted, failed)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": fmt.Sprintf("deleted %d, failed %d", deleted, failed),
		"deleted": deleted,
		"failed":  failed,
		"results": results,
	})
}

// ── Tag management ──────────────────────────────────────────────────────────

// handleDocTagsUpdate updates tags for a document.
func (srv *Server) handleDocTagsUpdate(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := srv.Service().ValidateComponent(slug); err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	meta, err := svc.ReadMeta(slug)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found: "+slug)
		return
	}

	meta.Tags = body.Tags
	if err := svc.WriteMeta(slug, meta); err != nil {
		log.Errorf("TagsUpdate: slug=%q failed: %v", slug, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Infof("TagsUpdate: slug=%q tags=%v", slug, body.Tags)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "tags updated",
		"slug":    slug,
		"tags":    body.Tags,
	})
}

// ── Document chunk preview ──────────────────────────────────────────────────

// handleDocChunks returns all chunk content for a document.
func (srv *Server) handleDocChunks(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := srv.Service().ValidateComponent(slug); err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	// Get chunk IDs
	ids, err := srv.Backend().ListChunkIDs(svc.KBName(), slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type chunkPreview struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Index   int    `json:"index"`
	}

	var chunks []chunkPreview
	for _, id := range ids {
		content, err := srv.Backend().ReadChunk(svc.KBName(), slug, id)
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

	writeJSON(w, http.StatusOK, map[string]any{
		"slug":       slug,
		"chunkCount": len(chunks),
		"chunks":     chunks,
	})
}

// ── Document download ───────────────────────────────────────────────────────

// handleDocDownload returns the original file bytes for a document.
func (srv *Server) handleDocDownload(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := srv.Service().ValidateComponent(slug); err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	meta, err := svc.ReadMeta(slug)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}

	// Try to read the raw text via backend.
	if rawText, err := srv.Backend().ReadRawText(svc.KBName(), slug); err == nil && rawText != "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.txt"`, meta.OriginalName))
		w.Write([]byte(rawText))
		return
	}

	// Fallback: return chunk content as text
	ids, err := srv.Backend().ListChunkIDs(svc.KBName(), slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var buf bytes.Buffer
	for _, id := range ids {
		content, err := srv.Backend().ReadChunk(svc.KBName(), slug, id)
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

// ── Search console ───────────────────────────────────────────────────

// handleSearchConsole performs a search and returns raw scores for debugging.
func (srv *Server) handleSearchConsole(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	var body struct {
		Query  string `json:"query"`
		Mode   string `json:"mode"` // bm25, vector, hybrid (overrides runtime setting)
		KBName string `json:"kbName"`
		Limit  int    `json:"limit"`
		Rerank bool   `json:"rerank"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	if msg := ValidateSearchQuery(body.Query); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if body.Limit <= 0 {
		body.Limit = 10
	}
	if body.Limit > 50 {
		body.Limit = 50
	}

	searchSvc := srv.Service()
	if body.KBName != "" {
		searchSvc = searchSvc.WithKBService(body.KBName)
	}

	start := time.Now()

	// Perform search — dispatch based on selected mode.
	var results []knowledge.SearchHit
	var err error
	switch body.Mode {
	case "bm25":
		results, err = searchSvc.SearchBM25(body.Query, body.Limit)
	case "vector":
		results, err = searchSvc.SearchVector(body.Query, body.Limit)
	case "hybrid":
		results, err = searchSvc.HybridSearch(body.Query, body.Limit, knowledge.SearchFilter{})
	default:
		// Default: use Search() when rerank is requested, SearchBM25 otherwise.
		if body.Rerank {
			results, err = searchSvc.Search(body.Query, body.Limit, knowledge.SearchFilter{})
		} else {
			results, err = searchSvc.SearchBM25(body.Query, body.Limit)
		}
	}
	if err != nil {
		log.Errorf("SearchConsole: query=%q failed: %v", body.Query, err)
		writeError(w, http.StatusInternalServerError, err.Error())
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

	IncrementSearchCounter()
	writeJSON(w, http.StatusOK, map[string]any{
		"query":     body.Query,
		"mode":      body.Mode,
		"latencyMs": latencyMs,
		"totalHits": len(debugResults),
		"results":   debugResults,
	})
}

// ── Document replace / re-upload ────────────────────────────────────────────

// handleDocReplace replaces an existing document with a new file.
func (srv *Server) handleDocReplace(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := srv.Service().ValidateComponent(slug); err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	// Verify document exists
	_, err := svc.ReadMeta(slug)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found: "+slug)
		return
	}

	maxSize := int64(svc.GetUploadMaxSizeMB()) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		log.Errorf("DocReplace: parse multipart form failed: %v", err)
		writeError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	// Upload the new file BEFORE removing the old document. A failed
	// replacement must leave the original metadata, source, chunks and search
	// results intact; the old slug is only removed once the new content is
	// fully persisted.
	meta, err := saveFile(svc, file, header.Filename)
	if err != nil {
		// Best-effort cleanup of a partially created replacement. The
		// generated slug is removed only when it is available and distinct
		// from the original, so the old slug is never touched. A cleanup
		// failure is logged and still reported as a failed replacement.
		if meta.Slug != "" && meta.Slug != slug {
			if rmErr := svc.RemoveDocument(meta.Slug); rmErr != nil {
				log.Errorf("DocReplace: cleanup partial replacement slug=%q failed: %v", meta.Slug, rmErr)
			}
		} else if meta.Slug == slug {
			log.Errorf("DocReplace: upload failed after reusing slug=%q; original may be partial", slug)
		}
		log.Errorf("DocReplace: upload new file failed: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The new content is fully persisted. Remove the old document last: if
	// removal fails, keep the new (good) document and surface the error rather
	// than reporting a false success.
	if meta.Slug != slug {
		if err := svc.RemoveDocument(slug); err != nil {
			log.Errorf("DocReplace: new slug=%q persisted but removing old slug=%q failed: %v", meta.Slug, slug, err)
			writeError(w, http.StatusInternalServerError, "new document saved but failed to remove old document: "+err.Error())
			return
		}
	}

	log.Infof("DocReplace: slug=%q old replaced by new slug=%q", slug, meta.Slug)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "document replaced",
		"oldSlug": slug,
		"newSlug": meta.Slug,
		"name":    meta.OriginalName,
	})
}

// ── Knowledge base export ───────────────────────────────────────────────────

// handleKBExport exports a knowledge base as a ZIP file.
func (srv *Server) handleKBExport(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := srv.Service().ValidateComponent(name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid name: "+err.Error())
		return
	}

	// Verify the KB actually exists.
	kbs, err := srv.Service().ListKBsInfo()
	if err != nil {
		log.Errorf("KBExport: list KBs failed: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to list knowledge bases")
		return
	}
	found := false
	for _, kb := range kbs {
		if kb.Name == name {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "knowledge base not found: "+name)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, name))

	mb, ok := srv.Backend().(*knowledge.MySQLBackend)
	if !ok {
		writeError(w, http.StatusInternalServerError, "export requires MySQL backend")
		return
	}
	exportMySQLKB(mb, name, w, log)
}

// exportMySQLKB exports a MySQL-backed KB by building a zip from database records.
func exportMySQLKB(mb *knowledge.MySQLBackend, name string, w http.ResponseWriter, log *logging.Logger) {
	// Verify KB exists.
	slugs, err := mb.ListDocSlugs(name)
	if err != nil {
		log.Errorf("KBExport MySQL: list docs for %q: %v", name, err)
		writeError(w, http.StatusInternalServerError, "failed to list documents")
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
		exportMySQLDoc(mb, name, slug, zw, log)
	}
}

// exportMySQLDoc writes all files for a single document into the zip.
func exportMySQLDoc(mb *knowledge.MySQLBackend, kbName, slug string, zw *zip.Writer, log *logging.Logger) {
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
func (srv *Server) handleKBImport(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")

	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := srv.Service().ValidateComponent(name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid name: "+err.Error())
		return
	}

	svc := srv.Service()
	maxSize := int64(svc.GetUploadMaxSizeMB()) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid zip file: "+err.Error())
		return
	}

	mb, ok := srv.Backend().(*knowledge.MySQLBackend)
	if !ok {
		writeError(w, http.StatusInternalServerError, "import requires MySQL backend")
		return
	}
	importMySQLKB(mb, name, zr, w, log)
}

// importMySQLKB imports a zip into a MySQL-backed KB.
func importMySQLKB(mb *knowledge.MySQLBackend, name string, zr *zip.Reader, w http.ResponseWriter, log *logging.Logger) {
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
			var idx knowledge.InvertedIndex
			if gob.NewDecoder(bytes.NewReader(data)).Decode(&idx) == nil {
				mb.WriteInvertedIndex(name, &idx)
			}
		}
	}

	// ── LIST_SNAPSHOT.json ──
	if f, ok := files["LIST_SNAPSHOT.json"]; ok {
		if data := readZipFile(f); data != nil {
			var snapshot struct {
				Documents []knowledge.DocumentMeta `json:"documents"`
			}
			if json.Unmarshal(data, &snapshot) == nil && len(snapshot.Documents) > 0 {
				mb.WriteSnapshot(name, snapshot.Documents)
			}
		}
	}

	// ── Per-document imports ──
	count := 0
	for slug := range slugs {
		count += importMySQLDoc(mb, name, slug, files, log)
	}

	log.Infof("KBImport MySQL: name=%q docs=%d", name, count)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":   "knowledge base imported",
		"name":      name,
		"documents": count,
	})
}

// importMySQLDoc imports all files for a single document from the zip into MySQL.
func importMySQLDoc(mb *knowledge.MySQLBackend, kbName, slug string, files map[string]*zip.File, log *logging.Logger) int {
	prefix := slug + "/"

	// meta.json — required, use as signal the doc is complete.
	metaData := readZipFile(files[prefix+"meta.json"])
	if metaData == nil {
		return 0
	}
	var meta knowledge.DocumentMeta
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
		var index knowledge.ChunksIndex
		if _, err := toml.Decode(string(data), &index); err == nil {
			mb.WriteChunksIndex(kbName, slug, &index)
		}
	}

	// MANIFEST.json
	if data := readZipFile(files[prefix+"MANIFEST.json"]); data != nil {
		if manifest, err := knowledge.UnmarshalChunkManifest(data); err == nil {
			mb.WriteManifest(kbName, slug, manifest)
		}
	}

	// TASK.json
	if data := readZipFile(files[prefix+"TASK.json"]); data != nil {
		if task, err := knowledge.UnmarshalTaskRecord(data); err == nil {
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
func (srv *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	writeJSON(w, http.StatusOK, map[string]any{
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
