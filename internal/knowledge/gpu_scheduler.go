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

// GPUScheduler manages the sleep/wake lifecycle of embedding and reranker
// models on a shared GPU, ensuring only one model is loaded at a time.
// This is useful when both models cannot fit simultaneously in GPU memory.
//
// Each model has its own sleep/wake API URLs since the two models may use
// different endpoints or require different request bodies (e.g. reranker
// sleep requires a JSON body with sleep level).
type GPUScheduler struct {
	embeddingSleepURL  string        // URL to sleep the embedding model
	embeddingWakeURL   string        // URL to wake the embedding model
	embeddingSleepBody string        // Optional JSON body for embedding sleep request
	rerankerSleepURL   string        // URL to sleep the reranker model
	rerankerWakeURL    string        // URL to wake the reranker model
	rerankerSleepBody  string        // Optional JSON body for reranker sleep request (default `{"level":2}`)
	timeout            time.Duration // HTTP timeout for sleep/wake requests (default 30s)
	wakeDelay          time.Duration // Delay after wake to let the model load into GPU (default 3s)
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

// WithSchedulerEmbeddingWakeURL sets the URL to wake the embedding model.
func WithSchedulerEmbeddingWakeURL(url string) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.embeddingWakeURL = url
	}
}

// WithSchedulerRerankerSleepURL sets the URL to sleep the reranker model.
func WithSchedulerRerankerSleepURL(url string) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.rerankerSleepURL = url
	}
}

// WithSchedulerRerankerWakeURL sets the URL to wake the reranker model.
func WithSchedulerRerankerWakeURL(url string) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.rerankerWakeURL = url
	}
}

// WithSchedulerTimeout sets the HTTP timeout for sleep/wake requests.
func WithSchedulerTimeout(d time.Duration) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.timeout = d
	}
}

// WithSchedulerWakeDelay sets the delay after wake for model loading.
func WithSchedulerWakeDelay(d time.Duration) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.wakeDelay = d
	}
}

// WithSchedulerLogger sets the logger on the scheduler.
func WithSchedulerLogger(l *logging.Logger) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.logger = l
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
//	GPU_SCHEDULER_EMBEDDING_WAKE_URL     — Embedding model wake API URL (default: empty, must be set if enabled)
//	GPU_SCHEDULER_EMBEDDING_SLEEP_BODY   — JSON body for embedding sleep request (default: empty)
//	GPU_SCHEDULER_RERANKER_SLEEP_URL     — Reranker model sleep API URL (default: empty, must be set if enabled)
//	GPU_SCHEDULER_RERANKER_WAKE_URL      — Reranker model wake API URL (default: empty, must be set if enabled)
//	GPU_SCHEDULER_RERANKER_SLEEP_BODY    — JSON body for reranker sleep (default: {"level":2})
//	GPU_SCHEDULER_TIMEOUT                — HTTP timeout (default: "30s")
//	GPU_SCHEDULER_WAKE_DELAY             — delay after wake (default: "3s")
func NewGPUScheduler(opts ...GPUSchedulerOption) *GPUScheduler {
	s := &GPUScheduler{
		rerankerSleepURL: "",
		rerankerWakeURL:  "",
		rerankerSleepBody: `{"level":2}`,
		timeout:          30 * time.Second,
		wakeDelay:        3 * time.Second,
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
	if v := os.Getenv("GPU_SCHEDULER_EMBEDDING_WAKE_URL"); v != "" {
		s.embeddingWakeURL = v
	}
	if v := os.Getenv("GPU_SCHEDULER_EMBEDDING_SLEEP_BODY"); v != "" {
		s.embeddingSleepBody = v
	}
	if v := os.Getenv("GPU_SCHEDULER_RERANKER_SLEEP_URL"); v != "" {
		s.rerankerSleepURL = v
	}
	if v := os.Getenv("GPU_SCHEDULER_RERANKER_WAKE_URL"); v != "" {
		s.rerankerWakeURL = v
	}
	if v := os.Getenv("GPU_SCHEDULER_RERANKER_SLEEP_BODY"); v != "" {
		s.rerankerSleepBody = v
	}
	if v := os.Getenv("GPU_SCHEDULER_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			s.timeout = d
			s.client.Timeout = d
		}
	}
	if v := os.Getenv("GPU_SCHEDULER_WAKE_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			s.wakeDelay = d
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
	if s.embeddingWakeURL != "" {
		parts = append(parts, "embed-wake="+s.embeddingWakeURL)
	}
	if s.rerankerSleepURL != "" {
		parts = append(parts, "reranker-sleep="+s.rerankerSleepURL)
	}
	if s.rerankerWakeURL != "" {
		parts = append(parts, "reranker-wake="+s.rerankerWakeURL)
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

// doWake sends a POST request to the given URL, then waits for wakeDelay to
// allow the model to load into GPU memory.
func (s *GPUScheduler) doWake(ctx context.Context, url string) error {
	if url == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("create wake request for %q: %w", url, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("wake request to %q failed: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("wake %q returned status %d", url, resp.StatusCode)
	}
	s.logger.Debugf("gpu-scheduler: wake %q → %s", url, resp.Status)
	// Wait for the model to load.
	if s.wakeDelay > 0 {
		s.logger.Debugf("gpu-scheduler: waiting %v for %q to load", s.wakeDelay, url)
		select {
		case <-time.After(s.wakeDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// ProbeResult holds the probe result for a single endpoint.
type ProbeResult struct {
	URL    string
	Status string
	Err    string `json:",omitempty"`
}

// Probe checks connectivity to each configured sleep/wake endpoint by sending
// a GET request. Returns a human-readable summary.
func (s *GPUScheduler) Probe(ctx context.Context) (string, error) {
	urls := []string{}
	if s.embeddingSleepURL != "" {
		urls = append(urls, s.embeddingSleepURL)
	}
	if s.embeddingWakeURL != "" {
		urls = append(urls, s.embeddingWakeURL)
	}
	if s.rerankerSleepURL != "" {
		urls = append(urls, s.rerankerSleepURL)
	}
	if s.rerankerWakeURL != "" {
		urls = append(urls, s.rerankerWakeURL)
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

// PrepareForEmbedding switches the GPU to embedding mode:
// it sleeps the reranker (if loaded) and wakes the embedding model.
// Returns a restore function that restores the previous state (sleeps embedding,
// wakes reranker). The restore function is idempotent-safe — it logs warnings
// on failure but does not return an error, making it suitable for defer.
//
// When the scheduler is disabled, this is a no-op and returns a no-op restore.
func (s *GPUScheduler) PrepareForEmbedding() (restore func()) {
	if !s.enabled {
		return func() {}
	}
	log := s.logger.WithModule("gpu-scheduler")

	// Sleep reranker first (releases GPU memory for embedding).
	if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
		log.Warnf("sleep reranker failed (continuing): %v", err)
	}
	// Wake embedding model.
	if err := s.doWake(context.Background(), s.embeddingWakeURL); err != nil {
		log.Warnf("wake embedding failed (continuing): %v", err)
	}

	return func() {
		// Restore: sleep embedding, wake reranker.
		if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
			log.Warnf("sleep embedding (restore) failed: %v", err)
		}
		if err := s.doWake(context.Background(), s.rerankerWakeURL); err != nil {
			log.Warnf("wake reranker (restore) failed: %v", err)
		}
	}
}

// PrepareForReranking switches the GPU to reranker mode:
// it sleeps the embedding model and wakes the reranker.
// Returns a restore function that restores the previous state (sleeps reranker,
// wakes embedding). The restore function is idempotent-safe.
//
// When the scheduler is disabled, this is a no-op and returns a no-op restore.
func (s *GPUScheduler) PrepareForReranking() (restore func()) {
	if !s.enabled {
		return func() {}
	}
	log := s.logger.WithModule("gpu-scheduler")

	// Sleep embedding first (releases GPU memory for reranker).
	if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
		log.Warnf("sleep embedding failed (continuing): %v", err)
	}
	// Wake reranker model.
	if err := s.doWake(context.Background(), s.rerankerWakeURL); err != nil {
		log.Warnf("wake reranker failed (continuing): %v", err)
	}

	return func() {
		// Restore: sleep reranker, wake embedding.
		if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
			log.Warnf("sleep reranker (restore) failed: %v", err)
		}
		if err := s.doWake(context.Background(), s.embeddingWakeURL); err != nil {
			log.Warnf("wake embedding (restore) failed: %v", err)
		}
	}
}

// ensure encoding/json is used (for ProbeResult struct tags)
var _ = json.Marshal
