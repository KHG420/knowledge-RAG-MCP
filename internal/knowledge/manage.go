package knowledge

import (
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
		kbs, err := s.ListKBs()
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
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeManageError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Name == "" {
			writeManageError(w, http.StatusBadRequest, "name is required")
			return
		}
		if err := s.CreateKB(body.Name); err != nil {
			writeManageError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeManageJSON(w, http.StatusOK, map[string]string{"message": "created", "name": body.Name})
	})
	mux.HandleFunc("DELETE /api/knowledge-bases/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := s.DeleteKB(name); err != nil {
			writeManageError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeManageJSON(w, http.StatusOK, map[string]string{"message": "deleted", "name": name})
	})

	// API: model info (embedder + reranker)
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		writeManageJSON(w, http.StatusOK, map[string]any{
			"embedder":            s.EmbedderInfo(),
			"reranker":            s.RerankerInfo(),
			"rerankCandidateLimit": s.RerankCandidateLimit(),
		})
	})

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
	kb := r.URL.Query().Get("kb")
	var docs []DocumentMeta
	var err error
	if kb != "" {
		s = s.WithKB(kb)
		docs, err = s.ListDocuments()
	} else {
		docs, err = s.ListDocumentsAll()
	}
	if err != nil {
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Parse query params
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	sourceType := strings.TrimSpace(r.URL.Query().Get("sourceType"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
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
	for _, d := range docs {
		if sourceType != "" && d.SourceType != sourceType {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(d.OriginalName), strings.ToLower(search)) {
			continue
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
	}

	total := len(items)

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
		"documents": page,
		"total":     total,
		"offset":    offset,
		"limit":     limit,
	})
}

func (s *Store) handleManageUpload(w http.ResponseWriter, r *http.Request) {
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeManageError(w, http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		// Single file via `file` field (curl-friendly)
		file, header, err := r.FormFile("file")
		if err == nil {
			defer file.Close()
			meta, err := saveManageFile(s, file, header.Filename)
			if err != nil {
				writeManageError(w, http.StatusInternalServerError, err.Error())
				return
			}
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

	type uploadResult struct {
		Name  string `json:"name"`
		Slug  string `json:"slug,omitempty"`
		Error string `json:"error,omitempty"`
	}
	results := make([]uploadResult, 0, len(files))
	for _, fh := range files {
		file, err := fh.Open()
		if err != nil {
			results = append(results, uploadResult{Name: fh.Filename, Error: err.Error()})
			continue
		}
		meta, err := saveManageFile(s, file, fh.Filename)
		file.Close()
		if err != nil {
			results = append(results, uploadResult{Name: fh.Filename, Error: err.Error()})
		} else {
			results = append(results, uploadResult{Name: fh.Filename, Slug: meta.Slug})
		}
	}

	successCount := 0
	for _, r := range results {
		if r.Error == "" {
			successCount++
		}
	}
	if successCount == 0 {
		writeManageError(w, http.StatusInternalServerError, "all files failed to upload")
		return
	}
	writeManageJSON(w, http.StatusOK, map[string]any{
		"message": "upload complete",
		"results": results,
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

func (s *Store) handleManageDelete(w http.ResponseWriter, r *http.Request) {
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := s.RemoveDocument(slug); err != nil {
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeManageJSON(w, http.StatusOK, map[string]string{"message": "deleted", "slug": slug})
}

// handleManageDocDetail returns full document metadata.
func (s *Store) handleManageDocDetail(w http.ResponseWriter, r *http.Request) {
	kb := r.URL.Query().Get("kb")
	if kb != "" {
		s = s.WithKB(kb)
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeManageError(w, http.StatusBadRequest, "slug is required")
		return
	}

	meta, err := s.ReadMeta(slug)
	if err != nil {
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

	var hits []SearchHit
	var err error
	if kb != "" {
		s = s.WithKB(kb)
		hits, err = s.Search(q, limit)
	} else {
		hits, err = s.SearchAll(q, limit)
	}
	if err != nil {
		writeManageError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hits == nil {
		hits = []SearchHit{}
	}

	writeManageJSON(w, http.StatusOK, map[string]any{
		"query": q,
		"hits":  hits,
		"count": len(hits),
	})
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

// Compile-time check that *multipart.FileHeader has the expected shape.
var _ = (*multipart.FileHeader)(nil)
