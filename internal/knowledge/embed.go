package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Embedder generates dense vector representations for text passages.
type Embedder interface {
	// Embed returns a list of vectors, one per input text.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dim returns the dimensionality of the embedding vectors.
	Dim() int
}

// OpenAIEmbedder calls an OpenAI-compatible /v1/embeddings API.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dim     int
	client  *http.Client
}

// OpenAIEmbedderOption configures an OpenAIEmbedder.
type OpenAIEmbedderOption func(*OpenAIEmbedder)

// WithBaseURL sets the API base URL (default "https://api.openai.com/v1").
func WithBaseURL(baseURL string) OpenAIEmbedderOption {
	return func(e *OpenAIEmbedder) {
		e.baseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithAPIKey sets the API key.
func WithAPIKey(key string) OpenAIEmbedderOption {
	return func(e *OpenAIEmbedder) {
		e.apiKey = key
	}
}

// WithModel sets the embedding model name.
func WithModel(model string) OpenAIEmbedderOption {
	return func(e *OpenAIEmbedder) {
		e.model = model
	}
}

// WithDim sets the vector dimension. When 0 (default), the embedder tries to
// detect it from the first API response.
func WithDim(dim int) OpenAIEmbedderOption {
	return func(e *OpenAIEmbedder) {
		e.dim = dim
	}
}

// NewOpenAIEmbedder returns an OpenAI-compatible Embedder.
// Defaults: baseURL "https://api.openai.com/v1", model "text-embedding-ada-002".
func NewOpenAIEmbedder(opts ...OpenAIEmbedderOption) *OpenAIEmbedder {
	e := &OpenAIEmbedder{
		baseURL: "https://api.openai.com/v1",
		model:   "text-embedding-ada-002",
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

type embedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"` // API returns float64 in JSON
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed sends texts to the embedding API and returns vectors as float32 slices.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body := embedRequest{
		Input: texts,
		Model: e.model,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("embed marshal: %w", err)
	}

	u, err := url.JoinPath(e.baseURL, "/embeddings")
	if err != nil {
		return nil, fmt.Errorf("embed url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		rbody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed api status %d: %s", resp.StatusCode, strings.TrimSpace(string(rbody)))
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("embed decode: %w", err)
	}

	// Map by index to preserve order.
	vectors := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index >= 0 && d.Index < len(texts) {
			vec := make([]float32, len(d.Embedding))
			for i, v := range d.Embedding {
				vec[i] = float32(v)
			}
			vectors[d.Index] = vec
		}
	}

	// Detect dimension from first response.
	if e.dim == 0 && len(vectors) > 0 && len(vectors[0]) > 0 {
		e.dim = len(vectors[0])
	}

	return vectors, nil
}

// Dim returns the embedding dimension.
func (e *OpenAIEmbedder) Dim() int {
	return e.dim
}

// MockEmbedder returns random-ish vectors for testing and when no embedding
// service is available. The vectors are deterministic for a given text.
type MockEmbedder struct {
	dim    int
	rngIdx int
}

// NewMockEmbedder returns a MockEmbedder with the given dimension.
func NewMockEmbedder(dim int) *MockEmbedder {
	if dim <= 0 {
		dim = 4 // small default for tests
	}
	return &MockEmbedder{dim: dim}
}

// Embed generates deterministic pseudo-random vectors based on text hash.
func (m *MockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vec := make([]float32, m.dim)
		h := hashString(text)
		for j := range vec {
			// Deterministic value based on text and dimension.
			vec[j] = float32(h%256) / 256.0
			h = h*31 + int64(j)
		}
		vectors[i] = vec
	}
	return vectors, nil
}

// Dim returns the mock dimension.
func (m *MockEmbedder) Dim() int {
	return m.dim
}

// hashString produces a deterministic 64-bit hash of a string.
func hashString(s string) int64 {
	var h int64 = 5381
	for _, r := range s {
		h = ((h << 5) + h) + int64(r)
	}
	return h
}

// cosineSimilarity computes the cosine similarity between two float64 vectors.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// Reranker scores query-document pairs using a cross-encoder model,
// producing more accurate relevance scores than standalone embedding similarity.
type Reranker interface {
	// Rerank takes a query and a list of document texts and returns
	// relevance scores (higher = more relevant).
	Rerank(ctx context.Context, query string, documents []string) ([]float64, error)
}

// MockReranker returns similarity-based scores for testing. It computes a
// simple token-overlap score between the query and each document.
type MockReranker struct{}

// Rerank returns mock relevance scores based on word overlap between the
// query and each document.
func (MockReranker) Rerank(_ context.Context, query string, documents []string) ([]float64, error) {
	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)
	wordSet := map[string]bool{}
	for _, w := range queryWords {
		wordSet[w] = true
	}

	scores := make([]float64, len(documents))
	for i, doc := range documents {
		docLower := strings.ToLower(doc)
		matches := 0
		for w := range wordSet {
			if strings.Contains(docLower, w) {
				matches++
			}
		}
		if len(wordSet) > 0 {
			scores[i] = float64(matches) / float64(len(wordSet))
		}
	}
	return scores, nil
}

// SetReranker configures the cross-encoder reranker on the store. When set,
// HybridSearch applies reranking to its top-K results for improved precision.
func (s *Store) SetReranker(r Reranker) {
	s.reranker = r
}

// SetRerankCandidateLimit sets the maximum number of candidates passed to the
// reranker. Default 0 means use the legacy limit*2 heuristic. Set to 100 or more
// for a proper "wide recall → fine rerank" two-stage pipeline.
func (s *Store) SetRerankCandidateLimit(n int) {
	s.rerankCandidateLimit = n
}

// SetEmbedder configures the vector embedder on the store. When set, uploaded
// documents will have their chunk vectors pre-computed and stored in the index,
// enabling hybrid (BM25 + dense) search.
func (s *Store) SetEmbedder(e Embedder) {
	s.embedder = e
}

// SetSearchLogger configures an optional search logger on the store. When set,
// every Search/HybridSearch call records query metadata to the logger. A nil
// logger is silently ignored.
func (s *Store) SetSearchLogger(l SearchLogger) {
	s.searchLogger = l
}
