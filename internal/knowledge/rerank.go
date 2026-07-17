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

// InfinityReranker calls an Infinity or Cohere-compatible /rerank API for
// cross-encoder reranking. It implements the Reranker interface.
//
// Deploy with Infinity (recommended):
//
//	pip install infinity-emb[all]
//	infinity_emb v2 --model-id Alibaba-NLP/gte-multilingual-reranker-base --port 7997
//
// Or any server exposing Cohere's /rerank format.
type InfinityReranker struct {
	endpointURL string
	apiKey      string
	model       string
	client      *http.Client
	logger      *logging.Logger
}

// InfinityRerankerOption configures an InfinityReranker.
type InfinityRerankerOption func(*InfinityReranker)

// WithRerankEndpointURL sets the full reranker API endpoint URL.
func WithRerankEndpointURL(endpointURL string) InfinityRerankerOption {
	return func(r *InfinityReranker) {
		r.endpointURL = strings.TrimRight(endpointURL, "/")
	}
}

// WithRerankAPIKey sets an optional API key for the reranker endpoint.
func WithRerankAPIKey(key string) InfinityRerankerOption {
	return func(r *InfinityReranker) {
		r.apiKey = key
	}
}

// WithRerankModel sets the reranker model name (default "gte-multilingual-reranker-base").
func WithRerankModel(model string) InfinityRerankerOption {
	return func(r *InfinityReranker) {
		r.model = model
	}
}

// WithRerankLogger sets the logger on the reranker.
func WithRerankLogger(l *logging.Logger) InfinityRerankerOption {
	return func(r *InfinityReranker) {
		r.logger = l
	}
}

// Timeout returns the HTTP client timeout for reranker requests.
func (r *InfinityReranker) Timeout() time.Duration {
	return r.client.Timeout
}

// WithRerankTimeout sets the HTTP client timeout for reranker requests.
// Default is 30 seconds. Use Go duration strings like "10s", "30s", "1m".
func WithRerankTimeout(d time.Duration) InfinityRerankerOption {
	return func(r *InfinityReranker) {
		r.client.Timeout = d
	}
}

// NewInfinityReranker returns an InfinityReranker.
func NewInfinityReranker(opts ...InfinityRerankerOption) *InfinityReranker {
	r := &InfinityReranker{
		endpointURL: "http://localhost:7997/rerank",
		model:   "gte-multilingual-reranker-base",
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logging.NewNopLogger(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

type rerankRequest struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"` // not used in our case; we return all scores
}

type rerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type rerankResponse struct {
	Results []rerankResult `json:"results"`
}

// Rerank sends a query-document batch to the reranker and returns per-document
// relevance scores (higher = more relevant). Scores are returned in documents
// input order; unmatched entries receive score 0.
func (r *InfinityReranker) Rerank(ctx context.Context, query string, documents []string) ([]float64, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	body := rerankRequest{
		Model:     r.model,
		Query:     query,
		Documents: documents,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("rerank marshal: %w", err)
	}

	u := r.endpointURL
	start := time.Now()
	r.logger.Debugf("[rerank] POST %s model=%s query_len=%d docs=%d", u, r.model, len(query), len(documents))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		rbody, _ := io.ReadAll(resp.Body)
		r.logger.Errorf("[rerank] FAIL model=%s status=%d body=%s", r.model, resp.StatusCode, strings.TrimSpace(string(rbody)))
		return nil, fmt.Errorf("rerank api status %d: %s", resp.StatusCode, strings.TrimSpace(string(rbody)))
	}

	var result rerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("rerank decode: %w", err)
	}

	// Map scores back to original document order.
	scores := make([]float64, len(documents))
	for _, rr := range result.Results {
		if rr.Index >= 0 && rr.Index < len(documents) {
			scores[rr.Index] = rr.RelevanceScore
		}
	}
	r.logger.Debugf("[rerank] OK model=%s docs=%d elapsed=%s", r.model, len(documents), time.Since(start))
	return scores, nil
}
