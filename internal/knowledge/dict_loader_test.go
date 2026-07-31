package knowledge

import (
	"strings"
	"testing"
)

func TestDictLoader_ParseYAML(t *testing.T) {
	entries, err := LoadDictionaries("../../dictionaries")
	if err != nil {
		t.Fatalf("LoadDictionaries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries loaded")
	}
	t.Logf("loaded %d dictionary entries", len(entries))

	// Verify a known term.
	found := false
	for _, e := range entries {
		if strings.Contains(e.Term, "同步横摇") || strings.Contains(strings.ToLower(e.Term), "synchronous") {
			found = true
			t.Logf("term=%q exact=%v related=%v", e.Term, e.ExactSynonyms, e.RelatedTerms)
			if len(e.ExactSynonyms) == 0 {
				t.Error("expected exact_synonyms for 同步横摇")
			}
			break
		}
	}
	if !found {
		t.Error("同步横摇 not found in dictionary entries")
	}
}

func TestDictLoader_ToSynonyms(t *testing.T) {
	entries, _ := LoadDictionaries("../../dictionaries")
	syns := DictToSynonyms(entries)

	// Should have bidirectional mappings.
	hasIkeda := false
	for term := range syns {
		if strings.Contains(term, "ikeda") {
			hasIkeda = true
			break
		}
	}
	if !hasIkeda {
		t.Error("ikeda should be in synonym map")
	}
}

func TestDictLoader_RelatedTerms(t *testing.T) {
	entries, _ := LoadDictionaries("../../dictionaries")
	related := DictToRelatedTerms(entries)
	if len(related) == 0 {
		t.Error("expected related_terms")
	}
	t.Logf("related terms: %v", related)
}

func TestSynonymRewriter_WithDict(t *testing.T) {
	entries, _ := LoadDictionaries("../../dictionaries")
	syns := DictToSynonyms(entries)
	r := NewSynonymRewriter()
	for term, synonyms := range syns {
		for _, syn := range synonyms {
			r.AddSynonym(term, syn)
		}
	}

	variants := r.Rewrite("什么是同步横摇")
	t.Logf("rewrite(什么是同步横摇) → %v", variants)
	if len(variants) < 2 {
		t.Error("expected at least 2 variants after synonym expansion")
	}
}
