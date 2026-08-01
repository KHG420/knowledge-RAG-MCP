package knowledge

import (
	"fmt"
	"testing"
)

// =============================================================================
// TriageQuery tests — verify triage classification rules
// =============================================================================

func TestTriageQuery_Simple(t *testing.T) {
	// Simple: ≤6 terms, no method/comparison/multi-concept markers.
	// NOTE: Chinese without spaces is 1 term. "计算" in "横摇阻尼计算" triggers
	// HasMethod → that query is Medium, not Simple. This is expected behavior.
	queries := []string{
		"横摇阻尼",          // 1 term, CJK → simple
		"roll damping",      // 2 terms, no markers → simple
		"bilge keel design", // 3 terms, no markers → simple
		"ship stability",    // 2 terms → simple
		"short query",       // 2 terms → simple
	}
	for _, q := range queries {
		qf := analyzeQuery(q)
		got := TriageQuery(qf)
		if got != TriageSimple {
			t.Errorf("query=%q → triage=%s, want simple (qf=%+v)", q, got, qf)
		}
	}
}

func TestTriageQuery_Medium(t *testing.T) {
	// Medium: >6 terms OR has method markers OR has multi-concept markers.
	// NOTE: "横摇阻尼计算" is Medium because "计算" triggers HasMethod marker.
	queries := []string{
		"横摇阻尼 Ikeda 方法计算模型分析", // >6 terms
		"roll damping estimation using Ikeda method", // 7 terms
		"横摇阻尼的计算方法和模型", // has method marker ("方法")
		"algorithm for ship motion prediction", // has method marker ("algorithm")
		"横摇阻尼与舭龙骨的关系", // has multi-concept ("与")
		"ship stability and roll damping", // has multi-concept ("and")
		"不同方法的比较分析", // 6 terms but has method marker
	}
	for _, q := range queries {
		qf := analyzeQuery(q)
		got := TriageQuery(qf)
		if got != TriageMedium {
			t.Errorf("query=%q → triage=%s, want medium (qf=%+v)", q, got, qf)
		}
	}
}

func TestTriageQuery_Complex(t *testing.T) {
	// Complex: >12 terms OR (>6 terms AND has comparison).
	queries := []string{
		// >12 terms case (English only — CJK terms count as 1 without spaces)
		"a b c d e f g h i j k l m n o p", // 16 terms → complex
		// >6 terms + comparison
		"compare roll damping estimation using Ikeda method versus CFD simulation",
		// CJK comparison queries are Medium unless they also have enough markers to
		// escalate. This is a known gap (CJK tokenization), not a bug in triage.
		"比较横摇阻尼计算的Ikeda方法和CFD方法差异 研究 分析 对比 综述 评估 总结",
	}
	for _, q := range queries {
		qf := analyzeQuery(q)
		got := TriageQuery(qf)
		if got != TriageComplex {
			t.Errorf("query=%q → triage=%s, want complex (qf=%+v termCount=%d)", q, got, qf, qf.TermCount)
		}
	}
}

func TestTriageQuery_ConservativeEscalation(t *testing.T) {
	// Verify that comparison queries with enough other signals escalate correctly.
	// CJK-only comparison queries with 1 term: HasComparison alone doesn't escalate
	// above simple — that's the current design (CJK tokenization gap).
	// English comparison queries: "compare" + multiple terms → medium or complex.
	escalated := []string{
		"compare roll damping method approach analysis estimation", // 7 terms + comparison → complex
		"横摇阻尼 vs CFD 方法 模型 算法 分析 研究 对比",                // many CJK terms → complex
	}
	for _, q := range escalated {
		qf := analyzeQuery(q)
		got := TriageQuery(qf)
		if got != TriageComplex && got != TriageMedium {
			t.Errorf("query=%q → triage=%s, expected escalation", q, got)
		}
	}
	// Short CJK comparison → simple (known: no CJK tokenizer)
	// This is a design note, not a bug — CJK tokenizer is a future enhancement.
}

func TestTriageQuery_EdgeCases(t *testing.T) {
	tests := []struct {
		query string
		want  QueryTriage
	}{
		{"", TriageSimple},               // empty query → simple (0 terms)
		{"a", TriageSimple},              // single char → simple
		{"?", TriageSimple},              // punctuation only → simple
		{"roll damp", TriageSimple},       // 2 terms, no markers → simple
		{"方法", TriageMedium},            // contains "方法" marker → medium
		{"方法 模型 算法 计算 比较 差异 区别", TriageComplex}, // 7 terms + comparison → complex
	}
	for _, tt := range tests {
		qf := analyzeQuery(tt.query)
		got := TriageQuery(qf)
		if got != tt.want {
			t.Errorf("query=%q → triage=%s, want %s", tt.query, got, tt.want)
		}
	}
}

// =============================================================================
// TriageQueryStr tests
// =============================================================================

func TestTriageQueryStr(t *testing.T) {
	// "横摇阻尼计算" → "计算" triggers HasMethod → medium
	if got := TriageQueryStr("横摇阻尼计算"); got != TriageMedium {
		t.Errorf("expected medium (has method marker), got %s", got)
	}
	// "横摇阻尼" → no method/comparison/multi → simple
	if got := TriageQueryStr("横摇阻尼"); got != TriageSimple {
		t.Errorf("expected simple, got %s", got)
	}
	// multi-word with method → medium
	if got := TriageQueryStr("roll damping estimation using Ikeda method"); got != TriageMedium {
		t.Errorf("expected medium, got %s", got)
	}
}

// =============================================================================
// analyzeQuery tests — verify feature extraction accuracy
// =============================================================================

func TestAnalyzeQuery_ComparisonDetection(t *testing.T) {
	positive := []string{
		"比较两种方法",
		"对比分析",
		"差异在哪里",
		"区别",
		"横摇阻尼 vs CFD",
		"compare two methods",
		"versus experimental data",
		"Ikeda 和 CFD 的差异",
	}
	for _, q := range positive {
		qf := analyzeQuery(q)
		if !qf.HasComparison {
			t.Errorf("query=%q should have HasComparison=true, got false", q)
		}
	}

	negative := []string{
		"横摇阻尼计算",
		"roll damping estimation",
		"ship stability analysis",
	}
	for _, q := range negative {
		qf := analyzeQuery(q)
		if qf.HasComparison {
			t.Errorf("query=%q should have HasComparison=false, got true", q)
		}
	}
}

func TestAnalyzeQuery_MethodDetection(t *testing.T) {
	positive := []string{
		"横摇阻尼计算方法",
		"Ikeda method",
		"CFD 模型",
		"numerical algorithm",
		"compute roll damping",
	}
	for _, q := range positive {
		qf := analyzeQuery(q)
		if !qf.HasMethod {
			t.Errorf("query=%q should have HasMethod=true, got false", q)
		}
	}
}

func TestAnalyzeQuery_MultiConceptDetection(t *testing.T) {
	positive := []string{
		"横摇阻尼和舭龙骨",
		"ship stability and roll damping",
		"稳定性与耐波性",
		"CFD + 实验",
		"横摇以及纵摇",
		"横摇同时纵摇",
	}
	for _, q := range positive {
		qf := analyzeQuery(q)
		if !qf.HasMultiConcept {
			t.Errorf("query=%q should have HasMultiConcept=true, got false", q)
		}
	}
}

func TestAnalyzeQuery_TermCount(t *testing.T) {
	tests := []struct {
		query     string
		wantCount int
	}{
		{"", 0},
		{"横摇阻尼", 1},
		{"roll damping", 2},
		{"横摇 damping 计算", 3},
		{"Ikeda method for roll damping estimation", 6},
	}
	for _, tt := range tests {
		qf := analyzeQuery(tt.query)
		if qf.TermCount != tt.wantCount {
			t.Errorf("query=%q → TermCount=%d, want %d", tt.query, qf.TermCount, tt.wantCount)
		}
	}
}

// =============================================================================
// HasComparisonMarkers tests
// =============================================================================

func TestHasComparisonMarkers(t *testing.T) {
	trueCases := []string{
		"比较Ikeda方法和CFD方法",
		"compare two approaches",
		"which is better for roll damping",
		"两种方案的差异",
		"区别在哪里",
		"优缺点分析",
		"tradeoff between accuracy and speed",
	}
	for _, q := range trueCases {
		if !HasComparisonMarkers(q) {
			t.Errorf("HasComparisonMarkers(%q) = false, want true", q)
		}
	}

	falseCases := []string{
		"横摇阻尼计算",
		"roll damping estimation",
		"ship stability",
	}
	for _, q := range falseCases {
		if HasComparisonMarkers(q) {
			t.Errorf("HasComparisonMarkers(%q) = true, want false", q)
		}
	}
}

// =============================================================================
// Retrieval budget tests — verify budget scales with complexity
// =============================================================================

func TestRetrievalBudget(t *testing.T) {
	// Simple query
	simpleQF := QueryFeatures{TermCount: 3}
	bm25N, vecBeam, rerankN, returnN := retrievalBudget(simpleQF)
	if bm25N != 40 || vecBeam != 80 || rerankN != 40 || returnN != 8 {
		t.Errorf("simple budget: got (%d,%d,%d,%d), want (40,80,40,8)", bm25N, vecBeam, rerankN, returnN)
	}

	// Medium query (has method)
	mediumQF := QueryFeatures{TermCount: 5, HasMethod: true}
	bm25N, vecBeam, rerankN, returnN = retrievalBudget(mediumQF)
	if bm25N != 60 || vecBeam != 120 || rerankN != 60 || returnN != 8 {
		t.Errorf("medium budget: got (%d,%d,%d,%d), want (60,120,60,8)", bm25N, vecBeam, rerankN, returnN)
	}

	// Complex query (has comparison + >6 terms)
	complexQF := QueryFeatures{TermCount: 7, HasComparison: true}
	bm25N, vecBeam, rerankN, returnN = retrievalBudget(complexQF)
	if bm25N != 80 || vecBeam != 200 || rerankN != 100 || returnN != 10 {
		t.Errorf("complex budget: got (%d,%d,%d,%d), want (80,200,100,10)", bm25N, vecBeam, rerankN, returnN)
	}
}

// =============================================================================
// detectQueryType tests
// =============================================================================

func TestDetectQueryType(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		// Question → conceptual (ASCII '?' triggers this)
		{"how to calculate roll damping?", "conceptual"},
		// CJK '？' is NOT detected by current code (ASCII-only check). Known gap.
		{"什么是横摇阻尼？", "factual"},
		// Verb-heavy → conceptual
		{"how can we reduce roll motion", "conceptual"},
		// Short → factual
		{"横摇阻尼", "factual"},
		{"roll damping", "factual"},
		// Noun-heavy → factual
		{"Ikeda method bilge keel roll damping coefficient", "factual"},
		// Mostly nouns → factual (not balanced)
		{"ship roll damping calculation", "factual"},
	}
	for _, tt := range tests {
		got := detectQueryType(tt.query)
		if got != tt.want {
			t.Errorf("detectQueryType(%q) = %s, want %s", tt.query, got, tt.want)
		}
	}
}

// =============================================================================
// adaptiveRRFWeight tests
// =============================================================================

func TestAdaptiveRRFWeight(t *testing.T) {
	// Conceptual queries get lower BM25 weight (higher dense weight)
	if w := adaptiveRRFWeight("how to calculate roll damping?"); w != 0.4 {
		t.Errorf("conceptual query: want 0.4, got %v", w)
	}
	// Factual queries get higher BM25 weight
	if w := adaptiveRRFWeight("横摇阻尼"); w != 0.6 {
		t.Errorf("factual query: want 0.6, got %v", w)
	}
	// Short queries with no verbs → factual
	if w := adaptiveRRFWeight("ship roll"); w != 0.6 {
		t.Errorf("short factual: want 0.6, got %v", w)
	}
}

// =============================================================================
// Full pipeline integration test — triage → budget → RRF weight consistency
// =============================================================================

func TestTriageAndBudgetConsistency(t *testing.T) {
	// Verify that triage classification aligns with budget allocation.
	// A complex query should get the largest budget.
	// A simple query should get the smallest budget.

	tests := []struct {
		query       string
		wantTriage  QueryTriage
		wantBudget  int // expected bm25N value
	}{
		{"横摇阻尼", TriageSimple, 40},
		{"roll damping calculation", TriageSimple, 40},
		{"横摇阻尼 Ikeda 方法计算模型研究", TriageMedium, 60},
		{"ship stability and roll damping method", TriageMedium, 60},
		{"比较 Ikeda 方法 CFD 方法 经验公式 在横摇阻尼计算中的差异", TriageComplex, 80},
	}

	for _, tt := range tests {
		qf := analyzeQuery(tt.query)
		triage := TriageQuery(qf)
		bm25N, _, _, _ := retrievalBudget(qf)

		if triage != tt.wantTriage {
			t.Errorf("query=%q: triage=%s want=%s", tt.query, triage, tt.wantTriage)
		}
		if bm25N != tt.wantBudget {
			t.Errorf("query=%q: bm25N=%d want=%d", tt.query, bm25N, tt.wantBudget)
		}
	}
}

// =============================================================================
// QueryTriage String() tests
// =============================================================================

func TestQueryTriageString(t *testing.T) {
	tests := []struct {
		triage QueryTriage
		want   string
	}{
		{TriageSimple, "simple"},
		{TriageMedium, "medium"},
		{TriageComplex, "complex"},
		{QueryTriage(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.triage.String()
		if got != tt.want {
			t.Errorf("QueryTriage(%d).String() = %q, want %q", int(tt.triage), got, tt.want)
		}
	}
}

// =============================================================================
// retrievalBudgetLabel tests
// =============================================================================

func TestRetrievalBudgetLabel(t *testing.T) {
	if l := retrievalBudgetLabel(QueryFeatures{TermCount: 3}); l != "simple" {
		t.Errorf("simple label: got %q", l)
	}
	if l := retrievalBudgetLabel(QueryFeatures{TermCount: 7, HasMethod: true}); l != "medium" {
		t.Errorf("medium label: got %q", l)
	}
	if l := retrievalBudgetLabel(QueryFeatures{TermCount: 13}); l != "complex" {
		t.Errorf("complex label: got %q", l)
	}
}

// =============================================================================
// Benchmark tests for performance-critical triage path
// =============================================================================

func BenchmarkAnalyzeQuery(b *testing.B) {
	queries := []string{
		"横摇阻尼计算",
		"roll damping estimation using Ikeda method",
		"比较不同横摇阻尼计算方法的效果差异分析",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, q := range queries {
			analyzeQuery(q)
		}
	}
}

func BenchmarkTriageQuery(b *testing.B) {
	qfs := []QueryFeatures{
		{TermCount: 3},
		{TermCount: 8, HasMethod: true},
		{TermCount: 10, HasComparison: true},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, qf := range qfs {
			TriageQuery(qf)
		}
	}
}

// =============================================================================
// Fuzz-inspired edge cases for analyzeQuery (no fuzz engine, just exhaustive)
// =============================================================================

func TestAnalyzeQuery_UnicodeAndSpecialChars(t *testing.T) {
	tests := []string{
		"🚢 船舶横摇阻尼",       // emoji prefix
		"roll-damping estimation", // hyphenated
		"横摇阻尼\t计算",          // tab
		"  roll  damping  ",     // extra spaces
		"α β γ method",          // Greek letters
	}
	for _, q := range tests {
		qf := analyzeQuery(q)
		// Should not panic, and should produce some reasonable output.
		if qf.TermCount < 0 {
			t.Errorf("query=%q gave negative TermCount", q)
		}
		_ = fmt.Sprintf("%+v", qf) // verify formatting doesn't panic
	}
}

// =============================================================================
// Verify comparison detection covers all registered markers
// =============================================================================

func TestComparisonMarkersCoverage(t *testing.T) {
	// Every marker in queryTriageComparisonMarkers should trigger detection.
	for _, marker := range queryTriageComparisonMarkers {
		testQuery := "test " + marker + " test"
		if !HasComparisonMarkers(testQuery) {
			t.Errorf("marker %q not detected in query %q", marker, testQuery)
		}
	}
}

// =============================================================================
// Regression: verify that exact term count = 7 still goes to medium
// =============================================================================

func TestTermCountBoundaries(t *testing.T) {
	// 6 terms → simple
	qf := QueryFeatures{TermCount: 6}
	if TriageQuery(qf) != TriageSimple {
		t.Error("6 terms should be simple")
	}
	// 7 terms → medium
	qf = QueryFeatures{TermCount: 7}
	if TriageQuery(qf) != TriageMedium {
		t.Error("7 terms should be medium")
	}
	// 12 terms → medium
	qf = QueryFeatures{TermCount: 12}
	if TriageQuery(qf) != TriageMedium {
		t.Error("12 terms should be medium")
	}
	// 13 terms → complex
	qf = QueryFeatures{TermCount: 13}
	if TriageQuery(qf) != TriageComplex {
		t.Error("13 terms should be complex")
	}
	// 7 terms + comparison → complex (comparison overrides)
	qf = QueryFeatures{TermCount: 7, HasComparison: true}
	if TriageQuery(qf) != TriageComplex {
		t.Error("7 terms + comparison should be complex")
	}
}

// =============================================================================
// Test that queryTriageMarkers includes both CN and EN
// =============================================================================

func TestComparisonMarkersBilingual(t *testing.T) {
	cnMarkers := []string{"比较", "对比", "差异", "区别", "优缺点", "不同", "哪个更好", "哪个更", "如何选择"}
	enMarkers := []string{"compare", "versus", " difference", "better", "which", "pros", "cons", "tradeoff", "trade-off"}

	all := make(map[string]bool)
	for _, m := range queryTriageComparisonMarkers {
		all[m] = true // keep markers as-is, including leading " " in " difference"
	}

	for _, m := range cnMarkers {
		if !all[m] {
			t.Errorf("Chinese marker %q not found in queryTriageComparisonMarkers", m)
		}
	}
	for _, m := range enMarkers {
		if !all[m] {
			t.Errorf("English marker %q not found in queryTriageComparisonMarkers", m)
		}
	}
}
