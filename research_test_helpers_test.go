package main

import (
	"testing"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/chunkstore"
	"knowledge-mcp/internal/knowledge/search"
	"knowledge-mcp/internal/logging"
)

// newTestResearchStore wires an in-memory Store with a real search engine and
// chunk store, mirroring init.go's production assembly minus MySQL and the
// optional embedder/reranker. The store's dataDir points at the test's temp
// directory so no user files are touched.
func newTestResearchStore(t *testing.T) *knowledge.Store {
	t.Helper()
	return newTestResearchStoreWithBackend(t, knowledge.NewMockBackend())
}

// newTestResearchStoreWithBackend is newTestResearchStore with a caller-supplied
// backend, used to inject failures into the real search path.
func newTestResearchStoreWithBackend(t *testing.T, backend knowledge.StorageBackend) *knowledge.Store {
	t.Helper()
	// Isolate the default data/task directory so tests never read or write the
	// user's real ~/knowledge_base.
	t.Setenv("HOME", t.TempDir())

	store := knowledge.NewStoreWithBackend(backend)
	logger := logging.NewNopLogger()

	eng := search.New(store.Mutex(), logger.WithModule("search"))
	eng.SetBackend(store.Backend())
	eng.SetVecState(store.VecState())
	eng.SetRerankState(store.RerankState())
	store.SetSearchEngine(eng)

	cs := chunkstore.New(store.Backend(), "", t.TempDir(), store.Mutex(), logger.WithModule("chunkstore"))
	store.SetChunkStore(cs)
	eng.SetChunkStore(cs)
	return store
}

// addTestDoc writes a document with the given chunk index entries directly to
// the backend so the real search engine can find it via a full scan.
func addTestDoc(t *testing.T, backend knowledge.StorageBackend, kb, slug, title string, chunks []knowledge.ChunkIndexEntry) {
	t.Helper()
	if err := backend.WriteMeta(kb, slug, &knowledge.DocumentMeta{
		Slug: slug, OriginalName: slug + ".md", SourceType: "md", Title: title,
	}); err != nil {
		t.Fatalf("WriteMeta(%s/%s): %v", kb, slug, err)
	}
	for _, e := range chunks {
		if err := backend.WriteChunk(kb, slug, e.ID, "content for "+slug+"/"+e.ID); err != nil {
			t.Fatalf("WriteChunk(%s/%s/%s): %v", kb, slug, e.ID, err)
		}
	}
	if err := backend.WriteChunksIndex(kb, slug, &knowledge.ChunksIndex{
		Slug: slug, ChunkCount: len(chunks), Chunks: chunks,
	}); err != nil {
		t.Fatalf("WriteChunksIndex(%s/%s): %v", kb, slug, err)
	}
}
