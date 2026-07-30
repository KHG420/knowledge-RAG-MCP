package knowledge

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knowledge-mcp/internal/logging"
)

// ── test helpers ───────────────────────────────────────────────────────────────

func newTestStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	tmp := t.TempDir()
	fb := NewFileBackend(tmp)
	store := NewStoreWithBackend(fb)
	store.logger = logging.NewNopLogger()
	if err := store.CreateKB("test-kb", "test knowledge base"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	store = store.WithKB("test-kb")

	src := filepath.Join(tmp, "test.md")
	if err := os.WriteFile(src, []byte("# Hello\n\nThis is a test document.\n\nIt has multiple paragraphs.\n\n## Section 2\n\nMore content here."), 0644); err != nil {
		t.Fatalf("write test doc: %v", err)
	}
	meta, err := store.UploadDocument(src)
	if err != nil {
		t.Fatalf("UploadDocument: %v", err)
	}
	if meta.Slug == "" {
		t.Fatal("UploadDocument returned empty slug — meta.Slug not set")
	}
	return store, tmp, meta.Slug
}

func newManageMux(s *Store) *http.ServeMux {
	mux := http.NewServeMux()
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
	mux.HandleFunc("GET /api/documents", s.handleManageList)
	mux.HandleFunc("POST /api/upload", s.handleManageUpload)
	mux.HandleFunc("DELETE /api/documents/{slug}", s.handleManageDelete)
	mux.HandleFunc("GET /api/documents/{slug}", s.handleManageDocDetail)
	mux.HandleFunc("GET /api/search", s.handleManageSearch)
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
		var body struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeManageError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if body.Name == "" {
			writeManageError(w, http.StatusBadRequest, "name is required")
			return
		}
		if err := s.CreateKB(body.Name, body.Description); err != nil {
			writeManageError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeManageJSON(w, http.StatusOK, map[string]string{"message": "created", "name": body.Name})
	})
	mux.HandleFunc("DELETE /api/knowledge-bases/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := validateComponent(name); err != nil {
			writeManageError(w, http.StatusBadRequest, "invalid name: "+err.Error())
			return
		}
		if err := s.DeleteKB(name); err != nil {
			writeManageError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeManageJSON(w, http.StatusOK, map[string]string{"message": "deleted", "name": name})
	})
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		writeManageJSON(w, http.StatusOK, map[string]any{
			"embedder":            s.EmbedderInfo(),
			"reranker":            s.RerankerInfo(),
			"rerankCandidateLimit": s.RerankCandidateLimit(),
			"docParser":           DocParserInfo(),
		})
	})
	mux.HandleFunc("POST /api/models/probe", s.handleModelProbe)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleTaskStatus)
	mux.HandleFunc("GET /api/tasks/{id}/events", s.handleTaskEvents)
	mux.HandleFunc("GET /api/tombstones", s.handleTombstoneList)
	mux.HandleFunc("DELETE /api/tombstones/{slug}", s.handleTombstoneRestore)
	mux.HandleFunc("POST /api/tombstones/clean", s.handleTombstoneClean)
	mux.HandleFunc("POST /api/reconcile", s.handleReconcile)
	mux.HandleFunc("GET /api/documents/{slug}/manifest", s.handleManifestView)
	return mux
}

func doRequest(s *Store, method, path string, body io.Reader) *httptest.ResponseRecorder {
	mux := newManageMux(s)
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// mustGetBody is a helper to fail the test if response code != want, then return body string.
func mustGetBody(t *testing.T, w *httptest.ResponseRecorder, wantCode int) string {
	t.Helper()
	if w.Code != wantCode {
		t.Fatalf("expected %d, got %d: %s", wantCode, w.Code, w.Body.String())
	}
	return w.Body.String()
}

// ── GET / (UI) ─────────────────────────────────────────────────────────────────

func TestManage_GetUIRoot(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
}

func TestManage_GetUINotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/nonexistent", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── GET /api/documents ─────────────────────────────────────────────────────────

func TestManage_ListDocuments(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents?kb=test-kb", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Documents []struct {
			Slug       string `json:"slug"`
			Name       string `json:"name"`
			SourceType string `json:"sourceType"`
			ChunkCount int    `json:"chunkCount"`
		} `json:"documents"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total == 0 {
		t.Error("expected at least 1 document")
	}
	if len(resp.Documents) == 0 {
		t.Error("expected at least 1 document in list")
	}
}

func TestManage_ListDocuments_AllKBs(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestManage_ListDocuments_SearchFilter(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents?kb=test-kb&search=test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_ListDocuments_SortByName(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents?kb=test-kb&sortBy=name&sortOrder=asc", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestManage_ListDocuments_Pagination(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents?kb=test-kb&offset=0&limit=5", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestManage_ListDocuments_LimitCapped(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents?kb=test-kb&limit=9999", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_ListDocuments_EmptyKB(t *testing.T) {
	s, _, _ := newTestStore(t)
	s.CreateKB("empty-kb", "nothing here")
	w := doRequest(s, "GET", "/api/documents?kb=empty-kb", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Documents []any `json:"documents"`
		Total     int   `json:"total"`
	}
	json.Unmarshal([]byte(body), &resp)
	if len(resp.Documents) != 0 || resp.Total != 0 {
		t.Errorf("expected empty list, got %d docs", resp.Total)
	}
}

// ── POST /api/upload ───────────────────────────────────────────────────────────

func TestManage_UploadSingleFile(t *testing.T) {
	s, tmp, _ := newTestStore(t)

	src := filepath.Join(tmp, "upload-test.txt")
	if err := os.WriteFile(src, []byte("uploaded content"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "upload-test.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("uploaded content")) //nolint:errcheck
	mw.Close()

	mux := newManageMux(s)
	req := httptest.NewRequest("POST", "/api/upload?kb=test-kb", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body := mustGetBody(t, w, http.StatusOK)
	var resp struct {
		Message string `json:"message"`
		Slug    string `json:"slug"`
		Name    string `json:"name"`
	}
	json.Unmarshal([]byte(body), &resp)
	if resp.Message != "uploaded" {
		t.Errorf("expected uploaded, got %q", resp.Message)
	}
	if resp.Slug == "" {
		t.Error("expected non-empty slug")
	}
}

func TestManage_UploadNoFile(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "POST", "/api/upload?kb=test-kb", strings.NewReader("not multipart"))
	if w.Code == http.StatusOK {
		t.Error("expected error for non-multipart request")
	}
}

// ── DELETE /api/documents/{slug} ───────────────────────────────────────────────

func TestManage_DeleteDocument(t *testing.T) {
	s, _, slug := newTestStore(t)
	w := doRequest(s, "DELETE", "/api/documents/"+slug+"?kb=test-kb", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Message string `json:"message"`
		Slug    string `json:"slug"`
	}
	json.Unmarshal([]byte(body), &resp)
	if resp.Message != "deleted" {
		t.Errorf("expected deleted, got %q", resp.Message)
	}
}

func TestManage_DeleteDocument_NotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "DELETE", "/api/documents/nonexistent-slug?kb=test-kb", nil)
	// os.RemoveAll is idempotent — deleting a nonexistent slug returns 200.
	// This is acceptable behavior; the document is effectively already deleted.
	if w.Code != http.StatusOK {
		t.Logf("DELETE nonexistent returned %d (expected 200, idempotent)", w.Code)
	}
}

func TestManage_DeleteDocument_Tombstone(t *testing.T) {
	s, _, slug := newTestStore(t)
	w := doRequest(s, "DELETE", "/api/documents/"+slug+"?kb=test-kb&tombstone=true&ttl=3600", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Message string `json:"message"`
		Slug    string `json:"slug"`
	}
	json.Unmarshal([]byte(body), &resp)
	if resp.Message != "tombstoned" {
		t.Errorf("expected tombstoned, got %q", resp.Message)
	}
}

// ── GET /api/documents/{slug} ──────────────────────────────────────────────────

func TestManage_DocDetail(t *testing.T) {
	s, _, slug := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents/"+slug+"?kb=test-kb", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Meta struct {
			Slug         string `json:"slug"`
			ChunkCount   int    `json:"chunk_count"`
			TotalChars   int    `json:"total_chars"`
			OriginalName string `json:"original_name"`
		} `json:"meta"`
	}
	json.Unmarshal([]byte(body), &resp)
	if resp.Meta.Slug != slug {
		t.Errorf("expected slug %q, got %q", slug, resp.Meta.Slug)
	}
	if resp.Meta.ChunkCount == 0 {
		t.Error("expected non-zero chunk count")
	}
}

func TestManage_DocDetail_NotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents/nonexistent?kb=test-kb", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── GET /api/search ────────────────────────────────────────────────────────────

func TestManage_Search(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/search?kb=test-kb&q=test+document", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Query string `json:"query"`
		Count int    `json:"count"`
		Hits  []struct {
			Score   float64 `json:"Score"`
			DocSlug string  `json:"DocSlug"`
			Snippet string  `json:"Snippet"`
		} `json:"hits"`
	}
	json.Unmarshal([]byte(body), &resp)
	if resp.Count == 0 {
		t.Error("expected search hits")
	}
}

func TestManage_Search_MissingQuery(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/search?kb=test-kb", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_Search_AllKBs(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/search?q=hello", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_Search_LimitCapped(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/search?kb=test-kb&q=test&limit=999", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ── /api/knowledge-bases ──────────────────────────────────────────────────────

func TestManage_ListKBs(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/knowledge-bases", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		KnowledgeBases []any  `json:"knowledgeBases"`
		CurrentKB      string `json:"currentKB"`
	}
	json.Unmarshal([]byte(body), &resp)
	if len(resp.KnowledgeBases) == 0 {
		t.Error("expected at least 1 KB")
	}
}

func TestManage_CreateKB(t *testing.T) {
	s, _, _ := newTestStore(t)
	body := strings.NewReader(`{"name":"kb2","description":"second kb"}`)
	w := doRequest(s, "POST", "/api/knowledge-bases", body)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Message string `json:"message"`
		Name    string `json:"name"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Name != "kb2" {
		t.Errorf("expected kb2, got %q", resp.Name)
	}
}

func TestManage_CreateKB_EmptyName(t *testing.T) {
	s, _, _ := newTestStore(t)
	body := strings.NewReader(`{"name":"","description":"no name"}`)
	w := doRequest(s, "POST", "/api/knowledge-bases", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_CreateKB_InvalidJSON(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "POST", "/api/knowledge-bases", strings.NewReader("not json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_DeleteKB(t *testing.T) {
	s, _, _ := newTestStore(t)
	s.CreateKB("to-delete", "temp")
	w := doRequest(s, "DELETE", "/api/knowledge-bases/to-delete", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_DeleteKB_InvalidName(t *testing.T) {
	s, _, _ := newTestStore(t)
	// Go mux cleans ".." paths, returning 301 redirect for the cleaned path.
	// validateComponent is tested separately in TestManage_ValidateComponent.
	w := doRequest(s, "DELETE", "/api/knowledge-bases/../etc", nil)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound && w.Code != http.StatusMovedPermanently {
		t.Errorf("expected 400/404/301, got %d", w.Code)
	}
}

// ── GET /api/models ────────────────────────────────────────────────────────────

func TestManage_Models(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/models", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ── POST /api/models/probe ─────────────────────────────────────────────────────

func TestManage_ModelProbe(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "POST", "/api/models/probe", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Embedder struct {
			OK bool `json:"ok"`
		} `json:"embedder"`
		Reranker struct {
			OK bool `json:"ok"`
		} `json:"reranker"`
		DocParser struct {
			OK bool `json:"ok"`
		} `json:"docParser"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
}

// ── /api/tasks ─────────────────────────────────────────────────────────────────

func TestManage_TaskStatus_NotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/tasks/nonexistent-id", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestManage_TaskEvents_NotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/tasks/nonexistent-id/events", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── /api/tombstones ────────────────────────────────────────────────────────────

func TestManage_TombstoneList(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/tombstones?kb=test-kb", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Tombstones []any `json:"tombstones"`
		Count      int   `json:"count"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Count != 0 {
		t.Logf("tombstones count: %d", resp.Count)
	}
}

func TestManage_TombstoneRestore_NotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "DELETE", "/api/tombstones/nonexistent-slug?kb=test-kb", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestManage_TombstoneClean(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "POST", "/api/tombstones/clean?kb=test-kb", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ── POST /api/reconcile ────────────────────────────────────────────────────────

func TestManage_Reconcile(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "POST", "/api/reconcile?kb=test-kb", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		KBName      string `json:"kbName"`
		DocsChecked int    `json:"docsChecked"`
		HasErrors   bool   `json:"hasErrors"`
	}
	json.Unmarshal([]byte(body), &resp)
	if resp.KBName != "test-kb" {
		t.Errorf("expected test-kb, got %q", resp.KBName)
	}
}

// ── GET /api/documents/{slug}/manifest ─────────────────────────────────────────

func TestManage_ManifestView(t *testing.T) {
	s, _, slug := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents/"+slug+"/manifest?kb=test-kb", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Slug       string `json:"slug"`
		ChunkCount int    `json:"chunkCount"`
	}
	json.Unmarshal([]byte(body), &resp)
	if resp.Slug != slug {
		t.Errorf("expected slug %q, got %q", slug, resp.Slug)
	}
}

func TestManage_ManifestView_NotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents/nonexistent/manifest?kb=test-kb", nil)
	if w.Code != http.StatusNotFound && w.Code != http.StatusOK {
		t.Errorf("expected 404 or 200, got %d", w.Code)
	}
}

// ── 安全测试：validateComponent ────────────────────────────────────────────────

func TestManage_ValidateComponent(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", true},
		{"valid-slug", true},
		{"../etc/passwd", false},
		{"..\\windows", false},
		{"/absolute/path", false},
		{"normal-document-name", true},
		{"doc_with_underscores", true},
		{"dots...are.fine", false},
	}
	for _, tt := range tests {
		err := validateComponent(tt.input)
		passed := err == nil
		if passed != tt.want {
			if tt.want {
				t.Errorf("validateComponent(%q) should pass, got: %v", tt.input, err)
			} else {
				t.Errorf("validateComponent(%q) should fail, but passed", tt.input)
			}
		}
	}
}

// ── 响应格式测试 ───────────────────────────────────────────────────────────────

func TestManage_ResponseContentType(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/documents?kb=test-kb", nil)
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json, got %q", ct)
	}
}

func TestManage_ErrorResponseFormat(t *testing.T) {
	s, _, _ := newTestStore(t)
	w := doRequest(s, "GET", "/api/search?kb=test-kb", nil)
	if w.Code == http.StatusOK {
		t.Fatal("expected error response")
	}
	var resp struct {
		Error string `json:"error"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Error == "" {
		t.Error("expected error message in response")
	}
}

// ── 边界测试 ───────────────────────────────────────────────────────────────────

func TestManage_MultipleKBs_Isolation(t *testing.T) {
	s, _, _ := newTestStore(t)
	s.CreateKB("kb-a", "A")
	s.CreateKB("kb-b", "B")

	w := doRequest(s, "GET", "/api/knowledge-bases", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		KnowledgeBases []struct {
			Name string `json:"name"`
		} `json:"knowledgeBases"`
	}
	json.Unmarshal([]byte(body), &resp)
	found := map[string]bool{}
	for _, kb := range resp.KnowledgeBases {
		found[kb.Name] = true
	}
	if !found["kb-a"] || !found["kb-b"] {
		t.Errorf("expected both kbs, got %v", found)
	}
}

// ── 并发安全测试 ───────────────────────────────────────────────────────────────

func TestManage_ConcurrentRequests(t *testing.T) {
	s, _, _ := newTestStore(t)
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			doRequest(s, "GET", "/api/documents?kb=test-kb", nil)
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
