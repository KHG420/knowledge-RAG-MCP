package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/logging"
)

// =============================================================================
// Validation unit tests
// =============================================================================

func TestManage_ValidateComponent(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", true},
		{"valid-slug", true},
		{"../etc/passwd", false},
		{"..\\windows", false},
		{"/absolute/path", false},
		{"normal-document-name", true},
		{"doc_with_underscores", true},
		{"dots...are.fine", false},
	}
	for _, tt := range tests {
		err := validateComponent(tt.input)
		passed := err == nil
		if passed != tt.want {
			if tt.want {
				t.Errorf("validateComponent(%q) should pass, got: %v", tt.input, err)
			} else {
				t.Errorf("validateComponent(%q) should fail, but passed", tt.input)
			}
		}
	}
}

// =============================================================================
// Cache TTL setter unit tests
// =============================================================================

func TestStore_SetCacheTTLs(t *testing.T) {
	s := NewStoreWithBackend(newMockBackend())

	// All setters should work without panic.
	s.SetCacheQueryTTL(time.Duration(600) * time.Second)
	s.SetCacheChunkTTL(time.Duration(3600) * time.Second)
	s.SetCacheMetaTTL(time.Duration(1800) * time.Second)
	s.SetCacheIndexTTL(time.Duration(900) * time.Second)
	s.SetCacheKBListTTL(time.Duration(120) * time.Second)

	// Verify by reading the fields directly.
	if s.queryCacheTTL != 600*time.Second {
		t.Errorf("queryCacheTTL: got %v, want 600s", s.queryCacheTTL)
	}
	if s.chunkCacheTTL != 3600*time.Second {
		t.Errorf("chunkCacheTTL: got %v, want 3600s", s.chunkCacheTTL)
	}
	if s.metaCacheTTL != 1800*time.Second {
		t.Errorf("metaCacheTTL: got %v, want 1800s", s.metaCacheTTL)
	}
	if s.indexCacheTTL != 900*time.Second {
		t.Errorf("indexCacheTTL: got %v, want 900s", s.indexCacheTTL)
	}
	if s.kbListCacheTTL != 120*time.Second {
		t.Errorf("kbListCacheTTL: got %v, want 120s", s.kbListCacheTTL)
	}
}

func TestStore_SetCacheTTLs_Zero(t *testing.T) {
	s := NewStoreWithBackend(newMockBackend())

	// Zero TTL (no expiry) should be accepted.
	s.SetCacheQueryTTL(0)
	s.SetCacheChunkTTL(0)

	if s.queryCacheTTL != 0 {
		t.Errorf("queryCacheTTL: got %v, want 0", s.queryCacheTTL)
	}
	if s.chunkCacheTTL != 0 {
		t.Errorf("chunkCacheTTL: got %v, want 0", s.chunkCacheTTL)
	}
}

// =============================================================================
// reloadDeepSeek unit tests
// =============================================================================

func TestReloadDeepSeek_WithAPIKey_SetsRewriter(t *testing.T) {
	s := NewStoreWithBackend(newMockBackend())
	s.logger = logging.NewNopLogger()

	cfg := config.DefaultConfig()
	cfg.DeepSeekEndpoint = "https://api.deepseek.com/chat/completions"
	cfg.DeepSeekAPIKey = "sk-test-key"
	cfg.DeepSeekModel = "deepseek-v4-flash"

	s.reloadDeepSeek(cfg)

	if s.llmRewriter == nil {
		t.Fatal("expected llmRewriter to be set when API key is present")
	}
}

func TestReloadDeepSeek_WithoutAPIKey_RemovesRewriter(t *testing.T) {
	s := NewStoreWithBackend(newMockBackend())
	s.logger = logging.NewNopLogger()

	// First set a rewriter.
	cfg := config.DefaultConfig()
	cfg.DeepSeekAPIKey = "sk-test-key"
	s.reloadDeepSeek(cfg)
	if s.llmRewriter == nil {
		t.Fatal("expected llmRewriter to be set initially")
	}

	// Then clear the API key — rewriter should be removed.
	cfg.DeepSeekAPIKey = ""
	s.reloadDeepSeek(cfg)
	if s.llmRewriter != nil {
		t.Error("expected llmRewriter to be nil after clearing API key")
	}
}

func TestReloadDeepSeek_NoConfig_NoRewriter(t *testing.T) {
	s := NewStoreWithBackend(newMockBackend())
	s.logger = logging.NewNopLogger()

	// Default config has no API key.
	cfg := config.DefaultConfig()
	s.reloadDeepSeek(cfg)

	if s.llmRewriter != nil {
		t.Error("expected llmRewriter to be nil when no API key is configured")
	}
}

// =============================================================================
// Config API — configAPIResponse JSON roundtrip
// =============================================================================

func TestConfigAPIResponse_JSONRoundtrip(t *testing.T) {
	resp := configAPIResponse{
		EmbedEndpoint: "http://localhost:11434/api/embed",
		EmbedModel:    "bge-m3",
		EmbedDim:      1024,
		EmbedAPIKey:   "***",
		SearchMode:    "hybrid",
		RerankEnabled: true,
		DeepSeekEndpoint: "https://api.deepseek.com/chat/completions",
		DeepSeekModel:    "deepseek-v4-flash",
		DeepSeekAPIKey:   "***",
		RedisEnabled:  true,
		RedisAddr:     "127.0.0.1:6379",
		RedisDB:       0,
		RedisPrefix:   "kmcp:",
		RedisPoolSize: 10,
		RedisPassword: "***",
		CacheQueryTTL:  300,
		CacheChunkTTL:  0,
		CacheMetaTTL:   0,
		CacheIndexTTL:  0,
		CacheKBListTTL: 60,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back configAPIResponse
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.EmbedModel != "bge-m3" {
		t.Errorf("EmbedModel: got %q", back.EmbedModel)
	}
	if back.RerankEnabled != true {
		t.Error("RerankEnabled should be true")
	}
	if back.DeepSeekModel != "deepseek-v4-flash" {
		t.Errorf("DeepSeekModel: got %q", back.DeepSeekModel)
	}
	if back.RedisAddr != "127.0.0.1:6379" {
		t.Errorf("RedisAddr: got %q", back.RedisAddr)
	}
	if back.CacheQueryTTL != 300 {
		t.Errorf("CacheQueryTTL: got %d", back.CacheQueryTTL)
	}
	if back.CacheKBListTTL != 60 {
		t.Errorf("CacheKBListTTL: got %d", back.CacheKBListTTL)
	}
}

func TestConfigUpdateRequest_Omitempty(t *testing.T) {
	// Empty request should marshal to {}.
	req := configUpdateRequest{}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("empty request should be {}, got %s", string(data))
	}

	// Single field should only contain that field.
	logLevel := "debug"
	req2 := configUpdateRequest{LogLevel: &logLevel}
	data2, err := json.Marshal(req2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var partial map[string]any
	json.Unmarshal(data2, &partial)
	if len(partial) != 1 || partial["logLevel"] != "debug" {
		t.Errorf("single-field request: got %s", string(data2))
	}
}

// =============================================================================
// Config API — deepseekApiKey lifecycle (Store-level test)
// =============================================================================

func TestConfigPut_DeepSeekAPIKey_SetAndClear(t *testing.T) {
	tmp := t.TempDir()
	backend := newMockBackend()
	s := NewStoreWithBackend(backend)
	s.dataDir = tmp
	s.logger = logging.NewNopLogger()

	if err := s.CreateKB("test-kb", "test knowledge base"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	s = s.WithKB("test-kb")

	cfg := config.DefaultConfig()
	cfg.DataDir = tmp
	cfg.DeepSeekEndpoint = "https://api.deepseek.com/chat/completions"
	cfg.DeepSeekModel = "deepseek-v4-flash"
	cfg.DeepSeekAPIKey = "sk-test-key"
	s.SetConfig(cfg, tmp+"/knowledge-mcp.toml")

	// Set a new API key.
	s.reloadDeepSeek(cfg)
	if s.llmRewriter == nil {
		t.Error("expected llmRewriter to be set after setting deepseekApiKey")
	}

	// Clear the API key (empty string should remove the rewriter).
	cfg.DeepSeekAPIKey = ""
	s.reloadDeepSeek(cfg)
	if s.llmRewriter != nil {
		t.Error("expected llmRewriter to be nil after clearing deepseekApiKey")
	}
}

// ── parseSSEEvents helper (used by SSE tests in the manage_test package) ─────

// sseEvent is a parsed SSE event.
type sseEvent struct {
	eventType string
	data      string
}

// parseSSEEvents parses an SSE stream body into a slice of events.
func parseSSEEvents(body string) []sseEvent {
	var events []sseEvent
	lines := strings.Split(body, "\n")
	var currentType, currentData string
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			currentType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			currentData = strings.TrimPrefix(line, "data: ")
		} else if line == "" && currentData != "" {
			events = append(events, sseEvent{
				eventType: currentType,
				data:      currentData,
			})
			currentType = ""
			currentData = ""
		}
	}
	return events
}
