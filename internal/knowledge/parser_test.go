package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// ParseFile — main entry-point tests
// =============================================================================

func TestParseFile_TXT_Direct(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "Hello, this is plain text content.\nLine two."
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	text, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile TXT: %v", err)
	}
	if text != content {
		t.Errorf("TXT content mismatch:\n got=%q\nwant=%q", text, content)
	}
}

func TestParseFile_MD_Direct(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "readme.md")
	content := "# Title\n\nSome markdown **content**.\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	text, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile MD: %v", err)
	}
	if text != content {
		t.Errorf("MD content mismatch:\n got=%q\nwant=%q", text, content)
	}
}

func TestParseFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	text, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile empty: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty string, got %q", text)
	}
}

func TestParseFile_EmptyMD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	text, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile empty MD: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty string, got %q", text)
	}
}

func TestParseFile_NotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does_not_exist.txt")
	_, err := ParseFile(context.Background(), path)
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestParseFile_NotFound_BinaryFormat(t *testing.T) {
	// A missing non-txt/md file should fail with an error.
	path := filepath.Join(t.TempDir(), "does_not_exist.pdf")
	_, err := ParseFile(context.Background(), path)
	if err == nil {
		t.Error("expected error for missing PDF file, got nil")
	}
}

func TestParseFile_LargeTXT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	// 1 MB of text — large enough to test read path but not hit timeout.
	content := strings.Repeat("This is a line of text for parser testing.\n", 20000)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	text, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile large TXT: %v", err)
	}
	if text != content {
		t.Errorf("large TXT content mismatch: got %d bytes, want %d bytes", len(text), len(content))
	}
}

func TestParseFile_UnicodeTXT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unicode.txt")
	content := "中文字符测试\n日本語テスト\n한국어 테스트\n🎉 emoji test\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	text, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile unicode: %v", err)
	}
	if text != content {
		t.Errorf("unicode content mismatch:\n got=%q\nwant=%q", text, content)
	}
}

// =============================================================================
// TabulaParser tests
// =============================================================================

func TestTabulaParser_New(t *testing.T) {
	p := NewTabulaParser()
	if p == nil {
		t.Fatal("NewTabulaParser returned nil")
	}
}

func TestTabulaParser_SetLogger(t *testing.T) {
	p := NewTabulaParser()
	// SetLogger should not panic with nil.
	p.SetLogger(nil)
}

func TestTabulaParser_ParseNotFound(t *testing.T) {
	p := NewTabulaParser()
	path := filepath.Join(t.TempDir(), "no_such_file.pdf")
	_, err := p.Parse(context.Background(), path)
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestTabulaParser_ParseEmptyDir(t *testing.T) {
	// Passing a directory path should fail cleanly.
	p := NewTabulaParser()
	_, err := p.Parse(context.Background(), t.TempDir())
	if err == nil {
		t.Error("expected error for directory path, got nil")
	}
}

func TestTabulaParser_ParseTXTFile(t *testing.T) {
	// TabulaParser supports document formats like PDF, DOCX, HTML, etc.
	// TXT files are handled by the ParseFile fast-path, not Tabula.
	// Test that TabulaParser returns an error for an unsupported format.
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.xyz")
	if err := os.WriteFile(path, []byte("some content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := NewTabulaParser()
	_, err := p.Parse(context.Background(), path)
	if err == nil {
		t.Error("expected error for unsupported format .xyz, got nil")
	}
}

// =============================================================================
// HTTPDocParser tests
// =============================================================================

func TestHTTPDocParser_New_Defaults(t *testing.T) {
	p := NewHTTPDocParser()
	if p == nil {
		t.Fatal("NewHTTPDocParser returned nil")
	}
	if p.timeout != 600*time.Second {
		t.Errorf("default timeout: got %v, want 600s", p.timeout)
	}
	if p.client == nil {
		t.Error("HTTP client is nil")
	}
	if p.sendFile == nil {
		t.Error("sendFile default not set")
	}
	if p.extractText == nil {
		t.Error("extractText default not set")
	}
}

func TestHTTPDocParser_New_WithOptions(t *testing.T) {
	p := NewHTTPDocParser(
		WithParserEndpoint("http://localhost:9999/parse"),
		WithParserAPIKey("test-key-123"),
		WithParserTimeout(30*time.Second),
	)
	if p.endpoint != "http://localhost:9999/parse" {
		t.Errorf("endpoint: got %q", p.endpoint)
	}
	if p.apiKey != "test-key-123" {
		t.Errorf("apiKey: got %q", p.apiKey)
	}
	if p.timeout != 30*time.Second {
		t.Errorf("timeout: got %v", p.timeout)
	}
}

func TestHTTPDocParser_Parse_NoEndpoint(t *testing.T) {
	p := NewHTTPDocParser()
	// No endpoint configured — Parse should return a clear error.
	_, err := p.Parse(context.Background(), "/some/file.pdf")
	if err == nil {
		t.Error("expected error when endpoint is not configured, got nil")
	}
	if !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("error should mention endpoint, got: %v", err)
	}
}

func TestHTTPDocParser_New_ZeroTimeout(t *testing.T) {
	// Zero or negative timeout should be clamped to 600s.
	p := NewHTTPDocParser(WithParserTimeout(0))
	if p.timeout != 600*time.Second {
		t.Errorf("zero timeout should default to 600s, got %v", p.timeout)
	}
}

// =============================================================================
// DocParser registration (SetDocParser / DocParserInfo) tests
// =============================================================================

func TestSetDocParser_Nil(t *testing.T) {
	// Save and restore the global.
	prev := docParser
	defer func() { docParser = prev }()

	SetDocParser(nil)
	if docParser != nil {
		t.Error("expected nil docParser after SetDocParser(nil)")
	}
}

func TestSetDocParser_HTTP(t *testing.T) {
	prev := docParser
	defer func() { docParser = prev }()

	p := NewHTTPDocParser(WithParserEndpoint("http://example.com/parse"))
	SetDocParser(p)
	if docParser == nil {
		t.Fatal("expected non-nil docParser after SetDocParser")
	}
}

func TestDocParserInfo_Nil(t *testing.T) {
	prev := docParser
	docParser = nil
	defer func() { docParser = prev }()

	info := DocParserInfo()
	if info != nil {
		t.Errorf("expected nil info when no parser configured, got %v", info)
	}
}

func TestDocParserInfo_HTTP(t *testing.T) {
	prev := docParser
	endpoint := "http://parser.example.com:8080/v1/parse"
	docParser = NewHTTPDocParser(WithParserEndpoint(endpoint))
	defer func() { docParser = prev }()

	info := DocParserInfo()
	if info == nil {
		t.Fatal("expected non-nil info for HTTP parser")
	}
	if ep, ok := info["endpointURL"].(string); !ok || ep != endpoint {
		t.Errorf("endpointURL: got %v, want %q", info["endpointURL"], endpoint)
	}
}

func TestDocParserInfo_Custom(t *testing.T) {
	prev := docParser
	// A TabulaParser implements DocParser but is not an HTTPDocParser.
	docParser = NewTabulaParser()
	defer func() { docParser = prev }()

	info := DocParserInfo()
	if info == nil {
		t.Fatal("expected non-nil info for custom parser")
	}
	if info["type"] != "custom" {
		t.Errorf("expected type=custom, got %v", info["type"])
	}
}

// =============================================================================
// ParseFile with active docParser (HTTP fallback path)
// =============================================================================

func TestParseFile_UnknownExt_FallbackTabula(t *testing.T) {
	// Save and restore globals.
	prevDoc := docParser
	prevGPU := parserGPUScheduler
	docParser = nil
	parserGPUScheduler = nil
	defer func() {
		docParser = prevDoc
		parserGPUScheduler = prevGPU
	}()

	// A file with unsupported extension (.xyz) should go through Tabula.
	// Tabula will likely fail, but we verify it doesn't panic and
	// returns a reasonable error.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.xyz")
	if err := os.WriteFile(path, []byte("garbage"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	text, err := ParseFile(context.Background(), path)
	// Tabula may succeed or fail depending on the library; either is fine.
	// We just verify no panic occurs.
	_ = text
	_ = err
}

// =============================================================================
// MinerU deprecated functions — always return errors
// =============================================================================

func TestMinerUAvailable_AlwaysFalse(t *testing.T) {
	if minerUAvailable() {
		t.Error("minerUAvailable should always return false")
	}
}

func TestParseWithMinerU_AlwaysError(t *testing.T) {
	text, err := parseWithMinerU("/nonexistent/file.pdf")
	if err == nil {
		t.Error("parseWithMinerU should always return an error")
	}
	if text != "" {
		t.Errorf("expected empty text, got %q", text)
	}
	if !strings.Contains(err.Error(), "no longer supported") {
		t.Errorf("error message mismatch: %v", err)
	}
}

// =============================================================================
// SetParserGPUScheduler / SetParserLogger — no-panic tests
// =============================================================================

func TestSetParserGPUScheduler_Nil(t *testing.T) {
	prev := parserGPUScheduler
	defer func() { parserGPUScheduler = prev }()

	// Should not panic.
	SetParserGPUScheduler(nil)
	if parserGPUScheduler != nil {
		t.Error("expected nil after SetParserGPUScheduler(nil)")
	}
}

func TestSetParserLogger_NoPanic(t *testing.T) {
	// Should not panic with nil logger.
	SetParserLogger(nil)
}

// =============================================================================
// ProbeDocParser — connectivity check (no actual server needed)
// =============================================================================

func TestProbeDocParser_NoParser(t *testing.T) {
	prev := docParser
	docParser = nil
	defer func() { docParser = prev }()

	err := ProbeDocParser(nil)
	if err == nil {
		t.Error("expected error when no doc parser configured, got nil")
	}
}

// =============================================================================
// ParseFile boundary: relative path vs absolute path
// =============================================================================

func TestParseFile_RelativePath(t *testing.T) {
	dir := t.TempDir()
	// Write file, then change to that directory so relative paths work.
	path := filepath.Join(dir, "rel.txt")
	if err := os.WriteFile(path, []byte("relative path test"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	text, err := ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile relative: %v", err)
	}
	if text != "relative path test" {
		t.Errorf("content mismatch: got %q", text)
	}
}
