package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingCache records DeletePattern calls so tests can assert that a failed
// mutation still invalidates stale cache entries.
type countingCache struct {
	mu       sync.Mutex
	patterns []string
}

func (c *countingCache) Get(context.Context, string) ([]byte, error) { return nil, nil }
func (c *countingCache) Set(context.Context, string, []byte, time.Duration) error {
	return nil
}
func (c *countingCache) Delete(context.Context, ...string) error { return nil }
func (c *countingCache) DeletePattern(_ context.Context, pattern string) (int64, error) {
	c.mu.Lock()
	c.patterns = append(c.patterns, pattern)
	c.mu.Unlock()
	return 1, nil
}
func (c *countingCache) Ping(context.Context) error { return nil }
func (c *countingCache) Close() error               { return nil }

func (c *countingCache) patternCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.patterns)
}

func writeMarkdownTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// newStoreIsolated builds a Store with HOME pointed at a temp dir so the
// default task-persistence directory never touches the user's real data.
func newStoreIsolated(t *testing.T, backend StorageBackend) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return NewStoreWithBackend(backend)
}

func assertNoUploadSuccessEvent(t *testing.T, events []ProgressEvent) {
	t.Helper()
	for _, ev := range events {
		if ev.Stage == StageComplete {
			t.Fatalf("emitted %s/%s after a fatal failure", ev.Stage, ev.Status)
		}
		if ev.Stage == StageIndexing && ev.Status == "done" {
			t.Fatal("emitted indexing done after a fatal failure")
		}
	}
}

// indexFailBackend injects a CHUNKS.toml persistence failure into the real
// legacy Store upload path.
type indexFailBackend struct {
	*mockBackend
	failIndex  bool
	failedSlug string
}

func (b *indexFailBackend) WriteChunksIndex(kb, slug string, idx *ChunksIndex) error {
	if b.failIndex {
		b.failedSlug = slug
		return errors.New("injected index persistence failure")
	}
	return b.mockBackend.WriteChunksIndex(kb, slug, idx)
}

func TestStoreUploadProgress_IndexPersistenceErrorIsFatal(t *testing.T) {
	backend := &indexFailBackend{mockBackend: newMockBackend(), failIndex: true}
	if err := backend.CreateKB("kb", ""); err != nil {
		t.Fatal(err)
	}
	store := newStoreIsolated(t, backend).WithKB("kb")
	cache := &countingCache{}
	store.SetCache(cache, nil)

	var events []ProgressEvent
	meta, err := store.UploadDocumentWithProgress(
		writeMarkdownTemp(t, "# Title\n\nPlain markdown about roll damping."),
		func(ev ProgressEvent) { events = append(events, ev) },
	)
	if err == nil {
		t.Fatal("index persistence failure must fail the upload")
	}
	if !strings.Contains(err.Error(), "search index") {
		t.Fatalf("error should mention the search index, got %v", err)
	}
	if backend.failedSlug == "" {
		t.Fatal("index write was not attempted")
	}
	// A failure after the slug is derived must still return that slug so a
	// replacement caller can clean up the partial document.
	if meta.Slug != backend.failedSlug {
		t.Fatalf("post-slug failure must return generated metadata for cleanup: got %q want %q", meta.Slug, backend.failedSlug)
	}
	if cache.patternCount() == 0 {
		t.Fatal("failed mutation must still invalidate stale caches")
	}
	assertNoUploadSuccessEvent(t, events)
}

type sourceFailStoreBackend struct {
	*mockBackend
}

func (b *sourceFailStoreBackend) WriteSource(string, string, []byte, string) error {
	return errors.New("injected source persistence failure")
}

func TestStoreUploadProgress_SourcePersistenceErrorIsFatal(t *testing.T) {
	backend := &sourceFailStoreBackend{mockBackend: newMockBackend()}
	if err := backend.CreateKB("kb", ""); err != nil {
		t.Fatal(err)
	}
	store := newStoreIsolated(t, backend).WithKB("kb")

	var events []ProgressEvent
	_, err := store.UploadDocumentWithProgress(
		writeMarkdownTemp(t, "# Title\n\nPlain markdown about roll damping."),
		func(ev ProgressEvent) { events = append(events, ev) },
	)
	if err == nil || !strings.Contains(err.Error(), "persist source") {
		t.Fatalf("source persistence failure must be fatal, got %v", err)
	}
	assertNoUploadSuccessEvent(t, events)
}

type rawFailStoreBackend struct {
	*mockBackend
}

func (b *rawFailStoreBackend) WriteRawText(string, string, string) error {
	return errors.New("injected raw text persistence failure")
}

func TestStoreUploadProgress_RawTextPersistenceErrorIsFatal(t *testing.T) {
	backend := &rawFailStoreBackend{mockBackend: newMockBackend()}
	if err := backend.CreateKB("kb", ""); err != nil {
		t.Fatal(err)
	}
	store := newStoreIsolated(t, backend).WithKB("kb")

	var events []ProgressEvent
	_, err := store.UploadDocumentWithProgress(
		writeMarkdownTemp(t, "# Title\n\nPlain markdown about roll damping."),
		func(ev ProgressEvent) { events = append(events, ev) },
	)
	if err == nil || !strings.Contains(err.Error(), "persist raw text") {
		t.Fatalf("raw text persistence failure must be fatal, got %v", err)
	}
	assertNoUploadSuccessEvent(t, events)
}

// Positive case for the legacy progress path with no embedder: chunks, meta,
// raw text and CHUNKS.toml are all persisted and the upload succeeds.
func TestStoreUploadProgress_PlainMarkdownBM25OnlySucceeds(t *testing.T) {
	backend := newMockBackend()
	if err := backend.CreateKB("kb", ""); err != nil {
		t.Fatal(err)
	}
	store := newStoreIsolated(t, backend).WithKB("kb")

	meta, err := store.UploadDocumentWithProgress(
		writeMarkdownTemp(t, "# Title\n\nPlain markdown about roll damping and bilge keels."),
		nil,
	)
	if err != nil {
		t.Fatalf("plain markdown upload failed: %v", err)
	}
	index, idxErr := store.ReadChunksIndex(meta.Slug)
	if idxErr != nil || index == nil || len(index.Chunks) == 0 {
		t.Fatalf("chunks index not persisted: %v", idxErr)
	}
	raw, rawErr := backend.ReadRawText("kb", meta.Slug)
	if rawErr != nil || raw == "" {
		t.Fatalf("raw text not persisted: %v", rawErr)
	}
	if _, readErr := store.ReadChunk(meta.Slug, "000"); readErr != nil {
		t.Fatalf("persisted chunk not readable: %v", readErr)
	}
}

// nilMetaIngester simulates a misbehaving engine that reports success while
// returning no metadata. Store.UploadDocument must surface an error instead of
// authorizing a replacement deletion on an empty result.
type nilMetaIngester struct{ Ingester }

func (nilMetaIngester) UploadDocument(string, ...string) (*DocumentMeta, error) {
	return nil, nil
}

func TestStoreUploadDocument_NilEngineResultIsError(t *testing.T) {
	store := &Store{ingestSvc: nilMetaIngester{}}
	meta, err := store.UploadDocument("unused.md")
	if err == nil {
		t.Fatalf("nil engine metadata must not be reported as success: meta=%+v", meta)
	}
	if meta.Slug != "" {
		t.Fatalf("nil engine metadata must not fabricate a slug: %q", meta.Slug)
	}
}

// emptySlugIngester reports success with metadata whose slug is empty, which is
// equally unusable for a replacement caller.
type emptySlugIngester struct{ Ingester }

func (emptySlugIngester) UploadDocument(string, ...string) (*DocumentMeta, error) {
	return &DocumentMeta{OriginalName: "x.md"}, nil
}

func TestStoreUploadDocument_EmptySlugIsError(t *testing.T) {
	store := &Store{ingestSvc: emptySlugIngester{}}
	if _, err := store.UploadDocument("unused.md"); err == nil {
		t.Fatal("success with an empty document slug must be reported as an error")
	}
}
