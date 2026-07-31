package knowledge

import (
	"fmt"
	"testing"
	"time"
)

// ── MySQLBackendConfig tests ───────────────────────────────────────────────────

func TestMySQLBackendConfig_Defaults(t *testing.T) {
	cfg := defaultMySQLConfig()

	if cfg.User != "root" {
		t.Errorf("default user = %q, want \"root\"", cfg.User)
	}
	if cfg.Host != "127.0.0.1" {
		t.Errorf("default host = %q, want \"127.0.0.1\"", cfg.Host)
	}
	if cfg.Port != "3306" {
		t.Errorf("default port = %q, want \"3306\"", cfg.Port)
	}
	if cfg.Database != "knowledge_rag" {
		t.Errorf("default database = %q, want \"knowledge_rag\"", cfg.Database)
	}
}

func TestMySQLBackendConfig_DSN_Basic(t *testing.T) {
	cfg := MySQLBackendConfig{
		User:     "testuser",
		Password: "testpass",
		Host:     "db.example.com",
		Port:     "3307",
		Database: "testdb",
	}
	dsn := cfg.dsn()
	if dsn == "" {
		t.Fatal("DSN is empty")
	}

	// Verify key components in the DSN.
	checks := []string{
		"testuser:testpass",
		"tcp(db.example.com:3307)",
		"/testdb",
	}
	for _, c := range checks {
		if !containsStr(dsn, c) {
			t.Errorf("DSN %q missing expected component %q", dsn, c)
		}
	}
}

func TestMySQLBackendConfig_DSN_NoPassword(t *testing.T) {
	cfg := MySQLBackendConfig{
		User:     "nopass",
		Password: "",
		Host:     "localhost",
		Port:     "3306",
		Database: "test",
	}
	dsn := cfg.dsn()
	if dsn == "" {
		t.Fatal("DSN is empty")
	}
	if !containsStr(dsn, "nopass@tcp") {
		t.Errorf("DSN %q should contain nopass@tcp (no password)", dsn)
	}
}

func TestMySQLBackendConfig_DSN_SocketPath(t *testing.T) {
	cfg := MySQLBackendConfig{
		User:       "root",
		SocketPath: "/var/run/mysqld/mysqld.sock",
		Database:   "test",
	}
	dsn := cfg.dsn()
	if dsn == "" {
		t.Fatal("DSN is empty")
	}
	if !containsStr(dsn, "unix(/var/run/mysqld/mysqld.sock)") {
		t.Errorf("DSN %q should use unix socket", dsn)
	}
}

func TestMySQLBackendConfig_DSN_FromDSN(t *testing.T) {
	// When DSN is explicitly provided, it should be used directly.
	cfg := MySQLBackendConfig{
		DSN: "user:pass@tcp(localhost:3306)/mydb?parseTime=true",
	}
	dsn := cfg.dsn()
	if dsn != cfg.DSN {
		t.Errorf("DSN mismatch: got %q, want %q", dsn, cfg.DSN)
	}
}

func TestMySQLBackendConfig_DSN_ParseTimeAndLoc(t *testing.T) {
	cfg := MySQLBackendConfig{
		User:     "u",
		Host:     "h",
		Port:     "3306",
		Database: "d",
	}
	dsn := cfg.dsn()
	// The DSN should include parseTime=true for proper time handling.
	if !containsStr(dsn, "parseTime=true") {
		t.Errorf("DSN %q should contain parseTime=true", dsn)
	}
	// mysql.Config.FormatDSN may or may not include loc — just verify it parses.
	if !containsStr(dsn, "u@tcp") {
		t.Errorf("DSN %q should contain u@tcp", dsn)
	}
}

// ── Mock Backend tests ─────────────────────────────────────────────────────────

func TestMockBackend_CreateAndListKBs(t *testing.T) {
	mb := newMockBackend()
	if err := mb.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer mb.Close()

	if err := mb.CreateKB("kb1", "first kb"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if err := mb.CreateKB("kb2", "second kb"); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	// Duplicate creation should fail.
	if err := mb.CreateKB("kb1", "dup"); err == nil {
		t.Error("duplicate CreateKB should fail")
	}

	kbs, err := mb.ListKBs()
	if err != nil {
		t.Fatalf("ListKBs: %v", err)
	}
	if len(kbs) != 2 {
		t.Fatalf("expected 2 KBs, got %d", len(kbs))
	}
	if kbs[0].Name != "kb1" || kbs[0].Description != "first kb" {
		t.Errorf("kb1: %+v", kbs[0])
	}
	if kbs[1].Name != "kb2" || kbs[1].Description != "second kb" {
		t.Errorf("kb2: %+v", kbs[1])
	}
}

func TestMockBackend_DeleteKB(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb1", "")
	mb.CreateKB("kb2", "")

	if err := mb.DeleteKB("kb1"); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}
	kbs, _ := mb.ListKBs()
	if len(kbs) != 1 {
		t.Errorf("expected 1 KB after delete, got %d", len(kbs))
	}
	if kbs[0].Name != "kb2" {
		t.Errorf("remaining KB = %q, want \"kb2\"", kbs[0].Name)
	}
}

func TestMockBackend_WriteReadMeta(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")

	meta := &DocumentMeta{
		Slug:         "doc-1",
		Title:        "Test Document",
		OriginalName: "test.md",
		SourceType:   "markdown",
		ChunkCount:   3,
		TotalChars:   100,
		AddedAt:      testTime("2025-01-15"),
	}
	if err := mb.WriteMeta("kb", "doc-1", meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	read, err := mb.ReadMeta("kb", "doc-1")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if read.Title != "Test Document" {
		t.Errorf("title = %q, want \"Test Document\"", read.Title)
	}
	if read.Slug != "doc-1" {
		t.Errorf("slug = %q, want \"doc-1\"", read.Slug)
	}
}

func TestMockBackend_Exists(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")

	exists, err := mb.Exists("kb", "doc-1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("doc should not exist yet")
	}

	mb.WriteMeta("kb", "doc-1", &DocumentMeta{Slug: "doc-1"})
	exists, err = mb.Exists("kb", "doc-1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("doc should exist")
	}

	// Non-existent KB.
	exists, err = mb.Exists("nonexistent", "doc-1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("doc should not exist in non-existent KB")
	}
}

func TestMockBackend_ChunksCRUD(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")
	mb.WriteMeta("kb", "doc-1", &DocumentMeta{Slug: "doc-1"})

	// Write chunks.
	for i := 0; i < 5; i++ {
		cid := chunkID(i)
		if err := mb.WriteChunk("kb", "doc-1", cid, "content-"+cid); err != nil {
			t.Fatalf("WriteChunk: %v", err)
		}
	}

	// List chunk IDs.
	ids, err := mb.ListChunkIDs("kb", "doc-1")
	if err != nil {
		t.Fatalf("ListChunkIDs: %v", err)
	}
	if len(ids) != 5 {
		t.Fatalf("expected 5 chunks, got %d", len(ids))
	}

	// Read one chunk.
	content, err := mb.ReadChunk("kb", "doc-1", ids[0])
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}
	if content != "content-"+ids[0] {
		t.Errorf("content = %q, want %q", content, "content-"+ids[0])
	}

	// Delete chunks.
	if err := mb.DeleteChunks("kb", "doc-1"); err != nil {
		t.Fatalf("DeleteChunks: %v", err)
	}
	ids, _ = mb.ListChunkIDs("kb", "doc-1")
	if len(ids) != 0 {
		t.Errorf("expected 0 chunks after delete, got %d", len(ids))
	}
}

func TestMockBackend_ChunksIndex(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")
	mb.WriteMeta("kb", "doc-1", &DocumentMeta{Slug: "doc-1"})

	idx := &ChunksIndex{
		Slug:       "doc-1",
		ChunkCount: 3,
		HasVectors: true,
		VectorDim:  256,
		Chunks: []ChunkIndexEntry{
			{ID: "C001", TermCount: 10, Terms: []termFreq{{Term: "hello", Count: 5}}},
			{ID: "C002", TermCount: 8},
			{ID: "C003", TermCount: 12},
		},
	}
	if err := mb.WriteChunksIndex("kb", "doc-1", idx); err != nil {
		t.Fatalf("WriteChunksIndex: %v", err)
	}

	read, err := mb.ReadChunksIndex("kb", "doc-1")
	if err != nil {
		t.Fatalf("ReadChunksIndex: %v", err)
	}
	if read.ChunkCount != 3 {
		t.Errorf("ChunkCount = %d, want 3", read.ChunkCount)
	}
	if !read.HasVectors {
		t.Error("HasVectors should be true")
	}
	if len(read.Chunks) != 3 {
		t.Errorf("len(Chunks) = %d, want 3", len(read.Chunks))
	}
}

func TestMockBackend_RawText(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")
	mb.WriteMeta("kb", "doc-1", &DocumentMeta{Slug: "doc-1"})

	text := "This is the full raw text of the document."
	if err := mb.WriteRawText("kb", "doc-1", text); err != nil {
		t.Fatalf("WriteRawText: %v", err)
	}

	read, err := mb.ReadRawText("kb", "doc-1")
	if err != nil {
		t.Fatalf("ReadRawText: %v", err)
	}
	if read != text {
		t.Errorf("raw text = %q, want %q", read, text)
	}
}

func TestMockBackend_Manifest(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")
	mb.WriteMeta("kb", "doc-1", &DocumentMeta{Slug: "doc-1"})

	mf := &ChunkManifest{
		DocSlug:    "doc-1",
		ChunkCount: 5,
		Version:    2,
		Chunks: []ChunkManifestEntry{
			{ID: "Caa", LegacyID: "000"},
			{ID: "Cbb", LegacyID: "001"},
		},
	}
	if err := mb.WriteManifest("kb", "doc-1", mf); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	read, err := mb.ReadManifest("kb", "doc-1")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if read.Version != 2 {
		t.Errorf("version = %d, want 2", read.Version)
	}
	if len(read.Chunks) != 2 {
		t.Errorf("len(Chunks) = %d, want 2", len(read.Chunks))
	}
}

func TestMockBackend_Staging(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")
	mb.WriteMeta("kb", "doc-1", &DocumentMeta{Slug: "doc-1"})

	// Prepare must not error.
	if err := mb.PrepareStaging("kb", "doc-1"); err != nil {
		t.Fatalf("PrepareStaging: %v", err)
	}

	// ActiveVersion should start at 0.
	v, err := mb.ActiveVersion("kb", "doc-1")
	if err != nil {
		t.Fatalf("ActiveVersion: %v", err)
	}
	if v != 0 {
		t.Errorf("initial version = %d, want 0", v)
	}

	// Promote and verify version.
	if err := mb.PromoteStaging("kb", "doc-1", 3); err != nil {
		t.Fatalf("PromoteStaging: %v", err)
	}
	v, _ = mb.ActiveVersion("kb", "doc-1")
	if v != 3 {
		t.Errorf("version after promote = %d, want 3", v)
	}

	// Clean must not error.
	if err := mb.CleanStaging("kb", "doc-1"); err != nil {
		t.Fatalf("CleanStaging: %v", err)
	}
}

func TestMockBackend_InvertedIndex(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")

	idx := &InvertedIndex{
		Index: map[string][]Posting{
			"hello":  {{DocSlug: "doc-1"}, {DocSlug: "doc-2"}},
			"world":  {{DocSlug: "doc-1"}},
			"golang": {{DocSlug: "doc-2"}},
		},
	}
	if err := mb.WriteInvertedIndex("kb", idx); err != nil {
		t.Fatalf("WriteInvertedIndex: %v", err)
	}

	read, err := mb.ReadInvertedIndex("kb")
	if err != nil {
		t.Fatalf("ReadInvertedIndex: %v", err)
	}
	if len(read.Index) != 3 {
		t.Errorf("term count = %d, want 3", len(read.Index))
	}
	if docs, ok := read.Index["hello"]; !ok || len(docs) != 2 {
		t.Errorf("'hello' term docs: %v", read.Index["hello"])
	}
}

func TestMockBackend_RemoveDocument(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")
	mb.WriteMeta("kb", "doc-1", &DocumentMeta{Slug: "doc-1"})
	mb.WriteChunk("kb", "doc-1", "C01", "content")
	mb.WriteChunksIndex("kb", "doc-1", &ChunksIndex{Slug: "doc-1"})

	// Verify it exists.
	exists, _ := mb.Exists("kb", "doc-1")
	if !exists {
		t.Fatal("doc should exist before removal")
	}

	// Remove.
	if err := mb.RemoveDocument("kb", "doc-1"); err != nil {
		t.Fatalf("RemoveDocument: %v", err)
	}

	// Verify it's gone.
	exists, _ = mb.Exists("kb", "doc-1")
	if exists {
		t.Error("doc should not exist after removal")
	}
}

func TestMockBackend_ConcurrentAccess(t *testing.T) {
	mb := newMockBackend()
	mb.CreateKB("kb", "")

	done := make(chan bool, 20)
	for i := 0; i < 10; i++ {
		go func(n int) {
			slug := chunkID(n)
			mb.WriteMeta("kb", slug, &DocumentMeta{Slug: slug})
			mb.WriteChunk("kb", slug, "C01", "content")
			mb.WriteChunksIndex("kb", slug, &ChunksIndex{Slug: slug})
			done <- true
		}(i)
	}
	for i := 0; i < 10; i++ {
		// Concurrent reads.
		go func(n int) {
			mb.ListDocSlugs("kb")
			mb.ReadMeta("kb", chunkID(n))
			done <- true
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}

	slugs, _ := mb.ListDocSlugs("kb")
	if len(slugs) != 10 {
		t.Errorf("expected 10 docs, got %d", len(slugs))
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────────

func chunkID(i int) string {
	return fmt.Sprintf("C%03d", i)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func testTime(date string) time.Time {
	t, _ := time.Parse("2006-01-02", date)
	return t
}
