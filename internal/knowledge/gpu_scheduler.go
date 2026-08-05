package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"knowledge-mcp/internal/logging"
)

// modelState tracks which model is currently active on the GPU.
type modelState int

const (
	stateIdle      modelState = iota // all models sleeping
	stateEmbedding                   // embedding model loaded
	stateReranker                    // reranker model loaded
	stateDocParser                   // doc parser model loaded
)

func (s modelState) String() string {
	switch s {
	case stateIdle:
		return "idle"
	case stateEmbedding:
		return "embedding"
	case stateReranker:
		return "reranker"
	case stateDocParser:
		return "doc-parser"
	default:
		return "unknown"
	}
}

// GPUScheduler manages the sleep/wake lifecycle of embedding, reranker,
// and document parser models on a shared GPU, ensuring only one model is
// loaded at a time. This is useful when models cannot fit simultaneously
// in GPU memory.
//
// Each model has its own sleep/wake API URLs since they may use different
// endpoints or require different request bodies (e.g. reranker sleep
// requires a JSON body with sleep level).
//
// Concurrency: all PrepareFor* methods acquire an internal mutex and return
// a restore function that releases it. Only one model operation can be in
// flight at a time. Callers MUST call the restore function before invoking
// another PrepareFor* — failing to do so will deadlock.
type GPUScheduler struct {
	mu sync.Mutex

	embeddingSleepURL  string        // URL to sleep the embedding model
	embeddingSleepBody string        // Optional JSON body for embedding sleep request
	rerankerSleepURL   string        // URL to sleep the reranker model
	rerankerSleepBody  string        // Optional JSON body for reranker sleep request (default `{"level":2}`)
	docParserSleepURL  string        // URL to sleep the document parser model
	docParserSleepBody string        // Optional JSON body for doc parser sleep request
	timeout            time.Duration // HTTP timeout for sleep requests (default 30s)
	sleepCooldown      time.Duration // wait after sleep to let CUDA free memory (default 3s)
	enabled            bool
	client             *http.Client
	logger             *logging.Logger

	// State tracking to avoid redundant sleep calls.
	activeModel modelState
}

// GPUSchedulerOption configures a GPUScheduler.
type GPUSchedulerOption func(*GPUScheduler)

// validateLoopbackURL checks that urlStr is safe for SSRF prevention:
// host must be localhost, 127.0.0.1, or ::1 (loopback-only).
func validateLoopbackURL(urlStr string) error {
	if urlStr == "" {
		return nil
	}
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("gpu-scheduler: parse URL %q: %w", urlStr, err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("gpu-scheduler: URL %q has no host", urlStr)
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("gpu-scheduler: URL %q host %q is not loopback — SSRF prevention", urlStr, host)
	}
	return nil
}

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

// WithSchedulerSleepCooldown sets the wait duration after a sleep request
// before considering the model unloaded from GPU memory. Default 3s.
func WithSchedulerSleepCooldown(d time.Duration) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.sleepCooldown = d
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
//	GPU_SCHEDULER_SLEEP_COOLDOWN          — wait after sleep for CUDA mem free (default: "0", Python services handle this)
func NewGPUScheduler(opts ...GPUSchedulerOption) *GPUScheduler {
	s := &GPUScheduler{
		rerankerSleepURL:  "",
		rerankerSleepBody: `{"level":2}`,
		timeout:           30 * time.Second,
		sleepCooldown:     0, // Python 端已处理显存释放等待，Go 端默认不重复等待
		enabled:           false,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					// SSRF prevention: only allow loopback connections.
					host, _, err := net.SplitHostPort(addr)
					if err != nil {
						host = addr
					}
					if host != "127.0.0.1" && host != "::1" && host != "localhost" {
						return nil, fmt.Errorf("gpu-scheduler: connection to %q blocked (loopback only)", host)
					}
					d := net.Dialer{}
					return d.DialContext(ctx, network, addr)
				},
			},
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
	if v := os.Getenv("GPU_SCHEDULER_SLEEP_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			s.sleepCooldown = d
		}
	}

	for _, opt := range opts {
		opt(s)
	}

	// SSRF prevention: validate all sleep URLs are loopback-only and disable
	// the scheduler if any are misconfigured.
	if s.enabled {
		for _, u := range []string{s.embeddingSleepURL, s.rerankerSleepURL, s.docParserSleepURL} {
			if err := validateLoopbackURL(u); err != nil {
				s.logger.Errorf("%v — GPU scheduler disabled", err)
				s.enabled = false
				break
			}
		}
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

// doSleep sends a POST request to the given URL and waits for the cooldown
// duration to allow CUDA to free GPU memory. If body is non-empty, it is
// sent as the request body with Content-Type: application/json.
//
// IMPORTANT: Caller must hold s.mu.
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
	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("sleep request to %q failed: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sleep %q returned status %d", url, resp.StatusCode)
	}
	s.logger.Infof("gpu-scheduler: sleep %q → %s (took %s)", url, resp.Status, time.Since(start))

	// Wait for CUDA to actually free GPU memory before returning.
	// The Python services (reranker-server.py, manager-server.py) now
	// handle GPU memory release verification via wait_gpu_memory_release(),
	// so this cooldown is disabled by default (set to 0).
	if s.sleepCooldown > 0 {
		s.logger.Debugf("gpu-scheduler: cooling down %s for CUDA memory release", s.sleepCooldown)
		time.Sleep(s.sleepCooldown)
	}
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
//
// This method acquires the scheduler's internal mutex. The returned restore
// function MUST be called to release the lock — failing to do so will deadlock
// all other model operations.
//
// The restore function only releases the lock; it does NOT sleep the embedding
// model. The next PrepareFor* call will sleep whatever is currently active.
//
// When the scheduler is disabled, this is a no-op and returns a no-op restore.
func (s *GPUScheduler) PrepareForEmbedding() (restore func()) {
	if !s.enabled {
		return func() {}
	}

	s.mu.Lock()
	log := s.logger.WithModule("gpu-scheduler")

	// If already in embedding mode, skip the switch (avoid redundant sleep+cooldown).
	if s.activeModel == stateEmbedding {
		log.Debugf("gpu-scheduler: already in embedding mode, skipping switch")
		return func() {
			s.mu.Unlock()
		}
	}

	log.Infof("gpu-scheduler: switching from %s → embedding", s.activeModel)

	// Sleep whatever is currently active to free GPU memory.
	switch s.activeModel {
	case stateReranker:
		if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
			log.Warnf("sleep reranker failed (continuing): %v", err)
		}
	case stateDocParser:
		if err := s.doSleep(context.Background(), s.docParserSleepURL, s.docParserSleepBody); err != nil {
			log.Warnf("sleep doc parser failed (continuing): %v", err)
		}
	default:
		// Also sleep other models defensively if state is unknown (idle or uninitialized).
		if s.activeModel != stateReranker {
			if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
				log.Warnf("sleep reranker failed (continuing): %v", err)
			}
		}
		if s.activeModel != stateDocParser {
			if err := s.doSleep(context.Background(), s.docParserSleepURL, s.docParserSleepBody); err != nil {
				log.Warnf("sleep doc parser failed (continuing): %v", err)
			}
		}
	}

	s.activeModel = stateEmbedding
	log.Infof("gpu-scheduler: embedding model ready")

	return func() {
		log.Debugf("gpu-scheduler: releasing embedding lock")
		s.mu.Unlock()
	}
}

// PrepareForReranking ensures the reranker model has GPU access by sleeping
// the embedding model and document parser (if loaded). The reranker API
// auto-wakes on first call.
//
// This method acquires the scheduler's internal mutex. The returned restore
// function MUST be called to release the lock — failing to do so will deadlock
// all other model operations.
//
// The restore function only releases the lock; it does NOT sleep the reranker
// model. The next PrepareFor* call will sleep whatever is currently active.
//
// When the scheduler is disabled, this is a no-op and returns a no-op restore.
func (s *GPUScheduler) PrepareForReranking() (restore func()) {
	if !s.enabled {
		return func() {}
	}

	s.mu.Lock()
	log := s.logger.WithModule("gpu-scheduler")

	if s.activeModel == stateReranker {
		log.Debugf("gpu-scheduler: already in reranker mode, skipping switch")
		return func() {
			s.mu.Unlock()
		}
	}

	log.Infof("gpu-scheduler: switching from %s → reranker", s.activeModel)

	switch s.activeModel {
	case stateEmbedding:
		if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
			log.Warnf("sleep embedding failed (continuing): %v", err)
		}
	case stateDocParser:
		if err := s.doSleep(context.Background(), s.docParserSleepURL, s.docParserSleepBody); err != nil {
			log.Warnf("sleep doc parser failed (continuing): %v", err)
		}
	default:
		if s.activeModel != stateEmbedding {
			if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
				log.Warnf("sleep embedding failed (continuing): %v", err)
			}
		}
		if s.activeModel != stateDocParser {
			if err := s.doSleep(context.Background(), s.docParserSleepURL, s.docParserSleepBody); err != nil {
				log.Warnf("sleep doc parser failed (continuing): %v", err)
			}
		}
	}

	s.activeModel = stateReranker
	log.Infof("gpu-scheduler: reranker model ready")

	return func() {
		log.Debugf("gpu-scheduler: releasing reranker lock")
		s.mu.Unlock()
	}
}

// PrepareForDocParsing ensures the document parser model has GPU access by
// sleeping the embedding and reranker models (if loaded). The doc parser API
// auto-wakes on first call.
//
// This method acquires the scheduler's internal mutex. The returned restore
// function MUST be called to release the lock — failing to do so will deadlock
// all other model operations.
//
// The restore function only releases the lock; it does NOT sleep the doc parser
// model. The next PrepareFor* call will sleep whatever is currently active.
//
// When the scheduler is disabled, this is a no-op and returns a no-op restore.
func (s *GPUScheduler) PrepareForDocParsing() (restore func()) {
	if !s.enabled {
		return func() {}
	}

	s.mu.Lock()
	log := s.logger.WithModule("gpu-scheduler")

	if s.activeModel == stateDocParser {
		log.Debugf("gpu-scheduler: already in doc-parser mode, skipping switch")
		return func() {
			s.mu.Unlock()
		}
	}

	log.Infof("gpu-scheduler: switching from %s → doc-parser", s.activeModel)

	switch s.activeModel {
	case stateEmbedding:
		if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
			log.Warnf("sleep embedding failed (continuing): %v", err)
		}
	case stateReranker:
		if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
			log.Warnf("sleep reranker failed (continuing): %v", err)
		}
	default:
		if s.activeModel != stateEmbedding {
			if err := s.doSleep(context.Background(), s.embeddingSleepURL, s.embeddingSleepBody); err != nil {
				log.Warnf("sleep embedding failed (continuing): %v", err)
			}
		}
		if s.activeModel != stateReranker {
			if err := s.doSleep(context.Background(), s.rerankerSleepURL, s.rerankerSleepBody); err != nil {
				log.Warnf("sleep reranker failed (continuing): %v", err)
			}
		}
	}

	s.activeModel = stateDocParser
	log.Infof("gpu-scheduler: doc-parser model ready")

	return func() {
		log.Debugf("gpu-scheduler: releasing doc-parser lock")
		s.mu.Unlock()
	}
}

// ensure encoding/json is used (for ProbeResult struct tags)
var _ = json.Marshal
