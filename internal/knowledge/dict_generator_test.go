package knowledge

import (
	"strings"
	"testing"
)

func TestParseSimpleDictJSON(t *testing.T) {
	input := `[
  {
    "term": "横摇阻尼",
    "exact_synonyms": ["roll damping", "roll damping coefficient"],
    "related_terms": ["Ikeda method", "bilge keel"]
  },
  {
    "term": "舭龙骨",
    "exact_synonyms": ["bilge keel", "bilge keels"],
    "related_terms": ["roll damping", "eddy making"]
  }
]`

	results := parseSimpleDictJSON(input)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].Term != "横摇阻尼" {
		t.Errorf("term[0] = %q, want %q", results[0].Term, "横摇阻尼")
	}
	if len(results[0].ExactSynonyms) != 2 {
		t.Errorf("exact_synonyms[0] len = %d, want 2", len(results[0].ExactSynonyms))
	}
	if len(results[0].RelatedTerms) != 2 {
		t.Errorf("related_terms[0] len = %d, want 2", len(results[0].RelatedTerms))
	}

	if results[1].Term != "舭龙骨" {
		t.Errorf("term[1] = %q, want %q", results[1].Term, "舭龙骨")
	}
}

func TestExtractJSONDictResults(t *testing.T) {
	// Simulate an LLM response with markdown fences.
	llmResp := "Here are the terms:\n```json\n[\n  {\n    \"term\": \"CFD\",\n    \"exact_synonyms\": [\"computational fluid dynamics\", \"计算流体力学\"],\n    \"related_terms\": [\"RANS\", \"free surface\"]\n  }\n]\n```\nHope this helps!"

	results := extractJSONDictResults(llmResp)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Term != "CFD" {
		t.Errorf("term = %q, want CFD", results[0].Term)
	}
	if len(results[0].ExactSynonyms) != 2 {
		t.Errorf("exact_synonyms len = %d, want 2", len(results[0].ExactSynonyms))
	}
}

func TestFormatDictAsYAML(t *testing.T) {
	entries := []DictGenerationResult{
		{Term: "横摇阻尼", ExactSynonyms: []string{"roll damping"}, RelatedTerms: []string{"Ikeda method"}},
	}

	yaml := FormatDictAsYAML(entries)
	if !strings.Contains(yaml, "横摇阻尼") {
		t.Error("YAML output missing term")
	}
	if !strings.Contains(yaml, "roll damping") {
		t.Error("YAML output missing synonym")
	}
	if !strings.Contains(yaml, "exact_synonyms") {
		t.Error("YAML output missing exact_synonyms key")
	}
	t.Logf("YAML output:\n%s", yaml)
}

func TestExtractJSONString(t *testing.T) {
	obj := `{"term": "横摇阻尼", "exact_synonyms": ["roll damping"]}`
	got := extractJSONString(obj, "term")
	if got != "横摇阻尼" {
		t.Errorf("got %q, want %q", got, "横摇阻尼")
	}
}

func TestExtractJSONStringArray(t *testing.T) {
	obj := `{"exact_synonyms": ["roll damping", "damping coefficient", "roll decay"]}`
	got := extractJSONStringArray(obj, "exact_synonyms")
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d: %v", len(got), got)
	}
	if got[0] != "roll damping" {
		t.Errorf("got[0] = %q", got[0])
	}
}
