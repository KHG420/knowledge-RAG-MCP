package knowledge

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileBackend implements StorageBackend using the local filesystem.
// Document directories are stored under <dataDir>/<kbName>/<slug>/ with the same
// layout as the original Store: chunks/*.md, meta.json, CHUNKS.toml, etc.
type FileBackend struct {
	dataDir string // root directory for all KB data
}

// NewFileBackend creates a FileBackend rooted at dataDir.
// If dataDir is empty, it defaults to ~/knowledge_base/.
func NewFileBackend(dataDir string) *FileBackend {
	if dataDir == "" {
		homeDir, _ := os.UserHomeDir()
		dataDir = filepath.Join(homeDir, "knowledge_base")
	} else {
		// Expand ~ to home directory if present.
		if strings.HasPrefix(dataDir, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				dataDir = filepath.Join(home, dataDir[2:])
			}
		}
		// Resolve to absolute path.
		if abs, err := filepath.Abs(dataDir); err == nil {
			dataDir = abs
		}
	}
	return &FileBackend{dataDir: dataDir}
}

// ── path helpers ──────────────────────────────────────────────────────────────

func (fb *FileBackend) kbDir(kbName string) string       { return filepath.Join(fb.dataDir, kbName) }
func (fb *FileBackend) docDir(kbName, slug string) string { return filepath.Join(fb.kbDir(kbName), slug) }
func (fb *FileBackend) metaPath(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), "meta.json")
}
func (fb *FileBackend) chunksDir(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), "chunks")
}
func (fb *FileBackend) sectionsDir(kbName, slug string) string {
	return filepath.Join(fb.chunksDir(kbName, slug), "sections")
}
func (fb *FileBackend) chunkPath(kbName, slug, chunkID string) string {
	return filepath.Join(fb.chunksDir(kbName, slug), chunkID+".md")
}
func (fb *FileBackend) sectionChunkPath(kbName, slug, sectionID string) string {
	return filepath.Join(fb.sectionsDir(kbName, slug), sectionID+".md")
}
func (fb *FileBackend) chunksIndexPath(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), "CHUNKS.toml")
}
func (fb *FileBackend) invertedIndexPath(kbName string) string {
	return filepath.Join(fb.kbDir(kbName), "INVERTED.gob")
}
func (fb *FileBackend) indexMDPath(kbName string) string {
	return filepath.Join(fb.kbDir(kbName), "INDEX.md")
}
func (fb *FileBackend) snapshotPath(kbName string) string {
	return filepath.Join(fb.kbDir(kbName), "LIST_SNAPSHOT.json")
}
func (fb *FileBackend) kbJSONPath(kbName string) string {
	return filepath.Join(fb.kbDir(kbName), "kb.json")
}
func (fb *FileBackend) docDirSlug(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), slug)
}
func (fb *FileBackend) rawTextPath(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), "document.md")
}

// ── Lifecycle ─────────────────────────────────────────────────────────────────

func (fb *FileBackend) Init() error {
	return os.MkdirAll(fb.dataDir, 0o755)
}

func (fb *FileBackend) Close() error { return nil }

// ── KB operations ─────────────────────────────────────────────────────────────

func (fb *FileBackend) ListKBs() ([]KBInfo, error) {
	entries, err := os.ReadDir(fb.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read knowledge dir: %w", err)
	}
	var kbs []KBInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		indexPath := filepath.Join(fb.kbDir(e.Name()), "INDEX.md")
		if _, err := os.Stat(indexPath); err != nil {
			continue
		}
		info := KBInfo{Name: e.Name()}
		// Read description from kb.json
		if data, readErr := os.ReadFile(fb.kbJSONPath(e.Name())); readErr == nil {
			var kbMeta struct {
				Description string `json:"description"`
			}
			if json.Unmarshal(data, &kbMeta) == nil {
				info.Description = kbMeta.Description
			}
		}
		kbs = append(kbs, info)
	}
	return kbs, nil
}

func (fb *FileBackend) CreateKB(name, description string) error {
	kbDir := fb.kbDir(name)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		return fmt.Errorf("create KB dir: %w", err)
	}
	indexPath := filepath.Join(kbDir, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte("# "+name+"\n"), 0o644); err != nil {
		return fmt.Errorf("create INDEX.md: %w", err)
	}
	kbMeta := struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}{Name: name, Description: description}
	metaData, err := json.MarshalIndent(kbMeta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal kb.json: %w", err)
	}
	if err := os.WriteFile(fb.kbJSONPath(name), metaData, 0o644); err != nil {
		return fmt.Errorf("create kb.json: %w", err)
	}
	return nil
}

func (fb *FileBackend) DeleteKB(name string) error {
	return os.RemoveAll(fb.kbDir(name))
}

// ── Document metadata ─────────────────────────────────────────────────────────

func (fb *FileBackend) ReadMeta(kbName, slug string) (*DocumentMeta, error) {
	data, err := os.ReadFile(fb.metaPath(kbName, slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document %q not found", slug)
		}
		return nil, fmt.Errorf("read meta.json for %q: %w", slug, err)
	}
	var meta DocumentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal meta.json for %q: %w", slug, err)
	}
	meta.Slug = slug
	return &meta, nil
}

func (fb *FileBackend) WriteMeta(kbName, slug string, meta *DocumentMeta) error {
	dir := fb.docDir(kbName, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := os.WriteFile(fb.metaPath(kbName, slug), data, 0o644); err != nil {
		return fmt.Errorf("write meta.json: %w", err)
	}
	return nil
}

func (fb *FileBackend) Exists(kbName, slug string) (bool, error) {
	_, err := os.Stat(fb.docDir(kbName, slug))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (fb *FileBackend) ListDocSlugs(kbName string) ([]string, error) {
	kd := fb.kbDir(kbName)
	entries, err := os.ReadDir(kd)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read knowledge dir: %w", err)
	}
	var slugs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slugs = append(slugs, e.Name())
	}
	return slugs, nil
}

func (fb *FileBackend) RemoveDocument(kbName, slug string) error {
	return os.RemoveAll(fb.docDir(kbName, slug))
}

// ── Chunks ────────────────────────────────────────────────────────────────────

func (fb *FileBackend) ReadChunk(kbName, slug, chunkID string) (string, error) {
	data, err := os.ReadFile(fb.chunkPath(kbName, slug, chunkID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("chunk %q not found in document %q", chunkID, slug)
		}
		return "", fmt.Errorf("read chunk %q in %q: %w", chunkID, slug, err)
	}
	return string(data), nil
}

func (fb *FileBackend) WriteChunk(kbName, slug, chunkID, content string) error {
	dir := fb.chunksDir(kbName, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create chunks dir: %w", err)
	}
	return os.WriteFile(fb.chunkPath(kbName, slug, chunkID), []byte(content), 0o644)
}

func (fb *FileBackend) ListChunkIDs(kbName, slug string) ([]string, error) {
	dir := fb.chunksDir(kbName, slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("document %q not found", slug)
		}
		return nil, fmt.Errorf("read chunks dir for %q: %w", slug, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".md") {
			ids = append(ids, strings.TrimSuffix(name, ".md"))
		}
	}
	return ids, nil
}

func (fb *FileBackend) DeleteChunks(kbName, slug string) error {
	return os.RemoveAll(fb.chunksDir(kbName, slug))
}

// ── Section chunks ────────────────────────────────────────────────────────────

func (fb *FileBackend) ReadSectionChunk(kbName, slug, sectionID string) (string, error) {
	data, err := os.ReadFile(fb.sectionChunkPath(kbName, slug, sectionID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("section chunk %q not found in document %q", sectionID, slug)
		}
		return "", fmt.Errorf("read section chunk %q in %q: %w", sectionID, slug, err)
	}
	return string(data), nil
}

func (fb *FileBackend) WriteSectionChunk(kbName, slug, sectionID, content string) error {
	dir := fb.sectionsDir(kbName, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sections dir: %w", err)
	}
	return os.WriteFile(fb.sectionChunkPath(kbName, slug, sectionID), []byte(content), 0o644)
}

func (fb *FileBackend) ListSectionChunkIDs(kbName, slug string) ([]string, error) {
	dir := fb.sectionsDir(kbName, slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sections dir for %q: %w", slug, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".md") {
			ids = append(ids, strings.TrimSuffix(name, ".md"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (fb *FileBackend) DeleteSectionChunks(kbName, slug string) error {
	return os.RemoveAll(fb.sectionsDir(kbName, slug))
}

// ── Search index ──────────────────────────────────────────────────────────────

func (fb *FileBackend) ReadChunksIndex(kbName, slug string) (*ChunksIndex, error) {
	data, err := os.ReadFile(fb.chunksIndexPath(kbName, slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read CHUNKS.toml: %w", err)
	}
	// Try new format ([]termFreq Terms).
	var index ChunksIndex
	if _, err := toml.Decode(string(data), &index); err == nil {
		// G10: verify checksum if present; on mismatch return nil (force rebuild).
		if index.Checksum != "" {
			actualCS, csErr := fb.ComputeChunksChecksum(kbName, slug)
			if csErr == nil && actualCS != index.Checksum {
				return nil, nil
			}
		}
		return &index, nil
	}
	// Fall back to old format (map[string]int Terms) — converted to current format.
	type ChunkIndexEntryV1 struct {
		ID             string         `toml:"id"`
		TermCount      int            `toml:"term_count"`
		Terms          map[string]int `toml:"terms"`
		Section        string         `toml:"section"`
		Offset         int            `toml:"offset"`
		PageStart      int            `toml:"page_start,omitempty"`
		PageEnd        int            `toml:"page_end,omitempty"`
		Vector         []float64      `toml:"vector,omitempty"`
		SectionChunkID string         `toml:"section_chunk_id,omitempty"`
		SectionRole    string         `toml:"section_role,omitempty"`
	}
	type ChunksIndexV1 struct {
		Slug       string              `toml:"slug"`
		ChunkCount int                 `toml:"chunk_count"`
		VectorDim  int                 `toml:"vector_dim,omitempty"`
		HasVectors bool                `toml:"has_vectors,omitempty"`
		Chunks     []ChunkIndexEntryV1 `toml:"chunks"`
	}
	var indexV1 ChunksIndexV1
	if _, err := toml.Decode(string(data), &indexV1); err != nil {
		return nil, fmt.Errorf("decode CHUNKS.toml (tried both formats): %w", err)
	}
	index = ChunksIndex{
		Slug:       indexV1.Slug,
		ChunkCount: indexV1.ChunkCount,
		VectorDim:  indexV1.VectorDim,
		HasVectors: indexV1.HasVectors,
		Chunks:     make([]ChunkIndexEntry, len(indexV1.Chunks)),
	}
	for i, c := range indexV1.Chunks {
		terms := make([]termFreq, 0, len(c.Terms))
		for term, count := range c.Terms {
			terms = append(terms, termFreq{Term: term, Count: count})
		}
		sort.Slice(terms, func(i, j int) bool { return terms[i].Count > terms[j].Count })
		index.Chunks[i] = ChunkIndexEntry{
			ID: c.ID, TermCount: c.TermCount, Terms: terms,
			Section: c.Section, Offset: c.Offset, PageStart: c.PageStart, PageEnd: c.PageEnd,
			Vector: c.Vector, SectionChunkID: c.SectionChunkID, SectionRole: c.SectionRole,
		}
	}
	return &index, nil
}

func (fb *FileBackend) WriteChunksIndex(kbName, slug string, index *ChunksIndex) error {
	dir := fb.docDir(kbName, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	f, err := os.Create(fb.chunksIndexPath(kbName, slug))
	if err != nil {
		return fmt.Errorf("create CHUNKS.toml: %w", err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(index)
}

// ── Inverted index ────────────────────────────────────────────────────────────

func (fb *FileBackend) ReadInvertedIndex(kbName string) (*InvertedIndex, error) {
	f, err := os.Open(fb.invertedIndexPath(kbName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open INVERTED.gob: %w", err)
	}
	defer f.Close()
	var idx InvertedIndex
	if err := gob.NewDecoder(f).Decode(&idx); err != nil {
		return nil, fmt.Errorf("decode INVERTED.gob: %w", err)
	}
	return &idx, nil
}

func (fb *FileBackend) WriteInvertedIndex(kbName string, idx *InvertedIndex) error {
	dir := fb.kbDir(kbName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure kb dir: %w", err)
	}
	f, err := os.Create(fb.invertedIndexPath(kbName))
	if err != nil {
		return fmt.Errorf("create INVERTED.gob: %w", err)
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(idx)
}

// ── Raw text & source ─────────────────────────────────────────────────────────

func (fb *FileBackend) WriteRawText(kbName, slug, text string) error {
	dir := fb.docDir(kbName, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	return os.WriteFile(fb.rawTextPath(kbName, slug), []byte(text), 0o644)
}

func (fb *FileBackend) WriteSource(kbName, slug string, data []byte, ext string) error {
	dir := fb.docDir(kbName, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	srcPath := filepath.Join(dir, "source"+ext)
	return os.WriteFile(srcPath, data, 0o644)
}

// ── INDEX.md ──────────────────────────────────────────────────────────────────

func (fb *FileBackend) ReadIndex(kbName string) (string, error) {
	data, err := os.ReadFile(fb.indexMDPath(kbName))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read INDEX.md: %w", err)
	}
	return string(data), nil
}

func (fb *FileBackend) WriteIndex(kbName, content string) error {
	dir := fb.kbDir(kbName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure kb dir: %w", err)
	}
	return os.WriteFile(fb.indexMDPath(kbName), []byte(content), 0o644)
}

// ── Snapshot ───────────────────────────────────────────────────────────────────

func (fb *FileBackend) ReadSnapshot(kbName string) (string, []DocumentMeta, error) {
	data, readErr := os.ReadFile(fb.snapshotPath(kbName))
	if os.IsNotExist(readErr) {
		return "", nil, nil
	}
	if readErr != nil {
		return "", nil, fmt.Errorf("read snapshot: %w", readErr)
	}
	var snapshot struct {
		Checksum  string         `json:"checksum"`
		UpdatedAt string         `json:"updated_at"`
		Documents []DocumentMeta `json:"documents"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return "", nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return snapshot.Checksum, snapshot.Documents, nil
}

func (fb *FileBackend) WriteSnapshot(kbName string, docs []DocumentMeta) error {
	if len(docs) == 0 {
		os.Remove(fb.snapshotPath(kbName))
		return nil
	}
	cs := ListChecksum(docs)
	snapshot := struct {
		Checksum  string         `json:"checksum"`
		UpdatedAt string         `json:"updated_at"`
		Documents []DocumentMeta `json:"documents"`
	}{
		Checksum:  cs,
		UpdatedAt: "",
		Documents: docs,
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	return os.WriteFile(fb.snapshotPath(kbName), data, 0o644)
}

// ── Checksum ──────────────────────────────────────────────────────────────────

func (fb *FileBackend) ComputeChunksChecksum(kbName, slug string) (string, error) {
	ids, err := fb.ListChunkIDs(kbName, slug)
	if err != nil {
		return "", fmt.Errorf("list chunks: %w", err)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		data, readErr := os.ReadFile(fb.chunkPath(kbName, slug, id))
		if readErr != nil {
			return "", fmt.Errorf("read chunk %s: %w", id, readErr)
		}
		io.WriteString(h, id)
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── Chunk manifest ────────────────────────────────────────────────────────────

func (fb *FileBackend) manifestPath(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), ManifestFilename)
}

func (fb *FileBackend) ReadManifest(kbName, slug string) (*ChunkManifest, error) {
	data, err := os.ReadFile(fb.manifestPath(kbName, slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no manifest yet (legacy document)
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return UnmarshalChunkManifest(data)
}

func (fb *FileBackend) WriteManifest(kbName, slug string, manifest *ChunkManifest) error {
	dir := fb.docDir(kbName, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	data, err := manifest.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(fb.manifestPath(kbName, slug), data, 0o644)
}

// ── Task record ───────────────────────────────────────────────────────────────

func (fb *FileBackend) taskPath(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), TaskFilename)
}

func (fb *FileBackend) ReadTaskRecord(kbName, slug string) (*TaskRecord, error) {
	data, err := os.ReadFile(fb.taskPath(kbName, slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read task record: %w", err)
	}
	return UnmarshalTaskRecord(data)
}

func (fb *FileBackend) WriteTaskRecord(kbName, slug string, task *TaskRecord) error {
	dir := fb.docDir(kbName, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	data, err := task.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal task record: %w", err)
	}
	return os.WriteFile(fb.taskPath(kbName, slug), data, 0o644)
}

func (fb *FileBackend) DeleteTaskRecord(kbName, slug string) error {
	os.Remove(fb.taskPath(kbName, slug))
	return nil
}

// ── Atomic index staging ──────────────────────────────────────────────────────

func (fb *FileBackend) stagingDir(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), ".staging")
}

func (fb *FileBackend) versionsDir(kbName, slug string) string {
	return filepath.Join(fb.docDir(kbName, slug), "versions")
}

func (fb *FileBackend) PrepareStaging(kbName, slug string) error {
	builder := NewAtomicIndexBuilder(fb.docDir(kbName, slug))
	return builder.PrepareStaging()
}

func (fb *FileBackend) PromoteStaging(kbName, slug string, newVersion int) error {
	builder := NewAtomicIndexBuilder(fb.docDir(kbName, slug))
	return builder.PromoteAtomically(newVersion)
}

func (fb *FileBackend) CleanStaging(kbName, slug string) error {
	return os.RemoveAll(fb.stagingDir(kbName, slug))
}

func (fb *FileBackend) ActiveVersion(kbName, slug string) (int, error) {
	builder := NewAtomicIndexBuilder(fb.docDir(kbName, slug))
	return builder.ActiveVersion(), nil
}
