package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"knowledge-mcp/internal/logging"
)

// GPUScheduler manages the sleep/wake lifecycle of embedding and reranker
// models on a shared GPU, ensuring only one model is loaded at a time.
// This is useful when both models cannot fit simultaneously in GPU memory.
type GPUScheduler struct {
	managerURL string        // Manager API URL, e.g. "http://10.95.222.190:11436"
	timeout    time.Duration // HTTP timeout for sleep/wake requests (default 30s)
	wakeDelay  time.Duration // Delay after wake to let the model load into GPU (default 3s)
	enabled    bool
	client     *http.Client
	logger     *logging.Logger
}

// GPUSchedulerOption configures a GPUScheduler.
type GPUSchedulerOption func(*GPUScheduler)

// WithSchedulerManagerURL sets the Manager API URL.
func WithSchedulerManagerURL(url string) GPUSchedulerOption {
	return func(s *GPUScheduler) {
		s.managerURL = strings.TrimRight(url, "/")
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

// NewGPUScheduler creates a GPUScheduler from environment variables.
// Environment variables (all optional):
//
//	GPU_SCHEDULER_ENABLED   — "true" or "1" to enable (default: false)
//	GPU_SCHEDULER_MANAGER_URL — Manager API URL (default: "http://localhost:11436")
//	GPU_SCHEDULER_TIMEOUT    — HTTP timeout (default: "30s")
//	GPU_SCHEDULER_WAKE_DELAY — delay after wake (default: "3s")
func NewGPUScheduler(opts ...GPUSchedulerOption) *GPUScheduler {
	s := &GPUScheduler{
		managerURL: "http://localhost:11436",
		timeout:    30 * time.Second,
		wakeDelay:  3 * time.Second,
		enabled:    false,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logging.NewNopLogger(),
	}

	// Read from env vars.
	if v := os.Getenv("GPU_SCHEDULER_ENABLED"); v == "true" || v == "1" {
		s.enabled = true
	}
	if v := os.Getenv("GPU_SCHEDULER_MANAGER_URL"); v != "" {
		s.managerURL = strings.TrimRight(v, "/")
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

// ManagerURL returns the manager API URL.
func (s *GPUScheduler) ManagerURL() string {
	return s.managerURL
}

// sleep sends a POST /sleep/{service} request to the manager API.
func (s *GPUScheduler) sleep(ctx context.Context, service string) error {
	url := s.managerURL + "/sleep/" + service
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("create sleep request for %q: %w", service, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("sleep %q request failed: %w", service, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sleep %q returned status %d", service, resp.StatusCode)
	}
	s.logger.Debugf("gpu-scheduler: sleep %q → %s", service, resp.Status)
	return nil
}

// wake sends a POST /wake/{service} request to the manager API, then waits
// for wakeDelay to allow the model to load into GPU memory.
func (s *GPUScheduler) wake(ctx context.Context, service string) error {
	url := s.managerURL + "/wake/" + service
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("create wake request for %q: %w", service, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("wake %q request failed: %w", service, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("wake %q returned status %d", service, resp.StatusCode)
	}
	s.logger.Debugf("gpu-scheduler: wake %q → %s", service, resp.Status)
	// Wait for the model to load.
	if s.wakeDelay > 0 {
		s.logger.Debugf("gpu-scheduler: waiting %v for %q to load", s.wakeDelay, service)
		select {
		case <-time.After(s.wakeDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// GPUSchedulerStatus represents the status response from the manager API.
type GPUSchedulerStatus struct {
	Services map[string]struct {
		Status string `json:"status"`
	} `json:"services"`
}

// Status queries the manager API for current model states.
func (s *GPUScheduler) Status(ctx context.Context) (*GPUSchedulerStatus, error) {
	url := s.managerURL + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create status request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("status request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status returned %d", resp.StatusCode)
	}
	var st GPUSchedulerStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}
	return &st, nil
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
	if err := s.sleep(context.Background(), "reranker"); err != nil {
		log.Warnf("sleep reranker failed (continuing): %v", err)
	}
	// Wake embedding model.
	if err := s.wake(context.Background(), "embedding"); err != nil {
		log.Warnf("wake embedding failed (continuing): %v", err)
	}

	return func() {
		// Restore: sleep embedding, wake reranker.
		if err := s.sleep(context.Background(), "embedding"); err != nil {
			log.Warnf("sleep embedding (restore) failed: %v", err)
		}
		if err := s.wake(context.Background(), "reranker"); err != nil {
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
	if err := s.sleep(context.Background(), "embedding"); err != nil {
		log.Warnf("sleep embedding failed (continuing): %v", err)
	}
	// Wake reranker model.
	if err := s.wake(context.Background(), "reranker"); err != nil {
		log.Warnf("wake reranker failed (continuing): %v", err)
	}

	return func() {
		// Restore: sleep reranker, wake embedding.
		if err := s.sleep(context.Background(), "reranker"); err != nil {
			log.Warnf("sleep reranker (restore) failed: %v", err)
		}
		if err := s.wake(context.Background(), "embedding"); err != nil {
			log.Warnf("wake embedding (restore) failed: %v", err)
		}
	}
}
