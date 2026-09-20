package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"knowledge-mcp/internal/cache"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/chunkstore"
	"knowledge-mcp/internal/logging"
)

// countingCache records DeletePattern calls for invalidation assertions.
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

func writeMarkdown(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newEngine(t *testing.T, backend knowledge.StorageBackend, build func(string, []knowledge.ChunkWithMeta, []knowledge.ChunkWithMeta) error) *Engine {
	t.Helper()
	logger := logging.NewNopLogger()
	mu := &sync.Mutex{}
	if err := backend.CreateKB("kb", ""); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	cs := chunkstore.New(backend, "kb", t.TempDir(), mu, logger)
	e := NewSimple(knowledge.NewUploadTaskManager(t.TempDir(), logger), nil, mu, logger)
	e.SetBackend(backend)
	e.SetChunkStore(cs)
	e.SetDataDir(t.TempDir())
	e.SetKBName("kb")
	if build != nil {
		e.SetBuildChunksIndex(build)
	}
	return e
}

func uploadWithEvents(t *testing.T, e *Engine, path string) (knowledge.DocumentMeta, error, []knowledge.ProgressEvent) {
	t.Helper()
	var events []knowledge.ProgressEvent
	meta, err := e.uploadDocumentWithProgress(path, func(ev knowledge.ProgressEvent) {
		events = append(events, ev)
	})
	return meta, err, events
}

func assertNoSuccessAfterFailure(t *testing.T, events []knowledge.ProgressEvent) {
	t.Helper()
	for _, ev := range events {
		if ev.Stage == knowledge.StageComplete {
			t.Fatalf("emitted %s/%s after a fatal failure", ev.Stage, ev.Status)
		}
		if ev.Stage == knowledge.StageIndexing && ev.Status == "done" {
			t.Fatalf("emitted indexing done after a fatal failure")
		}
	}
}

func TestEngineUpload_IndexBuilderErrorIsFatal(t *testing.T) {
	var builtSlug string
	e := newEngine(t, knowledge.NewMockBackend(), func(slug string, _ []knowledge.ChunkWithMeta, _ []knowledge.ChunkWithMeta) error {
		builtSlug = slug
		return errors.New("injected index persistence failure")
	})
	cache := &countingCache{}
	e.SetCacheClient(cache)

	meta, err, events := uploadWithEvents(t, e, writeMarkdown(t, "# Title\n\nPlain markdown about roll damping."))
	if err == nil {
		t.Fatal("index persistence failure must fail the upload")
	}
	if !strings.Contains(err.Error(), "search index") {
		t.Fatalf("error should mention the search index, got %v", err)
	}
	if builtSlug == "" {
		t.Fatal("index builder was not invoked")
	}
	// A failure after the slug is derived must still return that slug so a
	// replacement caller can clean up the partial document.
	if meta.Slug != builtSlug {
		t.Fatalf("post-slug failure must return generated metadata for cleanup: got %q want %q", meta.Slug, builtSlug)
	}
	if cache.patternCount() == 0 {
		t.Fatal("failed mutation must still invalidate stale caches")
	}
	assertNoSuccessAfterFailure(t, events)
}

func TestEngineUpload_MissingIndexBuilderIsFatal(t *testing.T) {
	e := newEngine(t, knowledge.NewMockBackend(), nil)

	_, err, events := uploadWithEvents(t, e, writeMarkdown(t, "# Title\n\nPlain markdown about roll damping."))
	if err == nil {
		t.Fatal("missing index builder must fail the upload instead of claiming success")
	}
	if !strings.Contains(err.Error(), "index builder not configured") {
		t.Fatalf("error should explain the missing builder, got %v", err)
	}
	assertNoSuccessAfterFailure(t, events)
}

type sourceFailBackend struct {
	knowledge.StorageBackend
}

func (b *sourceFailBackend) WriteSource(string, string, []byte, string) error {
	return errors.New("injected source persistence failure")
}

func TestEngineUpload_SourcePersistenceErrorIsFatal(t *testing.T) {
	e := newEngine(t, &sourceFailBackend{StorageBackend: knowledge.NewMockBackend()},
		func(string, []knowledge.ChunkWithMeta, []knowledge.ChunkWithMeta) error { return nil })

	_, err, events := uploadWithEvents(t, e, writeMarkdown(t, "# Title\n\nPlain markdown about roll damping."))
	if err == nil {
		t.Fatal("source persistence failure must fail the upload")
	}
	if !strings.Contains(err.Error(), "persist source") {
		t.Fatalf("error should mention the source, got %v", err)
	}
	assertNoSuccessAfterFailure(t, events)
}

type rawTextFailBackend struct {
	knowledge.StorageBackend
}

func (b *rawTextFailBackend) WriteRawText(string, string, string) error {
	return errors.New("injected raw text persistence failure")
}

func TestEngineUpload_RawTextPersistenceErrorIsFatal(t *testing.T) {
	e := newEngine(t, &rawTextFailBackend{StorageBackend: knowledge.NewMockBackend()},
		func(string, []knowledge.ChunkWithMeta, []knowledge.ChunkWithMeta) error { return nil })

	_, err, events := uploadWithEvents(t, e, writeMarkdown(t, "# Title\n\nPlain markdown about roll damping."))
	if err == nil {
		t.Fatal("raw text persistence failure must fail the upload")
	}
	if !strings.Contains(err.Error(), "persist raw text") {
		t.Fatalf("error should mention raw text, got %v", err)
	}
	assertNoSuccessAfterFailure(t, events)
}

// Positive case: a plain Markdown upload without an embedder persists chunks
// and metadata, invokes the index builder, and emits the complete event.
func TestEngineUpload_PlainMarkdownBM25OnlySucceeds(t *testing.T) {
	backend := knowledge.NewMockBackend()
	var builtSlug string
	var builtChunks int
	e := newEngine(t, backend, func(slug string, chunks []knowledge.ChunkWithMeta, _ []knowledge.ChunkWithMeta) error {
		builtSlug = slug
		builtChunks = len(chunks)
		return nil
	})

	meta, err, events := uploadWithEvents(t, e, writeMarkdown(t, "# Title\n\nPlain markdown about roll damping and bilge keels."))
	if err != nil {
		t.Fatalf("plain markdown upload failed: %v", err)
	}
	if builtSlug != meta.Slug || builtChunks == 0 {
		t.Fatalf("index builder not invoked with chunk metadata: slug=%q chunks=%d", builtSlug, builtChunks)
	}
	ids, listErr := backend.ListChunkIDs("kb", meta.Slug)
	if listErr != nil || len(ids) == 0 {
		t.Fatalf("chunks were not persisted: %v", listErr)
	}
	readMeta, metaErr := backend.ReadMeta("kb", meta.Slug)
	if metaErr != nil || readMeta == nil || readMeta.OriginalName == "" {
		t.Fatalf("metadata was not persisted: %v", metaErr)
	}
	sawComplete := false
	for _, ev := range events {
		if ev.Stage == knowledge.StageComplete && ev.Status == "done" {
			sawComplete = true
		}
	}
	if !sawComplete {
		t.Fatal("successful upload must emit complete")
	}
}

// TestEngineUpload_IndexFailureInvalidatesRealCache drives the actual
// index-persistence failure path with a real MemCache. It asserts that every
// stale entry for the failed document's family is removed, while entries for
// another document in the same KB and for a different KB survive.
func TestEngineUpload_IndexFailureInvalidatesRealCache(t *testing.T) {
	backend := knowledge.NewMockBackend()
	c := cache.NewMemCache()
	ctx := context.Background()
	var failedSlug string

	e := newEngine(t, backend, func(slug string, _ []knowledge.ChunkWithMeta, _ []knowledge.ChunkWithMeta) error {
		failedSlug = slug
		for _, k := range []string{
			cache.ChunkKey("kb", slug, "000"),
			cache.MetaKey("kb", slug),
			cache.IndexKey("kb", slug),
			"meta:kb:" + slug,
			"index:kb:" + slug,
			cache.QueryKey("kb", "q"),
			// Unrelated document in the same KB, and another KB: must survive.
			cache.ChunkKey("kb", "other", "000"),
			cache.MetaKey("kb", "other"),
			"meta:kb:other",
			"index:kb:other",
			cache.QueryKey("otherkb", "q"),
		} {
			if err := c.Set(ctx, k, []byte("stale"), 0); err != nil {
				t.Fatalf("seed %s: %v", k, err)
			}
		}
		return errors.New("injected index persistence failure")
	})
	e.SetCacheClient(c)

	_, err, _ := uploadWithEvents(t, e, writeMarkdown(t, "# Title\n\nPlain markdown about roll damping."))
	if err == nil {
		t.Fatal("index persistence failure must fail the upload")
	}
	if failedSlug == "" {
		t.Fatal("index builder was not invoked")
	}

	for _, k := range []string{
		cache.ChunkKey("kb", failedSlug, "000"),
		cache.MetaKey("kb", failedSlug),
		cache.IndexKey("kb", failedSlug),
		"meta:kb:" + failedSlug,
		"index:kb:" + failedSlug,
		cache.QueryKey("kb", "q"),
	} {
		if v, _ := c.Get(ctx, k); v != nil {
			t.Errorf("stale cache survived invalidation: %s", k)
		}
	}
	for _, k := range []string{
		cache.ChunkKey("kb", "other", "000"),
		cache.MetaKey("kb", "other"),
		"meta:kb:other",
		"index:kb:other",
		cache.QueryKey("otherkb", "q"),
	} {
		if v, _ := c.Get(ctx, k); v == nil {
			t.Errorf("unrelated cache entry was evicted: %s", k)
		}
	}
}
