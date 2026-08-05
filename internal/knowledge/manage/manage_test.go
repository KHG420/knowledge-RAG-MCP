package manage_test

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
	"time"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/manage"
	"knowledge-mcp/internal/logging"
)

// ── test helpers ───────────────────────────────────────────────────────────────

func newTestStore(t *testing.T) (*knowledge.Store, *manage.Server, string, string) {
	t.Helper()
	tmp := t.TempDir()
	backend := knowledge.NewMockBackend()
	store := knowledge.NewStoreWithBackend(backend)
	store.SetDataDir(tmp)
	store.SetLogger(logging.NewNopLogger())
	if err := store.CreateKB("test-kb", "test knowledge base"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	store = store.WithKB("test-kb")

	cfg := config.DefaultConfig()
	cfg.DataDir = tmp
	store.SetConfig(cfg, tmp+"/knowledge-mcp.toml")

	srv := manage.New(
		store, cfg, tmp+"/knowledge-mcp.toml",
		backend,
		store.Embedder(), store.Reranker(), store.VectorIndexRaw(),
		store.KBName(), store.DataDir(),
		store.GPUScheduler(), store.KBRouter(), store.TaskManager(),
		store.Mutex(), logging.NewNopLogger(),
	)

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
	return store, srv, tmp, meta.Slug
}

// newTestStoreWithConfig creates a store with a config object attached,
// required for config API tests. Returns store, manage server, tmp dir, and the default config.
func newTestStoreWithConfig(t *testing.T) (*knowledge.Store, *manage.Server, string, *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	backend := knowledge.NewMockBackend()
	store := knowledge.NewStoreWithBackend(backend)
	store.SetDataDir(tmp)
	store.SetLogger(logging.NewNopLogger())
	if err := store.CreateKB("test-kb", "test knowledge base"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	store = store.WithKB("test-kb")

	cfg := config.DefaultConfig()
	cfg.DataDir = tmp
	cfg.EmbedEndpoint = "http://localhost:11434/api/embed"
	cfg.EmbedModel = "bge-m3"
	cfg.RerankEndpoint = "http://localhost:11435/rerank"
	cfg.RerankModel = "gte-multilingual-reranker-base"
	cfg.RerankCandidateLimit = 100
	cfg.RerankTimeout = "30s"
	cfg.DeepSeekEndpoint = "https://api.deepseek.com/chat/completions"
	cfg.DeepSeekModel = "deepseek-v4-flash"
	cfg.DeepSeekAPIKey = "sk-test-key"
	cfg.RedisEnabled = true
	cfg.RedisAddr = "127.0.0.1:6379"
	cfg.RedisDB = 0
	cfg.RedisPrefix = "kmcp:"
	cfg.RedisPoolSize = 10
	cfg.CacheQueryTTL = 300
	cfg.CacheChunkTTL = 0
	cfg.CacheMetaTTL = 0
	cfg.CacheIndexTTL = 0
	cfg.CacheKBListTTL = 60
	cfg.APIToken = "secret-token"
	cfg.LogLevel = "info"
	store.SetConfig(cfg, tmp+"/knowledge-mcp.toml")

	srv := manage.New(
		store, cfg, tmp+"/knowledge-mcp.toml",
		backend,
		store.Embedder(), store.Reranker(), store.VectorIndexRaw(),
		store.KBName(), store.DataDir(),
		store.GPUScheduler(), store.KBRouter(), store.TaskManager(),
		store.Mutex(), logging.NewNopLogger(),
	)

	return store, srv, tmp, cfg
}

func doRequest(srv *manage.Server, method, path string, body io.Reader) *httptest.ResponseRecorder {
	mux := manage.BuildMux(srv)
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// When APIToken is configured, include auth header.
	if cfg := srv.Config(); cfg != nil && cfg.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
}

func TestManage_GetUINotFound(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/nonexistent", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── GET /api/documents ─────────────────────────────────────────────────────────

func TestManage_ListDocuments(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents?kb=test-kb", nil)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestManage_ListDocuments_SearchFilter(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents?kb=test-kb&search=test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_ListDocuments_SortByName(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents?kb=test-kb&sortBy=name&sortOrder=asc", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestManage_ListDocuments_Pagination(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents?kb=test-kb&offset=0&limit=5", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestManage_ListDocuments_LimitCapped(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents?kb=test-kb&limit=9999", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_ListDocuments_EmptyKB(t *testing.T) {
	store, srv, _, _ := newTestStore(t)
	store.CreateKB("empty-kb", "nothing here")
	w := doRequest(srv, "GET", "/api/documents?kb=empty-kb", nil)
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
	store, srv, tmp, _ := newTestStore(t)
	_ = store

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

	mux := manage.BuildMux(srv)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/upload?kb=test-kb", strings.NewReader("not multipart"))
	if w.Code == http.StatusOK {
		t.Error("expected error for non-multipart request")
	}
}

// ── DELETE /api/documents/{slug} ───────────────────────────────────────────────

func TestManage_DeleteDocument(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	w := doRequest(srv, "DELETE", "/api/documents/"+slug+"?kb=test-kb", nil)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "DELETE", "/api/documents/nonexistent-slug?kb=test-kb", nil)
	if w.Code != http.StatusOK {
		t.Logf("DELETE nonexistent returned %d (expected 200, idempotent)", w.Code)
	}
}

func TestManage_DeleteDocument_Tombstone(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	w := doRequest(srv, "DELETE", "/api/documents/"+slug+"?kb=test-kb&tombstone=true&ttl=3600", nil)
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
	_, srv, _, slug := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/"+slug+"?kb=test-kb", nil)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/nonexistent?kb=test-kb", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── GET /api/search ────────────────────────────────────────────────────────────

func TestManage_Search(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/search?kb=test-kb&q=test+document", nil)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/search?kb=test-kb", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_Search_AllKBs(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/search?q=hello", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_Search_LimitCapped(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/search?kb=test-kb&q=test&limit=999", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ── /api/knowledge-bases ──────────────────────────────────────────────────────

func TestManage_ListKBs(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/knowledge-bases", nil)
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
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"name":"kb2","description":"second kb"}`)
	w := doRequest(srv, "POST", "/api/knowledge-bases", body)
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
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"name":"","description":"no name"}`)
	w := doRequest(srv, "POST", "/api/knowledge-bases", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_CreateKB_InvalidJSON(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/knowledge-bases", strings.NewReader("not json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_DeleteKB(t *testing.T) {
	store, srv, _, _ := newTestStore(t)
	store.CreateKB("to-delete", "temp")
	w := doRequest(srv, "DELETE", "/api/knowledge-bases/to-delete", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_DeleteKB_InvalidName(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "DELETE", "/api/knowledge-bases/../etc", nil)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound && w.Code != http.StatusMovedPermanently {
		t.Errorf("expected 400/404/301, got %d", w.Code)
	}
}

// ── GET /api/models ────────────────────────────────────────────────────────────

func TestManage_Models(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/models", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ── POST /api/models/probe ─────────────────────────────────────────────────────

func TestManage_ModelProbe(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/models/probe", nil)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/tasks/nonexistent-id", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestManage_TaskEvents_NotFound(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/tasks/nonexistent-id/events", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// ── /api/tombstones ────────────────────────────────────────────────────────────

func TestManage_TombstoneList(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/tombstones?kb=test-kb", nil)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "DELETE", "/api/tombstones/nonexistent-slug?kb=test-kb", nil)
	// New handler may return 200 (idempotent restore) or 404.
	if w.Code != http.StatusNotFound && w.Code != http.StatusOK {
		t.Errorf("expected 404 or 200, got %d", w.Code)
	}
}

func TestManage_TombstoneClean(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/tombstones/clean?kb=test-kb", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ── POST /api/reconcile ────────────────────────────────────────────────────────

func TestManage_Reconcile(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/reconcile?kb=test-kb", nil)
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
	_, srv, _, slug := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/"+slug+"/manifest?kb=test-kb", nil)
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
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/nonexistent/manifest?kb=test-kb", nil)
	if w.Code != http.StatusNotFound && w.Code != http.StatusOK {
		t.Errorf("expected 404 or 200, got %d", w.Code)
	}
}

// ── 响应格式测试 ───────────────────────────────────────────────────────────────

func TestManage_ResponseContentType(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents?kb=test-kb", nil)
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json, got %q", ct)
	}
}

func TestManage_ErrorResponseFormat(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/search?kb=test-kb", nil)
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
	store, srv, _, _ := newTestStore(t)
	store.CreateKB("kb-a", "A")
	store.CreateKB("kb-b", "B")

	w := doRequest(srv, "GET", "/api/knowledge-bases", nil)
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
	_, srv, _, _ := newTestStore(t)
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			doRequest(srv, "GET", "/api/documents?kb=test-kb", nil)
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

// ── 向量重建 SSE 事件验证 ─────────────────────────────────────────────────────

func TestRebuildVectors_SSEEvents(t *testing.T) {
	tmp := t.TempDir()
	backend := knowledge.NewMockBackend()
	store := knowledge.NewStoreWithBackend(backend)
	store.SetDataDir(tmp)
	store.SetLogger(logging.NewNopLogger())

	store.SetEmbedder(knowledge.NewMockEmbedder(256))

	if err := store.CreateKB("test-kb", ""); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	store = store.WithKB("test-kb")

	cfg := config.DefaultConfig()
	cfg.DataDir = tmp
	store.SetConfig(cfg, tmp+"/knowledge-mcp.toml")

	srv := manage.New(
		store, cfg, tmp+"/knowledge-mcp.toml",
		backend,
		store.Embedder(), store.Reranker(), store.VectorIndexRaw(),
		store.KBName(), store.DataDir(),
		store.GPUScheduler(), store.KBRouter(), store.TaskManager(),
		store.Mutex(), logging.NewNopLogger(),
	)

	src := filepath.Join(tmp, "doc.md")
	if err := os.WriteFile(src, []byte("# Test\n\nContent for vector testing."), 0644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	if _, err := store.UploadDocument(src); err != nil {
		t.Fatalf("UploadDocument: %v", err)
	}

	w := doRequest(srv, "POST", "/api/rebuild-vectors?kb=test-kb", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("rebuild-vectors: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var startResp struct {
		TaskID  string `json:"taskId"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode rebuild response: %v", err)
	}
	if startResp.TaskID == "" {
		t.Fatal("taskId is empty")
	}
	t.Logf("rebuild started, taskId=%s", startResp.TaskID)

	tm := store.TaskManager()
	if tsk := tm.Get(startResp.TaskID); tsk != nil {
		st, _, _ := tsk.Snapshot()
		t.Logf("task found in TM: status=%s", st)
	} else {
		t.Logf("task NOT found in TM after rebuild")
	}

	mux := manage.BuildMux(srv)
	var taskStatus string
	for i := 0; i < 50; i++ {
		statusReq := httptest.NewRequest("GET", "/api/tasks/"+startResp.TaskID, nil)
		statusW := httptest.NewRecorder()
		mux.ServeHTTP(statusW, statusReq)
		if statusW.Code == http.StatusOK {
			var ts struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(statusW.Body.Bytes(), &ts); err == nil {
				taskStatus = ts.Status
				if ts.Status == "done" || ts.Status == "error" {
					t.Logf("task status: %s", ts.Status)
					break
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("final task status: %s", taskStatus)

	sseURL := "/api/tasks/" + startResp.TaskID + "/events"
	sseReq := httptest.NewRequest("GET", sseURL, nil)
	sseReq.Header.Set("Accept", "text/event-stream")
	sseW := httptest.NewRecorder()

	mux.ServeHTTP(sseW, sseReq)

	if sseW.Code != http.StatusOK {
		t.Fatalf("SSE: expected 200, got %d: %s", sseW.Code, sseW.Body.String())
	}

	body := sseW.Body.String()
	events := parseSSEEvents(body)
	t.Logf("received %d SSE events: %v", len(events), events)

	foundComplete := false
	foundError := false
	for _, ev := range events {
		switch ev.eventType {
		case "complete":
			foundComplete = true
		case "error":
			foundError = true
			t.Errorf("unexpected SSE error event: %s", ev.data)
		}
	}

	if !foundComplete {
		t.Errorf("expected 'complete' SSE event, but not found in events: %v", events)
	}
	if foundError {
		t.Error("received 'error' SSE event when rebuild was successful")
	}
}

// sseEvent is a parsed SSE event.
type sseEvent struct {
	eventType string
	data      string
}

// parseSSEEvents parses an SSE stream body into a slice of events.
func parseSSEEvents(body string) []sseEvent {
	var events []sseEvent
	lines := strings.Split(body, "\n")
	var currentType, currentData string
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			currentType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			currentData = strings.TrimPrefix(line, "data: ")
		} else if line == "" && currentData != "" {
			events = append(events, sseEvent{
				eventType: currentType,
				data:      currentData,
			})
			currentType = ""
			currentData = ""
		}
	}
	return events
}

// =============================================================================
// Config API tests — GET /api/config and PUT /api/config
// =============================================================================

func TestConfigGet_OK(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	w := doRequest(srv, "GET", "/api/config", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json, got %q", ct)
	}
}

func TestConfigGet_AllFieldsPresent(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	w := doRequest(srv, "GET", "/api/config", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp map[string]any
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	required := []string{
		"embedEndpoint", "embedModel", "embedDim",
		"rerankEndpoint", "rerankModel", "rerankTimeout", "rerankCandidateLimit",
		"searchMode", "rerankEnabled", "rrfK", "abstractBoost", "bm25K1", "bm25B",
		"chunkMinChars", "chunkMaxChars", "chunkOverlapChars", "chunkSemanticThreshold",
		"uploadMaxSizeMb",
		"logLevel", "logFile",
		"managePort", "servePort", "serveBaseUrl",
		"dataDir", "defaultKB", "configPath",
		"deepseekEndpoint", "deepseekModel",
		"redisEnabled", "redisAddr", "redisDB", "redisPrefix", "redisPoolSize",
		"cacheQueryTTL", "cacheChunkTTL", "cacheMetaTTL", "cacheIndexTTL", "cacheKBListTTL",
	}
	for _, key := range required {
		if _, ok := resp[key]; !ok {
			t.Errorf("missing field in config response: %q", key)
		}
	}
}

func TestConfigGet_MaskedSensitiveFields(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	w := doRequest(srv, "GET", "/api/config", nil)
	body := mustGetBody(t, w, http.StatusOK)

	var resp struct {
		EmbedAPIKey     string `json:"embedApiKey"`
		RerankAPIKey    string `json:"rerankApiKey"`
		DocParserAPIKey string `json:"docParserApiKey"`
		APIToken        string `json:"apiToken"`
		DeepSeekAPIKey  string `json:"deepseekApiKey"`
		RedisPassword   string `json:"redisPassword"`
		MySQLDsn        string `json:"mysqlDsn"`
	}
	json.Unmarshal([]byte(body), &resp)

	maskedFields := map[string]string{
		"embedApiKey":     resp.EmbedAPIKey,
		"rerankApiKey":    resp.RerankAPIKey,
		"docParserApiKey": resp.DocParserAPIKey,
		"apiToken":        resp.APIToken,
		"deepseekApiKey":  resp.DeepSeekAPIKey,
	}
	for name, val := range maskedFields {
		if val != "" && val != "***" {
			t.Errorf("%s should be masked (got %q), expected \"\" or \"***\"", name, val)
		}
	}
	if resp.RedisPassword != "" && resp.RedisPassword != "***" {
		t.Errorf("redisPassword should be masked (\"\" or \"***\"), got %q", resp.RedisPassword)
	}
}

func TestConfigGet_DeepSeekFields(t *testing.T) {
	store, srv, _, cfg := newTestStoreWithConfig(t)
	cfg.DeepSeekEndpoint = "https://custom.api/v1"
	cfg.DeepSeekModel = "deepseek-v3"
	cfg.DeepSeekAPIKey = "sk-custom-key"
	store.SetConfig(cfg, cfg.DataDir+"/custom.toml")

	w := doRequest(srv, "GET", "/api/config", nil)
	var resp struct {
		DeepSeekEndpoint string `json:"deepseekEndpoint"`
		DeepSeekModel    string `json:"deepseekModel"`
		DeepSeekAPIKey   string `json:"deepseekApiKey"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)

	if resp.DeepSeekEndpoint != "https://custom.api/v1" {
		t.Errorf("deepseekEndpoint: got %q, want %q", resp.DeepSeekEndpoint, "https://custom.api/v1")
	}
	if resp.DeepSeekModel != "deepseek-v3" {
		t.Errorf("deepseekModel: got %q, want %q", resp.DeepSeekModel, "deepseek-v3")
	}
	if resp.DeepSeekAPIKey != "***" {
		t.Errorf("deepseekApiKey should be masked, got %q", resp.DeepSeekAPIKey)
	}
}

func TestConfigGet_RedisAndCacheTTLFields(t *testing.T) {
	store, srv, _, cfg := newTestStoreWithConfig(t)
	cfg.RedisEnabled = true
	cfg.RedisAddr = "10.0.0.1:6379"
	cfg.RedisDB = 3
	cfg.RedisPrefix = "myapp:"
	cfg.RedisPoolSize = 20
	cfg.CacheQueryTTL = 600
	cfg.CacheChunkTTL = 3600
	store.SetConfig(cfg, cfg.DataDir+"/redis.toml")

	w := doRequest(srv, "GET", "/api/config", nil)
	var resp struct {
		RedisEnabled  bool   `json:"redisEnabled"`
		RedisAddr     string `json:"redisAddr"`
		RedisDB       int    `json:"redisDB"`
		RedisPrefix   string `json:"redisPrefix"`
		RedisPoolSize int    `json:"redisPoolSize"`
		CacheQueryTTL int    `json:"cacheQueryTTL"`
		CacheChunkTTL int    `json:"cacheChunkTTL"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)

	if !resp.RedisEnabled {
		t.Error("redisEnabled should be true")
	}
	if resp.RedisAddr != "10.0.0.1:6379" {
		t.Errorf("redisAddr: got %q, want %q", resp.RedisAddr, "10.0.0.1:6379")
	}
	if resp.RedisDB != 3 {
		t.Errorf("redisDB: got %d, want 3", resp.RedisDB)
	}
	if resp.RedisPrefix != "myapp:" {
		t.Errorf("redisPrefix: got %q, want %q", resp.RedisPrefix, "myapp:")
	}
	if resp.RedisPoolSize != 20 {
		t.Errorf("redisPoolSize: got %d, want 20", resp.RedisPoolSize)
	}
	if resp.CacheQueryTTL != 600 {
		t.Errorf("cacheQueryTTL: got %d, want 600", resp.CacheQueryTTL)
	}
	if resp.CacheChunkTTL != 3600 {
		t.Errorf("cacheChunkTTL: got %d, want 3600", resp.CacheChunkTTL)
	}
}

// ── PUT /api/config ────────────────────────────────────────────────────────────

func TestConfigPut_ValidUpdate(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)

	body := strings.NewReader(`{"searchMode":"bm25","rrfK":80}`)
	w := doRequest(srv, "PUT", "/api/config", body)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Message string   `json:"message"`
		Changes []string `json:"changes"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Message != "configuration updated" {
		t.Errorf("unexpected message: %q", resp.Message)
	}

	w2 := doRequest(srv, "GET", "/api/config", nil)
	var cfg struct {
		SearchMode string `json:"searchMode"`
		RRFK       int    `json:"rrfK"`
	}
	json.Unmarshal([]byte(w2.Body.String()), &cfg)
	if cfg.SearchMode != "bm25" {
		t.Errorf("searchMode not updated: got %q", cfg.SearchMode)
	}
	if cfg.RRFK != 80 {
		t.Errorf("rrfK not updated: got %d", cfg.RRFK)
	}
}

func TestConfigPut_DeepSeekFields(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)

	body := strings.NewReader(`{"deepseekEndpoint":"https://deepseek.example.com","deepseekModel":"deepseek-v3"}`)
	w := doRequest(srv, "PUT", "/api/config", body)
	mustGetBody(t, w, http.StatusOK)

	w2 := doRequest(srv, "GET", "/api/config", nil)
	var resp struct {
		DeepSeekEndpoint string `json:"deepseekEndpoint"`
		DeepSeekModel    string `json:"deepseekModel"`
	}
	json.Unmarshal([]byte(w2.Body.String()), &resp)
	if resp.DeepSeekEndpoint != "https://deepseek.example.com" {
		t.Errorf("deepseekEndpoint: got %q", resp.DeepSeekEndpoint)
	}
	if resp.DeepSeekModel != "deepseek-v3" {
		t.Errorf("deepseekModel: got %q", resp.DeepSeekModel)
	}
}

func TestConfigPut_CacheTTLFields(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)

	body := strings.NewReader(`{"cacheQueryTTL":600,"cacheChunkTTL":3600,"cacheKBListTTL":120}`)
	w := doRequest(srv, "PUT", "/api/config", body)
	mustGetBody(t, w, http.StatusOK)

	w2 := doRequest(srv, "GET", "/api/config", nil)
	var resp struct {
		CacheQueryTTL  int `json:"cacheQueryTTL"`
		CacheChunkTTL  int `json:"cacheChunkTTL"`
		CacheKBListTTL int `json:"cacheKBListTTL"`
	}
	json.Unmarshal([]byte(w2.Body.String()), &resp)
	if resp.CacheQueryTTL != 600 {
		t.Errorf("cacheQueryTTL: got %d, want 600", resp.CacheQueryTTL)
	}
	if resp.CacheChunkTTL != 3600 {
		t.Errorf("cacheChunkTTL: got %d, want 3600", resp.CacheChunkTTL)
	}
	if resp.CacheKBListTTL != 120 {
		t.Errorf("cacheKBListTTL: got %d, want 120", resp.CacheKBListTTL)
	}
}

func TestConfigPut_InvalidJSON(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)

	w := doRequest(srv, "PUT", "/api/config", strings.NewReader("not-json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestConfigPut_PartialUpdateOnlyChangedFields(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)

	wBefore := doRequest(srv, "GET", "/api/config", nil)
	var before struct {
		SearchMode    string `json:"searchMode"`
		RerankEnabled bool   `json:"rerankEnabled"`
		LogLevel      string `json:"logLevel"`
	}
	json.Unmarshal([]byte(wBefore.Body.String()), &before)

	w := doRequest(srv, "PUT", "/api/config", strings.NewReader(`{"logLevel":"debug"}`))
	mustGetBody(t, w, http.StatusOK)

	wAfter := doRequest(srv, "GET", "/api/config", nil)
	var after struct {
		SearchMode    string `json:"searchMode"`
		RerankEnabled bool   `json:"rerankEnabled"`
		LogLevel      string `json:"logLevel"`
	}
	json.Unmarshal([]byte(wAfter.Body.String()), &after)

	if after.LogLevel != "debug" {
		t.Errorf("logLevel not updated: got %q", after.LogLevel)
	}
	if after.SearchMode != before.SearchMode {
		t.Errorf("searchMode changed unexpectedly: was %q, now %q", before.SearchMode, after.SearchMode)
	}
	if after.RerankEnabled != before.RerankEnabled {
		t.Errorf("rerankEnabled changed unexpectedly: was %v, now %v", before.RerankEnabled, after.RerankEnabled)
	}
}

func TestConfigPut_EmptyBody(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)

	w := doRequest(srv, "PUT", "/api/config", strings.NewReader(`{}`))
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Message string   `json:"message"`
		Changes []string `json:"changes"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Message != "configuration updated" {
		t.Errorf("unexpected message: %q", resp.Message)
	}
	if len(resp.Changes) != 0 {
		t.Errorf("expected 0 changes for empty body, got %d: %v", len(resp.Changes), resp.Changes)
	}
}

// ── PUT /api/config — type coercion roundtrip ──────────────────────────────────

func TestConfigPut_TypeCoercion(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)

	body := strings.NewReader(`{
		"embedDim": 768,
		"rerankCandidateLimit": 200,
		"rrfK": 100,
		"abstractBoost": 1.5,
		"bm25K1": 1.5,
		"bm25B": 0.5,
		"rerankEnabled": false,
		"chunkMinChars": 300,
		"chunkMaxChars": 4000,
		"chunkOverlapChars": 400,
		"chunkSemanticThreshold": 0.85,
		"uploadMaxSizeMb": 1000,
		"cacheQueryTTL": 120,
		"cacheChunkTTL": 7200
	}`)
	w := doRequest(srv, "PUT", "/api/config", body)
	mustGetBody(t, w, http.StatusOK)

	w2 := doRequest(srv, "GET", "/api/config", nil)
	var resp struct {
		EmbedDim               int     `json:"embedDim"`
		RerankCandidateLimit   int     `json:"rerankCandidateLimit"`
		RRFK                   int     `json:"rrfK"`
		AbstractBoost          float64 `json:"abstractBoost"`
		BM25K1                 float64 `json:"bm25K1"`
		BM25B                  float64 `json:"bm25B"`
		RerankEnabled          bool    `json:"rerankEnabled"`
		ChunkMinChars          int     `json:"chunkMinChars"`
		ChunkMaxChars          int     `json:"chunkMaxChars"`
		ChunkOverlapChars      int     `json:"chunkOverlapChars"`
		ChunkSemanticThreshold float64 `json:"chunkSemanticThreshold"`
		UploadMaxSizeMb        int     `json:"uploadMaxSizeMb"`
		CacheQueryTTL          int     `json:"cacheQueryTTL"`
		CacheChunkTTL          int     `json:"cacheChunkTTL"`
	}
	json.Unmarshal([]byte(w2.Body.String()), &resp)

	if resp.EmbedDim != 768 {
		t.Errorf("embedDim: got %d, want 768", resp.EmbedDim)
	}
	if resp.RerankCandidateLimit != 200 {
		t.Errorf("rerankCandidateLimit: got %d, want 200", resp.RerankCandidateLimit)
	}
	if resp.RRFK != 100 {
		t.Errorf("rrfK: got %d, want 100", resp.RRFK)
	}
	if resp.AbstractBoost != 1.5 {
		t.Errorf("abstractBoost: got %f, want 1.5", resp.AbstractBoost)
	}
	if resp.BM25K1 != 1.5 {
		t.Errorf("bm25K1: got %f, want 1.5", resp.BM25K1)
	}
	if resp.BM25B != 0.5 {
		t.Errorf("bm25B: got %f, want 0.5", resp.BM25B)
	}
	if resp.RerankEnabled != false {
		t.Error("rerankEnabled should be false")
	}
	if resp.ChunkMinChars != 300 {
		t.Errorf("chunkMinChars: got %d, want 300", resp.ChunkMinChars)
	}
	if resp.ChunkMaxChars != 4000 {
		t.Errorf("chunkMaxChars: got %d, want 4000", resp.ChunkMaxChars)
	}
	if resp.ChunkOverlapChars != 400 {
		t.Errorf("chunkOverlapChars: got %d, want 400", resp.ChunkOverlapChars)
	}
	if resp.ChunkSemanticThreshold != 0.85 {
		t.Errorf("chunkSemanticThreshold: got %f, want 0.85", resp.ChunkSemanticThreshold)
	}
	if resp.UploadMaxSizeMb != 1000 {
		t.Errorf("uploadMaxSizeMb: got %d, want 1000", resp.UploadMaxSizeMb)
	}
	if resp.CacheQueryTTL != 120 {
		t.Errorf("cacheQueryTTL: got %d, want 120", resp.CacheQueryTTL)
	}
	if resp.CacheChunkTTL != 7200 {
		t.Errorf("cacheChunkTTL: got %d, want 7200", resp.CacheChunkTTL)
	}
}

// ── Config API — concurrent safety ─────────────────────────────────────────────

func TestConfig_ConcurrentGet(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			w := doRequest(srv, "GET", "/api/config", nil)
			if w.Code != http.StatusOK {
				t.Errorf("concurrent GET returned %d", w.Code)
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
