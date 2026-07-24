package knowledge

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knowledge-mcp/internal/logging"
	"github.com/tsawler/tabula"
)

// ---------------------------------------------------------------------------
// DocParser interface — implemented by HTTP API and local fallback parsers.
// ---------------------------------------------------------------------------

// DocParser extracts the text content from a document file at the given path.
type DocParser interface {
	// Parse extracts text from the document at path. Returns the extracted
	// text or an error. Implementations should be safe for concurrent use.
	Parse(path string) (string, error)
}

// ---------------------------------------------------------------------------
// HTTPDocParser — sends the document to an external HTTP API for parsing.
// The API endpoint, auth header, and timeout are configurable.
//
// Usage (from config / main.go):
//
//	parser := knowledge.NewHTTPDocParser(
//	    knowledge.WithParserEndpoint("http://localhost:8000/parse"),
//	    knowledge.WithParserAPIKey("sk-..."),
//	    knowledge.WithParserTimeout(60*time.Second),
//	)
//	knowledge.SetDocParser(parser)
//
// The implementation sends the file as a multipart/form-data upload and
// expects the response body to contain the extracted text (plain text or
// Markdown). Customise the request/response handling by replacing the
// default "sendFile" and "extractText" fields via options.
// ---------------------------------------------------------------------------

// HTTPDocParser calls an external HTTP API to parse documents.
// The zero value is not usable; use NewHTTPDocParser with options.
type HTTPDocParser struct {
	endpoint string        // URL of the document parsing API
	apiKey   string        // Bearer token or API key (optional)
	timeout  time.Duration // HTTP client timeout (default 120s)
	client   *http.Client

	// sendFile constructs and sends the HTTP request for a given file path.
	// Override this field to customise the request format (e.g. change the
	// form field name, add custom headers, use base64 instead of multipart).
	sendFile func(path string) (*http.Response, error)

	// extractText reads the HTTP response and returns the extracted text.
	// Override this field to customise how the API response is parsed
	// (e.g. extract from a JSON field like {"text": "..."} instead of
	// reading the raw body).
	extractText func(resp *http.Response) (string, error)

	logger *logging.Logger
}

// HTTPDocParserOption configures an HTTPDocParser.
type HTTPDocParserOption func(*HTTPDocParser)

// WithParserEndpoint sets the document parsing API URL.
func WithParserEndpoint(url string) HTTPDocParserOption {
	return func(p *HTTPDocParser) { p.endpoint = url }
}

// WithParserAPIKey sets the API key sent as the Authorization header.
func WithParserAPIKey(key string) HTTPDocParserOption {
	return func(p *HTTPDocParser) { p.apiKey = key }
}

// WithParserTimeout sets the HTTP client timeout. Default is 120 seconds.
func WithParserTimeout(d time.Duration) HTTPDocParserOption {
	return func(p *HTTPDocParser) { p.timeout = d }
}

// WithParserLogger sets the logger on the HTTPDocParser.
func WithParserLogger(l *logging.Logger) HTTPDocParserOption {
	return func(p *HTTPDocParser) { p.logger = l }
}

// NewHTTPDocParser creates an HTTPDocParser with the given options.
//
// Default behaviour:
//   - Sends the file as multipart/form-data with form field "file"
//   - Reads the raw response body as extracted text
//
// To adapt to a specific API (e.g. one that returns JSON), use
// WithParserSendFile or WithParserExtractText to override the
// request construction and/or response parsing.
func NewHTTPDocParser(opts ...HTTPDocParserOption) *HTTPDocParser {
	p := &HTTPDocParser{
		timeout:  120 * time.Second,
		logger:   logging.NewNopLogger(),
		endpoint: "",
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.timeout <= 0 {
		p.timeout = 120 * time.Second
	}
	p.client = &http.Client{Timeout: p.timeout}

	// Set default sendFile behaviour (multipart upload).
	if p.sendFile == nil {
		p.sendFile = p.defaultSendFile
	}
	// Set default extractText behaviour (raw body).
	if p.extractText == nil {
		p.extractText = p.defaultExtractText
	}
	return p
}

// defaultSendFile sends the file as a multipart/form-data upload.
// The form field is named "file"; the filename is the basename of path.
func (p *HTTPDocParser) defaultSendFile(path string) (*http.Response, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, err := w.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(fw, file); err != nil {
		return nil, fmt.Errorf("copy file to form: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, p.endpoint, &b)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	return resp, nil
}

// defaultExtractText reads the entire response body as the extracted text.
func (p *HTTPDocParser) defaultExtractText(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	return string(data), nil
}

// Parse sends the file to the configured HTTP API for parsing.
// Implements the DocParser interface.
func (p *HTTPDocParser) Parse(path string) (string, error) {
	if p.endpoint == "" {
		return "", fmt.Errorf("HTTPDocParser: endpoint not configured")
	}
	p.logger.Infof("HTTP parser: sending %s to %s", filepath.Base(path), p.endpoint)

	resp, err := p.sendFile(path)
	if err != nil {
		return "", fmt.Errorf("HTTP parser: send failed: %w", err)
	}

	text, err := p.extractText(resp)
	if err != nil {
		return "", fmt.Errorf("HTTP parser: extract failed: %w", err)
	}

	p.logger.Infof("HTTP parser: success for %s (%d chars)", filepath.Base(path), len(text))
	return strings.TrimSpace(text), nil
}

// ---------------------------------------------------------------------------
// TabulaParser — local fallback using the tabula library.
// ---------------------------------------------------------------------------

// TabulaParser extracts text from documents using the tabula library.
// It supports PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX formats.
type TabulaParser struct {
	logger *logging.Logger
}

// NewTabulaParser creates a new TabulaParser.
func NewTabulaParser() *TabulaParser {
	return &TabulaParser{logger: logging.NewNopLogger()}
}

// SetLogger sets the logger on the TabulaParser.
func (p *TabulaParser) SetLogger(l *logging.Logger) {
	p.logger = l
}

// Parse extracts text using the tabula library. Implements the DocParser
// interface. Scanned-image PDFs with no embedded text return a descriptive
// error hinting that OCR requires a Tesseract installation with the "ocr"
// build tag.
func (p *TabulaParser) Parse(path string) (string, error) {
	text, warnings, err := tabula.Open(path).Text()
	if err != nil {
		return "", fmt.Errorf("tabula parse %s: %w", filepath.Base(path), err)
	}
	text = strings.TrimSpace(text)

	// Scanned-image PDF: tabula succeeded but produced no text.
	if text == "" && len(warnings) > 0 {
		return "", fmt.Errorf(
			"document appears to be a scanned image with no embedded text — " +
				"OCR requires Tesseract installed and Reasonix built with -tags ocr",
		)
	}
	_ = warnings // non-fatal warnings are ignored
	return text, nil
}

// ---------------------------------------------------------------------------
// Package-level parser configuration
// ---------------------------------------------------------------------------

// docParser is the active document parser used by ParseFile for non-plain-text
// formats. When set (via SetDocParser), it is tried first; if it fails or is
// not set, the fallbackParser (tabula) is used.
var docParser DocParser

// fallbackParser is always available for local parsing via tabula.
var fallbackParser = NewTabulaParser()

// parserLogger is the package-level logger for document parsing operations.
// Set by SetParserLogger (called from Store.SetLogger during init).
var parserLogger = logging.NewNopLogger()

// parserGPUScheduler coordinates GPU sleep/wake for the doc parser model.
// When set, ParseFile will sleep embedding+reranker before calling the
// external doc parser API, and sleep the doc parser when done.
var parserGPUScheduler *GPUScheduler

// SetLogger configures the logger used by ParseFile and related parser functions.
func SetParserLogger(l *logging.Logger) {
	parserLogger = l
	fallbackParser.SetLogger(l)
}

// SetDocParser configures the primary document parser (e.g. an HTTP API parser).
// When set, ParseFile will try the doc parser first before falling back to
// the local tabula parser. Set to nil to bypass the external parser and use
// tabula directly.
func SetDocParser(p DocParser) {
	docParser = p
	if p != nil {
		if hp, ok := p.(*HTTPDocParser); ok {
			hp.logger = parserLogger
		}
	}
}

// SetParserGPUScheduler configures the GPU scheduler for doc parser
// coordination. When set, ParseFile will sleep embedding+reranker before
// calling the external doc parser API, and restore (sleep doc parser) when
// done, ensuring only one model occupies GPU memory at a time.
func SetParserGPUScheduler(s *GPUScheduler) {
	parserGPUScheduler = s
}

// ---------------------------------------------------------------------------
// Legacy MinerU support — deprecated
// ---------------------------------------------------------------------------

// minerUEnabled is kept for backward compatibility but no longer used.
// Use SetDocParser with an HTTPDocParser instead.
var minerUEnabled bool

// SetMinerUEnabled is deprecated. Use SetDocParser with NewHTTPDocParser instead.
// It is kept for backward compatibility and called from main.go during init.
func SetMinerUEnabled(enabled bool) {
	minerUEnabled = enabled
	if enabled && docParser == nil {
		// Legacy: MinerU was meant to be tried; since the user hasn't configured
		// a custom HTTP parser, we treat this as a no-op hint that the admin
		// should configure an HTTP parser endpoint.
		parserLogger.Warnf("MinerU is no longer supported via CLI. " +
			"Configure a document parsing API endpoint (doc_parser_endpoint) instead.")
	}
}

// minerUAvailable always returns false — MinerU CLI is no longer supported.
func minerUAvailable() bool { return false }

// parseWithMinerU is kept but always returns an error.
func parseWithMinerU(_ string) (string, error) {
	return "", fmt.Errorf("MinerU CLI (magic-pdf) is no longer supported; use the HTTP doc parser instead")
}

// ---------------------------------------------------------------------------
// ParseFile — main entry point for document text extraction
// ---------------------------------------------------------------------------

// ParseFile extracts plain text from a document file.
//
// Supported formats:
//   - MD, TXT — read directly
//   - PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX — via the configured DocParser
//     (HTTP API) when available, with tabula as fallback
//
// The parser selection order is:
//  1. Plain-text formats (.md, .txt) → read directly
//  2. Configured DocParser (e.g. HTTP API) → if set
//  3. Tabula (local library) → final fallback
func ParseFile(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))

	// Plain-text formats: read directly — they are already text.
	switch ext {
	case ".md", ".txt":
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s file: %w", ext, err)
		}
		return string(data), nil
	}

	// Try the configured DocParser first (e.g. HTTP API).
	if docParser != nil {
		parserLogger.Infof("DocParser: parsing %s", filepath.Base(path))

		// Coordinate GPU: sleep embedding+reranker before doc parser API call.
		var restoreDoc func()
		if parserGPUScheduler != nil {
			restoreDoc = parserGPUScheduler.PrepareForDocParsing()
		}

		text, err := docParser.Parse(path)

		// Restore: sleep doc parser so others can reload.
		if restoreDoc != nil {
			restoreDoc()
		}

		if err == nil {
			parserLogger.Infof("DocParser: success for %s (%d chars)", filepath.Base(path), len(text))
			return text, nil
		}
		parserLogger.Warnf("DocParser: failed for %s, falling back to tabula: %v", filepath.Base(path), err)
	} else {
		parserLogger.Debugf("DocParser: not configured for %s", filepath.Base(path))
	}

	// Fall back to tabula for all other supported formats.
	parserLogger.Infof("Tabula fallback: parsing %s", filepath.Base(path))
	text, err := fallbackParser.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	parserLogger.Infof("Tabula: success for %s (%d chars)", filepath.Base(path), len(text))
	return text, nil
}
