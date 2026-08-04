package knowledge

import (
	"testing"
)

// =============================================================================
// Search integration tests — core retrieval pipeline using mock backend
// =============================================================================

// setupSearchStore creates a Store with a mock backend, a KB named "test",
// and populates it with sample documents, chunks, and an inverted index.
func setupSearchStore(t *testing.T) *Store {
	t.Helper()
	backend := newMockBackend()

	// Create KB directly in mock.
	if err := backend.CreateKB("test", "Test KB"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	// Document 1: "ship-roll" — 3 chunks about ship roll damping.
	doc1 := "ship-roll"
	doc1Meta := DocumentMeta{Slug: doc1, OriginalName: "ship-roll.pdf", SourceType: "pdf", Title: "Ship Roll Damping"}
	if err := backend.WriteMeta("test", doc1, &doc1Meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	doc1Entries := []ChunkIndexEntry{
		{ID: "000", Section: "Introduction", TermCount: 7,
			Terms: []termFreq{{"ship", 1}, {"roll", 1}, {"damping", 1}, {"critical", 1}, {"naval", 1}, {"architecture", 1},{ "aspect", 1}}},
		{ID: "001", Section: "Ikeda Method", TermCount: 9,
			Terms: []termFreq{{"ikeda", 1}, {"method", 1}, {"estimates", 1}, {"roll", 1}, {"damping", 1}, {"friction", 1}, {"eddy", 1}, {"wave", 1},{ "components", 1}}},
		{ID: "002", Section: "Bilge Keels", TermCount: 8,
			Terms: []termFreq{{"bilge", 1}, {"keels", 1}, {"increase", 1}, {"roll", 1}, {"damping", 1}, {"higher", 1}, {"speeds", 1},{ "significantly", 1}}},
	}
	for _, e := range doc1Entries {
		if err := backend.WriteChunk("test", doc1, e.ID, "content for "+doc1+"/"+e.ID); err != nil {
			t.Fatalf("WriteChunk: %v", err)
		}
	}
	if err := backend.WriteChunksIndex("test", doc1, &ChunksIndex{Chunks: doc1Entries}); err != nil {
		t.Fatalf("WriteChunksIndex: %v", err)
	}

	// Document 2: "wave-theory" — 2 chunks about wave mechanics.
	doc2 := "wave-theory"
	doc2Meta := DocumentMeta{Slug: doc2, OriginalName: "wave-theory.md", SourceType: "md", Title: "Wave Theory"}
	if err := backend.WriteMeta("test", doc2, &doc2Meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	doc2Entries := []ChunkIndexEntry{
		{ID: "000", Section: "Linear Theory", TermCount: 7,
			Terms: []termFreq{{"linear", 1}, {"wave", 1}, {"theory", 1}, {"foundation", 1}, {"ship", 1}, {"motion", 1}, {"analysis", 1}}},
		{ID: "001", Section: "Frequency Effects", TermCount: 7,
			Terms: []termFreq{{"wave", 1}, {"damping", 1}, {"effects", 1}, {"frequency", 1}, {"encounter", 1}, {"angle", 1},{ "dependent", 1}}},
	}
	for _, e := range doc2Entries {
		if err := backend.WriteChunk("test", doc2, e.ID, "content for "+doc2+"/"+e.ID); err != nil {
			t.Fatalf("WriteChunk: %v", err)
		}
	}
	if err := backend.WriteChunksIndex("test", doc2, &ChunksIndex{Chunks: doc2Entries}); err != nil {
		t.Fatalf("WriteChunksIndex: %v", err)
	}

	store := NewStoreWithBackend(backend).WithKB("test")
	if err := store.rebuildInvertedIndex(); err != nil {
		t.Fatalf("rebuildInvertedIndex: %v", err)
	}
	return store
}

func TestSearch_BasicKeyword(t *testing.T) {
	store := setupSearchStore(t)

	hits, err := store.Search("roll damping", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit for 'roll damping'")
	}
	foundIkeda := false
	for _, h := range hits {
		if h.Document.ID == "ship-roll" && h.Location.ChunkID == "001" {
			foundIkeda = true
		}
	}
	if !foundIkeda {
		t.Errorf("expected ship-roll/001 (Ikeda method) in results, got %d hits", len(hits))
	}
}

func TestHybridSearch_NoEmbedder(t *testing.T) {
	store := setupSearchStore(t)
	hits, err := store.HybridSearch("Ikeda method", 10)
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if hits[0].Document.ID != "ship-roll" || hits[0].Location.ChunkID != "001" {
		t.Errorf("expected ship-roll/001 as top hit, got %s/%s", hits[0].Document.ID, hits[0].Location.ChunkID)
	}
}

func TestSearchAll_CrossKB(t *testing.T) {
	store := setupSearchStore(t)
	backend := store.backend

	// Create a second KB with one document.
	if err := backend.CreateKB("secondary", "Secondary KB"); err != nil {
		t.Fatalf("CreateKB secondary: %v", err)
	}
	docSlug := "extra-doc"
	if err := backend.WriteChunk("secondary", docSlug, "000", "extra content"); err != nil {
		t.Fatalf("WriteChunk secondary: %v", err)
	}
	idx := &ChunksIndex{Chunks: []ChunkIndexEntry{{
		ID: "000", Section: "Intro", TermCount: 6,
		Terms: []termFreq{{"additional", 1}, {"damping", 1}, {"analysis", 1}, {"roll", 1}, {"motion", 1}},
	}}}
	if err := backend.WriteChunksIndex("secondary", docSlug, idx); err != nil {
		t.Fatalf("WriteChunksIndex secondary: %v", err)
	}
	if err := backend.WriteMeta("secondary", docSlug, &DocumentMeta{Slug: docSlug, Title: "Extra"}); err != nil {
		t.Fatalf("WriteMeta secondary: %v", err)
	}
	// Rebuild inverted index for secondary KB.
	store2 := NewStoreWithBackend(backend).WithKB("secondary")
	if err := store2.rebuildInvertedIndex(); err != nil {
		t.Fatalf("rebuildInvertedIndex secondary: %v", err)
	}

	// SearchAll from root store.
	rootStore := NewStoreWithBackend(backend)
	hits, err := rootStore.SearchAll("damping roll", 10)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected cross-KB hits")
	}
	// Verify results from both KBs exist.
	hasTest := false
	hasSecondary := false
	for _, h := range hits {
		switch h.Document.ID {
		case "ship-roll", "wave-theory":
			hasTest = true
		case "extra-doc":
			hasSecondary = true
		}
	}
	if !hasTest || !hasSecondary {
		t.Errorf("expected results from both test and secondary KBs: test=%v secondary=%v", hasTest, hasSecondary)
	}
}

func TestSearch_FilterBySourceType(t *testing.T) {
	store := setupSearchStore(t)

	hits, err := store.Search("wave", 10, SearchFilter{SourceType: "md"})
	if err != nil {
		t.Fatalf("Search with md filter: %v", err)
	}
	for _, h := range hits {
		if h.Document.ID != "wave-theory" {
			t.Errorf("expected only md docs, got %s", h.Document.ID)
		}
	}

	hits, err = store.Search("wave", 10, SearchFilter{SourceType: "pdf"})
	if err != nil {
		t.Fatalf("Search with pdf filter: %v", err)
	}
	for _, h := range hits {
		if h.Document.ID != "ship-roll" {
			t.Errorf("expected only pdf docs, got %s", h.Document.ID)
		}
	}
}

func TestSearch_FilterBySection(t *testing.T) {
	store := setupSearchStore(t)

	hits, err := store.Search("damping", 10, SearchFilter{Section: "Bilge Keels"})
	if err != nil {
		t.Fatalf("Search with section filter: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits in Bilge Keels section")
	}
	for _, h := range hits {
		if h.Location.Section != "Bilge Keels" {
			t.Errorf("expected only Bilge Keels, got section=%q", h.Location.Section)
		}
	}
}

func TestSearch_LimitEnforced(t *testing.T) {
	store := setupSearchStore(t)

	hits, err := store.Search("damping", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) > 2 {
		t.Errorf("expected max 2 hits, got %d", len(hits))
	}
}

func TestSearch_NoMatch(t *testing.T) {
	store := setupSearchStore(t)

	hits, err := store.Search("xyzzy nonexistent term", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits, got %d", len(hits))
	}
}
