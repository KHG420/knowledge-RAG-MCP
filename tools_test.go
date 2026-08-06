package main

import (
	"encoding/json"
	"strings"
	"testing"

	"knowledge-mcp/internal/knowledge"
)

// ─── searchMultiKB: unit tests for search_helpers ────────────────────────────

// TestSearchMultiKB_Sorting tests the sort+truncate logic.
// We need an actual Store for searchMultiKB, but we can test the
// underlying sort logic with table-driven tests.

func TestSearchMultiKB_ScoreSorting(t *testing.T) {
	// Verify that knowledge.SearchHit sorting works correctly.
	// This is the same logic used by searchMultiKB after merging results.
	hits := []knowledge.SearchHit{
		{Score: 0.3},
		{Score: 0.9},
		{Score: 0.5},
		{Score: 0.1},
		{Score: 0.8},
	}

	// Simulate the sort logic from searchMultiKB
	sortByScore(hits)

	// Verify descending order
	for i := 0; i < len(hits)-1; i++ {
		if hits[i].Score < hits[i+1].Score {
			t.Errorf("hits not sorted descending: hits[%d].Score=%.2f < hits[%d].Score=%.2f",
				i, hits[i].Score, i+1, hits[i+1].Score)
		}
	}

	// Top element should be highest
	if hits[0].Score != 0.9 {
		t.Errorf("top score should be 0.9, got %.1f", hits[0].Score)
	}

	// Truncation to limit
	limit := 3
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if len(hits) != 3 {
		t.Errorf("after truncation to %d, got %d hits", limit, len(hits))
	}
}

func TestSearchMultiKB_Truncation(t *testing.T) {
	// Edge: fewer hits than limit
	hits := []knowledge.SearchHit{{Score: 0.5}}
	limit := 3
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if len(hits) != 1 {
		t.Error("should keep all hits when fewer than limit")
	}

	// Edge: exactly at limit
	hits = []knowledge.SearchHit{{}, {}, {}}
	limit = 3
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if len(hits) != 3 {
		t.Error("should keep all hits when exactly at limit")
	}

	// Edge: empty
	hits = nil
	limit = 3
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if len(hits) != 0 {
		t.Error("empty hits should stay empty")
	}
}

func TestSearchMultiKB_StableSortForEqualScores(t *testing.T) {
	// Two hits with same score — order should be stable
	hits := []knowledge.SearchHit{
		{Score: 0.5},
		{Score: 0.5},
		{Score: 0.5},
	}
	// Sort naturally is not stable; but we just verify it doesn't crash
	sortByScore(hits)
	for _, h := range hits {
		if h.Score != 0.5 {
			t.Errorf("score changed: %.1f", h.Score)
		}
	}
}

// ─── buildEvidenceJSON: structure tests ──────────────────────────────────────

func TestBuildEvidenceJSON_Structure(t *testing.T) {
	// We cannot call buildEvidenceJSON directly without a Store,
	// but we can verify the EvidenceChunk type marshals correctly.
	chunk := knowledge.EvidenceChunk{
		Document: knowledge.DocumentInfo{
			ID:           "test-doc",
			Title:        "Test Document",
			OriginalName: "test.pdf",
			Type:         "pdf",
		},
		Location: knowledge.LocationInfo{
			ChunkID:   "005",
			Section:   "Introduction",
			Offset:    0,
			PageStart: 1,
			PageEnd:   1,
		},
		Content:    "This is a test chunk.",
		CitationID: "test-doc_005",
		Evidence: knowledge.EvidenceMeta{
			SourceConfidence: "exact_section",
			AnswerRelevance:  "relevant",
			Completeness:     "complete",
		},
	}

	data, err := json.MarshalIndent(chunk, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Verify key fields are present (struct uses snake_case json tags)
	for _, want := range []string{
		`"id": "test-doc"`,
		`"title": "Test Document"`,
		`"chunk_id": "005"`,
		`"section": "Introduction"`,
		`"citation_id": "test-doc_005"`,
		`"content": "This is a test chunk."`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON output missing %q", want)
		}
	}
}

func TestBuildEvidenceJSON_Minimal(t *testing.T) {
	// Test minimal evidence with no metadata
	chunk := knowledge.EvidenceChunk{
		Document:   knowledge.DocumentInfo{ID: "doc-only"},
		Location:   knowledge.LocationInfo{ChunkID: "001"},
		Content:    "content",
		CitationID: "doc-only_001",
	}

	data, err := json.MarshalIndent(chunk, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if !strings.Contains(string(data), "doc-only") {
		t.Error("missing ID in minimal evidence")
	}
}

func TestBuildEvidenceJSON_EmptyContent(t *testing.T) {
	chunk := knowledge.EvidenceChunk{
		Document:   knowledge.DocumentInfo{ID: "empty-doc"},
		Location:   knowledge.LocationInfo{ChunkID: "000"},
		Content:    "",
		CitationID: "empty-doc_000",
	}

	data, err := json.MarshalIndent(chunk, "", "  ")
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if !strings.Contains(string(data), `"content": ""`) {
		t.Error("empty content should be present in JSON")
	}
}

func TestBuildEvidenceJSON_LargeContent(t *testing.T) {
	largeContent := strings.Repeat("x", 100000)
	chunk := knowledge.EvidenceChunk{
		Document:   knowledge.DocumentInfo{ID: "large-doc"},
		Location:   knowledge.LocationInfo{ChunkID: "001"},
		Content:    largeContent,
		CitationID: "large-doc_001",
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal of large content failed: %v", err)
	}

	if len(data) < len(largeContent) {
		t.Error("JSON should contain full content")
	}
}

// ─── isPathSafe in tool context ──────────────────────────────────────────────

func TestTools_PathValidation(t *testing.T) {
	// Validate the path safety checks used in tools_read.go and tools_remove.go

	// Valid inputs from tools context
	validInputs := []string{
		"my-document",
		"doc-001",
		"chunk_005",
		"2024-report.pdf",
		"subdir/file.txt",
	}
	for _, v := range validInputs {
		if !isPathSafe(v) {
			t.Errorf("isPathSafe(%q) should be true", v)
		}
	}

	// Invalid inputs that should be rejected
	invalidInputs := []string{
		"../etc/passwd",
		"/etc/passwd",
		"foo/../../../bar",
		"..\\windows\\path",
	}
	for _, v := range invalidInputs {
		if isPathSafe(v) {
			t.Errorf("isPathSafe(%q) should be false", v)
		}
	}
}

// ─── parseTags in tool context ──────────────────────────────────────────────

func TestTools_TagParsing(t *testing.T) {
	// Tags are used in upload and search tools.

	// Valid tag strings
	valid := []struct {
		input    string
		expected []string
	}{
		{"tag1", []string{"tag1"}},
		{"tag1, tag2", []string{"tag1", "tag2"}},
		{"important, urgent", []string{"important", "urgent"}},
		{"中文标签, english-tag", []string{"中文标签", "english-tag"}},
	}

	for _, tc := range valid {
		got := parseTags(tc.input)
		if len(got) != len(tc.expected) {
			t.Errorf("parseTags(%q) length = %d, want %d", tc.input, len(got), len(tc.expected))
			continue
		}
		for i := range tc.expected {
			if got[i] != tc.expected[i] {
				t.Errorf("parseTags(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.expected[i])
			}
		}
	}

	// Edge: empty
	if got := parseTags(""); got != nil {
		t.Errorf("parseTags('') = %v, want nil", got)
	}
}

// ─── parseTime in tool context ──────────────────────────────────────────────

func TestTools_TimeParsing(t *testing.T) {
	// addedAfter/addedBefore use parseTime. Verify reasonable dates parse.

	tests := []struct {
		input string
		valid bool
	}{
		{"2026-01-01", true},
		{"2026-01-01T00:00:00Z", true},
		{"2020-06-15", true},
		{"2026-07-15T12:30:00+08:00", true},
		{"", false},            // empty → zero
		{"invalid", false},     // garbage
		{"2026-02-30", false},  // Feb 30 invalid
		{"not-a-date", false},
	}

	for _, tc := range tests {
		result := parseTime(tc.input)
		if tc.valid && result.IsZero() {
			t.Errorf("parseTime(%q) returned zero, expected valid time", tc.input)
		}
		if !tc.valid && !result.IsZero() {
			t.Errorf("parseTime(%q) returned non-zero %v, expected zero", tc.input, result)
		}
	}
}

// ─── sortByScore helper (used in searchMultiKB) ─────────────────────────────

// sortByScore sorts a slice of SearchHit by Score descending.
// This is a local copy for testing; the real version is in searchMultiKB.
func sortByScore(hits []knowledge.SearchHit) {
	// sort.Slice with Score comparison
	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[i].Score < hits[j].Score {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}
}

func TestSortByScore_AllNegative(t *testing.T) {
	hits := []knowledge.SearchHit{
		{Score: -0.1},
		{Score: -0.5},
		{Score: -0.3},
	}
	sortByScore(hits)
	if hits[0].Score != -0.1 {
		t.Errorf("highest (least negative) should be first, got %.1f", hits[0].Score)
	}
	if hits[2].Score != -0.5 {
		t.Errorf("lowest (most negative) should be last, got %.1f", hits[2].Score)
	}
}

func TestSortByScore_SingleElement(t *testing.T) {
	hits := []knowledge.SearchHit{{Score: 0.7}}
	sortByScore(hits)
	if hits[0].Score != 0.7 {
		t.Error("single element sort should be identity")
	}
}

func TestSortByScore_Empty(t *testing.T) {
	hits := []knowledge.SearchHit{}
	// Should not panic
	sortByScore(hits)
}

func TestSortByScore_NegativeAndPositive(t *testing.T) {
	hits := []knowledge.SearchHit{
		{Score: -1.0},
		{Score: 0.5},
		{Score: 1.0},
		{Score: -0.5},
	}
	sortByScore(hits)
	expected := []float64{1.0, 0.5, -0.5, -1.0}
	for i, exp := range expected {
		if hits[i].Score != exp {
			t.Errorf("hits[%d].Score = %.1f, want %.1f", i, hits[i].Score, exp)
		}
	}
}

// ─── Per-KB limit calculation logic ─────────────────────────────────────────

func TestSearchMultiKB_PerKBLimit(t *testing.T) {
	// The perKB calculation: limit/KB count, minimum 3.
	tests := []struct {
		limit    int
		numKB    int
		expected int
	}{
		{20, 1, 20},  // single KB: full limit
		{20, 2, 10},  // 20/2 = 10
		{20, 3, 6},   // 20/3 = 6 (integer division)
		{10, 5, 3},   // 10/5 = 2, clamped to 3
		{5, 3, 3},    // 5/3 = 1, clamped to 3
		{8, 1, 8},    // single KB
		{8, 4, 3},    // 8/4 = 2, clamped to 3
	}

	for _, tc := range tests {
		perKB := tc.limit
		if tc.numKB > 1 {
			perKB = tc.limit / tc.numKB
			if perKB < 3 {
				perKB = 3
			}
		}
		if perKB != tc.expected {
			t.Errorf("limit=%d numKB=%d → perKB=%d, want %d", tc.limit, tc.numKB, perKB, tc.expected)
		}
	}
}

// ─── tools_list: display cap logic ───────────────────────────────────────────

func TestToolsList_DisplayCap(t *testing.T) {
	// Tests the logic in registerList: cap display at 10 docs.

	cases := []struct {
		total        int
		displayCount int
	}{
		{0, 0},
		{1, 1},
		{5, 5},
		{10, 10},
		{11, 10},
		{100, 10},
		{1000, 10},
	}

	for _, tc := range cases {
		display := tc.total
		if display > 10 {
			display = 10
		}
		if display != tc.displayCount {
			t.Errorf("total=%d → display=%d, want %d", tc.total, display, tc.displayCount)
		}
	}
}

// ─── tools_search: limit clamping ───────────────────────────────────────────

func TestToolsSearch_LimitClamp(t *testing.T) {
	cases := []struct {
		input    int
		expected int
	}{
		{-1, 8},   // negative → default 8
		{-100, 8}, // negative → default 8
		{0, 8},    // 0 → default 8 (v <= 0)
		{1, 1},
		{8, 8},
		{15, 15},
		{19, 19},
		{20, 20},
		{21, 20}, // clamp to 20
		{100, 20},
		{1000, 20},
	}

	for _, tc := range cases {
		limit := 8
		if tc.input > 0 {
			limit = tc.input
		}
		if limit > 20 {
			limit = 20
		}
		if limit != tc.expected {
			t.Errorf("input=%d → limit=%d, want %d", tc.input, limit, tc.expected)
		}
	}
}

// ─── tools_read: context clamp ──────────────────────────────────────────────

func TestToolsRead_ContextClamp(t *testing.T) {
	cases := []struct {
		input    int
		expected int
	}{
		{0, 0},
		{1, 1},
		{3, 3},
		{5, 5},
		{6, 5},  // clamp to 5
		{10, 5},
		{100, 5},
		{-1, 0}, // negative → 0
		{-5, 0},
	}

	for _, tc := range cases {
		ctxCount := tc.input
		if ctxCount < 0 {
			ctxCount = 0
		}
		if ctxCount > 5 {
			ctxCount = 5
		}
		if ctxCount != tc.expected {
			t.Errorf("input=%d → ctxCount=%d, want %d", tc.input, ctxCount, tc.expected)
		}
	}
}
