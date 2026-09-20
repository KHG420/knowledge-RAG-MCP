package search

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/chunkstore"
	"knowledge-mcp/internal/logging"
)

// collectFaultBackend wraps a real StorageBackend and injects read faults for
// selected slugs/KBs so the production collect path can be exercised without
// mocking the search engine itself.
type collectFaultBackend struct {
	knowledge.StorageBackend
	failMetaSlugs  map[string]bool
	failIndexSlugs map[string]bool
	failChunkIDs   map[string]bool
	failListChunks map[string]bool
	failListDocs   bool
	invertedIdxErr error
}

func (b *collectFaultBackend) ReadMeta(kb, slug string) (*knowledge.DocumentMeta, error) {
	if b.failMetaSlugs[slug] {
		return nil, errors.New("injected metadata read failure")
	}
	return b.StorageBackend.ReadMeta(kb, slug)
}

func (b *collectFaultBackend) ReadChunksIndex(kb, slug string) (*knowledge.ChunksIndex, error) {
	if b.failIndexSlugs[slug] {
		return nil, errors.New("injected chunks index read failure")
	}
	return b.StorageBackend.ReadChunksIndex(kb, slug)
}

func (b *collectFaultBackend) ReadChunk(kb, slug, chunkID string) (string, error) {
	if b.failChunkIDs[slug] {
		return "", errors.New("injected chunk read failure")
	}
	return b.StorageBackend.ReadChunk(kb, slug, chunkID)
}

func (b *collectFaultBackend) ListChunkIDs(kb, slug string) ([]string, error) {
	if b.failListChunks[slug] {
		return nil, errors.New("injected list chunks failure")
	}
	return b.StorageBackend.ListChunkIDs(kb, slug)
}

func (b *collectFaultBackend) ListDocSlugs(kb string) ([]string, error) {
	if b.failListDocs {
		return nil, errors.New("injected list documents failure")
	}
	return b.StorageBackend.ListDocSlugs(kb)
}

func (b *collectFaultBackend) ReadInvertedIndex(kb string) (*knowledge.InvertedIndex, error) {
	if b.invertedIdxErr != nil {
		return nil, b.invertedIdxErr
	}
	return b.StorageBackend.ReadInvertedIndex(kb)
}

func newCollectEngine(t *testing.T, backend knowledge.StorageBackend, kb string) *Engine {
	t.Helper()
	if err := backend.CreateKB(kb, ""); err != nil {
		t.Fatalf("CreateKB(%s): %v", kb, err)
	}
	logger := logging.NewNopLogger()
	mu := &sync.Mutex{}
	cs := chunkstore.New(backend, kb, t.TempDir(), mu, logger)
	e := New(mu, logger)
	e.SetBackend(backend)
	e.SetChunkStore(cs)
	e.SetKBName(kb)
	return e
}

// seedIndexedDoc writes metadata, a raw chunk and a chunk index entry whose
// indexed term is `term`.
func seedIndexedDoc(t *testing.T, backend knowledge.StorageBackend, kb, slug, term string) {
	t.Helper()
	if err := backend.WriteMeta(kb, slug, &knowledge.DocumentMeta{
		Slug: slug, OriginalName: slug + ".md", SourceType: "md", Title: slug,
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := backend.WriteChunk(kb, slug, "000", "text about "+term); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	if err := backend.WriteChunksIndex(kb, slug, &knowledge.ChunksIndex{
		Slug: slug, ChunkCount: 1,
		Chunks: []knowledge.ChunkIndexEntry{{
			ID: "000", TermCount: 1,
			Terms: []knowledge.TermFreq{{Term: term, Count: 1}},
		}},
	}); err != nil {
		t.Fatalf("WriteChunksIndex: %v", err)
	}
}

// seedInvertedCandidate publishes one posting so the inverted-index fast path
// selects the slug as a candidate.
func seedInvertedCandidate(t *testing.T, backend knowledge.StorageBackend, kb, slug, term string) {
	t.Helper()
	idx := knowledge.NewInvertedIndex()
	idx.Index[term] = []knowledge.Posting{{DocSlug: slug, ChunkID: "000", TF: 1}}
	if err := backend.WriteInvertedIndex(kb, idx); err != nil {
		t.Fatalf("WriteInvertedIndex: %v", err)
	}
}

func TestSearch_FullScanMetadataErrorPropagates(t *testing.T) {
	mock := knowledge.NewMockBackend()
	backend := &collectFaultBackend{StorageBackend: mock, failMetaSlugs: map[string]bool{"doc": true}}
	e := newCollectEngine(t, backend, "kb")
	seedIndexedDoc(t, backend, "kb", "doc", "roll")

	_, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err == nil {
		t.Fatal("a metadata read failure must not be reported as an empty result")
	}
	if !strings.Contains(err.Error(), "read meta") {
		t.Fatalf("error should identify the metadata read failure, got %v", err)
	}
}

func TestSearch_FullScanIndexErrorPropagates(t *testing.T) {
	mock := knowledge.NewMockBackend()
	backend := &collectFaultBackend{StorageBackend: mock, failIndexSlugs: map[string]bool{"doc": true}}
	e := newCollectEngine(t, backend, "kb")
	seedIndexedDoc(t, backend, "kb", "doc", "roll")

	_, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err == nil {
		t.Fatal("a chunks-index read failure must not be reported as an empty result")
	}
	if !strings.Contains(err.Error(), "read chunks index") {
		t.Fatalf("error should identify the chunks-index failure, got %v", err)
	}
}

func TestSearch_FullScanChunkReadErrorPropagates(t *testing.T) {
	mock := knowledge.NewMockBackend()
	backend := &collectFaultBackend{StorageBackend: mock, failChunkIDs: map[string]bool{"doc": true}}
	e := newCollectEngine(t, backend, "kb")
	// No chunks index, so the full scan must fall back to raw chunks — which
	// then fail to read.
	if err := backend.WriteMeta("kb", "doc", &knowledge.DocumentMeta{
		Slug: "doc", OriginalName: "doc.md", SourceType: "md",
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := backend.WriteChunk("kb", "doc", "000", "text about roll"); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	_, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err == nil {
		t.Fatal("a chunk read failure must not be reported as an empty result")
	}
	if !strings.Contains(err.Error(), "read chunk") {
		t.Fatalf("error should identify the chunk read failure, got %v", err)
	}
}

func TestSearch_CandidateMetadataErrorPropagates(t *testing.T) {
	mock := knowledge.NewMockBackend()
	backend := &collectFaultBackend{StorageBackend: mock, failMetaSlugs: map[string]bool{"doc": true}}
	e := newCollectEngine(t, backend, "kb")
	seedIndexedDoc(t, backend, "kb", "doc", "roll")
	seedInvertedCandidate(t, backend, "kb", "doc", "roll")

	_, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err == nil {
		t.Fatal("a candidate metadata read failure must surface, not silently drop the candidate")
	}
	if !strings.Contains(err.Error(), "read meta") {
		t.Fatalf("error should identify the metadata read failure, got %v", err)
	}
}

func TestSearch_CandidateMissingIndexPropagates(t *testing.T) {
	mock := knowledge.NewMockBackend()
	backend := &collectFaultBackend{StorageBackend: mock}
	e := newCollectEngine(t, backend, "kb")
	// Metadata exists and the doc is an inverted-index candidate, but there is
	// no per-document chunk index.
	if err := backend.WriteMeta("kb", "doc", &knowledge.DocumentMeta{
		Slug: "doc", OriginalName: "doc.md", SourceType: "md",
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := backend.WriteChunk("kb", "doc", "000", "text about roll"); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	seedInvertedCandidate(t, backend, "kb", "doc", "roll")

	_, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err == nil {
		t.Fatal("an indexed candidate with a missing chunk index must surface a failure")
	}
	if !strings.Contains(err.Error(), "index missing for indexed candidate") {
		t.Fatalf("error should identify the missing index, got %v", err)
	}
}

func TestSearch_CandidateIndexReadErrorPropagates(t *testing.T) {
	mock := knowledge.NewMockBackend()
	backend := &collectFaultBackend{StorageBackend: mock, failIndexSlugs: map[string]bool{"doc": true}}
	e := newCollectEngine(t, backend, "kb")
	seedIndexedDoc(t, backend, "kb", "doc", "roll")
	seedInvertedCandidate(t, backend, "kb", "doc", "roll")

	_, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err == nil {
		t.Fatal("a candidate chunks-index read failure must surface")
	}
	if !strings.Contains(err.Error(), "read chunks index") {
		t.Fatalf("error should identify the chunks-index failure, got %v", err)
	}
}

func TestSearch_InvertedIndexReadErrorFallsBackToHealthyFullScan(t *testing.T) {
	mock := knowledge.NewMockBackend()
	backend := &collectFaultBackend{StorageBackend: mock, invertedIdxErr: errors.New("injected inverted index failure")}
	e := newCollectEngine(t, backend, "kb")
	seedIndexedDoc(t, backend, "kb", "doc", "roll")

	hits, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err != nil {
		t.Fatalf("a failed inverted-index read must fall back to a healthy full scan, got %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("full-scan fallback should still find the matching document")
	}
}

func TestSearch_FailedFallbackFullScanPropagates(t *testing.T) {
	mock := knowledge.NewMockBackend()
	backend := &collectFaultBackend{
		StorageBackend: mock,
		invertedIdxErr: errors.New("injected inverted index failure"),
		failListDocs:   true,
	}
	e := newCollectEngine(t, backend, "kb")
	seedIndexedDoc(t, backend, "kb", "doc", "roll")

	_, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err == nil {
		t.Fatal("when the inverted index and the fallback full scan both fail, the error must propagate")
	}
	if !strings.Contains(err.Error(), "list documents") {
		t.Fatalf("error should identify the full-scan listing failure, got %v", err)
	}
}

func TestSearch_LegitimateEmptyKBReturnsEmpty(t *testing.T) {
	e := newCollectEngine(t, knowledge.NewMockBackend(), "kb")

	hits, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{})
	if err != nil {
		t.Fatalf("an empty KB is a successful empty result, got error %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty KB should return no hits, got %d", len(hits))
	}
}

func TestSearch_NoMatchingQueryReturnsEmpty(t *testing.T) {
	backend := knowledge.NewMockBackend()
	e := newCollectEngine(t, backend, "kb")
	seedIndexedDoc(t, backend, "kb", "doc", "damping")

	hits, err := e.Search(context.Background(), "unrelatedterm", 8, knowledge.SearchFilter{})
	if err != nil {
		t.Fatalf("a no-match query is a successful empty result, got error %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("no-match query should return no hits, got %d", len(hits))
	}
}

func TestSearch_FilteredOutQueryReturnsEmpty(t *testing.T) {
	backend := knowledge.NewMockBackend()
	e := newCollectEngine(t, backend, "kb")
	seedIndexedDoc(t, backend, "kb", "doc", "roll")

	// The only document is sourceType "md"; filtering on "pdf" removes it.
	hits, err := e.Search(context.Background(), "roll", 8, knowledge.SearchFilter{SourceType: "pdf"})
	if err != nil {
		t.Fatalf("a filtered-out result is a successful empty result, got error %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("filter should exclude the only document, got %d hits", len(hits))
	}
}

func TestSearch_FullScanWithoutIndexUsesRawChunks(t *testing.T) {
	backend := knowledge.NewMockBackend()
	e := newCollectEngine(t, backend, "kb")
	// Metadata and raw chunks exist, but there is deliberately no chunks index.
	if err := backend.WriteMeta("kb", "doc", &knowledge.DocumentMeta{
		Slug: "doc", OriginalName: "doc.md", SourceType: "md",
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := backend.WriteChunk("kb", "doc", "000", "text about roll damping"); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	hits, err := e.Search(context.Background(), "roll damping", 8, knowledge.SearchFilter{})
	if err != nil {
		t.Fatalf("an absent index with readable raw chunks is a legitimate fallback, got %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("raw-chunk fallback should still find the matching document")
	}
}
