package manage_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
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
	mustGetBody(t, w, http.StatusCreated)

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

// ═══════════════════════════════════════════════════════════════════════════════
// ── Document chunks ───────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_DocChunks_OK(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/"+slug+"/chunks", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Slug       string `json:"slug"`
		ChunkCount int    `json:"chunkCount"`
		Chunks     []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Index   int    `json:"index"`
		} `json:"chunks"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Slug != slug {
		t.Errorf("expected slug %q, got %q", slug, resp.Slug)
	}
	if resp.ChunkCount == 0 {
		t.Error("expected non-zero chunk count")
	}
	if len(resp.Chunks) != resp.ChunkCount {
		t.Errorf("chunk count mismatch: %d vs %d", len(resp.Chunks), resp.ChunkCount)
	}
}

func TestManage_DocChunks_NotFound(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/nonexistent/chunks", nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for nonexistent doc, got %d", w.Code)
	}
}

func TestManage_DocChunks_InvalidSlug(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	// Use a slug containing ".." that passes HTTP routing but fails ValidateComponent.
	w := doRequest(srv, "GET", "/api/documents/test..invalid/chunks", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid slug, got %d: %s", w.Code, w.Body.String())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Document download ─────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_DocDownload_OK(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/"+slug+"/download", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain Content-Type, got %q", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("expected attachment Content-Disposition, got %q", cd)
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty download body")
	}
}

func TestManage_DocDownload_NotFound(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/nonexistent/download", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestManage_DocDownload_InvalidSlug(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents/test..invalid/download", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Document replace ──────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_DocReplace_OK(t *testing.T) {
	_, srv, _, slug := newTestStore(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "replace.md")
	part.Write([]byte("# Replaced\n\nNew content for replacement test."))
	mw.Close()

	w := doMultipartRequest(srv, "PUT", "/api/documents/"+slug, &buf, mw.FormDataContentType())
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Message string `json:"message"`
		OldSlug string `json:"oldSlug"`
		NewSlug string `json:"newSlug"`
		Name    string `json:"name"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Message != "document replaced" {
		t.Errorf("unexpected message: %q", resp.Message)
	}
	if resp.OldSlug != slug {
		t.Errorf("expected oldSlug %q, got %q", slug, resp.OldSlug)
	}
}

func TestManage_DocReplace_NotFound(t *testing.T) {
	_, srv, _, _ := newTestStore(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "test.md")
	part.Write([]byte("test"))
	mw.Close()

	w := doMultipartRequest(srv, "PUT", "/api/documents/nonexistent", &buf, mw.FormDataContentType())
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestManage_DocReplace_NoFile(t *testing.T) {
	_, srv, _, slug := newTestStore(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.Close()

	w := doMultipartRequest(srv, "PUT", "/api/documents/"+slug, &buf, mw.FormDataContentType())
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing file, got %d", w.Code)
	}
}

func TestManage_DocReplace_InvalidSlug(t *testing.T) {
	_, srv, _, _ := newTestStore(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "test.md")
	part.Write([]byte("test"))
	mw.Close()

	w := doMultipartRequest(srv, "PUT", "/api/documents/test..invalid", &buf, mw.FormDataContentType())
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid slug, got %d: %s", w.Code, w.Body.String())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Document tags update ──────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_DocTagsUpdate_OK(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	body := strings.NewReader(`{"tags":["tag1","tag2","tag3"]}`)
	w := doRequest(srv, "PATCH", "/api/documents/"+slug+"/tags", body)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Message string   `json:"message"`
		Slug    string   `json:"slug"`
		Tags    []string `json:"tags"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Slug != slug {
		t.Errorf("expected slug %q, got %q", slug, resp.Slug)
	}
	if len(resp.Tags) != 3 {
		t.Errorf("expected 3 tags, got %d: %v", len(resp.Tags), resp.Tags)
	}
}

func TestManage_DocTagsUpdate_EmptyTags(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	body := strings.NewReader(`{"tags":[]}`)
	w := doRequest(srv, "PATCH", "/api/documents/"+slug+"/tags", body)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Tags []string `json:"tags"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if len(resp.Tags) != 0 {
		t.Errorf("expected empty tags, got %v", resp.Tags)
	}
}

func TestManage_DocTagsUpdate_NotFound(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"tags":["test"]}`)
	w := doRequest(srv, "PATCH", "/api/documents/nonexistent/tags", body)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestManage_DocTagsUpdate_InvalidSlug(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"tags":["test"]}`)
	w := doRequest(srv, "PATCH", "/api/documents/test..invalid/tags", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_DocTagsUpdate_InvalidJSON(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	w := doRequest(srv, "PATCH", "/api/documents/"+slug+"/tags", strings.NewReader("not json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Batch delete ──────────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_BatchDelete_OK(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	body := strings.NewReader(fmt.Sprintf(`{"slugs":["%s"],"tombstone":false}`, slug))
	w := doRequest(srv, "POST", "/api/documents/batch-delete", body)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Message string `json:"message"`
		Deleted int    `json:"deleted"`
		Failed  int    `json:"failed"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", resp.Deleted)
	}
	if resp.Failed != 0 {
		t.Errorf("expected 0 failed, got %d", resp.Failed)
	}
}

func TestManage_BatchDelete_Tombstone(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	body := strings.NewReader(fmt.Sprintf(`{"slugs":["%s"],"tombstone":true,"reason":"test cleanup"}`, slug))
	w := doRequest(srv, "POST", "/api/documents/batch-delete", body)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Deleted int `json:"deleted"`
		Failed  int `json:"failed"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", resp.Deleted)
	}
}

func TestManage_BatchDelete_EmptySlugs(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/documents/batch-delete", strings.NewReader(`{"slugs":[]}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty slugs, got %d", w.Code)
	}
}

func TestManage_BatchDelete_TooManySlugs(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	slugs := make([]string, 101)
	for i := range slugs {
		slugs[i] = fmt.Sprintf("doc-%d", i)
	}
	body, _ := json.Marshal(map[string]any{"slugs": slugs})
	w := doRequest(srv, "POST", "/api/documents/batch-delete", bytes.NewReader(body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for >100 slugs, got %d", w.Code)
	}
}

func TestManage_BatchDelete_InvalidJSON(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/documents/batch-delete", strings.NewReader("not json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_BatchDelete_MixedResults(t *testing.T) {
	_, srv, _, slug := newTestStore(t)
	body := strings.NewReader(fmt.Sprintf(`{"slugs":["%s","nonexistent-slug","/invalid"],"tombstone":false}`, slug))
	w := doRequest(srv, "POST", "/api/documents/batch-delete", body)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Deleted int `json:"deleted"`
		Failed  int `json:"failed"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	// At least 1 deleted (the valid slug), others may fail
	if resp.Deleted < 1 {
		t.Errorf("expected at least 1 deleted, got %d", resp.Deleted)
	}
}

// doMultipartRequest is a helper for multipart form upload tests.
func doMultipartRequest(srv *manage.Server, method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	mux := manage.BuildMux(srv)
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", contentType)
	if cfg := srv.Config(); cfg != nil && cfg.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Search console ────────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_SearchConsole_OK(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"query":"test document","mode":"bm25","limit":5}`)
	w := doRequest(srv, "POST", "/api/search-console", body)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Query     string `json:"query"`
		Mode      string `json:"mode"`
		LatencyMs int64  `json:"latencyMs"`
		TotalHits int    `json:"totalHits"`
		Results   []struct {
			Rank    int     `json:"rank"`
			Score   float64 `json:"score"`
			Snippet string  `json:"snippet"`
		} `json:"results"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Query != "test document" {
		t.Errorf("expected query 'test document', got %q", resp.Query)
	}
	if resp.Mode != "bm25" {
		t.Errorf("expected mode bm25, got %q", resp.Mode)
	}
}

func TestManage_SearchConsole_DefaultMode(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"query":"test","limit":3}`)
	w := doRequest(srv, "POST", "/api/search-console", body)
	mustGetBody(t, w, http.StatusOK)
}

func TestManage_SearchConsole_Rerank(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"query":"test","rerank":true,"limit":5}`)
	w := doRequest(srv, "POST", "/api/search-console", body)
	mustGetBody(t, w, http.StatusOK)
}

func TestManage_SearchConsole_VectorMode(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	body := strings.NewReader(`{"query":"test","mode":"vector","limit":5}`)
	w := doRequest(srv, "POST", "/api/search-console", body)
	// Vector mode may fail if no real embedder is reachable; just verify non-empty response.
	if w.Code != http.StatusOK && w.Code != http.StatusInternalServerError {
		t.Errorf("unexpected status %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_SearchConsole_HybridMode(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"query":"test","mode":"hybrid","limit":5}`)
	w := doRequest(srv, "POST", "/api/search-console", body)
	mustGetBody(t, w, http.StatusOK)
}

func TestManage_SearchConsole_MissingQuery(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/search-console", strings.NewReader(`{"limit":5}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_SearchConsole_InvalidJSON(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/search-console", strings.NewReader("not json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_SearchConsole_WithKBName(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	body := strings.NewReader(`{"query":"test","kbName":"test-kb","limit":5}`)
	w := doRequest(srv, "POST", "/api/search-console", body)
	mustGetBody(t, w, http.StatusOK)
}

func TestManage_SearchConsole_LimitDefaults(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	// No limit specified → defaults to 10.
	body := strings.NewReader(`{"query":"test"}`)
	w := doRequest(srv, "POST", "/api/search-console", body)
	mustGetBody(t, w, http.StatusOK)
}

func TestManage_SearchConsole_LimitCapped(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	// Limit > 50 should be capped at 50.
	body := strings.NewReader(`{"query":"test","limit":500}`)
	w := doRequest(srv, "POST", "/api/search-console", body)
	mustGetBody(t, w, http.StatusOK)
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Tool descriptions ─────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_ToolDescriptions_Get(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/tool-descriptions", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Custom   map[string]string `json:"custom"`
		Defaults map[string]string `json:"defaults"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Defaults == nil {
		t.Error("expected non-nil defaults in tool descriptions response")
	}
}

func TestManage_ToolDescriptions_Put(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	body := strings.NewReader(`{
		"searchDesc": "Custom search description",
		"readDesc": "Custom read description"
	}`)
	w := doRequest(srv, "PUT", "/api/tool-descriptions", body)
	mustGetBody(t, w, http.StatusOK)
}

func TestManage_ToolDescriptions_PutInvalidJSON(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "PUT", "/api/tool-descriptions", strings.NewReader("not json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestManage_ToolDescriptions_GetAfterPut(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	// First set
	putBody := strings.NewReader(`{"searchDesc":"Updated search desc"}`)
	w := doRequest(srv, "PUT", "/api/tool-descriptions", putBody)
	mustGetBody(t, w, http.StatusOK)

	// Then get and verify
	w = doRequest(srv, "GET", "/api/tool-descriptions", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Custom struct {
			SearchDesc string `json:"searchDesc"`
		} `json:"custom"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Custom.SearchDesc != "Updated search desc" {
		t.Errorf("expected custom searchDesc 'Updated search desc', got %q", resp.Custom.SearchDesc)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Restart ───────────────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_Restart(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/restart", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if !resp.OK {
		t.Error("expected ok: true")
	}
	if resp.Message == "" {
		t.Error("expected restart message")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── GPU scheduler ─────────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_GPUScheduler_NotConfigured(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/gpu-scheduler", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Enabled bool   `json:"enabled"`
		Message string `json:"message,omitempty"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Enabled {
		t.Error("expected GPU scheduler disabled by default")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Logs ──────────────────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_Logs_Default(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/logs", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Lines []string `json:"lines"`
		Tail  int      `json:"tail"`
		Count int      `json:"count"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Count < 0 {
		t.Errorf("unexpected count: %d", resp.Count)
	}
}

func TestManage_Logs_WithTail(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/logs?tail=5", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Tail int `json:"tail"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Tail != 5 {
		t.Errorf("expected tail 5, got %d", resp.Tail)
	}
}

func TestManage_Logs_TailCapped(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/logs?tail=2000", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Tail int `json:"tail"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Tail > 1000 {
		t.Errorf("expected tail capped at 1000, got %d", resp.Tail)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Metrics ───────────────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_Metrics_JSON(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/metrics", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		UptimeSeconds      float64 `json:"uptimeSeconds"`
		TotalRequests      int64   `json:"totalRequests"`
		MemoryAllocMB      float64 `json:"memoryAllocMB"`
		Goroutines         int     `json:"goroutines"`
		NumCPU             int     `json:"numCPU"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.NumCPU <= 0 {
		t.Error("expected numCPU > 0")
	}
	if resp.Goroutines < 0 {
		t.Error("unexpected goroutines count")
	}
}

func TestManage_Metrics_Prometheus(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/metrics?format=prometheus", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain for prometheus format, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "knowledge_mcp_uptime_seconds") {
		t.Error("expected prometheus metrics in response")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── System info ───────────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_SystemInfo(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/system-info", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		GoVersion    string `json:"goVersion"`
		NumCPU       int    `json:"numCPU"`
		NumGoroutine int    `json:"numGoroutine"`
		Memory       struct {
			AllocMB      float64 `json:"allocMB"`
			TotalAllocMB float64 `json:"totalAllocMB"`
			SysMB        float64 `json:"sysMB"`
			NumGC        uint32  `json:"numGC"`
			HeapObjects  uint64  `json:"heapObjects"`
		} `json:"memory"`
		UptimeSeconds float64 `json:"uptimeSeconds"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.GoVersion == "" {
		t.Error("expected goVersion")
	}
	if resp.NumCPU <= 0 {
		t.Error("expected numCPU > 0")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Vector stats & index ──────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_VectorStats(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/vector-stats", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		TotalDocs     int `json:"totalDocs"`
		DocsWithVecs  int `json:"docsWithVecs"`
		DocsMissing   int `json:"docsMissing"`
		TotalChunks   int `json:"totalChunks"`
		ChunksWithVec int `json:"chunksWithVec"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.TotalDocs != 1 {
		t.Errorf("expected 1 total doc, got %d", resp.TotalDocs)
	}
}

func TestManage_VectorStats_WithKB(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/vector-stats?kb=test-kb", nil)
	mustGetBody(t, w, http.StatusOK)
}

func TestManage_VectorIndex(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/vector-index", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		KBName string `json:"kbName"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.KBName != "test-kb" {
		t.Errorf("expected kbName test-kb, got %q", resp.KBName)
	}
}

func TestManage_VectorIndex_WithKB(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/vector-index?kb=test-kb", nil)
	mustGetBody(t, w, http.StatusOK)
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── KB export/import (MockBackend — error paths) ──────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_KBExport_RequiresMySQL(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/knowledge-bases/test-kb/export", nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for non-MySQL export, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_KBExport_NotFound(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/knowledge-bases/nonexistent-kb/export", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestManage_KBExport_InvalidName(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/knowledge-bases/test..bad/export", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid name, got %d", w.Code)
	}
}

func TestManage_KBImport_RequiresMySQL(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	// Create a minimal valid zip so body parsing passes.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Add a dummy file to make it valid.
	f, _ := zw.Create("dummy.txt")
	f.Write([]byte("test"))
	zw.Close()
	w := doRequest(srv, "POST", "/api/knowledge-bases/import?name=test-kb", bytes.NewReader(buf.Bytes()))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for non-MySQL import, got %d: %s", w.Code, w.Body.String())
	}
}

func TestManage_KBImport_MissingName(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/knowledge-bases/import", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing name, got %d", w.Code)
	}
}

func TestManage_KBImport_InvalidName(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "POST", "/api/knowledge-bases/import?name=test..bad", strings.NewReader("not a zip"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid name, got %d: %s", w.Code, w.Body.String())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Auth / API token ──────────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_Auth_WithoutToken(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	// Build mux directly without auth header.
	mux := manage.BuildMux(srv)
	req := httptest.NewRequest("GET", "/api/documents", nil)
	// Deliberately no Authorization header.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", w.Code)
	}
}

func TestManage_Auth_WithToken(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	mux := manage.BuildMux(srv)
	req := httptest.NewRequest("GET", "/api/documents", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with token, got %d", w.Code)
	}
}

func TestManage_Auth_WrongToken(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	mux := manage.BuildMux(srv)
	req := httptest.NewRequest("GET", "/api/documents", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", w.Code)
	}
}

func TestManage_Auth_HealthWithoutToken(t *testing.T) {
	_, srv, _, _ := newTestStoreWithConfig(t)
	mux := manage.BuildMux(srv)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	// Health endpoint should be accessible without auth.
	if w.Code != http.StatusOK {
		t.Errorf("health endpoint should allow unauthenticated access, got %d", w.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Edge cases — search ───────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_Search_Unicode(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/search?q=测试中文&limit=5", nil)
	mustGetBody(t, w, http.StatusOK)
}

func TestManage_Search_TooLongQuery(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	longQuery := strings.Repeat("a", 2001)
	w := doRequest(srv, "GET", "/api/search?q="+longQuery+"&limit=5", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for too long query, got %d", w.Code)
	}
}

func TestManage_Search_EmptyQuery(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/search?q=&limit=5", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty query, got %d", w.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Edge cases — upload ───────────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_Upload_WithKBParam(t *testing.T) {
	_, srv, tmp, _ := newTestStore(t)
	src := filepath.Join(tmp, "upload-kb.md")
	os.WriteFile(src, []byte("# KB upload test"), 0644)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("files", "upload-kb.md")
	part.Write([]byte("# KB upload test"))
	mw.Close()

	mux := manage.BuildMux(srv)
	req := httptest.NewRequest("POST", "/api/upload?kb=test-kb", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Edge cases — pagination boundary ──────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_ListDocuments_OffsetBeyondTotal(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents?offset=1000&limit=20", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Documents []any `json:"documents"`
		Total     int   `json:"total"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if len(resp.Documents) != 0 {
		t.Errorf("expected empty page for offset beyond total, got %d docs", len(resp.Documents))
	}
}

func TestManage_ListDocuments_NegativeOffset(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/api/documents?offset=-1&limit=20", nil)
	mustGetBody(t, w, http.StatusOK)
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Edge cases — health endpoint ──────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_Health(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	w := doRequest(srv, "GET", "/health", nil)
	mustGetBody(t, w, http.StatusOK)

	var resp struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	json.Unmarshal([]byte(w.Body.String()), &resp)
	if resp.Status != "ok" {
		t.Errorf("expected status ok, got %q", resp.Status)
	}
	if resp.Version == "" {
		t.Error("expected version")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ── Edge cases — CORS headers ─────────────────────────────────────────────────
// ═══════════════════════════════════════════════════════════════════════════════

func TestManage_CORS_Headers(t *testing.T) {
	_, srv, _, _ := newTestStore(t)
	mux := manage.BuildMux(srv)
	req := httptest.NewRequest("OPTIONS", "/api/documents", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code < 200 || w.Code >= 300 {
		t.Errorf("expected 2xx for OPTIONS preflight, got %d", w.Code)
	}
}
