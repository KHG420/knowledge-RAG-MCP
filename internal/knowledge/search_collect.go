// Deprecated: collectEntries/queryCandidates are legacy fallbacks (searchEngine==nil).
// Production paths use internal/knowledge/search/collect.go.
package knowledge

import (
	"fmt"
	"strings"
	"time"
	"knowledge-mcp/internal/knowledge/search/retrieval"
)

// collectEntries gathers all search entries from the knowledge base,
// applying the given filter. It reads from the pre-computed CHUNKS.toml
// index when available and falls back to scanning chunk files otherwise.
// When queryTerms is non-empty and the global inverted index is available,
// it uses an accelerated path that only reads CHUNKS.toml for candidate
// documents (G7).
func (s *Store) collectEntries(filter SearchFilter, queryTerms []string) ([]searchEntry, error) {
	log := s.logger.WithModule("search")
	start := time.Now()
	nTerms := len(queryTerms)
	pathLabel := "fullscan"
	if nTerms > 0 {
		pathLabel = "inverted"
	}
	defer func() {
		log.Debugf("collectEntries done: terms=%d path=%s elapsed=%v", nTerms, pathLabel, time.Since(start))
	}()

	// G7: try inverted-index fast path when query terms are available.
	if len(queryTerms) > 0 {
		candidates, candErr := s.queryCandidates(queryTerms)
		if candErr == nil && candidates != nil {
			if len(candidates) == 0 {
				return nil, nil // no matches at all
			}
			return s.collectEntriesFromCandidates(candidates, filter)
		}
	}

	// Fallback: full scan using backend.
	docDirs, err := s.backend.ListDocSlugs(s.kbName)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}

	var entries []searchEntry

	for _, slug := range docDirs {
		// Filter by doc slug.
		if filter.DocSlug != "" && slug != filter.DocSlug {
			continue
		}

		// Read meta for source-type filtering and paper detection (G13).
		meta, metaErr := s.ReadMeta(slug)
		if metaErr != nil {
			log.Debugf("collectEntries: skipping %q: %v", slug, metaErr)
			continue
		}
		// Skip tombstoned documents (soft-deleted).
		if s.IsTombstoned(slug) {
			continue
		}
		if filter.SourceType != "" && meta.SourceType != filter.SourceType {
			continue
		}
		if !matchesMetaTags(meta.Tags, filter.Tags) {
			continue
		}
		if !filter.AddedAfter.IsZero() && meta.AddedAt.Before(filter.AddedAfter) {
			continue
		}
		if !filter.AddedBefore.IsZero() && meta.AddedAt.After(filter.AddedBefore) {
			continue
		}

		index, idxErr := s.ReadChunksIndex(slug)
		if idxErr != nil {
			// Corrupt index — skip this document.
			continue
		}
		if index != nil {
			// Index path: use pre-computed term frequencies.
			for _, e := range index.Chunks {
				// Filter by section.
				if filter.Section != "" && !strings.Contains(e.Section, filter.Section) {
					continue
				}
				entries = append(entries, searchEntry{
					docSlug:        slug,
					chunkID:        e.ID,
					terms:          TermFreqsToMap(e.Terms),
					termLen:        e.TermCount,
					section:        e.Section,
					offset:         e.Offset,
					sourceType:     meta.SourceType,
					title:          meta.Title,
					originalName:   meta.OriginalName,
					pageStart:      e.PageStart,
					pageEnd:        e.PageEnd,
					vector:         e.Vector,
					sectionRole:    e.SectionRole,
					sectionChunkID: e.SectionChunkID,
					isPaper:        meta.IsPaper,
				})
			}
		} else {
			// Fallback: read and tokenise each chunk file.
			ids, listErr := s.ListChunks(slug)
			if listErr != nil {
				continue
			}
			for _, id := range ids {
				text, readErr := s.ReadChunk(slug, id)
				if readErr != nil {
					continue
				}
				tokens := retrieval.Tokens(text)
				entries = append(entries, searchEntry{
					docSlug:      slug,
					chunkID:      id,
					text:         text,
					terms:        retrieval.Counts(tokens),
					termLen:      len(tokens),
					sourceType:   meta.SourceType,
					title:        meta.Title,
					originalName: meta.OriginalName,
					isPaper:      meta.IsPaper,
				})
			}
		}
	}

	return entries, nil
}

// collectEntriesFromCandidates reads only the CHUNKS.toml files for documents
// that have at least one query-term match, filters to matching chunks, and
// returns searchEntry values (G7 fast path).
func (s *Store) collectEntriesFromCandidates(candidates map[string]map[string]bool, filter SearchFilter) ([]searchEntry, error) {
	var entries []searchEntry
	for slug, chunkSet := range candidates {
		if filter.DocSlug != "" && slug != filter.DocSlug {
			continue
		}
		meta, metaErr := s.ReadMeta(slug)
		if metaErr != nil {
			continue
		}
		// Skip tombstoned documents (soft-deleted).
		if s.IsTombstoned(slug) {
			continue
		}
		if filter.SourceType != "" && meta.SourceType != filter.SourceType {
			continue
		}
		if !matchesMetaTags(meta.Tags, filter.Tags) {
			continue
		}
		if !filter.AddedAfter.IsZero() && meta.AddedAt.Before(filter.AddedAfter) {
			continue
		}
		if !filter.AddedBefore.IsZero() && meta.AddedAt.After(filter.AddedBefore) {
			continue
		}
		index, idxErr := s.ReadChunksIndex(slug)
		if idxErr != nil || index == nil {
			s.logger.WithModule("search").Debugf("collectEntriesFromCandidates: skipping doc %q (index missing or corrupt)", slug)
			continue
		}
		for _, e := range index.Chunks {
			if !chunkSet[e.ID] {
				continue // not in candidate set
			}
			if filter.Section != "" && !strings.Contains(e.Section, filter.Section) {
				continue
			}
			entries = append(entries, searchEntry{
				docSlug:        slug,
				chunkID:        e.ID,
				terms:          TermFreqsToMap(e.Terms),
				termLen:        e.TermCount,
				section:        e.Section,
				offset:         e.Offset,
				sourceType:     meta.SourceType,
				title:          meta.Title,
				originalName:   meta.OriginalName,
				pageStart:      e.PageStart,
				pageEnd:        e.PageEnd,
				vector:         e.Vector,
				sectionRole:    e.SectionRole,
				sectionChunkID: e.SectionChunkID,
				isPaper:        meta.IsPaper,
			})
		}
	}
	return entries, nil
}
