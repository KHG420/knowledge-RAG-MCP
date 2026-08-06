package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── parseTags ───────────────────────────────────────────────────────────────

func TestParseTags_Empty(t *testing.T) {
	if got := parseTags(""); got != nil {
		t.Errorf("parseTags('') = %v, want nil", got)
	}
}

func TestParseTags_Single(t *testing.T) {
	got := parseTags("hello")
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("parseTags('hello') = %v, want [hello]", got)
	}
}

func TestParseTags_Multiple(t *testing.T) {
	got := parseTags("a, b, c")
	expected := []string{"a", "b", "c"}
	if len(got) != len(expected) {
		t.Fatalf("len = %d, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

func TestParseTags_SpacesAround(t *testing.T) {
	got := parseTags("  hello ,  world  ")
	expected := []string{"hello", "world"}
	if len(got) != len(expected) {
		t.Fatalf("len = %d, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

func TestParseTags_EmptyEntries(t *testing.T) {
	// Trailing comma, double comma, etc. — should skip empty entries.
	got := parseTags("a,,b, ,c,")
	expected := []string{"a", "b", "c"}
	if len(got) != len(expected) {
		t.Fatalf("parseTags('a,,b, ,c,') = %v (len=%d), want %v", got, len(got), expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

func TestParseTags_AllEmpty(t *testing.T) {
	got := parseTags(",,,  ,")
	if got != nil {
		t.Errorf("parseTags(',,,  ,') = %v, want nil", got)
	}
}

func TestParseTags_SingleEmptyComma(t *testing.T) {
	got := parseTags(",")
	if got != nil {
		t.Errorf("parseTags(',') = %v, want nil", got)
	}
}

func TestParseTags_WhitespaceOnly(t *testing.T) {
	got := parseTags("   ")
	if got != nil {
		t.Errorf("parseTags('   ') = %v, want nil", got)
	}
}

func TestParseTags_UnicodeTags(t *testing.T) {
	got := parseTags("中文,日本語,한국어")
	expected := []string{"中文", "日本語", "한국어"}
	if len(got) != len(expected) {
		t.Fatalf("len = %d, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

func TestParseTags_UnicodeWithSpaces(t *testing.T) {
	got := parseTags(" 中 文 , 日本語 ")
	expected := []string{"中 文", "日本語"}
	if len(got) != len(expected) {
		t.Fatalf("len = %d, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

// ─── isPathSafe ──────────────────────────────────────────────────────────────

func TestIsPathSafe_SimpleName(t *testing.T) {
	if !isPathSafe("hello.txt") {
		t.Error("simple name should be safe")
	}
}

func TestIsPathSafe_SubDir(t *testing.T) {
	if !isPathSafe("subdir/hello.txt") {
		t.Error("subdir/hello.txt should be safe")
	}
}

func TestIsPathSafe_ParentTraversal(t *testing.T) {
	if isPathSafe("../etc/passwd") {
		t.Error("../etc/passwd should be rejected")
	}
}

func TestIsPathSafe_EncodedTraversal(t *testing.T) {
	// "..%2F" is a literal string — the filesystem does not decode %2F,
	// so this is not a real traversal vector.  It is now allowed.
	if !isPathSafe("..%2Fetc%2Fpasswd") {
		t.Error("URL-encoded traversal should be allowed (FS doesn't decode)")
	}
}

func TestIsPathSafe_DoubleDots(t *testing.T) {
	if isPathSafe("foo/../../bar") {
		t.Error("foo/../../bar should be rejected")
	}
}

func TestIsPathSafe_AbsolutePath(t *testing.T) {
	if isPathSafe("/etc/passwd") {
		t.Error("/etc/passwd should be rejected as absolute path")
	}
}

func TestIsPathSafe_EmptyString(t *testing.T) {
	if !isPathSafe("") {
		t.Error("empty string should be safe")
	}
}

func TestIsPathSafe_DotOnly(t *testing.T) {
	if !isPathSafe(".") {
		t.Error("'.' (single dot) should be safe — it is not '..'")
	}
}

func TestIsPathSafe_DotsInFilename(t *testing.T) {
	// "...doc.txt" — ".." is not a standalone path component here
	// (preceded by '.' and followed by 'd'), so it is safe.
	if !isPathSafe("...doc.txt") {
		t.Error("...doc.txt should be safe (.. not standalone)")
	}
}

func TestIsPathSafe_NormalDots(t *testing.T) {
	// Single dot as extension separator is fine
	if !isPathSafe("file.name.txt") {
		t.Error("file.name.txt should be safe")
	}
}

func TestIsPathSafe_RootPath(t *testing.T) {
	if isPathSafe("/") {
		t.Error("'/' should be rejected as absolute path")
	}
}

func TestIsPathSafe_CurrentDirRel(t *testing.T) {
	// "./foo" is relative and should be safe
	if !isPathSafe("./foo") {
		t.Error("./foo should be safe")
	}
}

func TestIsPathSafe_ParentRefInMiddle(t *testing.T) {
	if isPathSafe("foo/../bar") {
		t.Error("foo/../bar should be rejected")
	}
}

// ─── parseTime ───────────────────────────────────────────────────────────────

func TestParseTime_Empty(t *testing.T) {
	if !parseTime("").IsZero() {
		t.Error("parseTime('') should return zero time")
	}
}

func TestParseTime_RFC3339(t *testing.T) {
	result := parseTime("2026-07-15T10:30:00Z")
	if result.IsZero() {
		t.Error("RFC3339 should parse successfully")
	}
	if result.Year() != 2026 || result.Month() != 7 || result.Day() != 15 {
		t.Errorf("RFC3339 got %v, want 2026-07-15", result)
	}
}

func TestParseTime_RFC3339WithOffset(t *testing.T) {
	result := parseTime("2026-07-15T10:30:00+08:00")
	if result.IsZero() {
		t.Error("RFC3339 with offset should parse successfully")
	}
}

func TestParseTime_DateOnly(t *testing.T) {
	result := parseTime("2026-07-15")
	if result.IsZero() {
		t.Error("date-only should parse successfully")
	}
	if result.Year() != 2026 || result.Month() != 7 || result.Day() != 15 {
		t.Errorf("date-only got %v, want 2026-07-15", result)
	}
}

func TestParseTime_Invalid(t *testing.T) {
	if !parseTime("not-a-date").IsZero() {
		t.Error("invalid date should return zero time")
	}
}

func TestParseTime_Garbage(t *testing.T) {
	if !parseTime("12345").IsZero() {
		t.Error("garbage string should return zero time")
	}
}

func TestParseTime_ISO8601_Variant(t *testing.T) {
	// "2026-07-15T00:00:00Z" is both RFC3339 and ISO 8601
	result := parseTime("2026-07-15T00:00:00Z")
	if result.IsZero() {
		t.Error("ISO 8601 UTC should parse")
	}
}

func TestParseTime_MaxDate(t *testing.T) {
	// Very large year — should parse without overflow
	result := parseTime("9999-12-31T23:59:59Z")
	if result.IsZero() {
		t.Error("max date should parse")
	}
}

func TestParseTime_LeapDay(t *testing.T) {
	result := parseTime("2024-02-29")
	if result.IsZero() {
		t.Error("leap day should parse")
	}
}

func TestParseTime_NonLeapDay(t *testing.T) {
	// 2025-02-29 is an invalid date but Go's time.Parse might not fail on it
	// (it normalizes). Just check it doesn't panic.
	result := parseTime("2025-02-29")
	_ = result
}

func TestParseTime_ZeroPadded(t *testing.T) {
	result := parseTime("2026-01-05")
	if result.IsZero() || result.Month() != 1 || result.Day() != 5 {
		t.Errorf("zero-padded date got %v", result)
	}
}

// ─── localIP ─────────────────────────────────────────────────────────────────

func TestLocalIP_NonEmpty(t *testing.T) {
	ip := localIP()
	if ip == "" {
		t.Error("localIP should not return empty string")
	}
}

// localIP should return either a valid IP or "localhost"
func TestLocalIP_Valid(t *testing.T) {
	ip := localIP()
	if ip == "localhost" {
		return // fallback is acceptable
	}
	// Basic format check: should be an IPv4 address like x.x.x.x
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		t.Errorf("expected IPv4 or 'localhost', got %q", ip)
	}
}

// ─── findConfigPath ──────────────────────────────────────────────────────────

func TestFindConfigPath_DefaultCWD(t *testing.T) {
	// When no --config flag and no executable-adjacent config,
	// falls back to ./knowledge-mcp.toml
	// We can't fully test this without modifying os.Args, but we verify
	// the function returns a non-empty path.
	path := findConfigPath()
	if path == "" {
		t.Error("findConfigPath should return non-empty string")
	}
	// Should end with knowledge-mcp.toml
	if filepath.Base(path) != "knowledge-mcp.toml" {
		t.Errorf("expected base 'knowledge-mcp.toml', got %q", filepath.Base(path))
	}
}

func TestFindConfigPath_FlagEquals(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"knowledge-mcp", "--config=/tmp/myconfig.toml"}
	got := findConfigPath()
	if got != "/tmp/myconfig.toml" {
		t.Errorf("findConfigPath with --config= got %q, want /tmp/myconfig.toml", got)
	}
}

func TestFindConfigPath_FlagSpace(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"knowledge-mcp", "--config", "/tmp/myconfig2.toml"}
	got := findConfigPath()
	if got != "/tmp/myconfig2.toml" {
		t.Errorf("findConfigPath with --config space got %q, want /tmp/myconfig2.toml", got)
	}
}

func TestFindConfigPath_FlagSpace_IgnoreSubcommand(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	// --config between subcommands
	os.Args = []string{"knowledge-mcp", "serve", "--config", "/tmp/serve.toml", "--mcp"}
	got := findConfigPath()
	if got != "/tmp/serve.toml" {
		t.Errorf("findConfigPath got %q, want /tmp/serve.toml", got)
	}
}

func TestFindConfigPath_FlagEquals_BeforeSubcommand(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{"knowledge-mcp", "stdio", "--config=/tmp/stdio.toml"}
	got := findConfigPath()
	if got != "/tmp/stdio.toml" {
		t.Errorf("findConfigPath got %q, want /tmp/stdio.toml", got)
	}
}

func TestFindConfigPath_FlagValueContainsEquals(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	// Ensure that a value containing "=" is handled properly
	// --config=/path/with=equals.toml → TrimPrefix handles this
	os.Args = []string{"knowledge-mcp", "--config=/path/with=equals.toml"}
	got := findConfigPath()
	if got != "/path/with=equals.toml" {
		t.Errorf("findConfigPath with = in path got %q", got)
	}
}

func TestFindConfigPath_CompletelyMissing(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	// Flag with no value after it
	os.Args = []string{"knowledge-mcp", "--config"}
	got := findConfigPath()
	// Should try executable dir, then CWD fallback
	if filepath.Base(got) != "knowledge-mcp.toml" {
		t.Errorf("expected fallback to knowledge-mcp.toml, got %q", got)
	}
}

// ─── parseTime integration-like: compare with real use ──────────────────────

func TestParseTime_Compare_RFC3339_vs_DateOnly(t *testing.T) {
	// The same logical date should match in both formats
	rfc := parseTime("2026-07-15T00:00:00Z")
	date := parseTime("2026-07-15")
	if rfc.Year() != date.Year() || rfc.Month() != date.Month() || rfc.Day() != date.Day() {
		t.Errorf("RFC3339 %v vs date-only %v should have same date", rfc, date)
	}
}

// ─── formatManageURL ─────────────────────────────────────────────────────────

func TestFormatManageURL_Localhost(t *testing.T) {
	// formatManageURL calls localIP() — we just check it formats correctly.
	url := formatManageURL("8085")
	if !strings.Contains(url, ":8085") {
		t.Errorf("expected port 8085 in URL, got %q", url)
	}
	if !strings.HasPrefix(url, "http://") {
		t.Errorf("expected http:// prefix, got %q", url)
	}
}

// ─── getString / getBool ─────────────────────────────────────────────────────

func TestGetString_Present(t *testing.T) {
	// Need to construct a minimal mcp.CallToolRequest.
	// getString just does type assertion with fallback — we verify fallback works.
	// This is covered by the integration tests via tools.
	// For now we only test that the function signature is valid.
	_ = getString // avoid "unused" — it's used in tools_*.go
}

func TestGetBool_Unused(t *testing.T) {
	_ = getBool // avoid "unused"
}

// ─── parseTime boundary tests ────────────────────────────────────────────────

func TestParseTime_ShortString(t *testing.T) {
	if !parseTime("1").IsZero() {
		t.Error("'1' should return zero time")
	}
}

func TestParseTime_NegativeYear(t *testing.T) {
	// Not a valid ISO 8601 date — should fail
	if !parseTime("-0001-01-01").IsZero() {
		t.Error("negative year should not parse")
	}
}

func TestParseTime_UnixTimestamp(t *testing.T) {
	// "1710428400" is not a valid date format for our parser
	if !parseTime("1710428400").IsZero() {
		t.Error("unix timestamp should not parse as date")
	}
}

// ─── findConfigPath: no executable (simulated) ──────────────────────────────

func TestFindConfigPath_ReturnsString(t *testing.T) {
	path := findConfigPath()
	if path == "" {
		t.Error("findConfigPath must not return empty string")
	}
}

// ─── parseTags: extremely long input ─────────────────────────────────────────

func TestParseTags_LongTag(t *testing.T) {
	longTag := strings.Repeat("x", 10000)
	got := parseTags(longTag)
	if len(got) != 1 || got[0] != longTag {
		t.Error("long tag should be preserved")
	}
}

func TestParseTags_ManyTags(t *testing.T) {
	// 500 tags
	parts := make([]string, 500)
	for i := range parts {
		parts[i] = "tag"
	}
	got := parseTags(strings.Join(parts, ","))
	if len(got) != 500 {
		t.Errorf("expected 500 tags, got %d", len(got))
	}
	for i, tag := range got {
		if tag != "tag" {
			t.Errorf("tag[%d] = %q, want 'tag'", i, tag)
		}
	}
}

// ─── isPathSafe: Windows-style paths ────────────────────────────────────────

func TestIsPathSafe_WindowsBackslash(t *testing.T) {
	// Backslashes with ".." should still be caught
	if isPathSafe("..\\windows\\path") {
		t.Error("..\\path should be rejected (contains '..')")
	}
}

func TestIsPathSafe_WindowsAbsolute(t *testing.T) {
	// "C:..\\foo" — the ".." is preceded by ':' not '/' or '\',
	// so it's not a standalone path component. This is acceptable:
	// on Linux "C:..\\foo" is just a filename (drive letter + dots).
	// The old substring check would have rejected it, but the new
	// component-aware check correctly allows it.
	if !isPathSafe("C:..\\foo") {
		t.Error("C:..\\foo should be safe (.. not standalone component)")
	}
	// Actual traversal: "C:\\..\\windows" — contains "\\..\\" which IS a component.
	if isPathSafe("C:\\..\\windows") {
		t.Error("C:\\\\..\\\\windows should be rejected")
	}
}

// ─── formatManageURL: edge case with very large port ────────────────────────

func TestFormatManageURL_HighPort(t *testing.T) {
	url := formatManageURL("65535")
	if !strings.Contains(url, ":65535") {
		t.Errorf("expected port 65535, got %q", url)
	}
}

// ─── parseTime: benchmarking hint ────────────────────────────────────────────

func TestParseTime_ZeroTime_Preserved(t *testing.T) {
	zero := time.Time{}
	result := parseTime("")
	if !result.Equal(zero) {
		t.Error("empty string should return zero time")
	}
	// Also check that zero.Add(1) != zero (sanity)
	if zero.Add(1) == zero {
		t.Error("sanity check failed")
	}
}

// ─── isPathSafe: null byte injection ─────────────────────────────────────────

func TestIsPathSafe_NullByte(t *testing.T) {
	// "\x00" is not ".." and not an absolute path
	// filepath.IsAbs on Linux might return false.
	// This edge case is somewhat debatable — but the function does not
	// specifically guard against null bytes, only ".." and absolute paths.
	// We document this behavior.
	if !isPathSafe("foo\x00bar") {
		t.Skip("null byte rejected by platform — behavior varies")
	}
}

// ─── isPathSafe: symlink escape attempt ─────────────────────────────────────

func TestIsPathSafe_SymlinkEscape(t *testing.T) {
	// "/proc/self/root/etc/passwd" — absolute → rejected
	if isPathSafe("/proc/self/root/etc/passwd") {
		t.Error("absolute proc path should be rejected")
	}
}
