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

	// API: document detail with chunk previews
	mux.HandleFunc("GET /api/documents/{slug}", s.handleManageDocDetail)

	// API: full-text search
	mux.HandleFunc("GET /api/search", s.handleManageSearch)

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
		log.Infof("DeleteKB: name=%q", name)
		if err := s.DeleteKB(name); err != nil {
			log.Errorf("DeleteKB: name=%q failed: %v", name, err)
			writeManageError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Infof("DeleteKB: name=%q deleted", name)
		writeManageJSON(w, http.StatusOK, map[string]string{"message": "deleted", "name": name})
	})

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

	// Background cleanup of old tasks every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.TaskManager().Cleanup(30 * time.Minute)
		}
	}()

	// Listen on the port with per-family fallback.
	// On macOS, Go's net.Listen("tcp", ":port") can return EADDRINUSE even when
	// binding succeeds on one address family. This happens because getaddrinfo
	// returns both IPv4 and IPv6 addresses for ":port", and Go tries each in
	// sequence — the IPv6 socket (with IPV6_V6ONLY=0 on macOS) already covers
	// all addresses, making the subsequent IPv4 bind appear as "address already
	// in use". We try each family independently so the first success is used.
	var ln net.Listener
	var err error
	for _, network := range []string{"tcp6", "tcp4"} {
		ln, err = net.Listen(network, ":"+port)
		if err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("listen on :%s (tried tcp6, tcp4): %w", port, err)
	}
	defer ln.Close()
	return http.Serve(ln, mux)
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

	tmpPath := filepath.Join(tmpDir, filename)
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
		tmpPath := filepath.Join(tmpDir, fh.Filename)
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
		return
	}
	// Ignore write errors: the connection may have been closed by the client.
	_, _ = fmt.Fprintf(w, "%s\n", data)
	flusher.Flush()
}

// Compile-time check that *multipart.FileHeader has the expected shape.
var _ = (*multipart.FileHeader)(nil)
