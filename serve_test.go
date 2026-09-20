package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/logging"
)

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`

func newTestMCPHandler(t *testing.T, apiToken string) http.Handler {
	t.Helper()
	s := server.NewMCPServer("knowledge-mcp", "test", server.WithToolCapabilities(true))
	cfg := &config.Config{APIToken: apiToken}
	handler, _ := newMCPHandler(cfg, s, logging.NewNopLogger())
	return handler
}

func doRequest(h http.Handler, method, path, body, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestMCPAuth_TokenProtectsTransports verifies the existing api_token now
// guards /mcp, /sse and /message on the real routed handler.
func TestMCPAuth_TokenProtectsTransports(t *testing.T) {
	h := newTestMCPHandler(t, "secret-token")

	for _, path := range []string{"/mcp", "/sse", "/message"} {
		if rec := doRequest(h, http.MethodPost, path, initializeBody, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without bearer: got %d, want 401", path, rec.Code)
		}
		if rec := doRequest(h, http.MethodPost, path, initializeBody, "wrong-token"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with wrong bearer: got %d, want 401", path, rec.Code)
		}
	}
}

// TestMCPAuth_CorrectTokenInitializes checks a valid initialize request works
// when the correct bearer token is supplied.
func TestMCPAuth_CorrectTokenInitializes(t *testing.T) {
	h := newTestMCPHandler(t, "secret-token")

	rec := doRequest(h, http.MethodPost, "/mcp", initializeBody, "secret-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize with correct token: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "serverInfo") {
		t.Fatalf("expected an initialize result, got: %s", rec.Body.String())
	}
}

// TestMCPAuth_EmptyTokenStaysOpen verifies the current open behavior is
// unchanged when no api_token is configured.
func TestMCPAuth_EmptyTokenStaysOpen(t *testing.T) {
	h := newTestMCPHandler(t, "")

	rec := doRequest(h, http.MethodPost, "/mcp", initializeBody, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty token: /mcp without bearer got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "serverInfo") {
		t.Fatalf("expected an initialize result, got: %s", rec.Body.String())
	}
}

// TestRegisterAllTools_RegistersExactlyThree pins the agent-facing surface to
// the three tools the docs advertise.
func TestRegisterAllTools_RegistersExactlyThree(t *testing.T) {
	store := newTestResearchStore(t)
	s := server.NewMCPServer("knowledge-mcp", "test", server.WithToolCapabilities(true))
	registerAllTools(s, store, logging.NewNopLogger())

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp, ok := s.HandleMessage(context.Background(), body).(mcp.JSONRPCResponse)
	if !ok {
		t.Fatal("expected JSONRPCResponse for tools/list")
	}
	var tools []mcp.Tool
	switch v := resp.Result.(type) {
	case *mcp.ListToolsResult:
		tools = v.Tools
	case mcp.ListToolsResult:
		tools = v.Tools
	default:
		t.Fatalf("unexpected tools/list result type %T", resp.Result)
	}

	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{"knowledge_list_kbs", "knowledge_read", "knowledge_research"}
	if len(names) != len(want) {
		t.Fatalf("registered tools = %v, want exactly %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("registered tools = %v, want %v", names, want)
		}
	}
}
