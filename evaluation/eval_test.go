package evaluation

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// EvalQuestion is one test case from questions.json.
type EvalQuestion struct {
	ID                string      `json:"id"`
	Question          string      `json:"question"`
	Type              string      `json:"type"`
	Complexity        string      `json:"complexity"`
	ExpectedKB        flexStrings `json:"expected_kb"` // v4: accepts string or []string (multi-KB routing)
	ExpectedKeywords  []string    `json:"expected_keywords"`
	MinRelevantChunks int         `json:"min_relevant_chunks"`
	MultiKB           bool        `json:"multi_kb,omitempty"` // v4: true when query spans multiple KBs
}

// flexStrings accepts either a single JSON string or an array of strings.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(data []byte) error {
	// Try array first.
	var arr []string
	if json.Unmarshal(data, &arr) == nil {
		*f = arr
		return nil
	}
	// Fall back to single string.
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*f = []string{s}
	return nil
}

var questions []EvalQuestion

func TestMain(m *testing.M) {
	data, err := os.ReadFile("questions.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read questions.json: %v\n", err)
		os.Exit(1)
	}
	if err := json.Unmarshal(data, &questions); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse questions.json: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestQuestionsLoaded(t *testing.T) {
	if len(questions) == 0 {
		t.Fatal("no questions loaded from questions.json")
	}
	t.Logf("loaded %d questions", len(questions))
}

func TestQuestionIDsUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, q := range questions {
		if seen[q.ID] {
			t.Errorf("duplicate question ID: %s", q.ID)
		}
		seen[q.ID] = true
	}
}

func TestRequiredFields(t *testing.T) {
	validComplexities := map[string]bool{"simple": true, "medium": true, "complex": true}
	validTypes := map[string]bool{
		"concept_explanation":   true,
		"method_detail":         true,
		"comparison":            true,
		"mechanism_explanation": true,
	}

	for _, q := range questions {
		t.Run(q.ID, func(t *testing.T) {
			if q.ID == "" {
				t.Error("id is empty")
			}
			if q.Question == "" {
				t.Error("question is empty")
			}
			if !validComplexities[q.Complexity] {
				t.Errorf("invalid complexity %q, want simple/medium/complex", q.Complexity)
			}
			if !validTypes[q.Type] {
				t.Errorf("invalid type %q", q.Type)
			}
			if len(q.ExpectedKB) == 0 {
				t.Error("expected_kb is empty")
			}
			if len(q.ExpectedKeywords) == 0 {
				t.Error("expected_keywords is empty")
			}
			if q.MinRelevantChunks < 1 {
				t.Errorf("min_relevant_chunks=%d, want >=1", q.MinRelevantChunks)
			}
		})
	}
}

func TestComplexityDistribution(t *testing.T) {
	var simple, medium, complex int
	for _, q := range questions {
		switch q.Complexity {
		case "simple":
			simple++
		case "medium":
			medium++
		case "complex":
			complex++
		}
	}
	t.Logf("distribution: simple=%d medium=%d complex=%d", simple, medium, complex)

	if simple == 0 {
		t.Error("no simple questions — need baseline for basic queries")
	}
	if complex == 0 {
		t.Error("no complex questions — need baseline for comparison queries")
	}
}

func TestKeywordsLowercase(t *testing.T) {
	// Proper nouns and acronyms are allowed uppercase (individual words checked).
	allowedUppercaseWord := map[string]bool{
		"ikeda": true, "himeno": true, "kawahara": true,
		"mathieu": true, "melnikov": true,
		"rans": true, "les": true, "sph": true, "cfd": true,
		"gm": true,
	}
	for _, q := range questions {
		for _, kw := range q.ExpectedKeywords {
			// Split the keyword into words; check each word independently.
			for _, word := range strings.Fields(kw) {
				lower := strings.ToLower(word)
				if word != lower && !allowedUppercaseWord[lower] {
					t.Errorf("%s: keyword %q has unexpected uppercase word %q", q.ID, kw, word)
				}
			}
		}
	}
}

func TestMinRelevantChunksFeasible(t *testing.T) {
	for _, q := range questions {
		if q.MinRelevantChunks > 20 {
			t.Errorf("%s: min_relevant_chunks=%d seems unreasonably high", q.ID, q.MinRelevantChunks)
		}
	}
}

// BaselineReport is the shape of a baseline evaluation run, persisted so every
// change can be compared against it.
type BaselineReport struct {
	Timestamp         string  `json:"timestamp"`
	Questions         int     `json:"questions"`
	KBRoutingAccuracy float64 `json:"kb_routing_accuracy"`
	AvgRecallAt5      float64 `json:"avg_recall_at_5"`
	AvgRecallAt10     float64 `json:"avg_recall_at_10"`
	AvgMRR            float64 `json:"avg_mrr"`
	AvgLatencyMs      float64 `json:"avg_latency_ms"`
}

// TestBaselineReportShape validates that a baseline_report.json, if present,
// matches the expected schema.
func TestBaselineReportShape(t *testing.T) {
	data, err := os.ReadFile("baseline_report.json")
	if os.IsNotExist(err) {
		t.Skip("baseline_report.json not yet created — run full eval first")
	}
	if err != nil {
		t.Fatalf("read baseline_report.json: %v", err)
	}

	var report BaselineReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("parse baseline_report.json: %v", err)
	}

	if report.Questions < 1 {
		t.Error("questions count is zero")
	}
	if report.Timestamp == "" {
		t.Error("timestamp is empty")
	}

	t.Logf("baseline: questions=%d, routing_acc=%.2f%%, recall@5=%.2f%%, recall@10=%.2f%%, mrr=%.3f, latency=%dms",
		report.Questions,
		report.KBRoutingAccuracy*100,
		report.AvgRecallAt5*100,
		report.AvgRecallAt10*100,
		report.AvgMRR,
		int64(report.AvgLatencyMs),
	)
}
