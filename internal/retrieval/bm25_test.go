package retrieval

import (
	"testing"
)

func TestTokens_Latin(t *testing.T) {
	tokens := Tokens("Hello World")
	hasHello := false
	hasWorld := false
	for _, tok := range tokens {
		if tok == "hello" {
			hasHello = true
		}
		if tok == "world" {
			hasWorld = true
		}
	}
	if !hasHello || !hasWorld {
		t.Errorf("expected hello and world, got %v", tokens)
	}
}

func TestTokens_CJK(t *testing.T) {
	tokens := Tokens("你好世界")
	uniFound := make(map[string]bool)
	biFound := make(map[string]bool)
	for _, tok := range tokens {
		r := []rune(tok)
		if len(r) == 1 {
			uniFound[tok] = true
		} else if len(r) == 2 {
			biFound[tok] = true
		}
	}
	if !uniFound["你"] || !uniFound["好"] || !uniFound["世"] || !uniFound["界"] {
		t.Errorf("missing CJK unigrams in %v", tokens)
	}
	if !biFound["你好"] || !biFound["好世"] || !biFound["世界"] {
		t.Errorf("missing CJK bigrams in %v", tokens)
	}
}

func TestTokens_Mixed(t *testing.T) {
	tokens := Tokens("AI模型GPT-4")
	found := make(map[string]bool)
	for _, tok := range tokens {
		found[tok] = true
	}
	if !found["ai"] {
		t.Errorf("expected 'ai' token in %v", tokens)
	}
	if !found["gpt"] {
		t.Errorf("expected 'gpt' token in %v", tokens)
	}
	if !found["模"] || !found["型"] {
		t.Errorf("expected CJK unigrams in %v", tokens)
	}
	if !found["模型"] {
		t.Errorf("expected CJK bigram '模型' in %v", tokens)
	}
}

func TestTokens_Empty(t *testing.T) {
	tokens := Tokens("")
	if len(tokens) != 0 {
		t.Errorf("expected empty tokens, got %v", tokens)
	}
}

func TestTokens_Punctuation(t *testing.T) {
	tokens := Tokens("hello, world!")
	if len(tokens) != 2 {
		t.Errorf("expected 2 tokens, got %d: %v", len(tokens), tokens)
	}
}

func TestUnique(t *testing.T) {
	in := []string{"a", "b", "a", "c", "b", "a"}
	out := Unique(in)
	if len(out) != 3 {
		t.Errorf("expected 3 unique tokens, got %d: %v", len(out), out)
	}
	seen := make(map[string]bool)
	for _, s := range out {
		if seen[s] {
			t.Errorf("duplicate token %q in output %v", s, out)
		}
		seen[s] = true
	}
}

func TestUnique_Empty(t *testing.T) {
	if out := Unique(nil); len(out) != 0 {
		t.Errorf("expected empty from nil input, got %v", out)
	}
	if out := Unique([]string{}); len(out) != 0 {
		t.Errorf("expected empty from empty input, got %v", out)
	}
}

func TestCounts(t *testing.T) {
	tokens := []string{"a", "b", "a", "c", "a"}
	counts := Counts(tokens)
	if counts["a"] != 3 {
		t.Errorf("expected count(a)=3, got %d", counts["a"])
	}
	if counts["b"] != 1 {
		t.Errorf("expected count(b)=1, got %d", counts["b"])
	}
	if counts["c"] != 1 {
		t.Errorf("expected count(c)=1, got %d", counts["c"])
	}
}

func TestDocumentFrequency(t *testing.T) {
	docs := []map[string]int{
		{"a": 1, "b": 1},
		{"b": 1, "c": 1},
		{"a": 1},
	}
	df := DocumentFrequency(docs)
	if df["a"] != 2 {
		t.Errorf("expected df(a)=2, got %d", df["a"])
	}
	if df["b"] != 2 {
		t.Errorf("expected df(b)=2, got %d", df["b"])
	}
	if df["c"] != 1 {
		t.Errorf("expected df(c)=1, got %d", df["c"])
	}
}

func TestQueryTerms(t *testing.T) {
	terms, err := QueryTerms("hello world hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(terms) != 2 {
		t.Errorf("expected 2 unique query terms, got %d: %v", len(terms), terms)
	}
	found := make(map[string]bool)
	for _, term := range terms {
		found[term] = true
	}
	if !found["hello"] || !found["world"] {
		t.Errorf("expected hello and world in %v", terms)
	}
}

func TestBM25Score_Basic(t *testing.T) {
	doc := map[string]int{"hello": 2, "world": 1}
	queryTerms := []string{"hello", "world"}
	df := map[string]int{"hello": 10, "world": 5}
	score := BM25Score(doc, 5, queryTerms, df, 100, 10.0)
	if score <= 0 {
		t.Errorf("expected positive BM25 score, got %f", score)
	}
}

func TestBM25Score_MissingTerms(t *testing.T) {
	doc := map[string]int{"hello": 2}
	queryTerms := []string{"nonexistent"}
	df := map[string]int{}
	score := BM25Score(doc, 5, queryTerms, df, 100, 10.0)
	if score != 0 {
		t.Errorf("expected zero score for missing terms, got %f", score)
	}
}

func TestBM25Score_ZeroTermFreq(t *testing.T) {
	doc := map[string]int{"hello": 0}
	queryTerms := []string{"hello"}
	df := map[string]int{"hello": 1}
	score := BM25Score(doc, 0, queryTerms, df, 1, 0.0)
	if score != 0 {
		t.Errorf("expected zero score for zero term frequency, got %f", score)
	}
}

func TestKeepTopRelativeScore(t *testing.T) {
	type item struct {
		name  string
		score float64
	}
	items := []item{
		{name: "a", score: 1.0},
		{name: "b", score: 0.8},
		{name: "c", score: 0.14},
		{name: "d", score: 0.10},
	}
	items = KeepTopRelativeScore(items, 0.15, func(it item) float64 { return it.score })
	if len(items) != 2 {
		t.Errorf("expected 2 items kept, got %d: %v", len(items), items)
	}
	if items[0].name != "a" || items[1].name != "b" {
		t.Errorf("expected a and b, got %v", items)
	}
}

func TestMakeSnippet_Basic(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog. This is another sentence."
	terms, _ := QueryTerms("fox")
	snippet := MakeSnippet(text, "fox", terms, 100)
	if snippet == "" {
		t.Error("expected non-empty snippet")
	}
}

func TestMakeSnippet_CJK(t *testing.T) {
	text := "人工智能技术正在快速发展。深度学习是其中的核心。"
	terms, _ := QueryTerms("深度")
	snippet := MakeSnippet(text, "深度", terms, 50)
	if snippet == "" {
		t.Error("expected non-empty CJK snippet")
	}
}

func TestMakeSnippet_EmptyText(t *testing.T) {
	snippet := MakeSnippet("", "query", nil, 100)
	if snippet != "" {
		t.Errorf("expected empty snippet, got %q", snippet)
	}
}

func TestIsSentenceEnd(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'.', true},
		{'!', true},
		{'?', true},
		{'。', true},
		{'！', true},
		{'？', true},
		{'a', false},
		{' ', false},
	}
	for _, tt := range tests {
		if got := isSentenceEnd(tt.r); got != tt.want {
			t.Errorf("isSentenceEnd(%q) = %v, want %v", tt.r, got, tt.want)
		}
	}
}
