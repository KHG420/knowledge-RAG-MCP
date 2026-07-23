package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"knowledge-mcp/internal/logging"
	"knowledge-mcp/internal/retrieval"
)

const maxTermsPerChunk = 50 // top-N frequent terms retained in CHUNKS.toml

const boundaryMergeN = 5 // G12: number of old tail chunks for incremental boundary merge
const boundaryMergeM = 5 // G12: number of new head chunks for incremental boundary merge

// Store manages the on-disk knowledge base. By default data is stored under
// ~/knowledge_base/; call WithDataDir to use a custom directory.
type Store struct {
	dataDir string // if set, overrides the default knowledge dir path (~/knowledge_base/)
	kbName  string // knowledge base name; empty means flat legacy mode (no subdirectory)
	rewriter           QueryRewriter
	embedder           Embedder
	reranker           Reranker
	gpuScheduler       *GPUScheduler
	rerankCandidateLimit int   // max candidates fed to reranker (default 100)
	rerankBatchSize     int   // max documents per reranker request (default 20)
	searchLogger       SearchLogger
	AbstractBoost float64 // G13: multiplier for abstract-section chunks in papers (default 1.1)
	logger *logging.Logger
	mu     *sync.Mutex
}

// NewStore returns a Store. The data directory defaults to ~/knowledge_base/;
// call WithDataDir to override.
func NewStore() *Store {
	return &Store{AbstractBoost: 1.1, logger: logging.NewNopLogger(), mu: &sync.Mutex{}}
}

// WithDataDir sets an explicit data directory for the knowledge base,
// overriding the default ~/knowledge_base/ path.
func (s *Store) WithDataDir(dir string) *Store {
	s.dataDir = dir
	return s
}

// WithKB returns a Store view scoped to the named knowledge base.
// When name is empty, the store operates on the flat knowledge directory (legacy mode).
// The returned Store shares the same embedder, reranker, logger, and other
// configuration but reads/writes from a KB-scoped subdirectory.
func (s *Store) WithKB(name string) *Store {
	cp := *s
	cp.kbName = name
	return &cp
}

// SetLogger sets the logger on the Store.
func (s *Store) SetLogger(l *logging.Logger) {
	s.logger = l
	SetParserLogger(l)
}

// SetGPUScheduler configures a GPU scheduler for managing model sleep/wake.
// When set, embedding and reranker operations coordinate to share GPU memory.
func (s *Store) SetGPUScheduler(g *GPUScheduler) {
	s.gpuScheduler = g
}

// kbDir returns the KB-scoped data directory.
// When kbName is empty, it returns the base knowledge dir (legacy flat mode).
func (s *Store) kbDir() string {
	return filepath.Join(s.knowledgeDir(), s.kbName)
}

// ListKBs returns knowledge base names found under the knowledge directory.
// Each subdirectory containing an INDEX.md is considered a KB.
// If no KB subdirectories exist (legacy flat structure), returns an empty list.
func (s *Store) ListKBs() ([]string, error) {
	kd := s.knowledgeDir()
	entries, err := os.ReadDir(kd)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read knowledge dir: %w", err)
	}
	var kbs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Check if this directory has INDEX.md to confirm it's a KB.
		indexPath := filepath.Join(kd, e.Name(), "INDEX.md")
		if _, err := os.Stat(indexPath); err == nil {
			kbs = append(kbs, e.Name())
		}
	}
	return kbs, nil
}

// KBInfo holds metadata about a knowledge base.
type KBInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListKBsInfo returns knowledge base names with descriptions.
// Each KB's description is read from kb.json; if the file does not exist
// (legacy KB), the description is empty.
func (s *Store) ListKBsInfo() ([]KBInfo, error) {
	kd := s.knowledgeDir()
	entries, err := os.ReadDir(kd)
	if err != nil {
		if os.IsNotExist(err) {
			return []KBInfo{}, nil
		}
		return nil, fmt.Errorf("read knowledge dir: %w", err)
	}
	var infos []KBInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Check if this directory has INDEX.md to confirm it's a KB.
		indexPath := filepath.Join(kd, e.Name(), "INDEX.md")
		if _, err := os.Stat(indexPath); err != nil {
			continue
		}
		info := KBInfo{Name: e.Name()}
		// Read description from kb.json if it exists.
		kbJSONPath := filepath.Join(kd, e.Name(), "kb.json")
		if data, readErr := os.ReadFile(kbJSONPath); readErr == nil {
			var kbMeta struct {
				Description string `json:"description"`
			}
			if json.Unmarshal(data, &kbMeta) == nil {
				info.Description = kbMeta.Description
			}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// CreateKB creates a new empty knowledge base directory with an INDEX.md
// and a kb.json file containing the KB metadata.
func (s *Store) CreateKB(name, description string) error {
	s.logger.Infof("KB %q: creating", name)
	kbDir := filepath.Join(s.knowledgeDir(), name)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		s.logger.Errorf("KB %q: create dir failed: %v", name, err)
		return fmt.Errorf("create KB dir: %w", err)
	}
	indexPath := filepath.Join(kbDir, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte("# "+name+"\n"), 0o644); err != nil {
		s.logger.Errorf("KB %q: write INDEX.md failed: %v", name, err)
		return fmt.Errorf("create INDEX.md: %w", err)
	}
	// Write kb.json with the description.
	kbMeta := struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}{Name: name, Description: description}
	metaData, err := json.MarshalIndent(kbMeta, "", "  ")
	if err != nil {
		s.logger.Errorf("KB %q: marshal kb.json failed: %v", name, err)
		return fmt.Errorf("marshal kb.json: %w", err)
	}
	kbJSONPath := filepath.Join(kbDir, "kb.json")
	if err := os.WriteFile(kbJSONPath, metaData, 0o644); err != nil {
		s.logger.Errorf("KB %q: write kb.json failed: %v", name, err)
		return fmt.Errorf("create kb.json: %w", err)
	}
	s.logger.Infof("KB %q: created (description=%q)", name, description)
	return nil
}

// DeleteKB removes an entire knowledge base directory.
func (s *Store) DeleteKB(name string) error {
	s.logger.Infof("KB %q: deleting", name)
	kbDir := filepath.Join(s.knowledgeDir(), name)
	if err := os.RemoveAll(kbDir); err != nil {
		s.logger.Errorf("KB %q: delete failed: %v", name, err)
		return fmt.Errorf("delete KB dir: %w", err)
	}
	s.logger.Infof("KB %q: deleted", name)
	return nil
}

// knowledgeDir returns the data directory path. When dataDir is set it is used
// directly; otherwise it falls back to ~/knowledge_base/.
// The returned path is always absolute with ~ expanded (Go does not expand
// ~ natively), so the knowledge base location stays stable regardless of
// the current working directory.
func (s *Store) knowledgeDir() string {
	if s.dataDir != "" {
		dir := s.dataDir
		// Expand ~/ to the user's home directory.
		if strings.HasPrefix(dir, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				dir = filepath.Join(home, dir[2:])
			}
		}
		// Resolve to an absolute path.
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
		return dir
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, "knowledge_base")
}

// EnsureDir creates the knowledge directory tree if it doesn't exist.
func (s *Store) EnsureDir() error {
	if err := os.MkdirAll(s.knowledgeDir(), 0o755); err != nil {
		return err
	}
	s.logger.Infof("knowledge dir ready: %s", s.knowledgeDir())
	return nil
}

// IndexPath returns the path to INDEX.md.
func (s *Store) IndexPath() string {
	return filepath.Join(s.kbDir(), "INDEX.md")
}

// ReadIndex returns the raw content of INDEX.md. It returns an empty string
// if the file doesn't exist.
func (s *Store) ReadIndex() (string, error) {
	data, err := os.ReadFile(s.IndexPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read INDEX.md: %w", err)
	}
	return string(data), nil
}

// WriteIndex overwrites INDEX.md with the given content.
func (s *Store) WriteIndex(content string) error {
	if err := os.MkdirAll(s.kbDir(), 0o755); err != nil {
		return fmt.Errorf("ensure knowledge dir: %w", err)
	}
	if err := os.WriteFile(s.IndexPath(), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write INDEX.md: %w", err)
	}
	return nil
}

// DocDir returns the path for a document's directory.
func (s *Store) DocDir(slug string) string {
	return filepath.Join(s.kbDir(), slug)
}

// MetaPath returns the path to a document's meta.json.
func (s *Store) MetaPath(slug string) string {
	return filepath.Join(s.DocDir(slug), "meta.json")
}

// ChunksDir returns the path to a document's chunks/ directory.
func (s *Store) ChunksDir(slug string) string {
	return filepath.Join(s.DocDir(slug), "chunks")
}

// ChunkPath returns the path to a chunk file (e.g. "005" → ".../chunks/005.md").
func (s *Store) ChunkPath(slug, chunkID string) string {
	return filepath.Join(s.ChunksDir(slug), chunkID+".md")
}

// SectionsDir returns the path to a document's section chunks directory.
func (s *Store) SectionsDir(slug string) string {
	return filepath.Join(s.ChunksDir(slug), "sections")
}

// SectionChunkPath returns the path to a section-level chunk file (e.g. "S00" → ".../chunks/sections/S00.md").
func (s *Store) SectionChunkPath(slug, sectionID string) string {
	return filepath.Join(s.SectionsDir(slug), sectionID+".md")
}

// WriteMeta writes a DocumentMeta as JSON to the document's meta.json.
func (s *Store) WriteMeta(slug string, meta DocumentMeta) error {
	if err := os.MkdirAll(s.DocDir(slug), 0o755); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := os.WriteFile(s.MetaPath(slug), data, 0o644); err != nil {
		return fmt.Errorf("write meta.json: %w", err)
	}
	return nil
}

// ReadMeta reads and unmarshals a document's meta.json.
func (s *Store) ReadMeta(slug string) (DocumentMeta, error) {
	var meta DocumentMeta
	data, err := os.ReadFile(s.MetaPath(slug))
	if err != nil {
		if os.IsNotExist(err) {
			return meta, fmt.Errorf("document %q not found", slug)
		}
		return meta, fmt.Errorf("read meta.json for %q: %w", slug, err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, fmt.Errorf("unmarshal meta.json for %q: %w", slug, err)
	}
	return meta, nil
}

// WriteChunks creates the chunks/ directory and writes each chunk as NNN.md.
func (s *Store) WriteChunks(slug string, chunks []string) error {
	dir := s.ChunksDir(slug)
	// Start fresh: remove existing chunks dir if present.
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove old chunks: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create chunks dir: %w", err)
	}
	for i, content := range chunks {
		chunkID := fmt.Sprintf("%03d", i)
		path := s.ChunkPath(slug, chunkID)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write chunk %s: %w", chunkID, err)
		}
	}
	return nil
}

// AppendChunks writes additional chunks to an existing document's chunks/
// directory, picking up IDs where the existing chunks leave off. It does NOT
// remove existing chunks.
func (s *Store) AppendChunks(slug string, chunks []string) error {
	dir := s.ChunksDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create chunks dir: %w", err)
	}
	// Determine the starting ID from existing chunks.
	existing, err := s.ListChunks(slug)
	if err != nil {
		// Document doesn't exist yet; start from 0.
		existing = nil
	}
	startID := len(existing)
	for i, content := range chunks {
		chunkID := fmt.Sprintf("%03d", startID+i)
		path := s.ChunkPath(slug, chunkID)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write chunk %s: %w", chunkID, err)
		}
	}
	return nil
}

// AppendChunksIndex reads the existing CHUNKS.toml for a document, appends new
// index entries, and writes the result back. It creates a new index when none
// exists.
func (s *Store) AppendChunksIndex(slug string, newEntries []ChunkIndexEntry) error {
	index, err := s.ReadChunksIndex(slug)
	if err != nil {
		return fmt.Errorf("read existing chunks index: %w", err)
	}
	if index == nil {
		index = &ChunksIndex{
			Slug:       slug,
			ChunkCount: 0,
			Chunks:     nil,
		}
	}
	index.Chunks = append(index.Chunks, newEntries...)
	index.ChunkCount = len(index.Chunks)
	cs, csErr := s.computeChunksChecksum(slug)
	if csErr == nil {
		index.Checksum = cs
	}
	return s.WriteChunksIndex(slug, index)
}

// AppendDocumentText chunks new text and appends it to an existing document.
// It writes new chunk files, updates the search index, and updates meta.json.
// Returns the number of new chunks added.
func (s *Store) AppendDocumentText(slug string, newText string) (int, error) {
	// Verify the document exists.
	meta, err := s.ReadMeta(slug)
	if err != nil {
		return 0, fmt.Errorf("document %q not found: %w", slug, err)
	}

	// Chunk the new text.
	fineChunks, coarseChunks := ChunkTextHierarchical(newText)
	if len(fineChunks) == 0 {
		return 0, nil // nothing to append
	}

	// G12: Incremental boundary merge — when an embedder is configured and
	// there are existing chunks, perform semantic merging at the boundary
	// between old tail and new head chunks.
	oldModified := map[string]string{} // chunkID (e.g. "005") → new content for modified old chunks
	if meta.ChunkCount > 0 && s.embedder != nil {
		n := boundaryMergeN
		m := boundaryMergeM
		if m > len(fineChunks) {
			m = len(fineChunks)
		}

		// Read last N old chunks.
		startOld := meta.ChunkCount - n
		if startOld < 0 {
			startOld = 0
		}
		var oldTail []ChunkWithMeta
		for i := startOld; i < meta.ChunkCount; i++ {
			id := fmt.Sprintf("%03d", i)
			content, readErr := s.ReadChunk(slug, id)
			if readErr != nil {
				continue
			}
			oldTail = append(oldTail, ChunkWithMeta{Content: content})
		}

		if len(oldTail) > 0 && m > 0 {
			newHead := fineChunks[:m]
			boundary := append(oldTail, newHead...)

			merged, mergeErr := MergeSemanticNeighbors(context.Background(), boundary, s.embedder, defaultSemanticThreshold)
			if mergeErr == nil {
				// Detect changes to old chunks (content modified = absorbed new content).
				for j := 0; j < len(oldTail) && j < len(merged); j++ {
					if merged[j].Content != oldTail[j].Content {
						oldModified[fmt.Sprintf("%03d", startOld+j)] = merged[j].Content
					}
				}

				// Rewrite modified old chunk files.
				for idx, content := range oldModified {
					if writeErr := os.WriteFile(s.ChunkPath(slug, idx), []byte(content), 0o644); writeErr != nil {
						// Non-fatal: continue with best-effort merge.
						delete(oldModified, idx)
					}
				}

				// Determine which new chunks survived (appear after oldTail in merged output).
				if len(merged) > len(oldTail) {
					survivedNew := merged[len(oldTail):]
					survivedMap := make(map[string]bool, len(survivedNew))
					for _, sc := range survivedNew {
						survivedMap[sc.Content] = true
					}

					// Filter fineChunks to survivors, preserving order.
					filtered := make([]ChunkWithMeta, 0, len(survivedNew))
					for _, fc := range fineChunks {
						if survivedMap[fc.Content] {
							delete(survivedMap, fc.Content)
							filtered = append(filtered, fc)
						}
					}
					fineChunks = filtered

					// If fineChunks changed and coarse chunks exist, rebuild them.
					if len(fineChunks) > 0 && len(coarseChunks) > 0 {
						coarseChunks = rebuildCoarseFromFine(fineChunks)
					}
				} else {
					// All new chunks were absorbed — nothing to append.
					return 0, nil
				}
			}
			// If merge fails, continue with original fineChunks (non-fatal).
		}
	}

	// Step 1: write new chunk files.
	chunks := make([]string, len(fineChunks))
	for i, c := range fineChunks {
		chunks[i] = c.Content
	}
	if err := s.AppendChunks(slug, chunks); err != nil {
		return 0, fmt.Errorf("append chunks: %w", err)
	}

	// Step 2: rewrite section-level chunks.
	if len(coarseChunks) > 0 {
		_ = s.WriteSectionChunks(slug, coarseChunks)
	}

	// Step 3: build index entries — update modified old entries and append new entries.
	// Instead of AppendChunksIndex, we read-modify-write to handle old entry updates.
	index, idxErr := s.ReadChunksIndex(slug)
	if idxErr != nil || index == nil {
		index = &ChunksIndex{
			Slug:       slug,
			ChunkCount: 0,
			Chunks:     nil,
		}
	}

	// Update index entries for modified old chunks.
	if len(oldModified) > 0 {
		entryByID := make(map[string]int)
		for i, e := range index.Chunks {
			entryByID[e.ID] = i
		}
		for chunkID, content := range oldModified {
			tokens := retrieval.Tokens(content)
			tc := retrieval.Counts(tokens)
			replacement := ChunkIndexEntry{
				ID:        chunkID,
				TermCount: len(tokens),
				Terms:     trimTopTerms(tc, maxTermsPerChunk),
			}
			// Preserve original metadata (section, offset, vector, etc).
			if pos, ok := entryByID[chunkID]; ok {
				replacement.Section = index.Chunks[pos].Section
				replacement.Offset = index.Chunks[pos].Offset
				replacement.Vector = index.Chunks[pos].Vector
				replacement.SectionChunkID = index.Chunks[pos].SectionChunkID
				replacement.SectionRole = index.Chunks[pos].SectionRole
				index.Chunks[pos] = replacement
			}
		}
	}

	// Build and append index entries for surviving new chunks.
	for i, c := range fineChunks {
		id := fmt.Sprintf("%03d", meta.ChunkCount+i)
		tokens := retrieval.Tokens(c.Content)
		tc := retrieval.Counts(tokens)
		entry := ChunkIndexEntry{
			ID:          id,
			TermCount:   len(tokens),
			Terms:       trimTopTerms(tc, maxTermsPerChunk),
			Section:     c.Section,
			Offset:      meta.TotalChars + c.Offset,
			SectionRole: c.SectionRole,
		}
		if c.SectionID != "" {
			entry.SectionChunkID = c.SectionID
		}
		index.Chunks = append(index.Chunks, entry)
	}
	index.ChunkCount = len(index.Chunks)
	cs, csErr := s.computeChunksChecksum(slug)
	if csErr == nil {
		index.Checksum = cs
	}
	if err := s.WriteChunksIndex(slug, index); err != nil {
		return 0, fmt.Errorf("write chunks index: %w", err)
	}

	// Step 4: update meta.json.
	meta.ChunkCount += len(fineChunks)
	meta.TotalChars += len(newText)
	if err := s.WriteMeta(slug, meta); err != nil {
		return 0, fmt.Errorf("update meta: %w", err)
	}

	return len(fineChunks), nil
}

// rebuildCoarseFromFine rebuilds section-level (coarse) chunks from the given
// fine chunks, grouping by section heading and concatenating content.
// This is used by G12 to regenerate coarse chunks after boundary merge removes
// some fine chunks.
func rebuildCoarseFromFine(fine []ChunkWithMeta) []ChunkWithMeta {
	if len(fine) == 0 {
		return nil
	}
	// Group by section heading.
	sectionGroups := map[string][]ChunkWithMeta{}
	sectionOrder := []string{}
	for _, c := range fine {
		sec := c.Section
		if _, exists := sectionGroups[sec]; !exists {
			sectionOrder = append(sectionOrder, sec)
		}
		sectionGroups[sec] = append(sectionGroups[sec], c)
	}

	var coarse []ChunkWithMeta
	for _, sec := range sectionOrder {
		group := sectionGroups[sec]
		var b strings.Builder
		for j, c := range group {
			if j > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(c.Content)
		}
		coarse = append(coarse, ChunkWithMeta{
			Content:     b.String(),
			Section:     sec,
			Offset:      group[0].Offset,
			SectionID:   sec,
			SectionRole: classifySectionRole(sec),
		})
	}
	return coarse
}

// WriteSectionChunks writes section-level chunks into chunks/sections/.
// Each section chunk is stored as S00.md, S01.md, etc.
func (s *Store) WriteSectionChunks(slug string, sections []ChunkWithMeta) error {
	dir := s.SectionsDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sections dir: %w", err)
	}
	// Remove old section chunks.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		os.Remove(filepath.Join(dir, e.Name()))
	}
	for i, sec := range sections {
		id := fmt.Sprintf("S%02d", i)
		path := s.SectionChunkPath(slug, id)
		if err := os.WriteFile(path, []byte(sec.Content), 0o644); err != nil {
			return fmt.Errorf("write section chunk %s: %w", id, err)
		}
	}
	return nil
}

// ReadSectionChunk reads a single section-level chunk and returns its content.
func (s *Store) ReadSectionChunk(slug, sectionID string) (string, error) {
	data, err := os.ReadFile(s.SectionChunkPath(slug, sectionID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("section chunk %q not found in document %q", sectionID, slug)
		}
		return "", fmt.Errorf("read section chunk %q in %q: %w", sectionID, slug, err)
	}
	return string(data), nil
}

// ReadChunk reads a single chunk file and returns its content.
func (s *Store) ReadChunk(slug, chunkID string) (string, error) {
	data, err := os.ReadFile(s.ChunkPath(slug, chunkID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("chunk %q not found in document %q", chunkID, slug)
		}
		return "", fmt.Errorf("read chunk %q in %q: %w", chunkID, slug, err)
	}
	return string(data), nil
}

// ListChunks returns all chunk IDs for a document, sorted by name.
func (s *Store) ListChunks(slug string) ([]string, error) {
	dir := s.ChunksDir(slug)
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

// ListSectionChunks returns all section chunk IDs for a document, sorted.
func (s *Store) ListSectionChunks(slug string) ([]string, error) {
	dir := s.SectionsDir(slug)
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

// SlugFromPath derives a filesystem-safe document slug from a file path.
func SlugFromPath(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	// Replace problematic characters with hyphens.
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	// Collapse consecutive hyphens and trim.
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")
	if name == "" {
		name = "document"
	}
	// Append timestamp suffix for uniqueness.
	suffix := time.Now().Format("20060102-150405.000")
	return name + "-" + suffix
}

// ListDocuments returns metadata for all documents in the knowledge base.
func (s *Store) ListDocuments() ([]DocumentMeta, error) {
	kd := s.kbDir()
	entries, err := os.ReadDir(kd)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read knowledge dir: %w", err)
	}
	var docs []DocumentMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := s.ReadMeta(e.Name())
		if err != nil {
			continue // skip invalid entries
		}
		meta.Slug = e.Name()
		docs = append(docs, meta)
	}
	s.logger.WithModule("store").Debugf("ListDocuments: kb=%q docs=%d", s.kbName, len(docs))
	return docs, nil
}

// ListDocumentsAll returns metadata for all documents across all knowledge
// bases. When no kbName is set, it traverses every KB subdirectory and
// merges the results. Documents from the current/named KB come first,
// followed by docs from other KBs tagged with their KB name.
func (s *Store) ListDocumentsAll() ([]DocumentMeta, error) {
	kbs, err := s.ListKBs()
	if err != nil {
		return nil, err
	}
	var all []DocumentMeta
	for _, kb := range kbs {
		kbStore := s.WithKB(kb)
		docs, err := kbStore.ListDocuments()
		if err != nil {
			continue
		}
		// Tag each document with its KB name for display.
		for i := range docs {
			docs[i].Tags = append(docs[i].Tags, "kb:"+kb)
		}
		all = append(all, docs...)
	}
	return all, nil
}

// ListPreviewAll returns up to n documents across all knowledge bases for
// display, merging results from every KB. The display slice is capped at n.
func (s *Store) ListPreviewAll(n int) (display []DocumentMeta, full []DocumentMeta, err error) {
	full, err = s.ListDocumentsAll()
	if err != nil {
		return nil, nil, err
	}
	if len(full) > n {
		display = full[:n]
	} else {
		display = full
	}
	return display, full, nil
}

// SnapshotPath returns the path to the list snapshot file.
func (s *Store) SnapshotPath() string {
	return filepath.Join(s.kbDir(), "LIST_SNAPSHOT.json")
}

// ListChecksum computes a SHA256 checksum over the full list of DocumentMeta
// serialized as JSON. This is used to detect if the knowledge base has changed.
func ListChecksum(docs []DocumentMeta) string {
	h := sha256.New()
	data, _ := json.Marshal(docs)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// WriteListSnapshot saves the full document list and its checksum to the
// snapshot file. If docs is nil or empty, the file is removed.
func (s *Store) WriteListSnapshot(docs []DocumentMeta) error {
	if len(docs) == 0 {
		os.Remove(s.SnapshotPath())
		return nil
	}
	cs := ListChecksum(docs)
	snapshot := struct {
		Checksum  string         `json:"checksum"`
		UpdatedAt time.Time      `json:"updated_at"`
		Documents []DocumentMeta `json:"documents"`
	}{
		Checksum:  cs,
		UpdatedAt: time.Now(),
		Documents: docs,
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := os.WriteFile(s.SnapshotPath(), data, 0o644); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	return nil
}

// ReadListSnapshot reads the snapshot file and returns the saved checksum
// and document list. Returns empty checksum and nil docs if the file doesn't
// exist, so callers can treat "no snapshot" as "needs refresh".
func (s *Store) ReadListSnapshot() (checksum string, docs []DocumentMeta, err error) {
	data, readErr := os.ReadFile(s.SnapshotPath())
	if os.IsNotExist(readErr) {
		return "", nil, nil
	}
	if readErr != nil {
		return "", nil, fmt.Errorf("read snapshot: %w", readErr)
	}
	var snapshot struct {
		Checksum  string         `json:"checksum"`
		UpdatedAt time.Time      `json:"updated_at"`
		Documents []DocumentMeta `json:"documents"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return "", nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return snapshot.Checksum, snapshot.Documents, nil
}

// ListWithSnapshot returns the full document list. On first call or when the
// knowledge base has changed (checksum mismatch), it regenerates the snapshot
// file. On subsequent calls with no changes, it returns cached data without
// writing to disk.
func (s *Store) ListWithSnapshot() ([]DocumentMeta, error) {
	docs, err := s.ListDocuments()
	if err != nil {
		return nil, err
	}

	// If no documents, clean up and return.
	if len(docs) == 0 {
		os.Remove(s.SnapshotPath())
		return docs, nil
	}

	cs := ListChecksum(docs)
	savedCS, _, _ := s.ReadListSnapshot()

	if savedCS != cs {
		// Knowledge base changed: regenerate snapshot.
		_ = s.WriteListSnapshot(docs)
	}

	return docs, nil
}

// ListWithLimit returns up to n documents from the full list.
func (s *Store) ListWithLimit(n int) ([]DocumentMeta, error) {
	docs, err := s.ListDocuments()
	if err != nil {
		return nil, err
	}
	if len(docs) > n {
		docs = docs[:n]
	}
	return docs, nil
}

// Exists checks whether a document slug exists.
func (s *Store) Exists(slug string) bool {
	_, err := os.Stat(s.DocDir(slug))
	return err == nil
}

// ChunksIndexPath returns the path to a document's CHUNKS.toml.
func (s *Store) ChunksIndexPath(slug string) string {
	return filepath.Join(s.DocDir(slug), "CHUNKS.toml")
}

// WriteChunksIndex persists a ChunksIndex as TOML. It ensures the document
// directory exists before writing.
func (s *Store) WriteChunksIndex(slug string, index *ChunksIndex) error {
	if err := os.MkdirAll(s.DocDir(slug), 0o755); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	f, err := os.Create(s.ChunksIndexPath(slug))
	if err != nil {
		return fmt.Errorf("create CHUNKS.toml: %w", err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(index); err != nil {
		return fmt.Errorf("encode CHUNKS.toml: %w", err)
	}
	// G7: update the global inverted index. Non-fatal: a failure here doesn't
	// block search; it falls back to the full-scan path.
	if err := s.updateInvertedIndex(slug, index.Chunks); err != nil {
		// Non-fatal: inverted index update failure doesn't block search.
		// The next search will fall back to full scan.
	}
	return nil
}

// ReadChunksIndex reads and decodes a document's CHUNKS.toml. It returns nil
// and no error when the file does not exist, so callers can fall back to a
// full scan of chunk files.
//
// Backward compatibility: old-format indices (with map[string]int Terms) are
// detected and converted to the current []termFreq format on read.
// When a checksum is present (G10), chunk files are verified and the index
// is automatically rebuilt if they have drifted.
func (s *Store) ReadChunksIndex(slug string) (*ChunksIndex, error) {
	data, err := os.ReadFile(s.ChunksIndexPath(slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read CHUNKS.toml: %w", err)
	}

	// Try new format first ([]termFreq Terms).
	var index ChunksIndex
	if _, err := toml.Decode(string(data), &index); err == nil {
		// G10: verify checksum if present; on mismatch, fall back to full scan.
		if index.Checksum != "" {
			actualCS, csErr := s.computeChunksChecksum(slug)
			if csErr == nil && actualCS != index.Checksum {
				return nil, nil // checksum drift → fall back to full scan
			}
		}

		return &index, nil
	}

	// Fall back to old format (map[string]int Terms).
	type ChunkIndexEntryV1 struct {
		ID             string         `toml:"id"`
		TermCount      int            `toml:"term_count"`
		Terms          map[string]int `toml:"terms"`
		Section        string         `toml:"section"`
		Offset         int            `toml:"offset"`
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

	// Convert V1 (map[string]int) to current format ([]termFreq).
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
		sort.Slice(terms, func(i, j int) bool {
			return terms[i].Count > terms[j].Count
		})
		index.Chunks[i] = ChunkIndexEntry{
			ID:             c.ID,
			TermCount:      c.TermCount,
			Terms:          terms,
			Section:        c.Section,
			Offset:         c.Offset,
			Vector:         c.Vector,
			SectionChunkID: c.SectionChunkID,
			SectionRole:    c.SectionRole,
		}
	}

	return &index, nil
}

// writeChunksIndexFromMeta builds and persists a ChunksIndex from chunk
// metadata, including pre-computed term frequencies, position info,
// and optionally dense vectors when an embedder is configured.
// It delegates to writeChunksIndexFromMetaWithSections without section data.
func (s *Store) writeChunksIndexFromMeta(slug string, chunks []ChunkWithMeta) error {
	return s.writeChunksIndexFromMetaWithSections(slug, chunks, nil)
}

// writeChunksIndexFromMetaWithSections builds and persists a ChunksIndex from chunk
// metadata, including pre-computed term frequencies, position info,
// and optionally dense vectors when an embedder is configured.
// When sectionChunks is provided, each entry's SectionChunkID is populated
// from the chunk's SectionID field.
func (s *Store) writeChunksIndexFromMetaWithSections(slug string, chunks []ChunkWithMeta, sectionChunks []ChunkWithMeta) error {
	index := &ChunksIndex{
		Slug:       slug,
		ChunkCount: len(chunks),
		Chunks:     make([]ChunkIndexEntry, len(chunks)),
	}

	hasEmbedder := s.embedder != nil
	s.logger.Debugf("embed: slug=%q hasEmbedder=%v", slug, hasEmbedder)
	if hasEmbedder {
		s.logger.Debugf("embed: embedder=%T", s.embedder)
		index.HasVectors = true
	}

	// Generate vectors in batch if embedder is available.
	var vectors [][]float32
	if hasEmbedder {
		contents := make([]string, len(chunks))
		for i, c := range chunks {
			contents[i] = c.Content
		}
		var err error
		vectors, err = s.embedder.Embed(context.Background(), contents)
		if err != nil {
			// Non-fatal: continue without vectors.
			s.logger.Warnf("embed: embedding failed for %q: %v", slug, err)
			hasEmbedder = false
			index.VectorDim = 0
			index.HasVectors = false
		} else {
			// Embed succeeded: set dimension from the embedder (which may have
			// auto-detected it from the API response), then validate vectors.
			index.VectorDim = s.embedder.Dim()
			if index.VectorDim <= 0 {
				s.logger.Warnf("embed: embedding returned zero-dimension vectors for %q — disabling vectors", slug)
				hasEmbedder = false
				index.VectorDim = 0
				index.HasVectors = false
			}
		}
	}

	for i, c := range chunks {
		id := fmt.Sprintf("%03d", i)
		tokens := retrieval.Tokens(c.Content)
		tc := retrieval.Counts(tokens)
		entry := ChunkIndexEntry{
			ID:          id,
			TermCount:   len(tokens),
			Terms:       trimTopTerms(tc, maxTermsPerChunk),
			Section:     c.Section,
			Offset:      c.Offset,
			SectionRole: c.SectionRole,
		}
		// Link to parent section chunk when hierarchical data is available.
		if c.SectionID != "" && sectionChunks != nil {
			entry.SectionChunkID = c.SectionID
		}
		if hasEmbedder && i < len(vectors) && vectors[i] != nil {
			vec64 := make([]float64, len(vectors[i]))
			for j, v := range vectors[i] {
				vec64[j] = float64(v)
			}
			entry.Vector = vec64
		}
		index.Chunks[i] = entry
	}
	cs, csErr := s.computeChunksChecksum(slug)
	if csErr == nil {
		index.Checksum = cs
	}
	if err := s.WriteChunksIndex(slug, index); err != nil {
		return fmt.Errorf("write CHUNKS.toml: %w", err)
	}
	return nil
}

// ReadChunkContext reads a chunk identified by docSlug and chunkID, optionally
// including up to context adjacent chunks before and after. When context is 0
// it behaves like ReadChunk.
//
// If the document has a CHUNKS.toml with section metadata, adjacent chunks
// under the same section are merged into continuous text with section headers
// (## Section). Otherwise the result is formatted with chunk ID markers as a
// fallback.
func (s *Store) ReadChunkContext(slug, chunkID string, context int) (string, error) {
	if context <= 0 {
		return s.ReadChunk(slug, chunkID)
	}

	// Parse chunk ID to integer.
	id := chunkIDToInt(chunkID)

	// Collect all chunk IDs.
	allIDs, err := s.ListChunks(slug)
	if err != nil {
		return "", err
	}

	// Determine the window.
	start := id - context
	if start < 0 {
		start = 0
	}
	end := id + context + 1 // +1 to include the target
	maxID := len(allIDs)
	if end > maxID {
		end = maxID
	}

	// Try to load section metadata from CHUNKS.toml for richer output.
	sectionByID := map[string]string{}
	hasSections := false
	if index, err := s.ReadChunksIndex(slug); err == nil && index != nil {
		for _, entry := range index.Chunks {
			sectionByID[entry.ID] = entry.Section
			if entry.Section != "" {
				hasSections = true
			}
		}
	}

	var b strings.Builder
	if hasSections {
		// Rich output: merge adjacent chunks under the same section header.
		var lastSection string
		for i := start; i < end; i++ {
			cid := fmt.Sprintf("%03d", i)
			text, err := s.ReadChunk(slug, cid)
			if err != nil {
				continue
			}
			section := sectionByID[cid]
			if section != lastSection {
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				if section != "" {
					b.WriteString("## " + section + "\n")
				}
				lastSection = section
			} else if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(text)
		}
	} else {
		// Fallback: chunk ID markers for documents without section metadata.
		for i := start; i < end; i++ {
			cid := fmt.Sprintf("%03d", i)
			text, err := s.ReadChunk(slug, cid)
			if err != nil {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString(fmt.Sprintf("[%s]\n%s", cid, text))
		}
	}

	if b.Len() == 0 {
		return "", fmt.Errorf("chunk %q not found in document %q", chunkID, slug)
	}
	return b.String(), nil
}

// chunkIDToInt parses a zero-padded chunk ID like "005" to its integer value.
func chunkIDToInt(chunkID string) int {
	id := 0
	for _, r := range chunkID {
		if r >= '0' && r <= '9' {
			id = id*10 + int(r-'0')
		}
	}
	return id
}

// trimTopTerms keeps only the top n terms with the highest counts, reducing
// the size of the CHUNKS.toml index. Returns a []termFreq slice sorted by
// count descending. When counts is nil, returns nil.
func trimTopTerms(counts map[string]int, n int) []termFreq {
	if counts == nil {
		return nil
	}
	if len(counts) == 0 {
		return []termFreq{}
	}
	type kv struct {
		k string
		v int
	}
	sorted := make([]kv, 0, len(counts))
	for k, v := range counts {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].v > sorted[j].v
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	out := make([]termFreq, 0, n)
	for _, p := range sorted[:n] {
		out = append(out, termFreq{Term: p.k, Count: p.v})
	}
	return out
}

// computeChunksChecksum computes a SHA256 checksum over all chunk files for a
// document, sorted by chunk ID. Returns the hex-encoded hash.
func (s *Store) computeChunksChecksum(slug string) (string, error) {
	ids, err := s.ListChunks(slug)
	if err != nil {
		return "", fmt.Errorf("list chunks: %w", err)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		data, readErr := os.ReadFile(s.ChunkPath(slug, id))
		if readErr != nil {
			return "", fmt.Errorf("read chunk %s: %w", id, readErr)
		}
		io.WriteString(h, id)
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// computeChunksChecksum computes a SHA256 checksum over all chunk files for a
// document, sorted by chunk ID. Returns the hex-encoded hash.
