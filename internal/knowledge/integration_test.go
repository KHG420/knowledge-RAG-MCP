package knowledge

import (
	"context"
	"os"
	"strings"
	"testing"
)

// =============================================================================
// bm25Query tests — verify triage-aware BM25 expansion
// =============================================================================

func TestBm25Query_NoRewriter(t *testing.T) {
	store := NewStoreWithBackend(newMockBackend())
	
	got := store.bm25Query("横摇阻尼")
	if got != "横摇阻尼" {
		t.Errorf("no rewriter: got %q, want original", got)
	}
}

func TestBm25Query_SynonymOnly(t *testing.T) {
	store := NewStoreWithBackend(newMockBackend())
	
	rw := NewSynonymRewriter()
	rw.AddSynonym("横摇阻尼", "roll damping")
	store.SetSynonymRewriter(rw)

	got := store.bm25Query("横摇阻尼")
	if !strings.Contains(strings.ToLower(got), "roll damping") {
		t.Errorf("synonym expansion missing: %q", got)
	}
}

func TestBm25Query_TriageSimpleNoLLM(t *testing.T) {
	store := NewStoreWithBackend(newMockBackend())
	
	rw := NewSynonymRewriter()
	rw.AddSynonym("横摇阻尼", "roll damping")
	store.SetSynonymRewriter(rw)

	mockLLM := &mockTextCompleter{resp: "ship roll damping estimation\nmarine roll calculation"}
	store.SetLLMRewriter(NewLLMQueryRewriter(mockLLM).WithFallback(rw))

	got := store.bm25Query("横摇阻尼")
	if strings.Contains(got, "marine") {
		t.Errorf("simple query should NOT call LLM: got %q", got)
	}
}

func TestBm25Query_NoRelatedTerms(t *testing.T) {
	store := NewStoreWithBackend(newMockBackend())
	
	rw := NewSynonymRewriter()
	rw.AddSynonym("横摇阻尼", "roll damping")
	store.SetSynonymRewriter(rw)
	store.SetDictionaryRelatedTerms([]string{"Ikeda method", "bilge keel"})

	got := store.bm25Query("横摇阻尼")
	if strings.Contains(strings.ToLower(got), "ikeda") {
		t.Errorf("related_terms leaked into BM25: %q", got)
	}
}

// =============================================================================
// vectorQuery tests
// =============================================================================

func TestVectorQuery_WithRelatedTerms(t *testing.T) {
	store := NewStoreWithBackend(newMockBackend())
	
	store.SetDictionaryRelatedTerms([]string{"Ikeda method", "bilge keel"})

	got := store.vectorQuery("横摇阻尼")
	if !strings.Contains(got, "Ikeda method") {
		t.Errorf("related_terms missing from vector: %q", got)
	}
}

func TestQueryDecoupling(t *testing.T) {
	store := NewStoreWithBackend(newMockBackend())
	
	rw := NewSynonymRewriter()
	rw.AddSynonym("舭龙骨", "bilge keel")
	store.SetSynonymRewriter(rw)
	store.SetDictionaryRelatedTerms([]string{"roll damping", "eddy making"})

	bm25 := store.bm25Query("舭龙骨")
	if !strings.Contains(strings.ToLower(bm25), "bilge keel") {
		t.Errorf("BM25 missing synonym: %q", bm25)
	}
	if strings.Contains(strings.ToLower(bm25), "eddy making") {
		t.Errorf("BM25 leaked related term: %q", bm25)
	}

	vec := store.vectorQuery("舭龙骨")
	if !strings.Contains(strings.ToLower(vec), "eddy making") {
		t.Errorf("vector missing related term: %q", vec)
	}
}

// =============================================================================
// SynonymRewriter edge tests
// =============================================================================

func TestSynonymRewriter_EmptyOrWhitespace(t *testing.T) {
	rw := NewSynonymRewriter()
	if len(rw.Rewrite("")) != 0 {
		t.Error("empty query should be nil/empty")
	}
	if len(rw.Rewrite("  ")) != 0 {
		t.Error("whitespace-only should be nil/empty")
	}
}

func TestSynonymRewriter_NoMatch(t *testing.T) {
	rw := NewSynonymRewriter()
	got := rw.Rewrite("unique unknown term")
	if len(got) != 1 || got[0] != "unique unknown term" {
		t.Errorf("unmatched: got %v", got)
	}
}

func TestSynonymRewriter_Bidirectional(t *testing.T) {
	rw := NewSynonymRewriter()
	// AddSynonym is unidirectional. For bidirectional, add both directions.
	rw.AddSynonym("横摇阻尼", "roll damping")
	rw.AddSynonym("roll damping", "横摇阻尼")

	got := rw.Rewrite("横摇阻尼计算")
	if !containsAny(got, "roll damping") {
		t.Errorf("CN→EN: %v", got)
	}
	got = rw.Rewrite("roll damping estimation")
	if !containsAny(got, "横摇阻尼") {
		t.Errorf("EN→CN: %v", got)
	}
}

func TestSynonymRewriter_CaseInsensitive(t *testing.T) {
	rw := NewSynonymRewriter()
	rw.AddSynonym("rag", "retrieval augmented generation")
	for _, q := range []string{"RAG pipeline", "Rag pipeline", "rag pipeline"} {
		got := rw.Rewrite(q)
		if !containsAny(got, "retrieval augmented") {
			t.Errorf("case-insensitive fail: %q → %v", q, got)
		}
	}
}

func TestSynonymRewriter_SynonymCount(t *testing.T) {
	rw := NewSynonymRewriter()
	initial := rw.SynonymCount()
	rw.AddSynonym("custom", "bespoke")
	if rw.SynonymCount() != initial+1 {
		t.Errorf("count: got %d, want %d", rw.SynonymCount(), initial+1)
	}
}

// =============================================================================
// LLMQueryRewriter tests
// =============================================================================

type mockTextCompleter struct {
	resp string
	err  error
}

func (m *mockTextCompleter) Complete(ctx context.Context, prompt string) (string, error) {
	return m.resp, m.err
}

func TestLLMQueryRewriter_Success(t *testing.T) {
	mock := &mockTextCompleter{resp: "ship roll damping\nmarine roll calculation"}
	rw := NewLLMQueryRewriter(mock)
	got := rw.Rewrite("横摇阻尼计算")
	if got[0] != "横摇阻尼计算" {
		t.Errorf("original not first: %v", got)
	}
	if len(got) < 3 {
		t.Errorf("expected ≥3 variants, got %d: %v", len(got), got)
	}
}

func TestLLMQueryRewriter_FallbackOnError(t *testing.T) {
	mock := &mockTextCompleter{err: testErr("timeout")}
	fallback := NewSynonymRewriter()
	fallback.AddSynonym("横摇阻尼", "roll damping")
	rw := NewLLMQueryRewriter(mock).WithFallback(fallback)

	got := rw.Rewrite("横摇阻尼计算")
	if !containsAny(got, "roll damping") {
		t.Errorf("fallback not triggered: %v", got)
	}
}

func TestLLMQueryRewriter_CleanVariant(t *testing.T) {
	tests := []struct{ input, want string }{
		{"- ship roll damping", "ship roll damping"},
		{"* marine roll calculation", "marine roll calculation"},
		{"1. compare Ikeda and CFD", "compare Ikeda and CFD"},
		{`"roll damping coefficient"`, "roll damping coefficient"},
	}
	for _, tt := range tests {
		if got := cleanVariant(tt.input); got != tt.want {
			t.Errorf("cleanVariant(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// =============================================================================
// bm25Query: complex query uses LLM
// =============================================================================

func TestBm25Query_TriageComplexUsesLLM(t *testing.T) {
	store := NewStoreWithBackend(newMockBackend())
	
	rw := NewSynonymRewriter()
	store.SetSynonymRewriter(rw)

	mockLLM := &mockTextCompleter{resp: "compare Ikeda CFD for roll damping\nIkeda versus CFD estimation"}
	store.SetLLMRewriter(NewLLMQueryRewriter(mockLLM).WithFallback(rw))

	got := store.bm25Query("a b c d e f g h i j k l m n o") // 15 terms → complex
	if !strings.Contains(got, "Ikeda") {
		t.Errorf("complex query should use LLM: %q", got)
	}
}

// =============================================================================
// Parsing tests (dict_loader)
// =============================================================================

func TestParseDictYAML(t *testing.T) {
	dir := t.TempDir()
	content := "横摇阻尼:\n  exact_synonyms:\n    - roll damping\n    - damping coefficient\n  related_terms:\n    - Ikeda method\n"
	os.WriteFile(dir+"/test.yaml", []byte(content), 0644)

	entries, err := LoadDictionaries(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Term != "横摇阻尼" {
		t.Fatalf("parse failed: %+v", entries)
	}
	if len(entries[0].ExactSynonyms) != 2 {
		t.Errorf("synonyms: got %d", len(entries[0].ExactSynonyms))
	}
}

func TestParseDictYAML_EmptyDir(t *testing.T) {
	entries, err := LoadDictionaries(t.TempDir())
	if err != nil || len(entries) != 0 {
		t.Errorf("empty dir: err=%v, entries=%d", err, len(entries))
	}
}

// =============================================================================
// miner extra boundary tests
// =============================================================================

func TestMineSynonymsFromLog_MissingFile(t *testing.T) {
	_, err := MineSynonymsFromLog("/nonexistent/searchlog.jsonl", 0, 0)
	if err == nil {
		t.Error("missing file should error")
	}
}

func TestExtractKeyTerms_DedupAndStopwords(t *testing.T) {
	got := extractKeyTerms("the roll roll damping damping method")
	for _, term := range got {
		if isStopword(term) {
			t.Errorf("stopword %q leaked", term)
		}
	}
	// "roll" should appear at most once.
	count := 0
	for _, t := range got {
		if t == "roll" {
			count++
		}
	}
	if count > 1 {
		t.Errorf("duplicate 'roll': %v", got)
	}
}

// =============================================================================
// Helpers
// =============================================================================

func containsAny(variants []string, substr string) bool {
	lower := strings.ToLower(substr)
	for _, v := range variants {
		if strings.Contains(strings.ToLower(v), lower) {
			return true
		}
	}
	return false
}

type testErr string

func (e testErr) Error() string { return string(e) }
