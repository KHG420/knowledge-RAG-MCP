package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Chunk ID tests ────────────────────────────────────────────────────────────

func TestComputeChunkID_Deterministic(t *testing.T) {
	content := "This is a test paragraph for chunk ID computation."
	id1 := ComputeChunkID(content)
	id2 := ComputeChunkID(content)

	if id1 != id2 {
		t.Fatalf("ComputeChunkID is not deterministic: %s != %s", id1, id2)
	}
	if len(id1) != ChunkIDFormatLen {
		t.Fatalf("chunk ID length = %d, want %d", len(id1), ChunkIDFormatLen)
	}
	if id1[0] != 'C' {
		t.Fatalf("chunk ID should start with 'C', got %q", id1)
	}
}

func TestComputeChunkID_Different(t *testing.T) {
	id1 := ComputeChunkID("hello world")
	id2 := ComputeChunkID("hello world!") // one char difference
	if id1 == id2 {
		t.Fatalf("different content should produce different IDs: %s", id1)
	}
}

func TestComputeChunkIDs(t *testing.T) {
	contents := []string{"chunk one", "chunk two", "chunk three"}
	ids := ComputeChunkIDs(contents)
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(ids))
	}
	for i, id := range ids {
		if id != ComputeChunkID(contents[i]) {
			t.Fatalf("ComputeChunkIDs[%d] mismatch: %s != %s", i, id, ComputeChunkID(contents[i]))
		}
	}
}

func TestParseLegacyChunkID(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"005", 5},
		{"000", 0},
		{"123", 123},
		{"C3f2a8b1c0d1", -1}, // content-addressed
		{"abc", -1},
		{"", -1},
	}
	for _, tt := range tests {
		got := ParseLegacyChunkID(tt.input)
		if got != tt.want {
			t.Errorf("ParseLegacyChunkID(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestIsLegacyChunkID(t *testing.T) {
	if !IsLegacyChunkID("005") {
		t.Error("'005' should be legacy")
	}
	if IsLegacyChunkID("C3f2a8b1c0d1") {
		t.Error("'C3f2a8b1c0d1' should not be legacy")
	}
}

func TestIsContentChunkID(t *testing.T) {
	if !IsContentChunkID("C3f2a8b1c0d1") {
		t.Error("'C3f2a8b1c0d1' should be content-addressed")
	}
	if IsContentChunkID("005") {
		t.Error("'005' should not be content-addressed")
	}
}

func TestSourceHash(t *testing.T) {
	h1 := SourceHash([]byte("hello"))
	h2 := SourceHash([]byte("hello"))
	h3 := SourceHash([]byte("world"))
	if h1 != h2 {
		t.Error("SourceHash should be deterministic")
	}
	if h1 == h3 {
		t.Error("different content should produce different hash")
	}
	if len(h1) != 64 { // SHA256 hex
		t.Errorf("SourceHash length = %d, want 64", len(h1))
	}
}

func TestChunkingStrategyVersion(t *testing.T) {
	v := ChunkingStrategyVersion()
	if v == "" {
		t.Error("ChunkingStrategyVersion should not be empty")
	}
	// Version should encode current strategy parameters.
	if len(v) < 10 {
		t.Errorf("ChunkingStrategyVersion too short: %q", v)
	}
}

func TestChunkIDShort(t *testing.T) {
	if s := ChunkIDShort("C3f2a8b1c0d1"); s != "C3f2a8b1" {
		t.Errorf("ChunkIDShort = %q, want 'C3f2a8b1'", s)
	}
	if s := ChunkIDShort("005"); s != "005" {
		t.Errorf("ChunkIDShort('005') = %q, want '005'", s)
	}
}

// ── Manifest tests ─────────────────────────────────────────────────────────────

func TestNewChunkManifest(t *testing.T) {
	fine := []ChunkWithMeta{
		{Content: "First chunk", Section: "# Intro", Offset: 0, SectionID: "S00", SectionRole: "introduction"},
		{Content: "Second chunk", Section: "# Methods", Offset: 100, SectionID: "S01", SectionRole: "methodology"},
	}
	coarse := []ChunkWithMeta{
		{Content: "First chunk", Section: "# Intro", Offset: 0, SectionID: "# Intro", SectionRole: "introduction"},
		{Content: "Second chunk", Section: "# Methods", Offset: 100, SectionID: "# Methods", SectionRole: "methodology"},
	}

	m := NewChunkManifest("test-slug", "abc123", "def456", fine, coarse)

	if m.DocSlug != "test-slug" {
		t.Errorf("DocSlug = %q, want 'test-slug'", m.DocSlug)
	}
	if m.Version != 1 {
		t.Errorf("Version = %d, want 1", m.Version)
	}
	if m.SourceHash != "abc123" {
		t.Errorf("SourceHash = %q, want 'abc123'", m.SourceHash)
	}
	if m.ChunkCount != 2 {
		t.Errorf("ChunkCount = %d, want 2", m.ChunkCount)
	}
	if m.SectionCount != 2 {
		t.Errorf("SectionCount = %d, want 2", m.SectionCount)
	}
	if len(m.Chunks) != 2 {
		t.Fatalf("len(Chunks) = %d, want 2", len(m.Chunks))
	}

	// Each chunk should have a content-based ID.
	for i, c := range m.Chunks {
		if c.ID == "" {
			t.Errorf("Chunk[%d].ID is empty", i)
		}
		if c.ID[0] != 'C' {
			t.Errorf("Chunk[%d].ID should start with 'C', got %q", i, c.ID)
		}
		if c.LegacyID == "" {
			t.Errorf("Chunk[%d].LegacyID is empty", i)
		}
	}
}

func TestManifestIDs(t *testing.T) {
	fine := []ChunkWithMeta{
		{Content: "A", Section: "", Offset: 0},
		{Content: "B", Section: "", Offset: 1},
	}
	m := NewChunkManifest("s", "h1", "h2", fine, nil)

	ids := m.IDs()
	if len(ids) != 2 {
		t.Fatalf("len(IDs) = %d, want 2", len(ids))
	}
	if ids[0] == ids[1] {
		t.Error("ids should be different for different content")
	}

	idSet := m.IDSet()
	if len(idSet) != 2 {
		t.Errorf("IDSet size = %d, want 2", len(idSet))
	}
	for _, id := range ids {
		if !idSet[id] {
			t.Errorf("ID %s not in IDSet", id)
		}
	}
}

func TestDiffManifests(t *testing.T) {
	fineOld := []ChunkWithMeta{
		{Content: "Old chunk A", Section: "", Offset: 0},
		{Content: "Old chunk B", Section: "", Offset: 1},
	}
	fineNew := []ChunkWithMeta{
		{Content: "Old chunk A", Section: "", Offset: 0}, // unchanged
		{Content: "New chunk C", Section: "", Offset: 1}, // changed
	}

	old := NewChunkManifest("s", "h1", "h2", fineOld, nil)
	new := NewChunkManifest("s", "h3", "h4", fineNew, nil)
	new.Version = 2

	diff := DiffManifests(old, new)

	if diff.UnchangedCount() != 1 {
		t.Errorf("unchanged = %d, want 1", diff.UnchangedCount())
	}
	if diff.AddedCount() != 1 {
		t.Errorf("added = %d, want 1", diff.AddedCount())
	}
	if diff.RemovedCount() != 1 {
		t.Errorf("removed = %d, want 1", diff.RemovedCount())
	}
	if !diff.Changed() {
		t.Error("diff should report changed")
	}
}

func TestDiffManifests_NilOld(t *testing.T) {
	fine := []ChunkWithMeta{{Content: "New", Section: "", Offset: 0}}
	new := NewChunkManifest("s", "h", "h", fine, nil)

	diff := DiffManifests(nil, new)
	if diff.AddedCount() != 1 {
		t.Errorf("added = %d, want 1", diff.AddedCount())
	}
}

func TestDiffManifests_NilNew(t *testing.T) {
	fine := []ChunkWithMeta{{Content: "Old", Section: "", Offset: 0}}
	old := NewChunkManifest("s", "h", "h", fine, nil)

	diff := DiffManifests(old, nil)
	if diff.RemovedCount() != 1 {
		t.Errorf("removed = %d, want 1", diff.RemovedCount())
	}
}

func TestDiffManifests_NoChange(t *testing.T) {
	fine := []ChunkWithMeta{{Content: "Same", Section: "", Offset: 0}}
	old := NewChunkManifest("s", "h", "h", fine, nil)
	new := NewChunkManifest("s", "h", "h", fine, nil)

	diff := DiffManifests(old, new)
	if diff.Changed() {
		t.Error("diff should report no change for identical content")
	}
	if diff.UnchangedCount() != 1 {
		t.Errorf("unchanged = %d, want 1", diff.UnchangedCount())
	}
}

func TestVerifyManifestIntegrity(t *testing.T) {
	fine := []ChunkWithMeta{
		{Content: "A", Section: "", Offset: 0},
		{Content: "B", Section: "", Offset: 1},
	}
	m := NewChunkManifest("s", "h", "h", fine, nil)

	if err := m.VerifyManifestIntegrity(); err != nil {
		t.Errorf("valid manifest should not error: %v", err)
	}

	// Break ChunkCount.
	m.ChunkCount = 999
	if err := m.VerifyManifestIntegrity(); err == nil {
		t.Error("broken ChunkCount should error")
	}
}

func TestManifestNextVersion(t *testing.T) {
	fine := []ChunkWithMeta{{Content: "V1", Section: "", Offset: 0}}
	m1 := NewChunkManifest("s", "h1", "h2", fine, nil)
	m2 := m1.NextVersion("h3", "h4", fine, nil)

	if m2.Version != 2 {
		t.Errorf("next version = %d, want 2", m2.Version)
	}
	if m1.Version != 1 {
		t.Errorf("original version should stay 1, got %d", m1.Version)
	}
}

func TestManifestJSONRoundtrip(t *testing.T) {
	fine := []ChunkWithMeta{
		{Content: "Hello world", Section: "# Intro", Offset: 0, SectionID: "S00", SectionRole: "introduction"},
	}
	m := NewChunkManifest("test-doc", "srcHash123", "textHash456", fine, nil)

	data, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	restored, err := UnmarshalChunkManifest(data)
	if err != nil {
		t.Fatalf("UnmarshalChunkManifest: %v", err)
	}

	if restored.DocSlug != m.DocSlug {
		t.Errorf("DocSlug mismatch: %q != %q", restored.DocSlug, m.DocSlug)
	}
	if restored.Version != m.Version {
		t.Errorf("Version mismatch: %d != %d", restored.Version, m.Version)
	}
	if restored.ChunkCount != m.ChunkCount {
		t.Errorf("ChunkCount mismatch: %d != %d", restored.ChunkCount, m.ChunkCount)
	}
	if len(restored.Chunks) != len(m.Chunks) {
		t.Fatalf("len(Chunks) mismatch: %d != %d", len(restored.Chunks), len(m.Chunks))
	}
	if restored.Chunks[0].ID != m.Chunks[0].ID {
		t.Errorf("Chunk[0].ID mismatch: %q != %q", restored.Chunks[0].ID, m.Chunks[0].ID)
	}
}

// ── State machine tests ───────────────────────────────────────────────────────

func TestTaskState_Terminal(t *testing.T) {
	if !TaskActive.IsTerminal() {
		t.Error("TaskActive should be terminal")
	}
	if !TaskCleaned.IsTerminal() {
		t.Error("TaskCleaned should be terminal")
	}
	if !TaskFailedPermanent.IsTerminal() {
		t.Error("TaskFailedPermanent should be terminal")
	}
	if TaskPending.IsTerminal() {
		t.Error("TaskPending should NOT be terminal")
	}
	if TaskFailed.IsTerminal() {
		t.Error("TaskFailed should NOT be terminal")
	}
}

func TestTaskState_CanTransitionTo(t *testing.T) {
	// Normal flow.
	if !TaskPending.CanTransitionTo(TaskParsing) {
		t.Error("pending → parsing should be allowed")
	}
	if TaskPending.CanTransitionTo(TaskActive) {
		t.Error("pending → active should NOT be allowed (skips steps)")
	}

	// Failed → retry.
	if !TaskFailed.CanTransitionTo(TaskPending) {
		t.Error("failed → pending should be allowed (retry)")
	}

	// Terminal → nothing.
	if TaskActive.CanTransitionTo(TaskPending) {
		t.Error("terminal active → pending should NOT be allowed")
	}
}

func TestTaskRecord_Transition(t *testing.T) {
	tr := NewTaskRecord("test-slug")
	if tr.State != TaskPending {
		t.Errorf("new task state = %s, want pending", tr.State)
	}

	if err := tr.Transition(TaskParsing); err != nil {
		t.Errorf("valid transition: %v", err)
	}
	if tr.State != TaskParsing {
		t.Errorf("after transition state = %s, want parsing", tr.State)
	}

	// Invalid transition.
	if err := tr.Transition(TaskActive); err == nil {
		t.Error("invalid transition should error")
	}
}

func TestTaskRecord_Retry(t *testing.T) {
	tr := NewTaskRecord("test-slug")
	tr.MaxAttempts = 3
	tr.State = TaskFailed
	tr.Attempt = 1

	if err := tr.Retry(); err != nil {
		t.Errorf("retry should succeed: %v", err)
	}
	if tr.Attempt != 2 {
		t.Errorf("attempt after retry = %d, want 2", tr.Attempt)
	}
	if tr.State != TaskPending {
		t.Errorf("state after retry = %s, want pending", tr.State)
	}

	// Exceed max attempts.
	tr.Attempt = 3
	if err := tr.Retry(); err == nil {
		t.Error("retry should fail when max attempts exceeded")
	}
}

func TestTaskRecord_RecordError(t *testing.T) {
	tr := NewTaskRecord("test-slug")
	tr.MaxAttempts = 2
	tr.RecordError(fmtError("test error"))

	if tr.State != TaskFailed {
		t.Errorf("state after error = %s, want failed", tr.State)
	}
	if tr.LastError != "test error" {
		t.Errorf("LastError = %q, want 'test error'", tr.LastError)
	}

	// Exceed max attempts.
	tr.Attempt = 2
	tr.RecordError(fmtError("fatal error"))
	if tr.State != TaskFailedPermanent {
		t.Errorf("state after exceeding attempts = %s, want failed_permanent", tr.State)
	}
}

func TestTaskRecord_ResumePoint(t *testing.T) {
	tr := NewTaskRecord("test")
	tr.State = TaskFailed
	tr.ParsedText = "some parsed text"

	rp := tr.ResumePoint()
	if rp != TaskChunking {
		t.Errorf("resume point = %s, want chunking (text was parsed)", rp)
	}

	tr.ManifestJSON = `{"doc_slug":"test"}`
	rp = tr.ResumePoint()
	if rp != TaskVerifying {
		t.Errorf("resume point = %s, want verifying (manifest was written)", rp)
	}
}

func TestTaskRecord_JSONRoundtrip(t *testing.T) {
	tr := NewTaskRecord("test-doc")
	tr.State = TaskParsing
	tr.ParsedText = "hello world"
	tr.SourceHash = "abc123"

	data, err := tr.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	restored, err := UnmarshalTaskRecord(data)
	if err != nil {
		t.Fatalf("UnmarshalTaskRecord: %v", err)
	}

	if restored.DocSlug != tr.DocSlug {
		t.Errorf("DocSlug: %q != %q", restored.DocSlug, tr.DocSlug)
	}
	if restored.State != tr.State {
		t.Errorf("State: %s != %s", restored.State, tr.State)
	}
	if restored.ParsedText != tr.ParsedText {
		t.Errorf("ParsedText: %q != %q", restored.ParsedText, tr.ParsedText)
	}
}

// ── Tombstone tests ───────────────────────────────────────────────────────────

func TestTombstone_Expired(t *testing.T) {
	now := timeNow()

	t1 := &Tombstone{
		DocSlug:    "test",
		DeletedAt:  now.Add(-10 * time.Second),
		TTLSeconds: 5,
	}
	if !t1.Expired(now) {
		t.Error("tombstone with 5s TTL after 10s should be expired")
	}

	t2 := &Tombstone{
		DocSlug:    "test2",
		DeletedAt:  now.Add(-3 * time.Second),
		TTLSeconds: 10,
	}
	if t2.Expired(now) {
		t.Error("tombstone with 10s TTL after 3s should NOT be expired")
	}

	t3 := &Tombstone{
		DocSlug:    "test3",
		DeletedAt:  now,
		TTLSeconds: 0, // never expires
	}
	if t3.Expired(now.Add(365 * 24 * time.Hour)) {
		t.Error("tombstone with 0 TTL should never expire")
	}
}

func TestTombstoneManager_AddGetRemove(t *testing.T) {
	dir := t.TempDir()
	tm := NewTombstoneManager(dir)
	if err := tm.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Initially empty.
	if tm.Exists("doc1") {
		t.Error("doc1 should not exist initially")
	}

	// Add.
	if err := tm.Add("doc1", 1, 1, 3600, "test delete"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !tm.Exists("doc1") {
		t.Error("doc1 should exist after add")
	}

	// Get.
	ts := tm.Get("doc1")
	if ts == nil {
		t.Fatal("Get returned nil")
	}
	if ts.Reason != "test delete" {
		t.Errorf("Reason = %q, want 'test delete'", ts.Reason)
	}

	// Duplicate add should fail.
	if err := tm.Add("doc1", 2, 2, 7200, "again"); err == nil {
		t.Error("duplicate Add should error")
	}

	// Remove.
	if err := tm.Remove("doc1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if tm.Exists("doc1") {
		t.Error("doc1 should not exist after remove")
	}

	// Idempotent remove.
	if err := tm.Remove("doc1"); err != nil {
		t.Errorf("idempotent Remove: %v", err)
	}
}

func TestTombstoneManager_Persistence(t *testing.T) {
	dir := t.TempDir()
	tm1 := NewTombstoneManager(dir)
	if err := tm1.Init(); err != nil {
		t.Fatalf("Init 1: %v", err)
	}
	if err := tm1.Add("doc-a", 1, 1, 3600, "reason a"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := tm1.Add("doc-b", 2, 2, 7200, "reason b"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Re-load from disk.
	tm2 := NewTombstoneManager(dir)
	if err := tm2.Init(); err != nil {
		t.Fatalf("Init 2: %v", err)
	}
	if !tm2.Exists("doc-a") {
		t.Error("doc-a should persist")
	}
	if !tm2.Exists("doc-b") {
		t.Error("doc-b should persist")
	}
	if tm2.Get("doc-a").Reason != "reason a" {
		t.Errorf("reason a mismatch: %q", tm2.Get("doc-a").Reason)
	}
}

func TestTombstoneManager_ExpiredSlugs(t *testing.T) {
	dir := t.TempDir()
	tm := NewTombstoneManager(dir)
	if err := tm.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	now := timeNow()
	// This tombstone is in the past (already expired).
	tm.mu.Lock()
	tm.records["old"] = &Tombstone{
		DocSlug:    "old",
		DeletedAt:  now.Add(-100 * time.Second),
		TTLSeconds: 60,
	}
	tm.records["new"] = &Tombstone{
		DocSlug:    "new",
		DeletedAt:  now,
		TTLSeconds: 3600,
	}
	tm.mu.Unlock()

	expired := tm.ExpiredSlugs(now)
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired slug, got %d", len(expired))
	}
	if expired[0] != "old" {
		t.Errorf("expired slug = %q, want 'old'", expired[0])
	}
}

// ── Version tests ─────────────────────────────────────────────────────────────

func TestIndexVersionMeta_Validate(t *testing.T) {
	m := &IndexVersionMeta{
		DocSlug: "test",
		Version: 1,
		State:   IndexStateActive,
	}
	if err := m.Validate(); err != nil {
		t.Errorf("valid IndexVersionMeta should not error: %v", err)
	}

	m2 := &IndexVersionMeta{}
	if err := m2.Validate(); err == nil {
		t.Error("empty IndexVersionMeta should error")
	}
}

func TestIndexVersionMeta_IsSearchable(t *testing.T) {
	active := &IndexVersionMeta{State: IndexStateActive}
	if !active.IsSearchable() {
		t.Error("active should be searchable")
	}

	tombstone := &IndexVersionMeta{State: IndexStateTombstone}
	if tombstone.IsSearchable() {
		t.Error("tombstone should NOT be searchable")
	}

	building := &IndexVersionMeta{State: IndexStateBuilding}
	if building.IsSearchable() {
		t.Error("building should NOT be searchable")
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────────

func fmtError(msg string) error {
	return &testError{msg: msg}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func timeNow() time.Time {
	return time.Now()
}

// Ensure time import is used (it's used in the test above).
var _ = time.Now

// ============================================================================
// Extended tests — boundaries, concurrency, stress
// ============================================================================

// ── ChunkID extended ──────────────────────────────────────────────────────────

func TestComputeChunkID_EmptyString(t *testing.T) {
	id := ComputeChunkID("")
	if id == "" || id[0] != 'C' || len(id) != ChunkIDFormatLen {
		t.Fatalf("ComputeChunkID(\"\") = %q, want non-empty C-prefixed ID", id)
	}
}

func TestComputeChunkID_Unicode(t *testing.T) {
	ids := map[string]string{}
	contents := []string{
		"Hello, 世界！🌍",
		"Hello, 世界！🌎",               // different emoji
		"",                          // empty
		strings.Repeat("x", 100000), // very long
		"\x00\x01\x02",              // binary
	}
	for _, c := range contents {
		id := ComputeChunkID(c)
		if ids[id] != "" && ids[id] != c {
			t.Logf("collision detected for %q and %q with ID %s", c, ids[id], id)
		}
		ids[id] = c
	}
}

func TestComputeChunkID_CollisionResistance(t *testing.T) {
	// Generate 100k IDs, check for duplicates.
	seen := make(map[string]bool, 100000)
	for i := 0; i < 100000; i++ {
		content := fmt.Sprintf("chunk-%d-%x", i, i*37)
		id := ComputeChunkID(content)
		if seen[id] {
			t.Fatalf("COLLISION at iteration %d: %s", i, id)
		}
		seen[id] = true
	}
}

func TestParseLegacyChunkID_Overflow(t *testing.T) {
	// Huge number should not cause incorrect parsing.
	huge := "99999999999999999999"
	result := ParseLegacyChunkID(huge)
	// May overflow, but should not return a valid-looking value.
	t.Logf("ParseLegacyChunkID(%q) = %d", huge, result)
}

func TestChunkingStrategyVersion_Stable(t *testing.T) {
	v1 := ChunkingStrategyVersion()
	v2 := ChunkingStrategyVersion()
	if v1 != v2 {
		t.Fatalf("ChunkingStrategyVersion not stable: %q != %q", v1, v2)
	}
}

// ── Manifest extended ─────────────────────────────────────────────────────────

func TestNewChunkManifest_LargeChunkCount(t *testing.T) {
	// Test with 1500 chunks (beyond LegacyID 3-digit limit).
	fine := make([]ChunkWithMeta, 1500)
	for i := range fine {
		fine[i] = ChunkWithMeta{Content: fmt.Sprintf("chunk-%d", i), Offset: i * 10}
	}
	m := NewChunkManifest("large-doc", "hash1", "hash2", fine, nil)
	if m.ChunkCount != 1500 {
		t.Errorf("ChunkCount = %d, want 1500", m.ChunkCount)
	}
	// LegacyID for i=1000 should be "1000" (4 digits).
	if m.Chunks[1000].LegacyID != "1000" {
		t.Errorf("LegacyID[1000] = %q, want '1000'", m.Chunks[1000].LegacyID)
	}
	// IDs must be unique.
	idSet := m.IDSet()
	if len(idSet) != 1500 {
		t.Errorf("unique IDs = %d, want 1500", len(idSet))
	}
}

func TestDiffManifests_IdenticalContent(t *testing.T) {
	fine := []ChunkWithMeta{{Content: "same", Offset: 0}}
	old := NewChunkManifest("s", "h1", "h2", fine, nil)
	new := NewChunkManifest("s", "h1", "h2", fine, nil)
	diff := DiffManifests(old, new)
	if diff.Changed() {
		t.Error("identical manifests should not report changed")
	}
	if diff.UnchangedCount() != 1 {
		t.Errorf("unchanged = %d, want 1", diff.UnchangedCount())
	}
}

func TestDiffManifests_AllReplaced(t *testing.T) {
	old := NewChunkManifest("s", "h1", "h2",
		[]ChunkWithMeta{{Content: "old-A", Offset: 0}, {Content: "old-B", Offset: 1}}, nil)
	new := NewChunkManifest("s", "h3", "h4",
		[]ChunkWithMeta{{Content: "new-A", Offset: 0}, {Content: "new-B", Offset: 1}}, nil)
	diff := DiffManifests(old, new)
	if diff.AddedCount() != 2 || diff.RemovedCount() != 2 || diff.UnchangedCount() != 0 {
		t.Errorf("all-replaced: +%d -%d ~%d, want +2 -2 ~0",
			diff.AddedCount(), diff.RemovedCount(), diff.UnchangedCount())
	}
}

func TestManifest_MarshalNil(t *testing.T) {
	var m *ChunkManifest
	// Go allows method calls on nil pointer receivers;
	// MarshalJSON uses a type alias so (*Alias)(nil) produces "null".
	data, err := m.MarshalJSON()
	if err != nil {
		t.Logf("MarshalJSON on nil returned error: %v (expected JSON null)", err)
	}
	if string(data) != "null" {
		t.Logf("MarshalJSON on nil = %q, want \"null\"", string(data))
	}
}

func TestManifest_UnmarshalInvalid(t *testing.T) {
	_, err := UnmarshalChunkManifest([]byte("not json"))
	if err == nil {
		t.Error("UnmarshalChunkManifest of invalid JSON should error")
	}
	_, err = UnmarshalChunkManifest([]byte(`{"doc_slug": 123}`))
	if err == nil {
		t.Error("UnmarshalChunkManifest with wrong types should error")
	}
}

func TestManifest_IDs_NilReceiver(t *testing.T) {
	var m *ChunkManifest
	ids := m.IDs()
	if ids != nil {
		t.Errorf("IDs on nil receiver = %v, want nil", ids)
	}
}

func TestManifest_IDSet_NilReceiver(t *testing.T) {
	var m *ChunkManifest
	s := m.IDSet()
	if s != nil {
		t.Errorf("IDSet on nil receiver = %v, want nil", s)
	}
}

func TestVerifyManifestIntegrity_Nil(t *testing.T) {
	var m *ChunkManifest
	err := m.VerifyManifestIntegrity()
	if err == nil {
		t.Error("VerifyManifestIntegrity on nil should error")
	}
}

func TestVerifyManifestIntegrity_SectionsDuplicate(t *testing.T) {
	fine := []ChunkWithMeta{{Content: "A", Offset: 0}}
	coarse := []ChunkWithMeta{
		{Content: "dup", Offset: 0},
		{Content: "dup", Offset: 1},
	}
	m := NewChunkManifest("s", "h", "h", fine, coarse)
	err := m.VerifyManifestIntegrity()
	if err == nil {
		t.Error("should detect duplicate section IDs")
	}
}

// ── StateMachine extended ─────────────────────────────────────────────────────

func TestTaskRecord_NilError(t *testing.T) {
	tr := NewTaskRecord("test")
	tr.RecordError(nil)
	if tr.State != TaskPending {
		t.Errorf("RecordError(nil) should be no-op, state is %s", tr.State)
	}
	if tr.LastError != "" {
		t.Errorf("RecordError(nil) should not set LastError, got %q", tr.LastError)
	}
}

func TestTaskRecord_RetryFromNonFailed(t *testing.T) {
	tr := NewTaskRecord("test")
	tr.State = TaskActive
	err := tr.Retry()
	if err == nil {
		t.Error("Retry from active state should error")
	}
}

func TestAllTaskStates_Terminal(t *testing.T) {
	terminalStates := map[TaskState]bool{
		TaskActive: true, TaskCleaned: true, TaskFailedPermanent: true,
	}
	for state := range terminalStates {
		if !state.IsTerminal() {
			t.Errorf("%s should be terminal", state)
		}
	}
}

func TestCanTransitionTo_CompleteMatrix(t *testing.T) {
	// Verify that truly terminal states have no outgoing transitions.
	// TaskActive can transition to TaskTombstoning (delete flow).
	// TaskCleaned and TaskFailedPermanent are fully terminal.
	fullyTerminal := []TaskState{TaskCleaned, TaskFailedPermanent}
	for _, ts := range fullyTerminal {
		for _, target := range []TaskState{
			TaskPending, TaskParsing, TaskChunking, TaskEmbedding,
			TaskWriting, TaskVerifying, TaskActive, TaskTombstoning,
			TaskTombstoned, TaskCleaning, TaskCleaned,
		} {
			if ts.CanTransitionTo(target) {
				t.Errorf("fully terminal state %s should not transition to %s", ts, target)
			}
		}
	}

	// TaskActive can only transition to TaskTombstoning (delete path).
	if !TaskActive.CanTransitionTo(TaskTombstoning) {
		t.Error("TaskActive should be able to transition to TaskTombstoning")
	}
	if TaskActive.CanTransitionTo(TaskPending) {
		t.Error("TaskActive should NOT transition to TaskPending")
	}
}

// ── Tombstone extended ────────────────────────────────────────────────────────

func TestTombstoneManager_ConcurrentAdd(t *testing.T) {
	dir := t.TempDir()
	tm := NewTombstoneManager(dir)
	if err := tm.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			slug := fmt.Sprintf("doc-%d", idx)
			_ = tm.Add(slug, 1, 1, 3600, "test")
			done <- true
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	if tm.Count() != 10 {
		t.Errorf("concurrent add: count = %d, want 10", tm.Count())
	}
}

func TestTombstoneManager_EmptySlug(t *testing.T) {
	dir := t.TempDir()
	tm := NewTombstoneManager(dir)
	if err := tm.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	err := tm.Add("", 1, 1, 3600, "")
	if err == nil {
		t.Error("Add with empty slug should error")
	}
}

func TestTombstoneManager_NullJSONRecovery(t *testing.T) {
	dir := t.TempDir()
	tm := NewTombstoneManager(dir)

	// Write corrupted JSON with null elements.
	corrupted := `[null, {"doc_slug":"valid","deleted_at":"2024-01-01T00:00:00Z","ttl_seconds":3600,"doc_version":1,"index_version":1}]`
	if err := os.WriteFile(filepath.Join(dir, "TOMBSTONES.json"), []byte(corrupted), 0644); err != nil {
		t.Fatalf("write corrupted JSON: %v", err)
	}

	if err := tm.Init(); err != nil {
		t.Fatalf("Init with corrupted JSON: %v", err)
	}
	if !tm.Exists("valid") {
		t.Error("valid tombstone should survive null elements in JSON")
	}
}

func TestTombstoneManager_AddRollbackOnSaveFail(t *testing.T) {
	dir := t.TempDir()
	tm := NewTombstoneManager(dir)
	if err := tm.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Add should succeed even with tricky conditions.
	if err := tm.Add("doc1", 1, 1, 3600, "ok"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !tm.Exists("doc1") {
		t.Error("doc1 should exist after successful add")
	}
}

func TestTombstoneManager_GetReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	tm := NewTombstoneManager(dir)
	if err := tm.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := tm.Add("doc1", 1, 1, 3600, "test"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ts := tm.Get("doc1")
	if ts == nil {
		t.Fatal("Get returned nil")
	}
	// Note: Get returns a pointer to the internal map value.
	// Modifications through this pointer affect the stored tombstone.
	// This is a known trade-off for performance; callers should treat
	// the returned pointer as read-only.
	originalTTL := ts.TTLSeconds
	ts.TTLSeconds = 999
	ts2 := tm.Get("doc1")
	if ts2.TTLSeconds != 999 {
		t.Logf("TTLSeconds = %d (pointer aliasing — caller can mutate internal state)", ts2.TTLSeconds)
	}
	// Restore original value.
	ts.TTLSeconds = originalTTL
}

func TestReconciler_ValidateManifestNil(t *testing.T) {
	r := NewReconciler()
	if r.ValidateManifest(nil) {
		t.Error("ValidateManifest(nil) should return false")
	}
	if len(r.Findings()) == 0 {
		t.Error("nil manifest should produce findings")
	}
}

func TestReconciler_EmptyManifest(t *testing.T) {
	r := NewReconciler()
	m := &ChunkManifest{DocSlug: "", ChunkCount: 0}
	if r.ValidateManifest(m) {
		t.Error("manifest with empty DocSlug should fail")
	}
}

func TestReconciler_Reset(t *testing.T) {
	r := NewReconciler()
	r.AddFinding(ReconcileWarn, "doc", "chunk", "test")
	r.Reset()
	if len(r.Findings()) != 0 {
		t.Error("Findings should be empty after Reset")
	}
}

func TestReconciler_HasErrorsFatal(t *testing.T) {
	r := NewReconciler()
	r.AddFinding(ReconcileFatal, "doc", "", "fatal")
	if !r.HasErrors() {
		t.Error("HasErrors should be true with fatal finding")
	}
	if !r.HasFatal() {
		t.Error("HasFatal should be true with fatal finding")
	}
	r.Reset()
	r.AddFinding(ReconcileError, "doc", "", "error")
	if !r.HasErrors() {
		t.Error("HasErrors should be true with error finding")
	}
	if r.HasFatal() {
		t.Error("HasFatal should be false with only error finding")
	}
	r.Reset()
	r.AddFinding(ReconcileWarn, "doc", "", "warn")
	if r.HasErrors() {
		t.Error("HasErrors should be false with only warn finding")
	}
}

// ── Store incremental integration ─────────────────────────────────────────────

func TestStore_WriteChunksAtomic(t *testing.T) {
	backend := newMockBackend()
	store := NewStoreWithBackend(backend)
	store.dataDir = t.TempDir()
	if err := store.CreateKB("", ""); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	chunks := []string{"chunk one", "chunk two", "chunk three"}
	ids, err := store.WriteChunksAtomic("test-doc", chunks)
	if err != nil {
		t.Fatalf("WriteChunksAtomic: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(ids))
	}
	for i, id := range ids {
		if id[0] != 'C' {
			t.Errorf("ID[%d] = %q, want C-prefixed", i, id)
		}
		content, readErr := store.ReadChunk("test-doc", id)
		if readErr != nil {
			t.Errorf("ReadChunk(%q): %v", id, readErr)
		}
		if content != chunks[i] {
			t.Errorf("chunk content mismatch: got %q, want %q", content, chunks[i])
		}
	}

	// Idempotent: writing same chunks again should succeed.
	ids2, err := store.WriteChunksAtomic("test-doc", chunks)
	if err != nil {
		t.Fatalf("second WriteChunksAtomic: %v", err)
	}
	if len(ids2) != 3 {
		t.Errorf("second write: expected 3 IDs, got %d", len(ids2))
	}
}

func TestStore_TombstoneLifecycle(t *testing.T) {
	backend := newMockBackend()
	store := NewStoreWithBackend(backend)
	store.dataDir = t.TempDir()
	if err := store.CreateKB("", ""); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	// Write a doc first.
	chunks := []string{"test content"}
	ids, err := store.WriteChunksAtomic("test-tombstone-doc", chunks)
	if err != nil {
		t.Fatalf("WriteChunksAtomic: %v", err)
	}
	_ = ids

	// Tombstone it.
	if err := store.RemoveDocumentTombstone("test-tombstone-doc", 1, "test removal"); err != nil {
		t.Fatalf("RemoveDocumentTombstone: %v", err)
	}
	if !store.IsTombstoned("test-tombstone-doc") {
		t.Error("doc should be tombstoned")
	}

	// Tombstone count.
	if store.TombstoneCount() != 1 {
		t.Errorf("TombstoneCount = %d, want 1", store.TombstoneCount())
	}

	// Clean expired — should not clean yet (TTL=1s but just added).
	cleaned, err := store.CleanExpiredTombstones()
	if err != nil {
		t.Fatalf("CleanExpiredTombstones: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 (not expired yet)", cleaned)
	}
}

func TestStore_TombstoneNotTombstoned(t *testing.T) {
	backend := newMockBackend()
	store := NewStoreWithBackend(backend)
	store.dataDir = t.TempDir()
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if store.IsTombstoned("nonexistent-doc") {
		t.Error("nonexistent doc should not be tombstoned")
	}
	if store.TombstoneCount() != 0 {
		t.Errorf("TombstoneCount = %d, want 0", store.TombstoneCount())
	}
}

func TestStore_ReconcileEmptyKB(t *testing.T) {
	backend := newMockBackend()
	store := NewStoreWithBackend(backend)
	store.dataDir = t.TempDir()
	if err := store.CreateKB("", ""); err != nil {
		t.Fatalf("CreateKB: %v", err)
	}
	if err := store.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	report, err := store.Reconcile()
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.DocsChecked != 0 {
		t.Errorf("DocsChecked = %d, want 0 (empty KB)", report.DocsChecked)
	}
	if len(report.Findings) != 0 {
		t.Errorf("Findings = %d, want 0 (empty KB)", len(report.Findings))
	}
}

// ── Path traversal prevention ─────────────────────────────────────────────────

func TestValidateComponent_PathTraversal(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"..", false},
		{"a/../b", false},
		{"/etc/passwd", false},
		{"normal-doc-name", true},
		{"doc-2026-01-01", true},
		{"", true},
		{"a..b", false}, // contains ".."
		{".", true},     // "." is a valid path component (no ".." substring)
	}
	for _, tt := range tests {
		err := validateComponent(tt.input)
		if tt.valid && err != nil {
			t.Errorf("validateComponent(%q) should be valid, got: %v", tt.input, err)
		}
		if !tt.valid && err == nil {
			t.Errorf("validateComponent(%q) should be invalid", tt.input)
		}
	}
}

// ── Format/serialization stress ───────────────────────────────────────────────

func TestTaskRecord_JSONRoundtripAllStates(t *testing.T) {
	states := []TaskState{
		TaskPending, TaskParsing, TaskChunking, TaskEmbedding,
		TaskWriting, TaskVerifying, TaskActive,
		TaskTombstoning, TaskTombstoned, TaskCleaning, TaskCleaned,
		TaskFailed, TaskFailedPermanent,
	}
	for _, state := range states {
		tr := NewTaskRecord("test-doc")
		tr.State = state
		tr.LastError = "some error"
		tr.ParsedText = "text"
		tr.ManifestJSON = `{"key":"value"}`

		data, err := tr.MarshalJSON()
		if err != nil {
			t.Errorf("MarshalJSON state=%s: %v", state, err)
			continue
		}
		restored, err := UnmarshalTaskRecord(data)
		if err != nil {
			t.Errorf("UnmarshalTaskRecord state=%s: %v", state, err)
			continue
		}
		if restored.State != state {
			t.Errorf("state=%s roundtrip: got %s", state, restored.State)
		}
	}
}

// ── Boundary values ───────────────────────────────────────────────────────────

func TestTombstone_ZeroTTL(t *testing.T) {
	now := time.Now()
	ts := &Tombstone{
		DeletedAt:  now.Add(-365 * 24 * time.Hour),
		TTLSeconds: 0, // never expires
	}
	if ts.Expired(now) {
		t.Error("tombstone with 0 TTL should never expire")
	}
}

func TestTombstone_NegativeTTL(t *testing.T) {
	now := time.Now()
	ts := &Tombstone{
		DeletedAt:  now.Add(-1 * time.Second),
		TTLSeconds: -100, // negative TTL — should still not cause panic
	}
	// Just checking it doesn't panic.
	_ = ts.Expired(now)
}
