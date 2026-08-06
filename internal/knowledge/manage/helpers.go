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
	"strings"
	"time"
	"unicode"

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

// ValidateSearchQuery checks a search query for problematic characters and
// returns an error message suitable for a 400 response. Valid queries must
// contain at least one letter, number or CJK character, and must not contain
// null bytes or other non-printable control characters.
func ValidateSearchQuery(q string) string {
	// Reject empty / whitespace-only.
	q = strings.TrimSpace(q)
	if q == "" {
		return "query param 'q' is required"
	}

	// Reject queries that are too long (defence against abuse).
	if len(q) > 2000 {
		return "query is too long (max 2000 characters)"
	}

	hasValid := false
	for _, r := range q {
		// Reject null bytes and other dangerous control characters.
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			return "query contains invalid characters"
		}
		// Reject zero-width and bidi-override characters that can be used
		// for injection/spoofing attacks.
		if isInvisibleOrControl(r) {
			return "query contains invisible/control characters"
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			hasValid = true
		}
	}
	if !hasValid {
		return "query must contain at least one letter or number"
	}
	return ""
}

// isInvisibleOrControl reports whether r is a zero-width, bidi-control or
// other normally invisible character that should not appear in search queries.
func isInvisibleOrControl(r rune) bool {
	switch {
	case r == '\u200B', // zero-width space
		r == '\u200C', // zero-width non-joiner
		r == '\u200D', // zero-width joiner
		r == '\u200E', // left-to-right mark
		r == '\u200F', // right-to-left mark
		r == '\u202A', // left-to-right embedding
		r == '\u202B', // right-to-left embedding
		r == '\u202C', // pop directional formatting
		r == '\u202D', // left-to-right override
		r == '\u202E', // right-to-left override
		r == '\uFEFF', // BOM / zero-width no-break space
		r == '\u2060', // word joiner
		r == '\u2061', // function application
		r == '\u2062', // invisible times
		r == '\u2063', // invisible separator
		r == '\u2064': // invisible plus
		return true
	default:
		return false
	}
}

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
