package knowledge

import (
	"math"
	"math/rand"
	"os"
	"testing"
)

func TestHNSWIndex_Basic(t *testing.T) {
	idx := NewHNSWIndex(4)
	if idx.Len() != 0 {
		t.Fatalf("expected empty index, got %d", idx.Len())
	}

	// Add vectors.
	vecs := [][]float64{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
		{0, 0, 0, 1},
		{1, 1, 0, 0},
	}
	for i, v := range vecs {
		idx.Add(string(rune('A'+i)), v)
	}
	if idx.Len() != 5 {
		t.Fatalf("expected 5 vectors, got %d", idx.Len())
	}

	// Search should return the most similar vector.
	hits := idx.Search([]float64{1, 0, 0, 0}, 3)
	if len(hits) == 0 {
		t.Fatal("expected non-empty search results")
	}
	if hits[0].ID != "A" {
		t.Errorf("expected closest hit to be A, got %s (score=%.4f)", hits[0].ID, hits[0].Score)
	}
	if hits[0].Score < 0.99 {
		t.Errorf("expected score near 1.0 for exact match, got %.4f", hits[0].Score)
	}
}

func TestHNSWIndex_Update(t *testing.T) {
	idx := NewHNSWIndex(4)

	idx.Add("X", []float64{1, 0, 0, 0})
	hits := idx.Search([]float64{1, 0, 0, 0}, 1)
	if hits[0].Score < 0.99 {
		t.Fatalf("expected near-perfect score, got %.4f", hits[0].Score)
	}

	// Replace with a different vector.
	idx.Add("X", []float64{0, 0, 0, 1})
	hits = idx.Search([]float64{1, 0, 0, 0}, 3)
	// X should no longer be near [1,0,0,0].
	for _, h := range hits {
		if h.ID == "X" && h.Score > 0.5 {
			t.Errorf("X was updated but still scores high on old query: %.4f", h.Score)
		}
	}
}

func TestHNSWIndex_Remove(t *testing.T) {
	idx := NewHNSWIndex(4)
	idx.Add("A", []float64{1, 0, 0, 0})
	idx.Add("B", []float64{0, 1, 0, 0})

	if idx.Len() != 2 {
		t.Fatalf("expected 2, got %d", idx.Len())
	}
	idx.Remove("A")
	if idx.Len() != 1 {
		t.Fatalf("expected 1 after removal, got %d", idx.Len())
	}

	hits := idx.Search([]float64{1, 0, 0, 0}, 1)
	if len(hits) == 0 || hits[0].ID == "A" {
		t.Error("removed vector A should not appear in search results")
	}
}

func TestHNSWIndex_LargeScale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-scale test in short mode")
	}

	dim := 128
	idx := NewHNSWIndex(dim)
	idx.SetEfSearch(128)
	idx.efConstruction = 200

	n := 500
	rng := rand.New(rand.NewSource(42))
	allVecs := make([][]float64, n)
	for i := 0; i < n; i++ {
		v := make([]float64, dim)
		for j := range v {
			v[j] = rng.NormFloat64()
		}
		// Normalise.
		var norm float64
		for _, x := range v {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		for j := range v {
			v[j] /= norm
		}
		allVecs[i] = v
		idx.Add(string(rune('A'+i%26))+string(rune('a'+i/26)), v)
	}

	if idx.Len() != n {
		t.Fatalf("expected %d, got %d", n, idx.Len())
	}

	// Query with known vectors and verify recall.
	misses := 0
	for trial := 0; trial < 10; trial++ {
		qi := rng.Intn(n)
		query := allVecs[qi]

		hits := idx.Search(query, 20)
		if len(hits) == 0 {
			t.Fatal("expected non-empty results")
		}

		queryID := string(rune('A'+qi%26)) + string(rune('a'+qi/26))
		found := false
		for _, h := range hits {
			if h.ID == queryID {
				found = true
				if h.Score < 0.99 {
					t.Errorf("expected near-perfect score for self-match, got %.4f", h.Score)
				}
				break
			}
		}
		if !found {
			misses++
		}
	}
	// Allow at most 1 miss; >1 indicates a structural issue.
	if misses > 1 {
		t.Errorf("self-match recall: %d/10 misses (expected <= 1)", misses)
	}
}

func TestHNSWIndex_Persistence(t *testing.T) {
	idx := NewHNSWIndex(4)
	idx.Add("A", []float64{1, 0, 0, 0})
	idx.Add("B", []float64{0, 1, 0, 0})
	idx.Add("C", []float64{0, 0, 1, 0})

	path := t.TempDir() + "/test_vector.gob"
	if err := idx.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := LoadHNSWIndex(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Len() != 3 {
		t.Fatalf("expected 3 after load, got %d", loaded.Len())
	}

	hits := loaded.Search([]float64{1, 0, 0, 0}, 3)
	if len(hits) == 0 || hits[0].ID != "A" {
		t.Error("loaded index produced wrong results")
	}
}

func TestHNSWIndex_EmptyFile(t *testing.T) {
	loaded, err := LoadHNSWIndex("/nonexistent/path/vector.gob")
	if err != nil || loaded != nil {
		t.Error("expected nil,nil for nonexistent file")
	}
}

func TestHNSWIndex_EmptySearch(t *testing.T) {
	idx := NewHNSWIndex(4)
	hits := idx.Search([]float64{1, 0, 0, 0}, 10)
	if len(hits) != 0 {
		t.Error("expected empty results on empty index")
	}
}

func TestHNSWIndex_WrongDim(t *testing.T) {
	idx := NewHNSWIndex(4)
	idx.Add("A", []float64{1, 2, 3}) // wrong dim — should be silently ignored
	if idx.Len() != 0 {
		t.Error("expected wrong-dim vector to be ignored")
	}

	hits := idx.Search([]float64{1, 2, 3}, 10) // wrong dim — should return nil
	if len(hits) != 0 {
		t.Error("expected empty results for wrong-dim query")
	}
}

func TestHNSWIndex_CorruptFile(t *testing.T) {
	path := t.TempDir() + "/corrupt.gob"
	if err := os.WriteFile(path, []byte("not a gob file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadHNSWIndex(path)
	if err == nil {
		t.Error("expected error loading corrupt file")
	}
}

// ── VectorID / ParseVectorID tests ─────────────────────────────────────────

func TestVectorID(t *testing.T) {
	tests := []struct {
		slug, chunkID, want string
	}{
		{"doc", "005", "doc/005"},
		{"横摇论文", "S00", "横摇论文/S00"},
		{"a/b", "c", "a/b/c"}, // slug with slash is legal (though unusual)
		{"", "chunk", "/chunk"},
		{"slug", "", "slug/"},
	}
	for _, tt := range tests {
		got := VectorID(tt.slug, tt.chunkID)
		if got != tt.want {
			t.Errorf("VectorID(%q, %q) = %q, want %q", tt.slug, tt.chunkID, got, tt.want)
		}
	}
}

func TestParseVectorID(t *testing.T) {
	tests := []struct {
		id           string
		wantSlug     string
		wantChunkID  string
		wantOK       bool
	}{
		{"doc/005", "doc", "005", true},
		{"横摇论文/S00", "横摇论文", "S00", true},
		{"a/b/c", "a", "b/c", true}, // only first slash splits
		{"nodash", "", "", false},
		{"", "", "", false},
		{"/onlychunk", "", "onlychunk", true},
		{"slug/", "slug", "", true},
	}
	for _, tt := range tests {
		slug, chunkID, ok := ParseVectorID(tt.id)
		if ok != tt.wantOK {
			t.Errorf("ParseVectorID(%q) ok=%v, want %v", tt.id, ok, tt.wantOK)
		}
		if slug != tt.wantSlug || chunkID != tt.wantChunkID {
			t.Errorf("ParseVectorID(%q) = (%q, %q), want (%q, %q)",
				tt.id, slug, chunkID, tt.wantSlug, tt.wantChunkID)
		}
	}
}

func TestVectorIDRoundTrip(t *testing.T) {
	pairs := [][2]string{
		{"doc", "005"},
		{"横摇", "S02"},
		{"slug", ""},
		{"", "onlychunk"},
	}
	for _, p := range pairs {
		id := VectorID(p[0], p[1])
		s, c, ok := ParseVectorID(id)
		if !ok {
			t.Errorf("ParseVectorID(%q) roundtrip failed", id)
		}
		if s != p[0] || c != p[1] {
			t.Errorf("roundtrip mismatch: (%q,%q) → %q → (%q,%q)", p[0], p[1], id, s, c)
		}
	}

	// Non-roundtrip case: slug contains "/".
	// VectorID("a/b", "c") = "a/b/c", ParseVectorID splits on first "/" → ("a", "b/c").
	// This is expected — slugs should not contain "/".
	id := VectorID("a/b", "c")
	s, c, ok := ParseVectorID(id)
	if !ok {
		t.Error("ParseVectorID should succeed")
	}
	if s != "a" || c != "b/c" {
		t.Errorf("non-roundtrip: VectorID(a/b,c)=%q, ParseVectorID→(%q,%q)", id, s, c)
	}
}
