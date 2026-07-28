package knowledge

import (
	"time"
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

func (s *Store) loadInvertedIndex() (*InvertedIndex, error) {
	return s.backend.ReadInvertedIndex(s.kbName)
}

// saveInvertedIndex persists the inverted index via the backend.
func (s *Store) saveInvertedIndex(idx *InvertedIndex) error {
	return s.backend.WriteInvertedIndex(s.kbName, idx)
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
	start := time.Now()
	slugs, err := s.backend.ListDocSlugs(s.kbName)
	if err != nil {
		return err
	}
	idx := NewInvertedIndex()
	for _, slug := range slugs {
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
	s.logger.WithModule("store").Debugf("rebuildInvertedIndex: docs=%d terms=%d elapsed=%v", len(slugs), len(idx.Index), time.Since(start))
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
	s.logger.WithModule("store").Debugf("queryCandidates: queryTerms=%d candidates=%d", len(queryTerms), len(candidates))
	return candidates, nil
}
