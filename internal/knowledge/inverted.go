package knowledge

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
)

// Posting is a single entry in the inverted index: a chunk occurrence of a term.
type Posting struct {
	DocSlug string
	ChunkID string
	TF      int // term frequency in that chunk
}

// InvertedIndex maps each term to the list of chunks where it appears.
// It is a global index serialized as INVERTED.gob at the knowledge root.
type InvertedIndex struct {
	Index map[string][]Posting // term → posting list
}

// NewInvertedIndex returns an empty inverted index.
func NewInvertedIndex() *InvertedIndex {
	return &InvertedIndex{Index: make(map[string][]Posting)}
}

func (s *Store) invertedIndexPath() string {
	return filepath.Join(s.kbDir(), "INVERTED.gob")
}

// loadInvertedIndex reads the gob-encoded inverted index from disk.
// Returns nil when the file does not exist.
func (s *Store) loadInvertedIndex() (*InvertedIndex, error) {
	f, err := os.Open(s.invertedIndexPath())
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

// saveInvertedIndex persists the inverted index as gob.
func (s *Store) saveInvertedIndex(idx *InvertedIndex) error {
	if err := os.MkdirAll(s.knowledgeDir(), 0o755); err != nil {
		return fmt.Errorf("ensure knowledge dir: %w", err)
	}
	f, err := os.Create(s.invertedIndexPath())
	if err != nil {
		return fmt.Errorf("create INVERTED.gob: %w", err)
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(idx); err != nil {
		return fmt.Errorf("encode INVERTED.gob: %w", err)
	}
	return nil
}

// updateInvertedIndex adds postings for a document's chunks and removes any
// stale entries for that document. Called after writing CHUNKS.toml.
func (s *Store) updateInvertedIndex(slug string, entries []ChunkIndexEntry) error {
	idx, err := s.loadInvertedIndex()
	if err != nil {
		// Corrupt index — rebuild from scratch.
		idx = NewInvertedIndex()
	}
	if idx == nil {
		idx = NewInvertedIndex()
	}

	// Remove all existing postings for this document.
	for term, postings := range idx.Index {
		filtered := postings[:0]
		for _, p := range postings {
			if p.DocSlug != slug {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			delete(idx.Index, term)
		} else {
			idx.Index[term] = filtered
		}
	}

	// Add new postings from the index entries.
	for _, e := range entries {
		for _, tf := range e.Terms {
			idx.Index[tf.Term] = append(idx.Index[tf.Term], Posting{
				DocSlug: slug,
				ChunkID: e.ID,
				TF:      tf.Count,
			})
		}
	}

	return s.saveInvertedIndex(idx)
}

// rebuildInvertedIndex scans all CHUNKS.toml files and rebuilds the global
// inverted index from scratch.
func (s *Store) rebuildInvertedIndex() error {
	kd := s.knowledgeDir()
	docDirs, err := listDocDirs(kd)
	if err != nil {
		return fmt.Errorf("list documents: %w", err)
	}
	idx := NewInvertedIndex()
	for _, slug := range docDirs {
		index, idxErr := s.ReadChunksIndex(slug)
		if idxErr != nil || index == nil {
			continue
		}
		for _, e := range index.Chunks {
			for _, tf := range e.Terms {
				idx.Index[tf.Term] = append(idx.Index[tf.Term], Posting{
					DocSlug: slug,
					ChunkID: e.ID,
					TF:      tf.Count,
				})
			}
		}
	}
	return s.saveInvertedIndex(idx)
}

// queryCandidates returns the set of (DocSlug, ChunkID) pairs that match any
// of the query terms via the inverted index. When the inverted index is not
// available, returns nil (caller should fall back to full scan).
func (s *Store) queryCandidates(queryTerms []string) (map[string]map[string]bool, error) {
	idx, err := s.loadInvertedIndex()
	if err != nil || idx == nil {
		return nil, nil // inverted index not available
	}
	// Union of posting lists across all query terms.
	candidates := make(map[string]map[string]bool) // DocSlug → set of ChunkID
	for _, term := range queryTerms {
		for _, p := range idx.Index[term] {
			if candidates[p.DocSlug] == nil {
				candidates[p.DocSlug] = make(map[string]bool)
			}
			candidates[p.DocSlug][p.ChunkID] = true
		}
	}
	if len(candidates) == 0 {
		return nil, nil // no matches — empty result, not a fallback
	}
	return candidates, nil
}
