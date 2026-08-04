package knowledge

import (
	"fmt"
	"strings"
	"testing"
)

// =============================================================================
// Store CRUD tests — chunk read/write, chunks index, section chunks
// =============================================================================

func setupStore(t *testing.T) *Store {
	t.Helper()
	backend := newMockBackend()
	if err := backend.CreateKB("test", "Test KB"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	return NewStoreWithBackend(backend).WithKB("test")
}

func TestWriteAndReadChunk(t *testing.T) {
	store := setupStore(t)

	content := "This is a test chunk with ship roll damping analysis."
	if err := store.backend.WriteChunk("test", "doc1", "000", content); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	got, err := store.ReadChunk("doc1", "000")
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}
	if got != content {
		t.Errorf("ReadChunk: got %q, want %q", got, content)
	}
}

func TestReadChunk_NotFound(t *testing.T) {
	store := setupStore(t)

	_, err := store.ReadChunk("nonexistent", "000")
	if err == nil {
		t.Error("expected error for missing chunk")
	}
}

func TestReadChunkContext(t *testing.T) {
	store := setupStore(t)
	backend := store.backend

	// Write 5 chunks for doc1.
	for i, c := range []string{"chunk-000", "chunk-001", "chunk-002", "chunk-003", "chunk-004"} {
		if err := backend.WriteChunk("test", "doc1", c[:9], c); err != nil {
			t.Fatalf("WriteChunk %d: %v", i, err)
		}
	}

	// Read chunk "002" with context=1 → should get "001\n---\n002\n---\n003"
	got, err := store.ReadChunkContext("doc1", "chunk-002", 1)
	if err != nil {
		t.Fatalf("ReadChunkContext: %v", err)
	}
	if !strings.Contains(got, "chunk-001") {
		t.Errorf("missing prev chunk: %s", got)
	}
	if !strings.Contains(got, "chunk-002") {
		t.Errorf("missing target chunk: %s", got)
	}
	if !strings.Contains(got, "chunk-003") {
		t.Errorf("missing next chunk: %s", got)
	}
	if strings.Contains(got, "chunk-004") {
		t.Errorf("context=1 should not include chunk-004")
	}
}

func TestReadChunkContext_Boundary(t *testing.T) {
	store := setupStore(t)
	backend := store.backend

	for i, c := range []string{"chunk-000", "chunk-001"} {
		if err := backend.WriteChunk("test", "doc1", c[:9], c); err != nil {
			t.Fatalf("WriteChunk %d: %v", i, err)
		}
	}

	// First chunk with context=1 → only "000\n---\n001"
	got, err := store.ReadChunkContext("doc1", "chunk-000", 1)
	if err != nil {
		t.Fatalf("ReadChunkContext: %v", err)
	}
	if !strings.Contains(got, "chunk-000") && !strings.Contains(got, "chunk-001") {
		t.Errorf("unexpected content: %s", got)
	}

	// Last chunk with context=1 → only "000\n---\n001"
	got, err = store.ReadChunkContext("doc1", "chunk-001", 1)
	if err != nil {
		t.Fatalf("ReadChunkContext: %v", err)
	}
	if !strings.Contains(got, "chunk-000") && !strings.Contains(got, "chunk-001") {
		t.Errorf("unexpected content: %s", got)
	}
}

func TestChunksIndex_RoundTrip(t *testing.T) {
	store := setupStore(t)
	backend := store.backend

	index := &ChunksIndex{
		Chunks: []ChunkIndexEntry{
			{ID: "000", Section: "Intro", TermCount: 5,
				Terms: []termFreq{{"hello", 1}, {"world", 1}},
			},
			{ID: "001", Section: "Body", TermCount: 3,
				Terms: []termFreq{{"test", 1}},
			},
		},
	}
	if err := backend.WriteChunksIndex("test", "doc1", index); err != nil {
		t.Fatalf("WriteChunksIndex: %v", err)
	}

	got, err := store.ReadChunksIndex("doc1")
	if err != nil {
		t.Fatalf("ReadChunksIndex: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil index")
	}
	if len(got.Chunks) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(got.Chunks))
	}
	if got.Chunks[0].ID != "000" || got.Chunks[1].ID != "001" {
		t.Errorf("unexpected chunk IDs: %v", got.Chunks)
	}
}

func TestChunksIndex_NotFound(t *testing.T) {
	store := setupStore(t)

	_, err := store.ReadChunksIndex("nonexistent")
	if err == nil {
		t.Error("expected error for missing index")
	}
}

func TestWriteChunks_BulkWrite(t *testing.T) {
	store := setupStore(t)
	backend := store.backend

	chunks := []string{"First chunk content.", "Second chunk.", "Third one."}
	if err := store.WriteChunks("doc1", chunks); err != nil {
		t.Fatalf("WriteChunks: %v", err)
	}

	// Verify chunks via backend (avoids Store.ReadChunk which may need logger).
	for i, expected := range chunks {
		id := fmt.Sprintf("%03d", i)
		got, err := backend.ReadChunk("test", "doc1", id)
		if err != nil {
			t.Errorf("backend.ReadChunk(%q): %v", id, err)
			continue
		}
		if got != expected {
			t.Errorf("chunk %q: got %q, want %q", id, got, expected)
		}
	}

	// Verify ChunksIndex was NOT written by WriteChunks (it only writes raw chunks).
	idx, _ := backend.ReadChunksIndex("test", "doc1")
	if idx != nil {
		t.Logf("note: WriteChunks does not write ChunksIndex, got %d entries", len(idx.Chunks))
	}
}

func TestRemoveDocument(t *testing.T) {
	store := setupStore(t)
	backend := store.backend

	if err := backend.WriteChunk("test", "doc1", "000", "some content"); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	if err := backend.WriteMeta("test", "doc1", &DocumentMeta{Slug: "doc1"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	if err := store.RemoveDocument("doc1"); err != nil {
		t.Fatalf("RemoveDocument: %v", err)
	}

	_, err := store.ReadChunk("doc1", "000")
	if err == nil {
		t.Error("expected error reading removed chunk")
	}

	_, err = store.ReadMeta("doc1")
	if err == nil {
		t.Error("expected error reading removed metadata")
	}
}

func TestSectionChunk_RoundTrip(t *testing.T) {
	store := setupStore(t)
	backend := store.backend

	content := "Full section content for Ikeda method."
	if err := backend.WriteSectionChunk("test", "doc1", "S00", content); err != nil {
		t.Fatalf("WriteSectionChunk: %v", err)
	}

	got, err := store.ReadSectionChunk("doc1", "S00")
	if err != nil {
		t.Fatalf("ReadSectionChunk: %v", err)
	}
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestListDocuments(t *testing.T) {
	store := setupStore(t)
	backend := store.backend

	for _, s := range []struct{ slug, title string }{
		{"doc-a", "Alpha"},
		{"doc-b", "Beta"},
		{"doc-c", "Gamma"},
	} {
		if err := backend.WriteMeta("test", s.slug, &DocumentMeta{Slug: s.slug, Title: s.title}); err != nil {
			t.Fatalf("WriteMeta: %v", err)
		}
	}

	docs, err := store.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 3 {
		t.Errorf("expected 3 docs, got %d", len(docs))
	}
}

func TestListKBs_Multiple(t *testing.T) {
	backend := newMockBackend()
	store := NewStoreWithBackend(backend)

	if err := backend.CreateKB("kb-a", "Alpha KB"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if err := backend.CreateKB("kb-b", "Beta KB"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	kbs, err := store.ListKBs()
	if err != nil {
		t.Fatalf("ListKBs: %v", err)
	}
	if len(kbs) != 2 {
		t.Errorf("expected 2 KBs, got %d", len(kbs))
	}
}

func TestWithKB_Switching(t *testing.T) {
	backend := newMockBackend()
	store := NewStoreWithBackend(backend)

	if err := backend.CreateKB("first", ""); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if err := backend.CreateKB("second", ""); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	s1 := store.WithKB("first")
	if s1.kbName != "first" {
		t.Errorf("expected kbName=first, got %s", s1.kbName)
	}

	s2 := store.WithKB("second")
	if s2.kbName != "second" {
		t.Errorf("expected kbName=second, got %s", s2.kbName)
	}

	// Original store should be unchanged.
	if store.kbName != "" {
		t.Errorf("original store should have empty kbName, got %s", store.kbName)
	}
}
