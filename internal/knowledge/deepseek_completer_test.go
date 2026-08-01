package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeepSeekCompleter_Complete_OK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request basics.
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected Content-Type: %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}

		var req deepSeekRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != "deepseek-v4-flash" {
			t.Errorf("expected model=deepseek-v4-flash, got %s", req.Model)
		}
		if req.Stream {
			t.Error("expected stream=false")
		}
		if len(req.Messages) != 2 {
			t.Errorf("expected 2 messages, got %d", len(req.Messages))
		}

		// Return a valid chat completion response.
		resp := deepSeekResponse{
			Choices: []deepSeekChoice{
				{Message: deepSeekMessage{Role: "assistant", Content: "  rewrote query variant 1\nrewrote query variant 2  "}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	c := NewDeepSeekCompleter(ts.URL, "sk-test-key-123", "deepseek-v4-flash")
	content, err := c.Complete(context.Background(), "original query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content == "" {
		t.Error("expected non-empty content")
	}
	t.Logf("response content: %q", content)
}

func TestDeepSeekCompleter_Complete_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "Invalid API key: sk-abc123def456"}`))
	}))
	defer ts.Close()

	c := NewDeepSeekCompleter(ts.URL, "sk-test-key", "deepseek-v4-flash")
	_, err := c.Complete(context.Background(), "query")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	// The error message must NOT contain the real API key.
	if strings.Contains(err.Error(), "sk-test-key") {
		t.Errorf("error message should not leak API key: %v", err)
	}
	// The sanitised response must mask the sk- prefix.
	if strings.Contains(err.Error(), "sk-abc123") {
		t.Errorf("error message should not leak any sk-xxx value: %v", err)
	}
	if !strings.Contains(err.Error(), "sk-***") {
		t.Errorf("error message should contain sanitised key marker: %v", err)
	}
	t.Logf("sanitised error: %v", err)
}

func TestDeepSeekCompleter_Complete_EmptyChoices(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := deepSeekResponse{Choices: []deepSeekChoice{}} // empty
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	c := NewDeepSeekCompleter(ts.URL, "sk-key", "deepseek-v4-flash")
	_, err := c.Complete(context.Background(), "query")
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if !strings.Contains(err.Error(), "empty choices") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDeepSeekCompleter_Complete_InvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not-json"))
	}))
	defer ts.Close()

	c := NewDeepSeekCompleter(ts.URL, "sk-key", "deepseek-v4-flash")
	_, err := c.Complete(context.Background(), "query")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("expected unmarshal error, got: %v", err)
	}
}

func TestDeepSeekCompleter_Complete_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // longer than client timeout
	}))
	defer ts.Close()

	c := NewDeepSeekCompleter(ts.URL, "sk-key", "deepseek-v4-flash",
		WithDeepSeekClient(&http.Client{Timeout: 50 * time.Millisecond}),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.Complete(ctx, "query")
	if err == nil {
		t.Fatal("expected error for timeout")
	}
}

func TestDeepSeekCompleter_Complete_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	c := NewDeepSeekCompleter(ts.URL, "sk-key", "deepseek-v4-flash")
	_, err := c.Complete(context.Background(), "query")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status 500 in error, got: %v", err)
	}
}

func TestDeepSeekCompleter_Defaults(t *testing.T) {
	c := NewDeepSeekCompleter("", "", "")
	if c.endpoint != "https://api.deepseek.com/chat/completions" {
		t.Errorf("expected default endpoint, got %q", c.endpoint)
	}
	if c.model != "deepseek-v4-flash" {
		t.Errorf("expected default model deepseek-v4-flash, got %q", c.model)
	}
	if c.apiKey != "" {
		t.Error("expected empty apiKey")
	}
	if c.client.Timeout != 15*time.Second {
		t.Errorf("expected 15s timeout, got %v", c.client.Timeout)
	}
}

func TestDeepSeekCompleter_WithOptions(t *testing.T) {
	customClient := &http.Client{Timeout: 5 * time.Second}
	c := NewDeepSeekCompleter("http://custom:8080", "sk-key", "deepseek-v3",
		WithDeepSeekClient(customClient),
	)
	if c.endpoint != "http://custom:8080" {
		t.Errorf("endpoint mismatch: %q", c.endpoint)
	}
	if c.model != "deepseek-v3" {
		t.Errorf("model mismatch: %q", c.model)
	}
	if c.client != customClient {
		t.Error("custom client not used")
	}
}

func TestSanitiseForLog(t *testing.T) {
	tests := []struct {
		name  string
		input string
		max   int
		want  string
	}{
		{
			name:  "no key",
			input: "internal server error",
			max:   200,
			want:  "internal server error",
		},
		{
			name:  "mask sk- prefix",
			input: `{"error":"Invalid API key: sk-abc123def456"}`,
			max:   200,
			want:  `{"error":"Invalid API key: sk-***"}`,
		},
		{
			name:  "mask sk- at start",
			input: "sk-mysecretkey0987654321 is not valid",
			max:   200,
			want:  "sk-*** is not valid",
		},
		{
			name:  "truncate long",
			input: strings.Repeat("x", 300),
			max:   100,
			want:  strings.Repeat("x", 100) + "...",
		},
		{
			name:  "truncate with key",
			input: "something sk-key123 " + strings.Repeat("y", 500),
			max:   100,
			want:  "something sk-*** " + strings.Repeat("y", 100-len("something sk-*** ")) + "...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitiseForLog(tt.input, tt.max)
			if got != tt.want {
				t.Errorf("sanitiseForLog:\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestDeepSeekCompleter_Complete_ContextCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer ts.Close()

	c := NewDeepSeekCompleter(ts.URL, "sk-key", "deepseek-v4-flash")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := c.Complete(ctx, "query")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestDeepSeekCompleter_LoggerOption(t *testing.T) {
	c := NewDeepSeekCompleter("http://x", "sk-key", "m1")
	if c.logger == nil {
		t.Error("logger should not be nil (default is NopLogger)")
	}
}
