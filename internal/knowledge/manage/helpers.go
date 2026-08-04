// Package manage — shared helpers for HTTP management handlers.
package manage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// ── Response helpers ──

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func writeNDJSONLine(w http.ResponseWriter, flusher http.Flusher, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(w, `{"error":"internal serialization error"}`+"\n")
		return
	}
	fmt.Fprintf(w, "%s\n", data)
	if flusher != nil {
		flusher.Flush()
	}
}

// ── Upload helpers ──

// saveFile saves an uploaded reader to a temp file and indexes it via the store.
func saveFile(svc knowledge.ManageService, src io.Reader, filename string) (knowledge.DocumentMeta, error) {
	tmpDir, err := os.MkdirTemp("", "knowledge-upload-*")
	if err != nil {
		return knowledge.DocumentMeta{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	safeName := filepath.Base(filename)
	tmpPath := filepath.Join(tmpDir, safeName)
	dst, err := os.Create(tmpPath)
	if err != nil {
		return knowledge.DocumentMeta{}, fmt.Errorf("create temp file: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return knowledge.DocumentMeta{}, fmt.Errorf("copy upload: %w", err)
	}
	dst.Close()

	return svc.UploadDocument(tmpPath)
}

// ── Model probe helpers ──

// ProbeResult reports the result of probing an external service.
type ProbeResult struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
}

// probeEmbedder probes an embedder by calling its Probe method if available.
func ProbeEmbedder(ctx context.Context, emb knowledge.Embedder, log *logging.Logger) ProbeResult {
	if emb == nil {
		return ProbeResult{OK: false, Error: "未配置"}
	}
	switch e := emb.(type) {
	case *knowledge.MockEmbedder:
		return ProbeResult{OK: true, LatencyMs: 0}
	case *knowledge.OpenAIEmbedder:
		pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		start := time.Now()
		if err := e.Probe(pCtx); err != nil {
			log.Warnf("embedder probe failed: %v", err)
			return ProbeResult{OK: false, Error: err.Error()}
		}
		return ProbeResult{OK: true, LatencyMs: time.Since(start).Milliseconds()}
	default:
		return ProbeResult{OK: false, Error: "未知嵌入器类型"}
	}
}

// probeReranker probes a reranker by calling its Probe method if available.
func ProbeReranker(ctx context.Context, rer knowledge.Reranker, log *logging.Logger) ProbeResult {
	if rer == nil {
		return ProbeResult{OK: false, Error: "未配置"}
	}
	switch r := rer.(type) {
	case *knowledge.MockReranker:
		return ProbeResult{OK: true, LatencyMs: 0}
	case *knowledge.InfinityReranker:
		pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		start := time.Now()
		if err := r.Probe(pCtx); err != nil {
			log.Warnf("reranker probe failed: %v", err)
			return ProbeResult{OK: false, Error: err.Error()}
		}
		return ProbeResult{OK: true, LatencyMs: time.Since(start).Milliseconds()}
	default:
		return ProbeResult{OK: false, Error: "未知重排序器类型"}
	}
}

// probeDocParser probes the document parser.
func ProbeDocParser(ctx context.Context, log *logging.Logger) ProbeResult {
	pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := knowledge.ProbeDocParser(pCtx); err != nil {
		log.Warnf("doc parser probe failed: %v", err)
		return ProbeResult{OK: false, Error: err.Error()}
	}
	return ProbeResult{OK: true, LatencyMs: time.Since(start).Milliseconds()}
}

// ── Multipart helpers ──

// ParseMultipartFile extracts a single file from a multipart form upload.
func ParseMultipartFile(r *http.Request, formField string, maxSizeMB int) (multipart.File, *multipart.FileHeader, error) {
	maxBytes := int64(maxSizeMB) * 1024 * 1024
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		return nil, nil, err
	}
	file, header, err := r.FormFile(formField)
	if err != nil {
		return nil, nil, err
	}
	return file, header, nil
}

// ── SSE helpers ──

// sendSSEEvent writes an SSE event with the given event name and JSON data.
func SendSSEEvent(w io.Writer, flusher http.Flusher, event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	if flusher != nil {
		flusher.Flush()
	}
}

// sendTaskFinalEvent sends the final SSE event for an async task.
func SendTaskFinalEvent(w io.Writer, flusher http.Flusher, taskID, status, message string, doc interface{}) {
	SendSSEEvent(w, flusher, "task_final", map[string]interface{}{
		"taskId":  taskID,
		"status":  status,
		"message": message,
		"doc":     doc,
	})
}

// ── Other helpers ──

// parseIntParam parses an integer query parameter with a default.
func ParseIntParam(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return defaultVal
		}
		n = n*10 + int(c-'0')
	}
	return n
}
