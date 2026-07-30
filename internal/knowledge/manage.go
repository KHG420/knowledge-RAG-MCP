package knowledge

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"knowledge-mcp/internal/logging"
)

//go:embed ui/index.html
var manageUI embed.FS

// StartManageServer starts an HTTP management server on the given port.
// It provides a web UI for uploading, browsing, and deleting documents.
// This is intended to be called in a goroutine alongside the MCP server.
func (s *Store) StartManageServer(port string) error {
	mux := http.NewServeMux()

	// ── Health (no auth required) ──
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/health", s.handleHealth)

	// Serve the embedded UI
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := manageUI.ReadFile("ui/index.html")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data) //nolint:errcheck
	})

	// API: list documents
	mux.HandleFunc("GET /api/documents", s.handleManageList)

	// API: upload documents
	mux.HandleFunc("POST /api/upload", s.handleManageUpload)

	// API: delete a document
	mux.HandleFunc("DELETE /api/documents/{slug}", s.handleManageDelete)
	// API: batch delete documents
	mux.HandleFunc("POST /api/documents/batch-delete", s.handleBatchDelete)

	// API: document detail with chunk previews
	mux.HandleFunc("GET /api/documents/{slug}", s.handleManageDocDetail)
	// API: document chunk content preview
	mux.HandleFunc("GET /api/documents/{slug}/chunks", s.handleDocChunks)
	// API: document download
	mux.HandleFunc("GET /api/documents/{slug}/download", s.handleDocDownload)
	// API: document replace (re-upload)
	mux.HandleFunc("PUT /api/documents/{slug}", s.handleDocReplace)
	// API: document tags update
	mux.HandleFunc("PATCH /api/documents/{slug}/tags", s.handleDocTagsUpdate)

	// API: full-text search
	mux.HandleFunc("GET /api/search", s.handleManageSearch)
	// API: search console (debug)
	mux.HandleFunc("POST /api/search-console", s.handleSearchConsole)

	// API: config read/write
	mux.HandleFunc("GET /api/config", s.handleConfigGet)
	mux.HandleFunc("PUT /api/config", s.handleConfigPut)

	// API: knowledge-bases management
	mux.HandleFunc("GET /api/knowledge-bases", func(w http.ResponseWriter, r *http.Request) {
		kbs, err := s.ListKBsInfo()
		if err != nil {
			writeManageError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeManageJSON(w, http.StatusOK, map[string]any{
			"knowledgeBases": kbs,
			"currentKB":      s.kbName,
		})
	})
	mux.HandleFunc("POST /api/knowledge-bases", func(w http.ResponseWriter, r *http.Request) {
		log := s.logger.WithModule("manage")
		var body struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			log.Errorf("CreateKB: invalid JSON: %v", err)
			writeManageError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Name == "" {
			writeManageError(w, http.StatusBadRequest, "name is required")
			return
		}
		log.Infof("CreateKB: name=%q description=%q", body.Name, body.Description)
		if err := s.CreateKB(body.Name, body.Description); err != nil {
			log.Errorf("CreateKB: name=%q failed: %v", body.Name, err)
			writeManageError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Infof("CreateKB: name=%q created", body.Name)
		writeManageJSON(w, http.StatusOK, map[string]string{"message": "created", "name": body.Name})
	})
	mux.HandleFunc("DELETE /api/knowledge-bases/{name}", func(w http.ResponseWriter, r *http.Request) {
		log := s.logger.WithModule("manage")
		name := r.PathValue("name")
		if err := validateComponent(name); err != nil {
			writeManageError(w, http.StatusBadRequest, "invalid name: "+err.Error())
			return
		}
		log.Infof("DeleteKB: name=%q", name)
		if err := s.DeleteKB(name); err != nil {
			log.Errorf("DeleteKB: name=%q failed: %v", name, err)
			writeManageError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Infof("DeleteKB: name=%q deleted", name)
		writeManageJSON(w, http.StatusOK, map[string]string{"message": "deleted", "name": name})
	})
	// API: knowledge base export/import
	mux.HandleFunc("GET /api/knowledge-bases/{name}/export", s.handleKBExport)
	mux.HandleFunc("POST /api/knowledge-bases/import", s.handleKBImport)

	// API: model info (embedder + reranker)
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		writeManageJSON(w, http.StatusOK, map[string]any{
			"embedder":            s.EmbedderInfo(),
			"reranker":            s.RerankerInfo(),
			"rerankCandidateLimit": s.RerankCandidateLimit(),
			"docParser":           DocParserInfo(),
		})
	})

	// API: probe model connectivity (embedder, reranker, doc parser)
	mux.HandleFunc("POST /api/models/probe", s.handleModelProbe)

	// API: task status and SSE events for async upload
	mux.HandleFunc("GET /api/tasks/{id}", s.handleTaskStatus)
	mux.HandleFunc("GET /api/tasks/{id}/events", s.handleTaskEvents)

	// API: tombstone management
	mux.HandleFunc("GET /api/tombstones", s.handleTombstoneList)
	mux.HandleFunc("DELETE /api/tombstones/{slug}", s.handleTombstoneRestore)
	mux.HandleFunc("POST /api/tombstones/clean", s.handleTombstoneClean)

	// API: reconciliation
	mux.HandleFunc("POST /api/reconcile", s.handleReconcile)

	// API: manifest viewer
	mux.HandleFunc("GET /api/documents/{slug}/manifest", s.handleManifestView)

	// API: vector statistics and rebuild
	mux.HandleFunc("GET /api/vector-stats", s.handleVectorStats)
	mux.HandleFunc("POST /api/rebuild-vectors", s.handleRebuildVectors)

	// API: GPU scheduler status
	mux.HandleFunc("GET /api/gpu-scheduler", s.handleGPUSchedulerStatus)

	// API: logs viewer
	mux.HandleFunc("GET /api/logs", s.handleLogs)

	// API: metrics
	mux.HandleFunc("GET /api/metrics", s.handleMetrics)

	// API: system info
	mux.HandleFunc("GET /api/system-info", s.handleSystemInfo)

	// ── Apply middleware ──
	handler := CORSMiddleware(mux)
	if s.config != nil && s.config.APIToken != "" {
		handler = AuthMiddleware(s.config.APIToken)(handler)
	}

	// Background cleanup of old tasks every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.TaskManager().Cleanup(30 * time.Minute)
		}
	}()

	// Dual-stack listen (IPv4 + IPv6 on a single socket).
	// We prefer "tcp" which binds both address families when the OS supports it
	// (Linux, Windows). On macOS, net.Listen("tcp", ":port") can spuriously
	// return EADDRINUSE — we fall back to trying tcp4 then tcp6 independently.
	var ln net.Listener
	var err error
	ln, err = net.Listen("tcp", ":"+port)
	if err != nil {
		for _, network := range []string{"tcp4", "tcp6"} {
			ln, err = net.Listen(network, ":"+port)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return fmt.Errorf("listen on :%s (tried tcp, tcp4, tcp6): %w", port, err)
	}
	defer ln.Close()
	server := &http.Server{
		Handler:      handler,
		WriteTimeout: 10 * time.Minute,
		ReadTimeout:  10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}
	return server.Serve(ln)
}

// --- API handlers ---

type manageDocItem struct {
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	SourceType string   `json:"sourceType"`
	ChunkCount int      `json:"chunkCount"`
	TotalChars int      `json:"totalChars"`
	AddedAt    string   `json:"addedAt"`
	Title      string   `json:"title,omitempty"`
	Authors    []string `json:"authors,omitempty"`
	IsPaper    bool     `json:"isPaper"`
	Tags       []string `json:"tags"`
	HasVectors bool     `json:"hasVectors"`
	VectorDim  int      `json:"vectorDim,omitempty"`
}

func (s *Store) handleManageList(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	log.Debugf("List: kb=%q", kb)
	var docs []DocumentMeta
	var err error
	if kb != "" {
		s = s.WithKB(kb)
		docs, err = s.ListDocuments()
	} else {
		docs, err = s.ListDocumentsAll()
	}
	if err != nil {
		log.Errorf("List: kb=%q failed: %v", kb, err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
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
			Slug:       d.Slug,
			Name:       d.OriginalName,
			SourceType: d.SourceType,
			ChunkCount: d.ChunkCount,
			TotalChars: d.TotalChars,
			AddedAt:    d.AddedAt.Format(time.RFC3339),
			Title:      d.Title,
			Authors:    d.Authors,
			IsPaper:    d.IsPaper,
			Tags:       d.Tags,
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
	s.populateVectorStatus(items)

	// Paginate
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	page := items[offset:end]

	writeManageJSON(w, http.StatusOK, map[string]any{
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

func (s *Store) handleManageUpload(w http.ResponseWriter, r *http.Request) {
	// SSE streaming mode for real-time upload progress.
	if r.URL.Query().Get("stream") == "true" {
		s.handleManageUploadSSE(w, r)
		return
	}

	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		log.Errorf("Upload: parse multipart form failed: %v", err)
		writeManageError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		// Single file via `file` field (curl-friendly)
		file, header, err := r.FormFile("file")
		if err == nil {
			defer file.Close()
			log.Debugf("Upload: single file name=%q kb=%q", header.Filename, s.kbName)
			meta, err := saveManageFile(s, file, header.Filename)
			if err != nil {
				log.Errorf("Upload: single file %q failed: %v", header.Filename, err)
				writeManageError(w, http.StatusInternalServerError, err.Error())
				return
			}
			log.Infof("Upload: single file %q → slug=%q", header.Filename, meta.Slug)
			writeManageJSON(w, http.StatusOK, map[string]any{
				"message": "uploaded",
				"slug":    meta.Slug,
				"name":    meta.OriginalName,
			})
			return
		}
		writeManageError(w, http.StatusBadRequest, "no files uploaded")
		return
	}

	log.Debugf("Upload: %d files kb=%q", len(files), s.kbName)

	// NDJSON streaming: write each result as a JSON line as soon as the
	// file is processed, so the frontend can update the UI incrementally.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeManageError(w, http.StatusInternalServerError, "streaming not supported")
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
		meta, err := saveManageFile(s, file, fh.Filename)
		file.Close()
		if err != nil {
			writeNDJSONLine(w, flusher, uploadLine{Name: fh.Filename, Error: err.Error()})
		} else {
			successCount++
			writeNDJSONLine(w, flusher, uploadLine{Name: fh.Filename, Slug: meta.Slug})
		}
	}

	log.Infof("Upload: %d files, %d succeeded kb=%q", len(files), successCount, s.kbName)

	// Terminal line signals end of stream.
	writeNDJSONLine(w, flusher, uploadLine{
		Done: true,
		Name: fmt.Sprintf("%d/%d 成功", successCount, len(files)),
	})
}

func saveManageFile(s *Store, src io.Reader, filename string) (DocumentMeta, error) {
	tmpDir, err := os.MkdirTemp("", "knowledge-upload-*")
	if err != nil {
		return DocumentMeta{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	safeName := filepath.Base(filename)
	tmpPath := filepath.Join(tmpDir, safeName)
	dst, err := os.Create(tmpPath)
	if err != nil {
		return DocumentMeta{}, fmt.Errorf("create temp file: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return DocumentMeta{}, fmt.Errorf("copy upload: %w", err)
	}
	dst.Close()

	return s.UploadDocument(tmpPath)
}

// --- Async upload (task-based) ---

// handleManageUploadSSE handles file upload asynchronously.
// Instead of streaming progress inline, it creates tasks and returns
// immediately with task IDs. The frontend then subscribes to
// /api/tasks/{id}/events for real-time progress.
func (s *Store) handleManageUploadSSE(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		log.Errorf("Upload: parse multipart form failed: %v", err)
		writeManageError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
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
		writeManageError(w, http.StatusBadRequest, "no files uploaded")
		return
	}

	log.Debugf("Upload: %d files kb=%q", len(fileHeaders), s.kbName)
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
		tm := s.TaskManager()
		task := tm.Create(fh.Filename, s.kbName, tmpDir)

		go func(t *UploadTask, store *Store, path, kbName string) {
			// Apply KB scope for this task.
			var taskStore *Store = store
			if kbName != "" {
				taskStore = store.WithKB(kbName)
			}

			t.mu.Lock()
			t.Status = "processing"
			t.mu.Unlock()
			if t.mgr != nil {
				t.mgr.saveTask(t)
			}

			meta, err := taskStore.UploadDocumentWithProgress(path, func(ev ProgressEvent) {
				t.RecordEvent(ev)
			})
			if err != nil {
				t.RecordEvent(ProgressEvent{Stage: "error", Status: "error", Detail: err.Error()})
				t.MarkError(err)
			} else {
				t.RecordEvent(ProgressEvent{Stage: StageComplete, Status: "done", Detail: meta.Slug})
				t.MarkDone(meta.Slug)
			}
		}(task, s, tmpPath, s.kbName)

		tasks = append(tasks, taskInfo{ID: task.ID, FileName: fh.Filename})
	}

	if len(tasks) == 0 {
		writeManageError(w, http.StatusInternalServerError, "all files failed to submit")
		return
	}
	writeManageJSON(w, http.StatusOK, map[string]any{
		"message": "tasks created",
		"tasks":   tasks,
	})
}

// --- Task status & SSE handlers ---

func (s *Store) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validateComponent(id); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid task id: "+err.Error())
		return
	}
	task := s.TaskManager().Get(id)
	if task == nil {
		writeManageError(w, http.StatusNotFound, "task not found")
		return
	}
	writeManageJSON(w, http.StatusOK, map[string]any{
		"id":        task.ID,
		"fileName":  task.FileName,
		"kbName":    task.KBName,
		"status":    task.Status,
		"slug":      task.Slug,
		"error":     task.Error,
		"createdAt": task.CreatedAt,
		"events":    task.Events(),
	})
}

// handleTaskEvents streams task progress events via SSE.
// On connect it replays all recorded events, then waits for new ones
// until the task completes or the client disconnects.
func (s *Store) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validateComponent(id); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid task id: "+err.Error())
		return
	}
	task := s.TaskManager().Get(id)
	if task == nil {
		writeManageError(w, http.StatusNotFound, "task not found")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeManageError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Replay all existing events.
	events := task.Events()
	for _, ev := range events {
		sendSSEEvent(w, flusher, "progress", map[string]any{
			"stage":  ev.Stage,
			"status": ev.Status,
			"detail": ev.Detail,
		})
	}

	// If already in a terminal state, send final event and return.
	task.mu.RLock()
	terminal := task.Status == "done" || task.Status == "error"
	task.mu.RUnlock()
	if terminal {
		sendTaskFinalEvent(w, flusher, task)
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
				sendSSEEvent(w, flusher, "progress", map[string]any{
					"stage":  ev.Stage,
					"status": ev.Status,
					"detail": ev.Detail,
				})
			}
			sendTaskFinalEvent(w, flusher, task)
			return
		case <-ticker.C:
			allEvents := task.Events()
			if len(allEvents) > lastCount {
				for i := lastCount; i < len(allEvents); i++ {
					ev := allEvents[i]
					sendSSEEvent(w, flusher, "progress", map[string]any{
						"stage":  ev.Stage,
						"status": ev.Status,
						"detail": ev.Detail,
					})
				}
				lastCount = len(allEvents)

				// Check if task just completed.
				task.mu.RLock()
				done := task.Status == "done" || task.Status == "error"
				task.mu.RUnlock()
				if done {
					sendTaskFinalEvent(w, flusher, task)
					return
				}
			}
		}
	}
}

// sendTaskFinalEvent sends the terminal SSE event (complete or error) for a task.
func sendTaskFinalEvent(w http.ResponseWriter, flusher http.Flusher, task *UploadTask) {
	task.mu.RLock()
	status := task.Status
	slug := task.Slug
	errMsg := task.Error
	task.mu.RUnlock()

	if status == "done" {
		sendSSEEvent(w, flusher, "complete", map[string]any{
			"slug": slug,
			"name": task.FileName,
		})
	} else {
		sendSSEEvent(w, flusher, "error", map[string]string{
			"error": errMsg,
		})
	}
}

// sendSSEEvent writes an SSE-formatted event and flushes the response.
func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
	flusher.Flush()
}

func (s *Store) handleManageDelete(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		log.Errorf("Delete: slug is empty")
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := validateComponent(slug); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid slug")
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
		log.Debugf("Delete(tombstone): slug=%q ttl=%d reason=%q kb=%q", slug, ttl, reason, s.kbName)
		if err := s.RemoveDocumentTombstone(slug, ttl, reason); err != nil {
			log.Errorf("Delete(tombstone): slug=%q failed: %v", slug, err)
			writeManageError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Infof("Delete(tombstone): slug=%q tombstoned", slug)
		writeManageJSON(w, http.StatusOK, map[string]string{"message": "tombstoned", "slug": slug})
		return
	}

	log.Debugf("Delete: slug=%q kb=%q", slug, s.kbName)
	if err := s.RemoveDocument(slug); err != nil {
		log.Errorf("Delete: slug=%q failed: %v", slug, err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Infof("Delete: slug=%q deleted", slug)
	writeManageJSON(w, http.StatusOK, map[string]string{"message": "deleted", "slug": slug})
}

// handleManageDocDetail returns full document metadata.
func (s *Store) handleManageDocDetail(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		log.Errorf("DocDetail: slug is empty")
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := validateComponent(slug); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid slug")
		return
	}
	log.Debugf("DocDetail: slug=%q kb=%q", slug, s.kbName)

	meta, err := s.ReadMeta(slug)
	if err != nil {
		log.Errorf("DocDetail: slug=%q failed: %v", slug, err)
		writeManageError(w, http.StatusNotFound, err.Error())
		return
	}
	meta.Slug = slug

	writeManageJSON(w, http.StatusOK, map[string]any{
		"meta": meta,
	})
}

// handleManageSearch performs a full-text search across the knowledge base.
func (s *Store) handleManageSearch(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeManageError(w, http.StatusBadRequest, "query param 'q' is required")
		return
	}
	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 50 {
		limit = n
	}
	log.Debugf("Search: q=%q limit=%d kb=%q", q, limit, s.kbName)

	var hits []SearchHit
	var err error
	if kb != "" {
		s = s.WithKB(kb)
		hits, err = s.Search(q, limit)
	} else {
		hits, err = s.SearchAll(q, limit)
	}
	if err != nil {
		log.Errorf("Search: q=%q failed: %v", q, err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hits == nil {
		hits = []SearchHit{}
	}
	log.Debugf("Search: q=%q hits=%d", q, len(hits))

	writeManageJSON(w, http.StatusOK, map[string]any{
		"query": q,
		"hits":  hits,
		"count": len(hits),
	})
}

// --- probe helpers ---

type probeResult struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
}

// handleModelProbe probes connectivity to all configured models.
// Returns a JSON map of { embedder, reranker, docParser } probe results.
// Uses a 10-second context timeout per probe to avoid hanging.
func (s *Store) handleModelProbe(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")

	results := map[string]probeResult{}

	// Probe embedder
	results["embedder"] = s.probeEmbedder(r.Context(), log)

	// Probe reranker
	results["reranker"] = s.probeReranker(r.Context(), log)

	// Probe doc parser
	results["docParser"] = probeDocParser(r.Context(), log)

	writeManageJSON(w, http.StatusOK, results)
}

func (s *Store) probeEmbedder(ctx context.Context, log *logging.Logger) probeResult {
	if s.embedder == nil {
		return probeResult{OK: false, Error: "未配置"}
	}
	// MockEmbedder has no Probe method — assume always available.
	if _, ok := s.embedder.(*MockEmbedder); ok {
		return probeResult{OK: true, LatencyMs: 0}
	}
	if oe, ok := s.embedder.(*OpenAIEmbedder); ok {
		pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		start := time.Now()
		err := oe.Probe(pCtx)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			log.Errorf("[probe] embedder FAIL: %v", err)
			return probeResult{OK: false, Error: err.Error(), LatencyMs: latency}
		}
		return probeResult{OK: true, LatencyMs: latency}
	}
	return probeResult{OK: false, Error: "未知嵌入器类型"}
}

func (s *Store) probeReranker(ctx context.Context, log *logging.Logger) probeResult {
	if s.reranker == nil {
		return probeResult{OK: false, Error: "未配置"}
	}
	// MockReranker has no Probe method — assume always available.
	if _, ok := s.reranker.(*MockReranker); ok {
		return probeResult{OK: true, LatencyMs: 0}
	}
	if ir, ok := s.reranker.(*InfinityReranker); ok {
		pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		start := time.Now()
		err := ir.Probe(pCtx)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			log.Errorf("[probe] reranker FAIL: %v", err)
			return probeResult{OK: false, Error: err.Error(), LatencyMs: latency}
		}
		return probeResult{OK: true, LatencyMs: latency}
	}
	return probeResult{OK: false, Error: "未知重排序器类型"}
}

func probeDocParser(ctx context.Context, log *logging.Logger) probeResult {
	pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	err := ProbeDocParser(pCtx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		log.Errorf("[probe] docParser FAIL: %v", err)
		return probeResult{OK: false, Error: err.Error(), LatencyMs: latency}
	}
	return probeResult{OK: true, LatencyMs: latency}
}

// ── Tombstone handlers ─────────────────────────────────────────────────────

// handleTombstoneList returns all active tombstone records.
func (s *Store) handleTombstoneList(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	tm := s.getTombstoneManager()
	records := tm.All()
	if records == nil {
		records = []*Tombstone{}
	}
	log.Debugf("TombstoneList: kb=%q count=%d", s.kbName, len(records))

	type tombstoneItem struct {
		DocSlug     string `json:"docSlug"`
		DeletedAt   string `json:"deletedAt"`
		TTLSeconds  int64  `json:"ttlSeconds"`
		Expired     bool   `json:"expired"`
		Reason      string `json:"reason,omitempty"`
		DocVersion  int    `json:"docVersion"`
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

	writeManageJSON(w, http.StatusOK, map[string]any{
		"tombstones": items,
		"count":      len(items),
	})
}

// handleTombstoneRestore removes a tombstone, restoring the document to
// search visibility. The physical files must still exist (tombstone hasn't
// been cleaned yet).
func (s *Store) handleTombstoneRestore(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := validateComponent(slug); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid slug")
		return
	}
	log.Infof("TombstoneRestore: slug=%q kb=%q", slug, s.kbName)

	tm := s.getTombstoneManager()
	if !tm.Exists(slug) {
		writeManageError(w, http.StatusNotFound, "tombstone not found for slug: "+slug)
		return
	}
	if err := tm.Remove(slug); err != nil {
		log.Errorf("TombstoneRestore: slug=%q failed: %v", slug, err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Infof("TombstoneRestore: slug=%q restored", slug)
	writeManageJSON(w, http.StatusOK, map[string]string{
		"message": "restored",
		"slug":    slug,
	})
}

// handleTombstoneClean physically removes all expired tombstoned documents
// and their records.
func (s *Store) handleTombstoneClean(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	log.Infof("TombstoneClean: kb=%q", s.kbName)

	cleaned, err := s.CleanExpiredTombstones()
	if err != nil {
		log.Errorf("TombstoneClean: failed: %v", err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Infof("TombstoneClean: cleaned %d documents", cleaned)
	writeManageJSON(w, http.StatusOK, map[string]any{
		"message": "cleaned",
		"cleaned": cleaned,
	})
}

// ── Reconciliation handler ──────────────────────────────────────────────────

// handleReconcile runs a full consistency check and returns the report.
func (s *Store) handleReconcile(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	log.Infof("Reconcile: kb=%q", s.kbName)

	report, err := s.Reconcile()
	if err != nil {
		log.Errorf("Reconcile: failed: %v", err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
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

	writeManageJSON(w, http.StatusOK, map[string]any{
		"kbName":      report.KBName,
		"docsChecked": report.DocsChecked,
		"docsOK":      report.DocsOK,
		"duration":    report.Duration.String(),
		"findings":    findings,
		"hasErrors":   len(report.Findings) > 0 && report.DocsChecked > report.DocsOK,
	})
}

// ── Manifest handler ────────────────────────────────────────────────────────

// handleManifestView returns the chunk manifest for a document.
func (s *Store) handleManifestView(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := validateComponent(slug); err != nil {
		writeManageError(w, http.StatusBadRequest, "invalid slug")
		return
	}
	log.Debugf("ManifestView: slug=%q kb=%q", slug, s.kbName)

	manifest, err := s.backend.ReadManifest(s.kbName, slug)
	if err != nil {
		log.Errorf("ManifestView: slug=%q read error: %v", slug, err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if manifest == nil {
		writeManageJSON(w, http.StatusOK, map[string]any{
			"slug":     slug,
			"manifest": nil,
			"message":  "No manifest found (legacy document — re-upload with atomic mode to generate)",
		})
		return
	}

	// Compute diff against previous version if available.
	var diffInfo map[string]any
	if manifest.Version > 1 {
		// Try to load the previous version from versions dir.
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

	writeManageJSON(w, http.StatusOK, map[string]any{
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

// ── Vector status population for document list ────────────────────────────

// populateVectorStatus fills the HasVectors and VectorDim fields for each
// item by reading the lightweight header from chunks_index.
func (s *Store) populateVectorStatus(items []manageDocItem) {
	for i := range items {
		index, err := s.ReadChunksIndex(items[i].Slug)
		if err != nil || index == nil {
			continue
		}
		items[i].HasVectors = index.HasVectors
		items[i].VectorDim = index.VectorDim
	}
}

// ── Vector statistics handler ──────────────────────────────────────────────

// handleVectorStats returns per-KB vector statistics, including per-document
// breakdown of vector coverage.
func (s *Store) handleVectorStats(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	log.Infof("VectorStats: kb=%q", s.kbName)

	stats, err := s.GetVectorStats()
	if err != nil {
		log.Errorf("VectorStats: failed: %v", err)
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeManageJSON(w, http.StatusOK, stats)
}

// ── Vector rebuild handler ─────────────────────────────────────────────────

// handleRebuildVectors triggers vector re-embedding for documents that lack
// vectors. Returns immediately with a task ID for SSE progress tracking.
func (s *Store) handleRebuildVectors(w http.ResponseWriter, r *http.Request) {
	log := s.logger.WithModule("manage")
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}

	// Parse optional slug filter (rebuild a single document).
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))

	log.Infof("RebuildVectors: kb=%q slug=%q", s.kbName, slug)

	task := s.TaskManager().Create("vector-rebuild", s.kbName, "")
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
			task.RecordEvent(ProgressEvent{
				Stage:  fmt.Sprintf("embedding: %s", docName),
				Status: fmt.Sprintf("%d/%d (%d%%)", current, total, pct),
				Detail: docName,
			})
		}

		result, err := s.ReEmbedMissingVectors(ctx, slug, progressCB)
		if err != nil {
			task.RecordEvent(ProgressEvent{
				Stage:  "error",
				Status: "error",
				Detail: fmt.Sprintf("vector rebuild failed: %v", err),
			})
			return
		}

		task.RecordEvent(ProgressEvent{
			Stage:  "complete",
			Status: "done",
			Detail: fmt.Sprintf("embedded %d chunks across %d documents", result.ChunksEmbedded, result.DocsProcessed),
		})
	}()

	writeManageJSON(w, http.StatusAccepted, map[string]any{
		"taskId":  taskID,
		"message": "vector rebuild started",
	})
}

// ── Enhanced delete with tombstone option ────────────────────────────────────
// The existing handleManageDelete is extended to support ?tombstone=true query
// parameter for soft-delete with tombstone.

func init() {
	// Ensure tombstone handlers compile correctly.
	_ = (*Store).handleTombstoneList
	_ = (*Store).handleTombstoneRestore
	_ = (*Store).handleTombstoneClean
	_ = (*Store).handleReconcile
	_ = (*Store).handleManifestView
}

// --- helpers ---

type manageAPIError struct {
	Error string `json:"error"`
}

func writeManageJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeManageError(w http.ResponseWriter, status int, msg string) {
	writeManageJSON(w, status, manageAPIError{Error: msg})
}

// writeNDJSONLine marshals v as JSON, appends a newline, writes to w, and
// flushes. Used for NDJSON streaming responses where each line is a
// self-contained event.
func writeNDJSONLine(w http.ResponseWriter, flusher http.Flusher, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		// Fallback: write a safe error line so the client doesn't hang waiting
		// for a result that will never come.
		data = []byte(`{"error":"internal serialization error"}`)
	}
	// Ignore write errors: the connection may have been closed by the client.
	_, _ = fmt.Fprintf(w, "%s\n", data)
	flusher.Flush()
}

// Compile-time check that *multipart.FileHeader has the expected shape.
var _ = (*multipart.FileHeader)(nil)
