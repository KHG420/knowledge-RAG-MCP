package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"knowledge-mcp/internal/logging"
)

// GPUScheduler manages the sleep/wake lifecycle of embedding, reranker,
// and document parser models on a shared GPU, ensuring only one model is
// loaded at a time. This is useful when models cannot fit simultaneously
// in GPU memory.
//
// Each model has its own sleep/wake API URLs since they may use different
// endpoints or require different request bodies (e.g. reranker sleep
// requires a JSON body with sleep level).
type GPUScheduler struct {
	embeddingSleepURL  string        // URL to sleep the embedding model
	embeddingSleepBody string        // Optional JSON body for embedding sleep request
	rerankerSleepURL   string        // URL to sleep the reranker model
	rerankerSleepBody  string        // Optional JSON body for reranker sleep request (default `{"level":2}`)
	docParserSleepURL  string        // URL to sleep the document parser model
	docParserSleepBody string        // Optional JSON body for doc parser sleep request
	timeout            time.Duration // HTTP timeout for sleep requests (default 30s)
	enabled            bool
	client             *http.Client
	logger             *logging.Logger
}

// GPUSchedulerOption configures a GPUScheduler.
type GPUSchedulerOption func(*GPUScheduler)

// WithSchedulerEmbeddingSleepURL sets the URL to sleep the embedding model.
func WithSchedulerEmbeddingSleepURL(url string) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.embeddingSleepURL = url
	}
}

// WithSchedulerRerankerSleepURL sets the URL to sleep the reranker model.
func WithSchedulerRerankerSleepURL(url string) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.rerankerSleepURL = url
	}
}

// WithSchedulerTimeout sets the HTTP timeout for sleep/wake requests.
func WithSchedulerTimeout(d time.Duration) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.timeout = d
	}
}

// WithSchedulerLogger sets the logger on the scheduler.
func WithSchedulerLogger(l *logging.Logger) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.logger = l
	}
}

// WithSchedulerDocParserSleepURL sets the URL to sleep the document parser model.
func WithSchedulerDocParserSleepURL(url string) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.docParserSleepURL = url
	}
}

// WithSchedulerEnabled explicitly sets the enabled state of the GPU scheduler.
// When true, the scheduler coordinates model sleep/wake. Default is false.
// This overrides the GPU_SCHEDULER_ENABLED environment variable.
func WithSchedulerEnabled(enabled bool) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.enabled = enabled
	}
}

// NewGPUScheduler creates a GPUScheduler from environment variables.
// Environment variables (all optional):
//
//	GPU_SCHEDULER_ENABLED                — "true" or "1" to enable (default: false)
//	GPU_SCHEDULER_EMBEDDING_SLEEP_URL    — Embedding model sleep API URL (default: empty, must be set if enabled)
//	GPU_SCHEDULER_EMBEDDING_SLEEP_BODY   — JSON body for embedding sleep request (default: empty)
//	GPU_SCHEDULER_RERANKER_SLEEP_URL     — Reranker model sleep API URL (default: empty, must be set if enabled)
//	GPU_SCHEDULER_RERANKER_SLEEP_BODY    — JSON body for reranker sleep (default: {"level":2})
//	GPU_SCHEDULER_DOC_PARSER_SLEEP_URL   — Document parser model sleep API URL (default: empty)
//	GPU_SCHEDULER_DOC_PARSER_SLEEP_BODY  — JSON body for doc parser sleep request (default: empty)
//	GPU_SCHEDULER_TIMEOUT                — HTTP timeout (default: "30s")
func NewGPUScheduler(opts ...GPUSchedulerOption) *GPUScheduler {
	s := &GPUScheduler{
		rerankerSleepURL: "",
		rerankerSleepBody: `{"level":2}`,
		timeout:          30 * time.Second,
		enabled:          false,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logging.NewNopLogger(),
	}

	// Read from env vars.
	if v := os.Getenv("GPU_SCHEDULER_ENABLED"); v == "true" || v == "1" {
		s.enabled = true
	}
	if v := os.Getenv("GPU_SCHEDULER_EMBEDDING_SLEEP_URL"); v != "" {
		s.embeddingSleepURL = v
	}
	if v := os.Getenv("GPU_SCHEDULER_EMBEDDING_SLEEP_BODY"); v != "" {
		s.embeddingSleepBody = v
	}
	if v := os.Getenv("GPU_SCHEDULER_RERANKER_SLEEP_URL"); v != "" {
		s.rerankerSleepURL = v
	}
	if v := os.Getenv("GPU_SCHEDULER_RERANKER_SLEEP_BODY"); v != "" {
		s.rerankerSleepBody = v
	}
	if v := os.Getenv("GPU_SCHEDULER_DOC_PARSER_SLEEP_URL"); v != "" {
		s.docParserSleepURL = v
	}
	if v := os.Getenv("GPU_SCHEDULER_DOC_PARSER_SLEEP_BODY"); v != "" {
		s.docParserSleepBody = v
	}
	if v := os.Getenv("GPU_SCHEDULER_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			s.timeout = d
			s.client.Timeout = d
		}
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Enabled returns whether the GPU scheduler is active.
func (s *GPUScheduler) Enabled() bool {
	return s.enabled
}

// Summary returns a readable summary of the scheduler configuration for logging.
func (s *GPUScheduler) Summary() string {
	var parts []string
	if s.embeddingSleepURL != "" {
		parts = append(parts, "embed-sleep="+s.embeddingSleepURL)
	}
	if s.rerankerSleepURL != "" {
		parts = append(parts, "reranker-sleep="+s.rerankerSleepURL)
	}
	if s.docParserSleepURL != "" {
		parts = append(parts, "doc-parser-sleep="+s.docParserSleepURL)
	}
	return strings.Join(parts, ", ")
}

// doSleep sends a POST request to the given URL. If body is non-empty, it is
// sent as the request body with Content-Type: application/json.
func (s *GPUScheduler) doSleep(ctx context.Context, url, body string) error {
	if url == "" {
		return nil
	}
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return fmt.Errorf("create sleep request for %q: %w", url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("sleep request to %q failed: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sleep %q returned status %d", url, resp.StatusCode)
	}
	s.logger.Debugf("gpu-scheduler: sleep %q → %s", url, resp.Status)
	return nil
}

// ProbeResult holds the probe result for a single endpoint.
type ProbeResult struct {
	URL    string
	Status string
	Err    string `json:",omitempty"`
}

// Probe checks connectivity to each configured sleep endpoint by sending
// a GET request. Returns a human-readable summary.
func (s *GPUScheduler) Probe(ctx context.Context) (string, error) {
	urls := []string{}
	if s.embeddingSleepURL != "" {
		urls = append(urls, s.embeddingSleepURL)
	}
	if s.rerankerSleepURL != "" {
		urls = append(urls, s.rerankerSleepURL)
	}
	if s.docParserSleepURL != "" {
		urls = append(urls, s.docParserSleepURL)
	}

	var results []ProbeResult
	var lastErr error
	for _, u := range urls {
		pr := ProbeResult{URL: u}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			pr.Err = err.Error()
			lastErr = err
		} else {
			resp, reqErr := s.client.Do(req)
			if reqErr != nil {
				pr.Err = reqErr.Error()
				lastErr = reqErr
			} else {
				pr.Status = resp.Status
				resp.Body.Close()
			}
		}
		results = append(results, pr)
	}

	var lines []string
	for _, r := range results {
		if r.Err != "" {
			lines = append(lines, fmt.Sprintf("%s: %s (error: %s)", r.URL, r.Status, r.Err))
		} else {
			lines = append(lines, fmt.Sprintf("%s: %s", r.URL, r.Status))
		}
	}
	summary := strings.Join(lines, "; ")
	return summary, lastErr
}

// PrepareForEmbedding ensures the embedding model has GPU access by sleeping
// the reranker and document parser (if loaded). The embedding API auto-wakes
// on first call.
// Returns a restore function that sleeps the embedding model (so the reranker
// or doc parser can load later). The restore function is idempotent-safe — it
// logs warnings on failure but does not return an error, making it suitable
// for defer.
//
// When the scheduler is disabled, this is a no-op and returns a no-op restore.
func (s *GPUScheduler) PrepareForEmbedding() (restore func()) {
	if !s.enabled {
		return func() {}
	}
	log := s.logger.WithModule("gpu-scheduler")

	// Sleep reranker and doc parser (releases GPU memory for embedding).
	if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
		log.Warnf("sleep reranker failed (continuing): %v", err)
	}
	if err := s.doSleep(context.Background(), s.docParserSleepURL, s.docParserSleepBody); err != nil {
		log.Warnf("sleep doc parser failed (continuing): %v", err)
	}

	return func() {
		// Restore: sleep embedding (reranker/doc parser auto-wakes on next call).
		if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
			log.Warnf("sleep embedding (restore) failed: %v", err)
		}
	}
}

// PrepareForReranking ensures the reranker model has GPU access by sleeping
// the embedding model and document parser (if loaded). The reranker API
// auto-wakes on first call.
// Returns a restore function that sleeps the reranker (so the embedding model
// or doc parser can load later). The restore function is idempotent-safe.
//
// When the scheduler is disabled, this is a no-op and returns a no-op restore.
func (s *GPUScheduler) PrepareForReranking() (restore func()) {
	if !s.enabled {
		return func() {}
	}
	log := s.logger.WithModule("gpu-scheduler")

	// Sleep embedding and doc parser (releases GPU memory for reranker).
	if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
		log.Warnf("sleep embedding failed (continuing): %v", err)
	}
	if err := s.doSleep(context.Background(), s.docParserSleepURL, s.docParserSleepBody); err != nil {
		log.Warnf("sleep doc parser failed (continuing): %v", err)
	}

	return func() {
		// Restore: sleep reranker (embedding/doc parser auto-wakes on next call).
		if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
			log.Warnf("sleep reranker (restore) failed: %v", err)
		}
	}
}

// PrepareForDocParsing ensures the document parser model has GPU access by
// sleeping the embedding and reranker models (if loaded). The doc parser API
// auto-wakes on first call.
// Returns a restore function that sleeps the doc parser (so the embedding or
// reranker can load later). The restore function is idempotent-safe.
//
// When the scheduler is disabled, this is a no-op and returns a no-op restore.
func (s *GPUScheduler) PrepareForDocParsing() (restore func()) {
	if !s.enabled {
		return func() {}
	}
	log := s.logger.WithModule("gpu-scheduler")

	// Sleep embedding and reranker (releases GPU memory for doc parser).
	if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
		log.Warnf("sleep embedding failed (continuing): %v", err)
	}
	if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
		log.Warnf("sleep reranker failed (continuing): %v", err)
	}

	return func() {
		// Restore: sleep doc parser (embedding/reranker auto-wakes on next call).
		if err := s.doSleep(context.Background(), s.docParserSleepURL, s.docParserSleepBody); err != nil {
			log.Warnf("sleep doc parser (restore) failed: %v", err)
		}
	}
}

// ensure encoding/json is used (for ProbeResult struct tags)
var _ = json.Marshal
