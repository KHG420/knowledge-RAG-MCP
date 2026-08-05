// Package manage — core management HTTP handlers migrated from
// manage.go (originally part of package knowledge on Store).
package manage

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"knowledge-mcp/internal/knowledge"
)

// ── Types ────────────────────────────────────────────────────────────────────

// manageDocItem is the JSON shape for a document in the management list API.
type manageDocItem struct {
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	SourceType   string   `json:"sourceType"`
	ChunkCount   int      `json:"chunkCount"`
	TotalChars   int      `json:"totalChars"`
	AddedAt      string   `json:"addedAt"`
	Title        string   `json:"title,omitempty"`
	Authors      []string `json:"authors,omitempty"`
	IsPaper      bool     `json:"isPaper"`
	Tags         []string `json:"tags"`
	HasVectors   bool     `json:"hasVectors"`
	VectorDim    int      `json:"vectorDim,omitempty"`
	IsTombstoned bool     `json:"isTombstoned"`
}

// ── Vector status helper ─────────────────────────────────────────────────────

// populateVectorStatus fills the HasVectors and VectorDim fields for each
// item by reading the lightweight header from chunks_index.
func populateVectorStatus(svc knowledge.ManageService, items []manageDocItem) {
	for i := range items {
		index, err := svc.ReadChunksIndex(items[i].Slug)
		if err != nil || index == nil {
			continue
		}
		items[i].HasVectors = index.HasVectors
		items[i].VectorDim = index.VectorDim
	}
}

// ── Document list ────────────────────────────────────────────────────────────

// handleManageList returns a paginated, filterable list of documents.
func (srv *Server) handleManageList(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	kb := r.URL.Query().Get("kb")
	log.Debugf("List: kb=%q", kb)

	svc := srv.Service()
	var docs []knowledge.DocumentMeta
	var err error
	if kb != "" {
		svc = svc.WithKBService(kb)
		docs, err = svc.ListDocuments()
	} else {
		docs, err = svc.ListDocumentsAll()
	}
	if err != nil {
		log.Errorf("List: kb=%q failed: %v", kb, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Parse query params
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	sourceType := strings.TrimSpace(r.URL.Query().Get("sourceType"))
	tagFilter := strings.TrimSpace(r.URL.Query().Get("tag"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	} else if limit > 500 {
		limit = 500
	}
	sortBy := r.URL.Query().Get("sortBy")
	if sortBy == "" {
		sortBy = "addedAt"
	}
	sortOrder := r.URL.Query().Get("sortOrder")
	if sortOrder == "" {
		sortOrder = "desc"
	}

	// Build & filter
	items := make([]manageDocItem, 0, len(docs))
	totalChunks := 0
	totalPapers := 0
	typeSet := make(map[string]struct{})
	for _, d := range docs {
		if sourceType != "" && d.SourceType != sourceType {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(d.OriginalName), strings.ToLower(search)) {
			continue
		}
		if tagFilter != "" {
			hasTag := false
			for _, t := range d.Tags {
				if strings.EqualFold(t, tagFilter) {
					hasTag = true
					break
				}
			}
			if !hasTag {
				continue
			}
		}
		items = append(items, manageDocItem{
			Slug:         d.Slug,
			Name:         d.OriginalName,
			SourceType:   d.SourceType,
			ChunkCount:   d.ChunkCount,
			TotalChars:   d.TotalChars,
			AddedAt:      d.AddedAt.Format(time.RFC3339),
			Title:        d.Title,
			Authors:      d.Authors,
			IsPaper:      d.IsPaper,
			Tags:         d.Tags,
			IsTombstoned: svc.IsTombstoned(d.Slug),
		})
		totalChunks += d.ChunkCount
		if d.IsPaper {
			totalPapers++
		}
		typeSet[d.SourceType] = struct{}{}
	}

	total := len(items)
	totalTypes := len(typeSet)

	// Sort
	sort.Slice(items, func(i, j int) bool {
		var less bool
		switch sortBy {
		case "name":
			less = items[i].Name < items[j].Name
		case "chunkCount":
			less = items[i].ChunkCount < items[j].ChunkCount
		case "sourceType":
			less = items[i].SourceType < items[j].SourceType
		default:
			less = items[i].AddedAt < items[j].AddedAt
		}
		if sortOrder == "desc" {
			return !less
		}
		return less
	})

	// Populate vector status from chunks_index (lightweight, batched).
	populateVectorStatus(svc, items)

	// Paginate
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	page := items[offset:end]

	writeJSON(w, http.StatusOK, map[string]any{
		"documents":   page,
		"total":       total,
		"offset":      offset,
		"limit":       limit,
		"totalChunks": totalChunks,
		"totalPapers": totalPapers,
		"totalTypes":  totalTypes,
	})
	log.Debugf("List: kb=%q returned %d/%d docs", kb, len(page), total)
}

// ── Upload ───────────────────────────────────────────────────────────────────

// handleManageUpload handles file uploads (single or multi-file via multipart).
func (srv *Server) handleManageUpload(w http.ResponseWriter, r *http.Request) {
	// SSE streaming mode for real-time upload progress.
	if r.URL.Query().Get("stream") == "true" {
		srv.handleManageUploadSSE(w, r)
		return
	}

	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		log.Errorf("Upload: parse multipart form failed: %v", err)
		writeError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		// Single file via `file` field (curl-friendly)
		file, header, err := r.FormFile("file")
		if err == nil {
			defer file.Close()
			log.Debugf("Upload: single file name=%q kb=%q", header.Filename, svc.KBName())
			meta, err := saveFile(svc, file, header.Filename)
			if err != nil {
				log.Errorf("Upload: single file %q failed: %v", header.Filename, err)
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			log.Infof("Upload: single file %q → slug=%q", header.Filename, meta.Slug)
			IncrementUploadCounter()
			writeJSON(w, http.StatusOK, map[string]any{
				"message": "uploaded",
				"slug":    meta.Slug,
				"name":    meta.OriginalName,
			})
			return
		}
		writeError(w, http.StatusBadRequest, "no files uploaded")
		return
	}

	log.Debugf("Upload: %d files kb=%q", len(files), svc.KBName())

	// NDJSON streaming: write each result as a JSON line as soon as the
	// file is processed, so the frontend can update the UI incrementally.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	type uploadLine struct {
		Name  string `json:"name"`
		Slug  string `json:"slug,omitempty"`
		Error string `json:"error,omitempty"`
		Done  bool   `json:"done,omitempty"`
	}

	successCount := 0
	for _, fh := range files {
		file, err := fh.Open()
		if err != nil {
			writeNDJSONLine(w, flusher, uploadLine{Name: fh.Filename, Error: err.Error()})
			continue
		}
		meta, err := saveFile(svc, file, fh.Filename)
		file.Close()
		if err != nil {
			writeNDJSONLine(w, flusher, uploadLine{Name: fh.Filename, Error: err.Error()})
		} else {
			successCount++
			writeNDJSONLine(w, flusher, uploadLine{Name: fh.Filename, Slug: meta.Slug})
		}
	}

	log.Infof("Upload: %d files, %d succeeded kb=%q", len(files), successCount, svc.KBName())

	// Terminal line signals end of stream.
	writeNDJSONLine(w, flusher, uploadLine{
		Done: true,
		Name: fmt.Sprintf("%d/%d 成功", successCount, len(files)),
	})
}

// ── Async upload (task-based) ────────────────────────────────────────────────

// handleManageUploadSSE handles file upload asynchronously.
// Instead of streaming progress inline, it creates tasks and returns
// immediately with task IDs. The frontend then subscribes to
// /api/tasks/{id}/events for real-time progress.
func (srv *Server) handleManageUploadSSE(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		log.Errorf("Upload: parse multipart form failed: %v", err)
		writeError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	// Collect files from both "files" (multiple) and "file" (single) fields.
	fileHeaders := r.MultipartForm.File["files"]
	if len(fileHeaders) == 0 {
		f, h, err := r.FormFile("file")
		if err == nil {
			f.Close()
			fileHeaders = []*multipart.FileHeader{h}
		}
	}
	if len(fileHeaders) == 0 {
		writeError(w, http.StatusBadRequest, "no files uploaded")
		return
	}

	log.Debugf("Upload: %d files kb=%q", len(fileHeaders), svc.KBName())
	type taskInfo struct {
		ID       string `json:"id"`
		FileName string `json:"fileName"`
	}
	tasks := make([]taskInfo, 0, len(fileHeaders))

	for _, fh := range fileHeaders {
		file, err := fh.Open()
		if err != nil {
			log.Errorf("Upload: open file %q failed: %v", fh.Filename, err)
			continue
		}

		// Save uploaded file to a temp directory (cleanup managed by task).
		tmpDir, err := os.MkdirTemp("", "knowledge-upload-*")
		if err != nil {
			log.Errorf("Upload: create tmp dir for %q failed: %v", fh.Filename, err)
			file.Close()
			continue
		}
		safeName := filepath.Base(fh.Filename)
		tmpPath := filepath.Join(tmpDir, safeName)
		dst, err := os.Create(tmpPath)
		if err != nil {
			log.Errorf("Upload: create tmp file %q failed: %v", tmpPath, err)
			os.RemoveAll(tmpDir)
			file.Close()
			continue
		}
		if _, err := io.Copy(dst, file); err != nil {
			log.Errorf("Upload: copy %q failed: %v", fh.Filename, err)
			dst.Close()
			os.RemoveAll(tmpDir)
			file.Close()
			continue
		}
		dst.Close()
		file.Close()

		// Create task and launch background processing.
		tm := srv.TaskManager()
		kbName := svc.KBName()
		task := tm.Create(fh.Filename, kbName, tmpDir)

		go func(t *knowledge.UploadTask, taskSvc knowledge.ManageService, path, taskKBName string) {
			defer func() {
				if r := recover(); r != nil {
					t.RecordEvent(knowledge.ProgressEvent{
						Stage: "error", Status: "error",
						Detail: fmt.Sprintf("panic: %v", r),
					})
					t.MarkError(fmt.Errorf("panic: %v", r))
				}
				// Clean up temp directory regardless of outcome.
				os.RemoveAll(path)
			}()

			t.RecordEvent(knowledge.ProgressEvent{Stage: "prepare", Status: "pending", Detail: "task queued"})

			// Apply KB scope for this task.
			scopedSvc := taskSvc
			if taskKBName != "" {
				scopedSvc = taskSvc.WithKBService(taskKBName)
			}

			meta, err := scopedSvc.UploadDocumentWithProgress(path, func(ev knowledge.ProgressEvent) {
				t.RecordEvent(ev)
			})
			if err != nil {
				t.RecordEvent(knowledge.ProgressEvent{Stage: "error", Status: "error", Detail: err.Error()})
				t.MarkError(err)
			} else {
				t.RecordEvent(knowledge.ProgressEvent{Stage: knowledge.StageComplete, Status: "done", Detail: meta.Slug})
				t.MarkDone(meta.Slug)
			}
		}(task, svc, tmpPath, kbName)

		tasks = append(tasks, taskInfo{ID: task.ID, FileName: fh.Filename})
	}

	if len(tasks) == 0 {
		writeError(w, http.StatusInternalServerError, "all files failed to submit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "tasks created",
		"tasks":   tasks,
	})
}

// ── Task status & SSE handlers ───────────────────────────────────────────────

// handleTaskStatus returns the current state of an async upload task.
func (srv *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	svc := srv.Service()
	if err := svc.ValidateComponent(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid task id: "+err.Error())
		return
	}
	task := srv.TaskManager().Get(id)
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	status, slug, errMsg := task.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        task.ID,
		"fileName":  task.FileName,
		"kbName":    task.KBName,
		"status":    status,
		"slug":      slug,
		"error":     errMsg,
		"createdAt": task.CreatedAt,
		"events":    task.Events(),
	})
}

// handleTaskEvents streams task progress events via SSE.
// On connect it replays all recorded events, then waits for new ones
// until the task completes or the client disconnects.
func (srv *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	svc := srv.Service()
	if err := svc.ValidateComponent(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid task id: "+err.Error())
		return
	}
	task := srv.TaskManager().Get(id)
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Replay all existing events.
	events := task.Events()
	for _, ev := range events {
		SendSSEEvent(w, flusher, "progress", map[string]any{
			"stage":  ev.Stage,
			"status": ev.Status,
			"detail": ev.Detail,
		})
	}

	// If already in a terminal state, send final event and return.
	status, _, _ := task.Snapshot()
	terminal := status == "done" || status == "error"
	if terminal {
		sendUploadTaskFinalEvent(w, flusher, task)
		return
	}

	// Poll for new events until task completes or client disconnects.
	lastCount := len(events)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-task.Done():
			// Send any remaining events then final event.
			allEvents := task.Events()
			for i := lastCount; i < len(allEvents); i++ {
				ev := allEvents[i]
				SendSSEEvent(w, flusher, "progress", map[string]any{
					"stage":  ev.Stage,
					"status": ev.Status,
					"detail": ev.Detail,
				})
			}
			sendUploadTaskFinalEvent(w, flusher, task)
			return
		case <-ticker.C:
			allEvents := task.Events()
			if len(allEvents) > lastCount {
				for i := lastCount; i < len(allEvents); i++ {
					ev := allEvents[i]
					SendSSEEvent(w, flusher, "progress", map[string]any{
						"stage":  ev.Stage,
						"status": ev.Status,
						"detail": ev.Detail,
					})
				}
				lastCount = len(allEvents)

				// Check if task just completed.
				status, _, _ = task.Snapshot()
				done := status == "done" || status == "error"
				if done {
					sendUploadTaskFinalEvent(w, flusher, task)
					return
				}
			}
		}
	}
}

// sendUploadTaskFinalEvent sends the terminal SSE event (complete or error)
// for an upload task.
func sendUploadTaskFinalEvent(w http.ResponseWriter, flusher http.Flusher, task *knowledge.UploadTask) {
	status, slug, errMsg := task.Snapshot()

	if status == "done" {
		SendSSEEvent(w, flusher, "complete", map[string]any{
			"slug": slug,
			"name": task.FileName,
		})
	} else {
		SendSSEEvent(w, flusher, "error", map[string]string{
			"error": errMsg,
		})
	}
}

// ── Delete ───────────────────────────────────────────────────────────────────

// handleManageDelete deletes a document (hard or soft-delete with tombstone).
func (srv *Server) handleManageDelete(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		log.Errorf("Delete: slug is empty")
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := svc.ValidateComponent(slug); err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug")
		return
	}

	// Support soft-delete with tombstone via ?tombstone=true
	if r.URL.Query().Get("tombstone") == "true" {
		ttlStr := r.URL.Query().Get("ttl")
		ttl := int64(0)
		if ttlStr != "" {
			if n, err := strconv.ParseInt(ttlStr, 10, 64); err == nil && n > 0 {
				ttl = n
			}
		}
		reason := r.URL.Query().Get("reason")
		log.Debugf("Delete(tombstone): slug=%q ttl=%d reason=%q kb=%q", slug, ttl, reason, svc.KBName())
		if err := svc.RemoveDocumentTombstone(slug, ttl, reason); err != nil {
			log.Errorf("Delete(tombstone): slug=%q failed: %v", slug, err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Infof("Delete(tombstone): slug=%q tombstoned", slug)
		IncrementDeleteCounter()
		writeJSON(w, http.StatusOK, map[string]string{"message": "tombstoned", "slug": slug})
		return
	}

	log.Debugf("Delete: slug=%q kb=%q", slug, svc.KBName())
	if err := svc.RemoveDocument(slug); err != nil {
		log.Errorf("Delete: slug=%q failed: %v", slug, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Infof("Delete: slug=%q deleted", slug)
	IncrementDeleteCounter()
	writeJSON(w, http.StatusOK, map[string]string{"message": "deleted", "slug": slug})
}

// ── Document detail ──────────────────────────────────────────────────────────

// handleManageDocDetail returns full document metadata.
func (srv *Server) handleManageDocDetail(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		log.Errorf("DocDetail: slug is empty")
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := svc.ValidateComponent(slug); err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug")
		return
	}
	log.Debugf("DocDetail: slug=%q kb=%q", slug, svc.KBName())

	meta, err := svc.ReadMeta(slug)
	if err != nil {
		log.Errorf("DocDetail: slug=%q failed: %v", slug, err)
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	meta.Slug = slug

	writeJSON(w, http.StatusOK, map[string]any{
		"meta": meta,
	})
}

// ── Search ───────────────────────────────────────────────────────────────────

// handleManageSearch performs a full-text search across the knowledge base.
func (srv *Server) handleManageSearch(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	kb := r.URL.Query().Get("kb")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "query param 'q' is required")
		return
	}
	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 50 {
		limit = n
	}
	log.Debugf("Search: q=%q limit=%d kb=%q", q, limit, srv.KBName())

	svc := srv.Service()
	var hits []knowledge.SearchHit
	var err error
	if kb != "" {
		svc = svc.WithKBService(kb)
		hits, err = svc.Search(q, limit, knowledge.SearchFilter{})
	} else {
		hits, err = svc.SearchAll(q, limit, knowledge.SearchFilter{})
	}
	if err != nil {
		log.Errorf("Search: q=%q failed: %v", q, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hits == nil {
		hits = []knowledge.SearchHit{}
	}
	log.Debugf("Search: q=%q hits=%d", q, len(hits))

	IncrementSearchCounter()
	writeJSON(w, http.StatusOK, map[string]any{
		"query": q,
		"hits":  hits,
		"count": len(hits),
	})
}

// ── Model probe ──────────────────────────────────────────────────────────────

// handleModelProbe probes connectivity to all configured models.
// Returns a JSON map of { embedder, reranker, docParser } probe results.
// Uses a 10-second context timeout per probe to avoid hanging.
func (srv *Server) handleModelProbe(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")

	results := map[string]ProbeResult{}

	// Probe embedder
	results["embedder"] = ProbeEmbedder(r.Context(), srv.Embedder(), log)

	// Probe reranker
	results["reranker"] = ProbeReranker(r.Context(), srv.Reranker(), log)

	// Probe doc parser
	results["docParser"] = ProbeDocParser(r.Context(), log)

	writeJSON(w, http.StatusOK, results)
}

// ── Tombstone handlers ───────────────────────────────────────────────────────

// handleTombstoneList returns all active tombstone records.
func (srv *Server) handleTombstoneList(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	records, err := svc.ListTombstones()
	if err != nil {
		log.Errorf("TombstoneList: kb=%q failed: %v", svc.KBName(), err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if records == nil {
		records = []knowledge.Tombstone{}
	}
	log.Debugf("TombstoneList: kb=%q count=%d", svc.KBName(), len(records))

	type tombstoneItem struct {
		DocSlug    string `json:"docSlug"`
		DeletedAt  string `json:"deletedAt"`
		TTLSeconds int64  `json:"ttlSeconds"`
		Expired    bool   `json:"expired"`
		Reason     string `json:"reason,omitempty"`
		DocVersion int    `json:"docVersion"`
	}
	now := time.Now()
	items := make([]tombstoneItem, len(records))
	for i, ts := range records {
		items[i] = tombstoneItem{
			DocSlug:    ts.DocSlug,
			DeletedAt:  ts.DeletedAt.Format(time.RFC3339),
			TTLSeconds: ts.TTLSeconds,
			Expired:    ts.Expired(now),
			Reason:     ts.Reason,
			DocVersion: ts.DocVersion,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tombstones": items,
		"count":      len(items),
	})
}

// handleTombstoneRestore removes a tombstone, restoring the document to
// search visibility. The physical files must still exist (tombstone hasn't
// been cleaned yet).
func (srv *Server) handleTombstoneRestore(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := svc.ValidateComponent(slug); err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug")
		return
	}
	log.Infof("TombstoneRestore: slug=%q kb=%q", slug, svc.KBName())

	if err := svc.RestoreTombstone(slug); err != nil {
		log.Errorf("TombstoneRestore: slug=%q failed: %v", slug, err)
		writeError(w, http.StatusNotFound, "tombstone not found for slug: "+slug)
		return
	}
	log.Infof("TombstoneRestore: slug=%q restored", slug)
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "restored",
		"slug":    slug,
	})
}

// handleTombstoneClean physically removes all expired tombstoned documents
// and their records.
func (srv *Server) handleTombstoneClean(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}
	log.Infof("TombstoneClean: kb=%q", svc.KBName())

	cleaned, err := svc.CleanExpiredTombstones()
	if err != nil {
		log.Errorf("TombstoneClean: failed: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Infof("TombstoneClean: cleaned %d documents", cleaned)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "cleaned",
		"cleaned": cleaned,
	})
}

// ── Reconciliation handler ───────────────────────────────────────────────────

// handleReconcile runs a full consistency check and returns the report.
func (srv *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}
	log.Infof("Reconcile: kb=%q", svc.KBName())

	report, err := svc.Reconcile()
	if err != nil {
		log.Errorf("Reconcile: failed: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type findingItem struct {
		Severity string `json:"severity"`
		DocSlug  string `json:"docSlug"`
		ChunkID  string `json:"chunkId,omitempty"`
		Message  string `json:"message"`
	}
	findings := make([]findingItem, len(report.Findings))
	for i, f := range report.Findings {
		findings[i] = findingItem{
			Severity: string(f.Severity),
			DocSlug:  f.DocSlug,
			ChunkID:  f.ChunkID,
			Message:  f.Message,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kbName":      report.KBName,
		"docsChecked": report.DocsChecked,
		"docsOK":      report.DocsOK,
		"duration":    report.Duration.String(),
		"findings":    findings,
		"hasErrors":   len(report.Findings) > 0 && report.DocsChecked > report.DocsOK,
	})
}

// ── Manifest handler ─────────────────────────────────────────────────────────

// handleManifestView returns the chunk manifest for a document.
func (srv *Server) handleManifestView(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := svc.ValidateComponent(slug); err != nil {
		writeError(w, http.StatusBadRequest, "invalid slug")
		return
	}
	log.Debugf("ManifestView: slug=%q kb=%q", slug, svc.KBName())

	manifest, err := svc.ReadManifest(slug)
	if err != nil {
		log.Errorf("ManifestView: slug=%q read error: %v", slug, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if manifest == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"slug":     slug,
			"manifest": nil,
			"message":  "No manifest found (legacy document — re-upload with atomic mode to generate)",
		})
		return
	}

	// Compute diff against previous version if available.
	var diffInfo map[string]any
	if manifest.Version > 1 {
		diffInfo = map[string]any{
			"version":      manifest.Version,
			"chunkCount":   manifest.ChunkCount,
			"strategy":     manifest.Strategy,
			"previousDiff": "version history not yet persisted",
		}
	}

	type chunkEntry struct {
		ID          string `json:"id"`
		LegacyID    string `json:"legacyId"`
		Section     string `json:"section,omitempty"`
		Offset      int    `json:"offset"`
		CharCount   int    `json:"charCount"`
		SectionRole string `json:"sectionRole,omitempty"`
	}
	chunks := make([]chunkEntry, len(manifest.Chunks))
	for i, c := range manifest.Chunks {
		chunks[i] = chunkEntry{
			ID:          c.ID,
			LegacyID:    c.LegacyID,
			Section:     c.Section,
			Offset:      c.Offset,
			CharCount:   c.CharCount,
			SectionRole: c.SectionRole,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"slug":         manifest.DocSlug,
		"version":      manifest.Version,
		"sourceHash":   manifest.SourceHash,
		"textHash":     manifest.TextHash,
		"strategy":     manifest.Strategy,
		"chunkCount":   manifest.ChunkCount,
		"sectionCount": manifest.SectionCount,
		"createdAt":    manifest.CreatedAt.Format(time.RFC3339),
		"chunks":       chunks,
		"diff":         diffInfo,
	})
}

// ── Vector statistics handler ────────────────────────────────────────────────

// handleVectorStats returns per-KB vector statistics, including per-document
// breakdown of vector coverage.
func (srv *Server) handleVectorStats(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}
	log.Infof("VectorStats: kb=%q", svc.KBName())

	stats, err := svc.GetVectorStats()
	if err != nil {
		log.Errorf("VectorStats: failed: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// handleVectorIndexInfo returns HNSW index metadata combined with vector
// coverage stats.
func (srv *Server) handleVectorIndexInfo(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}
	log.Infof("VectorIndexInfo: kb=%q", svc.KBName())

	// Use the ManageService facade to get index info.
	info, err := svc.GetVectorIndexInfo()
	if err != nil {
		log.Warnf("VectorIndexInfo: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to get vector index info: "+err.Error())
		return
	}

	resp := map[string]any{
		"kbName": svc.KBName(),
	}
	for k, v := range info {
		resp[k] = v
	}

	// Vector coverage stats.
	stats, err := svc.GetVectorStats()
	if err != nil {
		log.Errorf("VectorIndexInfo: stats failed: %v", err)
		// Still return index info even if stats fail.
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["stats"] = stats

	// Embedder info.
	if srv.Embedder() != nil {
		resp["embedder"] = svc.EmbedderInfo()
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── Vector rebuild handler ───────────────────────────────────────────────────

// handleRebuildVectors triggers vector re-embedding for documents that lack
// vectors. Returns immediately with a task ID for SSE progress tracking.
func (srv *Server) handleRebuildVectors(w http.ResponseWriter, r *http.Request) {
	log := srv.Logger().WithModule("manage")
	svc := srv.Service()
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		svc = svc.WithKBService(kb)
	}

	// Parse optional slug filter (rebuild a single document).
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))

	log.Infof("RebuildVectors: kb=%q slug=%q", svc.KBName(), slug)

	task := srv.TaskManager().Create("vector-rebuild", svc.KBName(), "")
	taskID := task.ID

	go func() {
		defer task.MarkDone("")

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()

		progressCB := func(current, total int, docName string) {
			pct := 0
			if total > 0 {
				pct = current * 100 / total
			}
			task.RecordEvent(knowledge.ProgressEvent{
				Stage:  fmt.Sprintf("embedding: %s", docName),
				Status: fmt.Sprintf("%d/%d (%d%%)", current, total, pct),
				Detail: docName,
			})
		}

		result, err := svc.ReEmbedMissingVectors(ctx, slug, progressCB)
		if err != nil {
			task.RecordEvent(knowledge.ProgressEvent{
				Stage:  "error",
				Status: "error",
				Detail: fmt.Sprintf("vector rebuild failed: %v", err),
			})
			return
		}

		task.RecordEvent(knowledge.ProgressEvent{
			Stage:  "complete",
			Status: "done",
			Detail: fmt.Sprintf("embedded %d chunks across %d documents", result.ChunksEmbedded, result.DocsProcessed),
		})
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"taskId":  taskID,
		"message": "vector rebuild started",
	})
}

// Compile-time check that *multipart.FileHeader has the expected shape.
var _ = (*multipart.FileHeader)(nil)
