package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/ingest"
	"knowledge-mcp/internal/logging"
)

// TestIngestResearchRead_MultilingualProvenance is the AC8 in-process
// regression: a real Markdown file is ingested through the real ingest engine
// into in-memory mock storage, then queried through the real MCP research and
// read tool handlers. Provenance from the search result is fed to the read
// call, proving the follow-up path works end to end.
func TestIngestResearchRead_MultilingualProvenance(t *testing.T) {
	store := newTestResearchStore(t)
	backend := store.Backend()
	if err := backend.CreateKB("kb", "e2e KB"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}

	logger := logging.NewNopLogger()
	ing := ingest.NewSimple(store.TaskManager(), nil, store.Mutex(), logger.WithModule("ingest"))
	ing.SetBackend(backend)
	ing.SetChunkStore(store.ChunkStore())
	ing.SetBuildChunksIndex(store.BuildChunksIndex)
	store.SetIngestService(ing)

	content := "# 耐波性 Seakeeping\n\n" +
		"同步横摇 synchronous roll 发生在波浪 encounter frequency 接近横摇固有频率时。\n\n" +
		"The bilge keel increases roll damping at higher speeds.\n\n" +
		"Ikeda 方法估计摩擦阻尼和涡流阻尼 friction and eddy damping components.\n"
	path := filepath.Join(t.TempDir(), "seakeeping.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	meta, err := store.WithKB("kb").UploadDocument(path)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if meta.ChunkCount == 0 {
		t.Fatal("ingest produced no chunks")
	}

	s := server.NewMCPServer("knowledge-mcp", "test", server.WithToolCapabilities(true))
	registerAllTools(s, store, logger)

	// MCP research.
	research := callTool(t, s, "knowledge_research", map[string]any{"question": "roll damping"})
	if research.IsError {
		t.Fatalf("research returned an MCP error: %s", resultText(t, research))
	}
	var env researchEnvelope
	if err := json.Unmarshal([]byte(resultText(t, research)), &env); err != nil {
		t.Fatalf("research envelope is not JSON: %v", err)
	}
	if env.Coverage != coverageComplete {
		t.Fatalf("coverage=%q, want complete (searched=%v failed=%+v)", env.Coverage, env.SearchedKBs, env.FailedKBs)
	}
	if len(env.Results) == 0 {
		t.Fatal("ingested multilingual document was not searchable")
	}
	hit := env.Results[0]
	if hit.KBName != "kb" {
		t.Fatalf("hit kb_name=%q, want kb", hit.KBName)
	}
	if hit.Document.ID == "" || hit.Location.ChunkID == "" {
		t.Fatalf("hit is missing provenance: %+v", hit)
	}

	// MCP read using the provenance returned by research.
	read := callTool(t, s, "knowledge_read", map[string]any{
		"docSlug": hit.Document.ID,
		"chunkID": hit.Location.ChunkID,
		"kbName":  hit.KBName,
	})
	if read.IsError {
		t.Fatalf("read returned an MCP error: %s", resultText(t, read))
	}
	var ev knowledge.EvidenceChunk
	if err := json.Unmarshal([]byte(resultText(t, read)), &ev); err != nil {
		t.Fatalf("read evidence is not JSON: %v", err)
	}
	if ev.Document.ID != hit.Document.ID || ev.Location.ChunkID != hit.Location.ChunkID || ev.KBName != hit.KBName {
		t.Fatalf("read provenance does not match research result: %+v", ev)
	}
	if ev.CitationID != hit.CitationID {
		t.Fatalf("citation_id changed between search and read: %q vs %q", hit.CitationID, ev.CitationID)
	}
	if ev.Evidence.SourceConfidence != "exact_section" {
		t.Fatalf("source_confidence=%q, want exact_section", ev.Evidence.SourceConfidence)
	}
	if ev.Evidence.AnswerRelevance != evidenceUnknown || ev.Evidence.Completeness != evidenceUnknown {
		t.Fatalf("read evidence must stay unknown/unknown, got %q/%q", ev.Evidence.AnswerRelevance, ev.Evidence.Completeness)
	}
	if !strings.Contains(ev.Content, "damping") {
		t.Fatalf("read content lost the multilingual source text: %q", ev.Content)
	}
}
