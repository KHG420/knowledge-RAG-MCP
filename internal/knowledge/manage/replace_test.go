package manage_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/manage"
	"knowledge-mcp/internal/logging"
)

// replaceFaultBackend injects deterministic persistence failures into the real
// upload/removal path used by the replacement handler.
type replaceFaultBackend struct {
	knowledge.StorageBackend
	failIndex  bool
	failRemove bool
	indexSlug  string
}

func (b *replaceFaultBackend) WriteChunksIndex(kb, slug string, idx *knowledge.ChunksIndex) error {
	if b.failIndex {
		b.indexSlug = slug
		return errors.New("injected index persistence failure")
	}
	return b.StorageBackend.WriteChunksIndex(kb, slug, idx)
}

func (b *replaceFaultBackend) RemoveDocument(kb, slug string) error {
	if b.failRemove {
		return errors.New("injected remove failure")
	}
	return b.StorageBackend.RemoveDocument(kb, slug)
}

// newReplaceServer builds a manage server over the supplied backend and uploads
// one original document so replacement behaviour can be exercised end to end.
func newReplaceServer(t *testing.T, backend knowledge.StorageBackend, original string) (*knowledge.Store, *manage.Server, string) {
	t.Helper()
	tmp := t.TempDir()
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

	src := filepath.Join(tmp, "original.md")
	if err := os.WriteFile(src, []byte(original), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	meta, err := store.UploadDocument(src)
	if err != nil {
		t.Fatalf("upload original: %v", err)
	}
	if meta.Slug == "" {
		t.Fatal("original upload returned an empty slug")
	}
	return store, srv, meta.Slug
}

func replaceBody(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func TestDocReplace_SuccessKeepsResponseShapeAndRemovesOld(t *testing.T) {
	backend := &replaceFaultBackend{StorageBackend: knowledge.NewMockBackend()}
	store, srv, oldSlug := newReplaceServer(t, backend, "ORIGINALMARKER reserve 917 litres.\n")

	body, ctype := replaceBody(t, "replacement.md", "REPLACEMENTMARKER reserve 919 litres.\n")
	w := doMultipartRequest(srv, "PUT", "/api/documents/"+oldSlug, body, ctype)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Message string `json:"message"`
		OldSlug string `json:"oldSlug"`
		NewSlug string `json:"newSlug"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if resp.Message != "document replaced" || resp.OldSlug != oldSlug {
		t.Fatalf("unexpected response shape: %+v", resp)
	}
	if resp.NewSlug == "" || resp.NewSlug == oldSlug {
		t.Fatalf("newSlug must be a distinct persisted slug, got %q", resp.NewSlug)
	}
	if resp.Name != "replacement.md" {
		t.Fatalf("name=%q, want replacement.md", resp.Name)
	}

	// The old document is gone; the new content is readable.
	if _, err := store.ReadMeta(oldSlug); err == nil {
		t.Fatal("old document metadata should have been removed")
	}
	meta, err := store.ReadMeta(resp.NewSlug)
	if err != nil || meta.OriginalName != "replacement.md" {
		t.Fatalf("new document metadata not readable: %v (%+v)", err, meta)
	}
	raw, err := backend.ReadRawText("test-kb", resp.NewSlug)
	if err != nil || !strings.Contains(raw, "REPLACEMENTMARKER") {
		t.Fatalf("new raw text not persisted: %v (%q)", err, raw)
	}
	if _, err := store.ReadChunk(resp.NewSlug, "000"); err != nil {
		t.Fatalf("new chunk not readable: %v", err)
	}
}

func TestDocReplace_UploadFailurePreservesOriginalAndCleansPartial(t *testing.T) {
	backend := &replaceFaultBackend{StorageBackend: knowledge.NewMockBackend()}
	store, srv, oldSlug := newReplaceServer(t, backend, "ORIGINALMARKER reserve 917 litres.\n")

	// Fail the new upload at chunk-index persistence.
	backend.failIndex = true
	body, ctype := replaceBody(t, "failed-candidate.md", "CANDIDATEMARKER reserve 918 litres.\n")
	w := doMultipartRequest(srv, "PUT", "/api/documents/"+oldSlug, body, ctype)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	// Original metadata, chunks and raw text must remain usable.
	meta, err := store.ReadMeta(oldSlug)
	if err != nil {
		t.Fatalf("original metadata must survive a failed replacement: %v", err)
	}
	if meta.OriginalName != "original.md" {
		t.Fatalf("original metadata changed: %+v", meta)
	}
	if _, err := store.ReadChunk(oldSlug, "000"); err != nil {
		t.Fatalf("original chunk must survive: %v", err)
	}
	raw, err := backend.ReadRawText("test-kb", oldSlug)
	if err != nil || !strings.Contains(raw, "ORIGINALMARKER") {
		t.Fatalf("original raw text must survive: %v (%q)", err, raw)
	}

	// The partial replacement slug must have been cleaned up.
	slugs, err := backend.ListDocSlugs("test-kb")
	if err != nil {
		t.Fatalf("ListDocSlugs: %v", err)
	}
	if len(slugs) != 1 || slugs[0] != oldSlug {
		t.Fatalf("partial replacement was not cleaned up: slugs=%v want [%s]", slugs, oldSlug)
	}
	if backend.indexSlug == "" {
		t.Fatal("index persistence failure was never injected")
	}
}

func TestDocReplace_ParseFailurePreservesOriginal(t *testing.T) {
	backend := &replaceFaultBackend{StorageBackend: knowledge.NewMockBackend()}
	store, srv, oldSlug := newReplaceServer(t, backend, "ORIGINALMARKER reserve 917 litres.\n")

	// An empty document produces no chunks and fails before a slug is created.
	body, ctype := replaceBody(t, "empty.md", "")
	w := doMultipartRequest(srv, "PUT", "/api/documents/"+oldSlug, body, ctype)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for an unparseable replacement, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := store.ReadMeta(oldSlug); err != nil {
		t.Fatalf("original metadata must survive a parse failure: %v", err)
	}
	slugs, err := backend.ListDocSlugs("test-kb")
	if err != nil {
		t.Fatalf("ListDocSlugs: %v", err)
	}
	if len(slugs) != 1 || slugs[0] != oldSlug {
		t.Fatalf("parse failure created artifacts: slugs=%v want [%s]", slugs, oldSlug)
	}
}

func TestDocReplace_OldRemoveFailureKeepsNewDocument(t *testing.T) {
	backend := &replaceFaultBackend{StorageBackend: knowledge.NewMockBackend()}
	store, srv, oldSlug := newReplaceServer(t, backend, "ORIGINALMARKER reserve 917 litres.\n")

	// The new upload succeeds, but removing the old document fails.
	backend.failRemove = true
	body, ctype := replaceBody(t, "replacement.md", "REPLACEMENTMARKER reserve 919 litres.\n")
	w := doMultipartRequest(srv, "PUT", "/api/documents/"+oldSlug, body, ctype)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("old-removal failure must not be reported as success, got %d: %s", w.Code, w.Body.String())
	}

	// The fully persisted new document must be kept.
	slugs, err := backend.ListDocSlugs("test-kb")
	if err != nil {
		t.Fatalf("ListDocSlugs: %v", err)
	}
	var newSlug string
	for _, s := range slugs {
		if s != oldSlug {
			newSlug = s
		}
	}
	if newSlug == "" {
		t.Fatal("the new document must be kept when only old removal fails")
	}
	raw, err := backend.ReadRawText("test-kb", newSlug)
	if err != nil || !strings.Contains(raw, "REPLACEMENTMARKER") {
		t.Fatalf("new document content was lost: %v (%q)", err, raw)
	}
	// The old document still exists because its removal failed.
	if _, err := store.ReadMeta(oldSlug); err != nil {
		t.Fatalf("old document unexpectedly removed: %v", err)
	}
}

// ensure the fault backend really satisfies the interface used by the handler.
var _ knowledge.StorageBackend = (*replaceFaultBackend)(nil)
