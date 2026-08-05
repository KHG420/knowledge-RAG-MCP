// Package knowledge — mock StorageBackend implementation for use by tests in
// external packages (e.g. manage_test). This file intentionally does NOT use the
// _test.go suffix so that NewMockBackend is visible outside the knowledge package
// during test compilation of neighbouring packages.
package knowledge

import (
	"fmt"
	"sort"
	"sync"
)

// ── Mock backend types ──

// mockBackend is an in-memory StorageBackend for testing.
// It implements all StorageBackend methods using Go maps and slices.
type mockBackend struct {
	mu sync.Mutex

	kbs      map[string]*mockKB
	inverted map[string]*InvertedIndex
	snapshot map[string]*mockSnapshot
	indexMD  map[string]string
}

type mockKB struct {
	name        string
	description string
	docs        map[string]*mockDoc
}

type mockDoc struct {
	meta         DocumentMeta
	chunks       map[string]string // chunkID → content
	sections     map[string]string // sectionID → content
	chunksIndex  *ChunksIndex
	manifest     *ChunkManifest
	rawText      string
	sourceData   []byte
	sourceExt    string
	taskRecord   *TaskRecord
	indexVersion int
}

type mockSnapshot struct {
	checksum string
	docs     []DocumentMeta
}

// ── Constructor ──

// NewMockBackend returns an in-memory StorageBackend suitable for testing.
func NewMockBackend() *mockBackend {
	return newMockBackend()
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		kbs:      make(map[string]*mockKB),
		inverted: make(map[string]*InvertedIndex),
		snapshot: make(map[string]*mockSnapshot),
		indexMD:  make(map[string]string),
	}
}

// ── Internal helpers ──

func (m *mockBackend) getOrCreateDoc(kbName, slug string) (*mockKB, *mockDoc, error) {
	kb, ok := m.kbs[kbName]
	if !ok {
		return nil, nil, fmt.Errorf("kb not found: %s", kbName)
	}
	doc, ok := kb.docs[slug]
	if !ok {
		doc = &mockDoc{
			chunks:   make(map[string]string),
			sections: make(map[string]string),
		}
		kb.docs[slug] = doc
	}
	return kb, doc, nil
}

func (m *mockBackend) getDoc(kbName, slug string) (*mockKB, *mockDoc, error) {
	kb, ok := m.kbs[kbName]
	if !ok {
		return nil, nil, fmt.Errorf("kb not found: %s", kbName)
	}
	doc, ok := kb.docs[slug]
	if !ok {
		return nil, nil, fmt.Errorf("doc not found: %s/%s", kbName, slug)
	}
	return kb, doc, nil
}

// ── Lifecycle ──

func (m *mockBackend) Init() error  { return nil }
func (m *mockBackend) Close() error { return nil }

// ── KB management ──

func (m *mockBackend) ListKBs() ([]KBInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kbs []KBInfo
	for _, kb := range m.kbs {
		kbs = append(kbs, KBInfo{Name: kb.name, Description: kb.description})
	}
	sort.Slice(kbs, func(i, j int) bool { return kbs[i].Name < kbs[j].Name })
	return kbs, nil
}

func (m *mockBackend) CreateKB(name, description string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.kbs[name]; ok {
		return fmt.Errorf("kb already exists: %s", name)
	}
	m.kbs[name] = &mockKB{
		name:        name,
		description: description,
		docs:        make(map[string]*mockDoc),
	}
	return nil
}

func (m *mockBackend) DeleteKB(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.kbs, name)
	delete(m.inverted, name)
	delete(m.snapshot, name)
	delete(m.indexMD, name)
	return nil
}

// ── Document metadata ──

func (m *mockBackend) ReadMeta(kbName, slug string) (*DocumentMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return nil, err
	}
	meta := doc.meta
	return &meta, nil
}

func (m *mockBackend) WriteMeta(kbName, slug string, meta *DocumentMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	if meta != nil {
		doc.meta = *meta
	}
	return nil
}

func (m *mockBackend) Exists(kbName, slug string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kb, ok := m.kbs[kbName]
	if !ok {
		return false, nil
	}
	_, ok = kb.docs[slug]
	return ok, nil
}

func (m *mockBackend) ListDocSlugs(kbName string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kb, ok := m.kbs[kbName]
	if !ok {
		return nil, fmt.Errorf("kb not found: %s", kbName)
	}
	var slugs []string
	for slug := range kb.docs {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs, nil
}

func (m *mockBackend) RemoveDocument(kbName, slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kb, ok := m.kbs[kbName]
	if !ok {
		return nil
	}
	delete(kb.docs, slug)
	return nil
}

// ── Chunks ──

func (m *mockBackend) ReadChunk(kbName, slug, chunkID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return "", err
	}
	content, ok := doc.chunks[chunkID]
	if !ok {
		return "", fmt.Errorf("chunk not found: %s", chunkID)
	}
	return content, nil
}

func (m *mockBackend) WriteChunk(kbName, slug, chunkID, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.chunks[chunkID] = content
	return nil
}

func (m *mockBackend) ListChunkIDs(kbName, slug string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return nil, err
	}
	var ids []string
	for id := range doc.chunks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (m *mockBackend) DeleteChunks(kbName, slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.chunks = make(map[string]string)
	doc.sections = make(map[string]string)
	return nil
}

// ── Section chunks ──

func (m *mockBackend) ReadSectionChunk(kbName, slug, sectionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return "", err
	}
	content, ok := doc.sections[sectionID]
	if !ok {
		return "", fmt.Errorf("section not found: %s", sectionID)
	}
	return content, nil
}

func (m *mockBackend) WriteSectionChunk(kbName, slug, sectionID, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.sections[sectionID] = content
	return nil
}

func (m *mockBackend) ListSectionChunkIDs(kbName, slug string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return nil, err
	}
	var ids []string
	for id := range doc.sections {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (m *mockBackend) DeleteSectionChunks(kbName, slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.sections = make(map[string]string)
	return nil
}

// ── Chunks index ──

func (m *mockBackend) ReadChunksIndex(kbName, slug string) (*ChunksIndex, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return nil, err
	}
	return doc.chunksIndex, nil
}

func (m *mockBackend) WriteChunksIndex(kbName, slug string, index *ChunksIndex) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.chunksIndex = index
	return nil
}

// ── Inverted index ──

func (m *mockBackend) ReadInvertedIndex(kbName string) (*InvertedIndex, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inverted[kbName], nil
}

func (m *mockBackend) WriteInvertedIndex(kbName string, idx *InvertedIndex) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inverted[kbName] = idx
	return nil
}

func (m *mockBackend) DeleteInvertedDocEntries(kbName, docSlug string) error {
	return nil
}

func (m *mockBackend) UpsertInvertedEntries(kbName string, entries []InvertedEntry) error {
	return nil
}

// ── Raw text & source ──

func (m *mockBackend) WriteRawText(kbName, slug, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.rawText = text
	return nil
}

func (m *mockBackend) ReadRawText(kbName, slug string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return "", err
	}
	return doc.rawText, nil
}

func (m *mockBackend) WriteSource(kbName, slug string, data []byte, ext string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.sourceData = make([]byte, len(data))
	copy(doc.sourceData, data)
	doc.sourceExt = ext
	return nil
}

// ── INDEX.md ──

func (m *mockBackend) ReadIndex(kbName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.indexMD[kbName], nil
}

func (m *mockBackend) WriteIndex(kbName, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.indexMD[kbName] = content
	return nil
}

// ── Snapshot ──

func (m *mockBackend) ReadSnapshot(kbName string) (string, []DocumentMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.snapshot[kbName]
	if !ok {
		return "", nil, nil
	}
	return snap.checksum, snap.docs, nil
}

func (m *mockBackend) WriteSnapshot(kbName string, docs []DocumentMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot[kbName] = &mockSnapshot{
		checksum: fmt.Sprintf("snap-%d", len(docs)),
		docs:     docs,
	}
	return nil
}

// ── Checksum ──

func (m *mockBackend) ComputeChunksChecksum(kbName, slug string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("cs-%d", len(doc.chunks)), nil
}

// ── Manifest ──

func (m *mockBackend) ReadManifest(kbName, slug string) (*ChunkManifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return nil, nil
	}
	return doc.manifest, nil
}

func (m *mockBackend) WriteManifest(kbName, slug string, manifest *ChunkManifest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.manifest = manifest
	return nil
}

// ── Task record ──

func (m *mockBackend) ReadTaskRecord(kbName, slug string) (*TaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return nil, nil
	}
	return doc.taskRecord, nil
}

func (m *mockBackend) WriteTaskRecord(kbName, slug string, task *TaskRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.taskRecord = task
	return nil
}

func (m *mockBackend) DeleteTaskRecord(kbName, slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.taskRecord = nil
	return nil
}

// ── Staging ──

func (m *mockBackend) PrepareStaging(kbName, slug string) error { return nil }

func (m *mockBackend) PromoteStaging(kbName, slug string, newVersion int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getOrCreateDoc(kbName, slug)
	if err != nil {
		return err
	}
	doc.indexVersion = newVersion
	return nil
}

func (m *mockBackend) CleanStaging(kbName, slug string) error { return nil }

func (m *mockBackend) ActiveVersion(kbName, slug string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, doc, err := m.getDoc(kbName, slug)
	if err != nil {
		return 0, err
	}
	return doc.indexVersion, nil
}

// Ensure mockBackend satisfies StorageBackend at compile time.
var _ StorageBackend = (*mockBackend)(nil)
