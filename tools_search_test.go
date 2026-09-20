package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// kbFailBackend wraps a StorageBackend and makes ListDocSlugs fail for one KB,
// injecting a real search failure into the real search path.
type kbFailBackend struct {
	knowledge.StorageBackend
	failKB string
}

func (b *kbFailBackend) ListDocSlugs(kb string) ([]string, error) {
	if kb == b.failKB {
		return nil, fmt.Errorf("injected failure for %s", kb)
	}
	return b.StorageBackend.ListDocSlugs(kb)
}

func dampingChunks(n int) []knowledge.ChunkIndexEntry {
	chunks := make([]knowledge.ChunkIndexEntry, n)
	for i := 0; i < n; i++ {
		chunks[i] = knowledge.ChunkIndexEntry{
			ID:        fmt.Sprintf("%03d", i),
			Section:   "Body",
			TermCount: 2,
			Terms:     []knowledge.TermFreq{{Term: "roll", Count: 1}, {Term: "damping", Count: 1}},
		}
	}
	return chunks
}

func seedKB(t *testing.T, backend knowledge.StorageBackend, kb string, n int) {
	t.Helper()
	if err := backend.CreateKB(kb, ""); err != nil {
		t.Fatalf("CreateKB(%s): %v", kb, err)
	}
	if n > 0 {
		addTestDoc(t, backend, kb, "doc", "Doc "+kb, dampingChunks(n))
	}
}

// TestSearchMultiKB_ReturnsFullLimitFromOneKB is the AC3 regression: all 8
// matching chunks live in one of three KBs, so the merged result must contain
// all 8 from that KB rather than a small per-KB share.
func TestSearchMultiKB_ReturnsFullLimitFromOneKB(t *testing.T) {
	store := newTestResearchStore(t)
	backend := store.Backend()
	for _, kb := range []string{"big", "empty1", "empty2"} {
		seedKB(t, backend, kb, 0)
	}
	addTestDoc(t, backend, "big", "doc", "Big Doc", dampingChunks(8))

	out := searchMultiKB(store, "roll damping", 8, knowledge.SearchFilter{}, []string{"big", "empty1", "empty2"})
	if len(out.Hits) != 8 {
		t.Fatalf("want 8 hits from the one matching KB, got %d (searched=%v failed=%v)", len(out.Hits), out.Searched, out.Failed)
	}
	for _, h := range out.Hits {
		if h.KBName != "big" {
			t.Fatalf("hit has kb_name %q, want big", h.KBName)
		}
	}
	if len(out.Failed) != 0 {
		t.Fatalf("unexpected failures: %+v", out.Failed)
	}
	if len(out.Searched) != 3 {
		t.Fatalf("want all 3 KBs searched, got %v", out.Searched)
	}
}

func TestSearchMultiKB_LimitOne(t *testing.T) {
	store := newTestResearchStore(t)
	backend := store.Backend()
	seedKB(t, backend, "k1", 0)
	seedKB(t, backend, "k2", 0)
	addTestDoc(t, backend, "k1", "doc", "Doc", dampingChunks(8))

	out := searchMultiKB(store, "roll damping", 1, knowledge.SearchFilter{}, []string{"k1", "k2"})
	if len(out.Hits) != 1 {
		t.Fatalf("limit=1 must return exactly 1 hit, got %d", len(out.Hits))
	}
}

func TestSearchMultiKB_NoMatches(t *testing.T) {
	store := newTestResearchStore(t)
	seedKB(t, store.Backend(), "k1", 0)

	out := searchMultiKB(store, "unrelatedterm", 8, knowledge.SearchFilter{}, []string{"k1"})
	if len(out.Hits) != 0 {
		t.Fatalf("want 0 hits, got %d", len(out.Hits))
	}
	if len(out.Failed) != 0 {
		t.Fatalf("unexpected failures: %+v", out.Failed)
	}
	if len(out.Searched) != 1 || out.Searched[0] != "k1" {
		t.Fatalf("want k1 searched, got %v", out.Searched)
	}
}

func TestSearchMultiKB_SpecificKB(t *testing.T) {
	store := newTestResearchStore(t)
	backend := store.Backend()
	seedKB(t, backend, "one", 8)
	seedKB(t, backend, "two", 0)

	out := runResearch(store, "roll damping", 8, knowledge.SearchFilter{}, "one")
	if len(out.Hits) != 8 {
		t.Fatalf("specific KB read: want 8 hits, got %d", len(out.Hits))
	}
	if len(out.Searched) != 1 || out.Searched[0] != "one" {
		t.Fatalf("searched=%v, want [one]", out.Searched)
	}
}

func TestSearchMultiKB_PartialFailure(t *testing.T) {
	mock := knowledge.NewMockBackend()
	seedKB(t, mock, "good", 3)
	seedKB(t, mock, "bad", 0)
	store := newTestResearchStoreWithBackend(t, &kbFailBackend{StorageBackend: mock, failKB: "bad"})

	out := searchMultiKB(store, "roll damping", 8, knowledge.SearchFilter{}, []string{"good", "bad"})
	if len(out.Hits) == 0 {
		t.Fatal("partial failure must still return useful results from the healthy KB")
	}
	if len(out.Failed) != 1 || out.Failed[0].KB != "bad" {
		t.Fatalf("want one failure for bad, got %+v", out.Failed)
	}
	if out.Failed[0].Message == "" {
		t.Fatal("failure must carry an actionable message")
	}
	if len(out.Searched) != 1 || out.Searched[0] != "good" {
		t.Fatalf("searched=%v, want [good]", out.Searched)
	}
}

func TestSearchMultiKB_AllFail(t *testing.T) {
	mock := knowledge.NewMockBackend()
	seedKB(t, mock, "bad1", 0)
	seedKB(t, mock, "bad2", 0)
	store := newTestResearchStoreWithBackend(t, &allFailBackend{StorageBackend: mock})

	out := searchMultiKB(store, "roll damping", 8, knowledge.SearchFilter{}, []string{"bad1", "bad2"})
	if len(out.Searched) != 0 {
		t.Fatalf("all-fail must have no successful KBs, got %v", out.Searched)
	}
	if len(out.Failed) != 2 {
		t.Fatalf("want 2 failures, got %+v", out.Failed)
	}
	if len(out.Hits) != 0 {
		t.Fatalf("want 0 hits, got %d", len(out.Hits))
	}
}

type allFailBackend struct {
	knowledge.StorageBackend
}

func (b *allFailBackend) ListDocSlugs(kb string) ([]string, error) {
	return nil, fmt.Errorf("injected failure for %s", kb)
}

// readFailBackend injects metadata or chunk-index read failures for one KB,
// exercising the real collect path rather than failing HybridSearch itself.
type readFailBackend struct {
	knowledge.StorageBackend
	failKB     string
	failMeta   bool
	failChunks bool
}

func (b *readFailBackend) ReadMeta(kb, slug string) (*knowledge.DocumentMeta, error) {
	if b.failMeta && kb == b.failKB {
		return nil, fmt.Errorf("injected metadata read failure for %s", kb)
	}
	return b.StorageBackend.ReadMeta(kb, slug)
}

func (b *readFailBackend) ReadChunksIndex(kb, slug string) (*knowledge.ChunksIndex, error) {
	if b.failChunks && kb == b.failKB {
		return nil, fmt.Errorf("injected chunks index read failure for %s", kb)
	}
	return b.StorageBackend.ReadChunksIndex(kb, slug)
}

// TestResearchEnvelope_MetadataReadFailureIsPartial verifies that a real
// metadata read failure in one KB becomes a partial result (failed_kbs +
// healthy results), not a silently complete empty search.
func TestResearchEnvelope_MetadataReadFailureIsPartial(t *testing.T) {
	mock := knowledge.NewMockBackend()
	seedKB(t, mock, "good", 3)
	seedKB(t, mock, "bad", 2)
	store := newTestResearchStoreWithBackend(t, &readFailBackend{
		StorageBackend: mock, failKB: "bad", failMeta: true,
	})
	s := newResearchServer(store)

	result := callTool(t, s, "knowledge_research", map[string]any{"question": "roll damping"})
	if result.IsError {
		t.Fatalf("one failing KB plus one healthy KB must be partial, not an error: %s", resultText(t, result))
	}
	var env researchEnvelope
	if err := json.Unmarshal([]byte(resultText(t, result)), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if env.Coverage != coveragePartial {
		t.Fatalf("coverage=%q, want %q", env.Coverage, coveragePartial)
	}
	if len(env.Results) == 0 {
		t.Fatal("partial failure must still carry results from the healthy KB")
	}
	if len(env.FailedKBs) != 1 || env.FailedKBs[0].KB != "bad" || env.FailedKBs[0].Message == "" {
		t.Fatalf("failed_kbs=%+v, want one actionable failure for bad", env.FailedKBs)
	}
}

// TestResearchEnvelope_SingleKBMetadataReadFailureIsError verifies that the
// only attempted KB failing to read metadata becomes an MCP-level error.
func TestResearchEnvelope_SingleKBMetadataReadFailureIsError(t *testing.T) {
	mock := knowledge.NewMockBackend()
	seedKB(t, mock, "bad", 2)
	store := newTestResearchStoreWithBackend(t, &readFailBackend{
		StorageBackend: mock, failKB: "bad", failMeta: true,
	})
	s := newResearchServer(store)

	result := callTool(t, s, "knowledge_research", map[string]any{"question": "roll damping", "kbName": "bad"})
	if !result.IsError {
		t.Fatalf("a single KB read failure must be an MCP error, got: %s", resultText(t, result))
	}
}

// TestResearchEnvelope_ChunkIndexReadFailureIsPartial verifies the chunk-index
// read failure path in the real collect path is surfaced as partial coverage.
func TestResearchEnvelope_ChunkIndexReadFailureIsPartial(t *testing.T) {
	mock := knowledge.NewMockBackend()
	seedKB(t, mock, "good", 3)
	seedKB(t, mock, "bad", 2)
	store := newTestResearchStoreWithBackend(t, &readFailBackend{
		StorageBackend: mock, failKB: "bad", failChunks: true,
	})
	s := newResearchServer(store)

	result := callTool(t, s, "knowledge_research", map[string]any{"question": "roll damping"})
	if result.IsError {
		t.Fatalf("one failing KB plus one healthy KB must be partial, not an error: %s", resultText(t, result))
	}
	var env researchEnvelope
	if err := json.Unmarshal([]byte(resultText(t, result)), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if env.Coverage != coveragePartial {
		t.Fatalf("coverage=%q, want %q", env.Coverage, coveragePartial)
	}
	if len(env.Results) == 0 {
		t.Fatal("partial failure must still carry results from the healthy KB")
	}
	if len(env.FailedKBs) != 1 || env.FailedKBs[0].KB != "bad" {
		t.Fatalf("failed_kbs=%+v, want one failure for bad", env.FailedKBs)
	}
}

// callTool invokes a registered MCP tool through the real server message
// handler and returns the tool result.
func callTool(t *testing.T, s *server.MCPServer, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	msg := s.HandleMessage(context.Background(), body)
	resp, ok := msg.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T (%+v)", msg, msg)
	}
	switch v := resp.Result.(type) {
	case *mcp.CallToolResult:
		return v
	case mcp.CallToolResult:
		return &v
	default:
		t.Fatalf("expected CallToolResult, got %T", resp.Result)
		return nil
	}
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	tc, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	return tc.Text
}

func newResearchServer(store *knowledge.Store) *server.MCPServer {
	s := server.NewMCPServer("knowledge-mcp", "test", server.WithToolCapabilities(true))
	registerSearch(s, store, logging.NewNopLogger())
	return s
}

func TestResearchEnvelope_NoHitsUsesEnvelope(t *testing.T) {
	store := newTestResearchStore(t)
	seedKB(t, store.Backend(), "k1", 0)
	s := newResearchServer(store)

	result := callTool(t, s, "knowledge_research", map[string]any{"question": "unrelatedterm"})
	if result.IsError {
		t.Fatalf("no hits must not be an MCP error: %s", resultText(t, result))
	}
	var env researchEnvelope
	if err := json.Unmarshal([]byte(resultText(t, result)), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if env.Results == nil || len(env.Results) != 0 {
		t.Fatalf("results must be an empty array, got %#v", env.Results)
	}
	if env.Coverage != coverageComplete {
		t.Fatalf("coverage=%q, want %q", env.Coverage, coverageComplete)
	}
	if len(env.SearchedKBs) != 1 || env.SearchedKBs[0] != "k1" {
		t.Fatalf("searched_kbs=%v, want [k1]", env.SearchedKBs)
	}
	if !strings.Contains(resultText(t, result), `"results": []`) {
		t.Fatalf("results must serialize as an array: %s", resultText(t, result))
	}
}

func TestResearchEnvelope_PartialFailure(t *testing.T) {
	mock := knowledge.NewMockBackend()
	seedKB(t, mock, "good", 2)
	seedKB(t, mock, "bad", 0)
	store := newTestResearchStoreWithBackend(t, &kbFailBackend{StorageBackend: mock, failKB: "bad"})
	s := newResearchServer(store)

	result := callTool(t, s, "knowledge_research", map[string]any{"question": "roll damping"})
	if result.IsError {
		t.Fatalf("partial failure must not be an MCP error: %s", resultText(t, result))
	}
	var env researchEnvelope
	if err := json.Unmarshal([]byte(resultText(t, result)), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if env.Coverage != coveragePartial {
		t.Fatalf("coverage=%q, want %q", env.Coverage, coveragePartial)
	}
	if len(env.Results) == 0 {
		t.Fatal("partial failure must still carry results from the healthy KB")
	}
	if len(env.FailedKBs) != 1 || env.FailedKBs[0].KB != "bad" || env.FailedKBs[0].Message == "" {
		t.Fatalf("failed_kbs=%+v, want one actionable failure for bad", env.FailedKBs)
	}
}

func TestResearchEnvelope_AllFailIsMCPError(t *testing.T) {
	mock := knowledge.NewMockBackend()
	seedKB(t, mock, "bad1", 0)
	seedKB(t, mock, "bad2", 0)
	store := newTestResearchStoreWithBackend(t, &allFailBackend{StorageBackend: mock})
	s := newResearchServer(store)

	result := callTool(t, s, "knowledge_research", map[string]any{"question": "roll damping"})
	if !result.IsError {
		t.Fatalf("all-KB failure must be an MCP error, got: %s", resultText(t, result))
	}
}

func TestResearchEnvelope_NoKBs(t *testing.T) {
	store := newTestResearchStore(t)
	s := newResearchServer(store)

	result := callTool(t, s, "knowledge_research", map[string]any{"question": "anything"})
	if result.IsError {
		t.Fatalf("no KBs must be a handled envelope, not an error: %s", resultText(t, result))
	}
	var env researchEnvelope
	if err := json.Unmarshal([]byte(resultText(t, result)), &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if len(env.Results) != 0 || len(env.SearchedKBs) != 0 || len(env.FailedKBs) != 0 {
		t.Fatalf("unexpected no-KB envelope: %+v", env)
	}
	if len(env.Warnings) == 0 {
		t.Fatal("no-KB envelope should warn that no knowledge bases are configured")
	}
}

// TestResearchWarnings_ReportRuntimeUnknownAndNeutralRanking verifies the
// envelope never claims successful model use (runtime status is unknown) and
// uses neutral ranking wording when no reranker/embedder is configured.
func TestResearchWarnings_ReportRuntimeUnknownAndNeutralRanking(t *testing.T) {
	store := newTestResearchStore(t)
	warnings := researchWarnings(store, searchOutcome{Searched: []string{"k1"}})
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "not reported") {
		t.Fatalf("warnings must state runtime model status is not reported: %v", warnings)
	}
	if !strings.Contains(joined, "retrieval ranking") {
		t.Fatalf("expected neutral retrieval ranking wording, got: %v", warnings)
	}
	if strings.Contains(strings.ToLower(joined), "fused") {
		t.Fatalf("reranker warning must not say 'fused' when no embedder is configured: %v", warnings)
	}
}
