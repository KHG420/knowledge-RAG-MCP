package main

import (
	"encoding/json"
	"testing"

	"knowledge-mcp/internal/knowledge"
)

// evidenceChunkFromText calls the real buildEvidenceJSON and decodes it.
func evidenceChunkFromText(t *testing.T, store *knowledge.Store, kb, slug, chunkID, text string) knowledge.EvidenceChunk {
	t.Helper()
	raw, err := buildEvidenceJSON(store, kb, slug, chunkID, text)
	if err != nil {
		t.Fatalf("buildEvidenceJSON: %v", err)
	}
	var ev knowledge.EvidenceChunk
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("evidence is not valid JSON: %v\n%s", err, raw)
	}
	return ev
}

// TestBuildEvidenceJSON_UnrelatedTextIsNotHighConfidence is the AC4 regression:
// text full of definitions, numbers, causal markers and conclusions must not be
// reported as high-relevance / complete when it has not been compared with the
// caller's question.
func TestBuildEvidenceJSON_UnrelatedTextIsNotHighConfidence(t *testing.T) {
	store := newTestResearchStore(t)
	backend := store.Backend()
	seedKB(t, backend, "kb", 0)
	addTestDoc(t, backend, "kb", "doc", "Doc", []knowledge.ChunkIndexEntry{{
		ID: "000", Section: "Body", TermCount: 3,
		Terms: []knowledge.TermFreq{{Term: "coefficient", Count: 1}, {Term: "force", Count: 1}},
	}})

	// Every structural signal the old heuristic looked for, about an unrelated
	// numeric/definition statement.
	text := "定义：该系数为 123 N·m，因为存在因为机理，所以结果表明这是完整的结论。定义 defined as because therefore."

	ev := evidenceChunkFromText(t, store, "kb", "doc", "000", text)

	if ev.Evidence.AnswerRelevance != evidenceUnknown {
		t.Errorf("answer_relevance=%q, want %q (must not be guessed from text structure)", ev.Evidence.AnswerRelevance, evidenceUnknown)
	}
	if ev.Evidence.Completeness != evidenceUnknown {
		t.Errorf("completeness=%q, want %q (must not be guessed from text structure)", ev.Evidence.Completeness, evidenceUnknown)
	}
	if ev.Evidence.SourceConfidence != "exact_section" {
		t.Errorf("source_confidence=%q, want exact_section", ev.Evidence.SourceConfidence)
	}
	// Valid provenance/location must be retained.
	if ev.Document.ID != "doc" {
		t.Errorf("document.id=%q, want doc", ev.Document.ID)
	}
	if ev.Location.ChunkID != "000" {
		t.Errorf("location.chunk_id=%q, want 000", ev.Location.ChunkID)
	}
	if ev.CitationID != "doc_000" {
		t.Errorf("citation_id=%q, want doc_000", ev.CitationID)
	}
	if ev.Content != text {
		t.Errorf("content lost in evidence envelope")
	}
}

// The metadata-less degradation path must keep the same unknown verdict.
func TestBuildEvidenceJSON_MissingMetaStillUnknown(t *testing.T) {
	store := newTestResearchStore(t)
	seedKB(t, store.Backend(), "kb", 0)

	ev := evidenceChunkFromText(t, store, "kb", "missing-doc", "000", "定义 because 123 N·m")

	if ev.Evidence.AnswerRelevance != evidenceUnknown || ev.Evidence.Completeness != evidenceUnknown {
		t.Fatalf("missing-meta evidence=%q/%q, want unknown/unknown", ev.Evidence.AnswerRelevance, ev.Evidence.Completeness)
	}
	if ev.Document.ID != "missing-doc" || ev.Location.ChunkID != "000" {
		t.Fatalf("provenance not retained: %+v", ev)
	}
}
