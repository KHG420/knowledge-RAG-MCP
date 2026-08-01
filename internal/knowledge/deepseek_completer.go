package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"knowledge-mcp/internal/logging"
)

// DeepSeekCompleter implements TextCompleter by calling the DeepSeek
// chat completions API (OpenAI-compatible). It is used by LLMQueryRewriter
// to generate alternative query phrasings.
//
// The API key is never logged or embedded in error messages. Set it via
// the Config (knowledge-mcp.toml) or the DEEPSEEK_API_KEY environment
// variable.
type DeepSeekCompleter struct {
	endpoint string
	apiKey   string
	model    string
	client   *http.Client
	logger   *logging.Logger
}

// DeepSeekCompleterOption configures a DeepSeekCompleter.
type DeepSeekCompleterOption func(*DeepSeekCompleter)

// WithDeepSeekClient overrides the default HTTP client (useful for testing).
func WithDeepSeekClient(client *http.Client) DeepSeekCompleterOption {
	return func(c *DeepSeekCompleter) {
		c.client = client
	}
}

// WithDeepSeekLogger sets the logger on the completer. When nil, a no-op
// logger is used.
func WithDeepSeekLogger(l *logging.Logger) DeepSeekCompleterOption {
	return func(c *DeepSeekCompleter) {
		c.logger = l
	}
}

// NewDeepSeekCompleter creates a DeepSeekCompleter with the given
// parameters. The apiKey is treated as a secret and never reflected in
// logs or error messages.
func NewDeepSeekCompleter(endpoint, apiKey, model string, opts ...DeepSeekCompleterOption) *DeepSeekCompleter {
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/chat/completions"
	}
	if model == "" {
		model = "deepseek-flash"
	}
	c := &DeepSeekCompleter{
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		logger: logging.NewNopLogger(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Complete sends a prompt to DeepSeek and returns the model's text response.
// The prompt is sent as a single user message. On any non-2xx status or
// network error, the returned error omits the API key.
func (c *DeepSeekCompleter) Complete(ctx context.Context, prompt string) (string, error) {
	start := time.Now()

	reqBody := deepSeekRequest{
		Model: c.model,
		Messages: []deepSeekMessage{
			{Role: "system", Content: "You are a query expansion assistant. Reply concisely."},
			{Role: "user", Content: prompt},
		},
		Stream: false,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		c.logger.Errorf("deepseek: marshal request failed promptLen=%d err=%v", len(prompt), err)
		return "", fmt.Errorf("deepseek: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		c.logger.Errorf("deepseek: create request failed endpoint=%s err=%v", c.endpoint, err)
		return "", fmt.Errorf("deepseek: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	c.logger.Debugf("deepseek: request model=%s promptLen=%d bodyLen=%d", c.model, len(prompt), len(bodyBytes))

	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Warnf("deepseek: HTTP request failed model=%s elapsed=%v err=%v", c.model, time.Since(start), err)
		return "", fmt.Errorf("deepseek: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB max
	if err != nil {
		c.logger.Warnf("deepseek: read response body failed model=%s elapsed=%v err=%v", c.model, time.Since(start), err)
		return "", fmt.Errorf("deepseek: read response: %w", err)
	}

	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		// Sanitise the response body — strip anything that looks like an API key.
		sanitised := sanitiseForLog(string(respBytes), 200)
		c.logger.Warnf("deepseek: non-200 model=%s status=%d elapsed=%v body=%s",
			c.model, resp.StatusCode, elapsed, sanitised)
		return "", fmt.Errorf("deepseek: HTTP %d: %s", resp.StatusCode, sanitised)
	}

	var result deepSeekResponse
	if err := json.Unmarshal(respBytes, &result); err != nil {
		c.logger.Errorf("deepseek: unmarshal response failed model=%s elapsed=%v bodyLen=%d err=%v",
			c.model, elapsed, len(respBytes), err)
		return "", fmt.Errorf("deepseek: unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		c.logger.Warnf("deepseek: empty choices model=%s elapsed=%v bodyLen=%d", c.model, elapsed, len(respBytes))
		return "", fmt.Errorf("deepseek: empty choices in response")
	}

	content := strings.TrimSpace(result.Choices[0].Message.Content)
	c.logger.Debugf("deepseek: OK model=%s elapsed=%v promptLen=%d responseLen=%d",
		c.model, elapsed, len(prompt), len(content))

	return content, nil
}

// sanitiseForLog truncates s to maxLen and removes obvious key-like patterns,
// so that a 401 "Invalid API key: sk-xxx" response from the API won't leak
// the full key into application logs.
func sanitiseForLog(s string, maxLen int) string {
	// Replace common key patterns: "sk-" followed by alphanumerics.
	const keyPrefix = "sk-"
	if idx := strings.Index(s, keyPrefix); idx >= 0 {
		end := idx + len(keyPrefix)
		for end < len(s) && (s[end] >= 'a' && s[end] <= 'z' ||
			s[end] >= 'A' && s[end] <= 'Z' ||
			s[end] >= '0' && s[end] <= '9') {
			end++
		}
		s = s[:idx] + "sk-***" + s[end:]
	}
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

// ── JSON payload types ──

type deepSeekRequest struct {
	Model    string           `json:"model"`
	Messages []deepSeekMessage `json:"messages"`
	Stream   bool             `json:"stream"`
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepSeekResponse struct {
	Choices []deepSeekChoice `json:"choices"`
}

type deepSeekChoice struct {
	Message deepSeekMessage `json:"message"`
}
