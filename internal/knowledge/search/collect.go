package search

import (
	"fmt"
	"strings"
	"time"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/search/retrieval"
)

// ── Entry collection ────────────────────────────────────────────────────────

// collectEntries gathers all search entries from the knowledge base, applying
// the given filter. Uses the inverted-index fast path when query terms are
// available, falling back to a full scan.
func (e *Engine) collectEntries(filter knowledge.SearchFilter, queryTerms []string) ([]searchEntry, error) {
	log := e.logger.WithModule("search")
	start := time.Now()
	nTerms := len(queryTerms)
	pathLabel := "fullscan"
	if nTerms > 0 {
		pathLabel = "inverted"
	}
	defer func() {
		log.Debugf("collectEntries done: terms=%d path=%s elapsed=%v", nTerms, pathLabel, time.Since(start))
	}()

	// G7: try inverted-index fast path.
	if len(queryTerms) > 0 && e.backend != nil {
		candidates, candErr := e.queryCandidates(queryTerms)
		if candErr == nil && candidates != nil {
			if len(candidates) == 0 {
				return nil, nil
			}
			return e.collectEntriesFromCandidates(candidates, filter)
		}
	}

	// Fallback: full scan via backend.
	if e.backend == nil {
		return nil, fmt.Errorf("collectEntries: no backend configured")
	}
	docDirs, err := e.backend.ListDocSlugs(e.kbName)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}

	var entries []searchEntry
	for _, slug := range docDirs {
		if filter.DocSlug != "" && slug != filter.DocSlug {
			continue
		}

		meta, metaErr := e.chunkStore.ReadMeta(slug)
		if metaErr != nil {
			log.Debugf("collectEntries: skipping %q: %v", slug, metaErr)
			continue
		}
		if e.chunkStore.IsTombstoned(slug) {
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

		index, idxErr := e.chunkStore.ReadChunksIndex(slug)
		if idxErr != nil {
			continue
		}
		if index != nil {
			for _, ce := range index.Chunks {
				if filter.Section != "" && !strings.Contains(ce.Section, filter.Section) {
					continue
				}
				entries = append(entries, searchEntry{
					docSlug:        slug,
					chunkID:        ce.ID,
					terms:          knowledge.TermFreqsToMap(ce.Terms),
					termLen:        ce.TermCount,
					section:        ce.Section,
					offset:         ce.Offset,
					sourceType:     meta.SourceType,
					title:          meta.Title,
					originalName:   meta.OriginalName,
					pageStart:      ce.PageStart,
					pageEnd:        ce.PageEnd,
					vector:         ce.Vector,
					sectionRole:    ce.SectionRole,
					sectionChunkID: ce.SectionChunkID,
					isPaper:        meta.IsPaper,
				})
			}
		} else {
			ids, listErr := e.chunkStore.ListChunks(slug)
			if listErr != nil {
				continue
			}
			for _, id := range ids {
				text, readErr := e.chunkStore.ReadChunk(slug, id)
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
// with at least one query-term match (G7 fast path).
func (e *Engine) collectEntriesFromCandidates(candidates map[string]map[string]bool, filter knowledge.SearchFilter) ([]searchEntry, error) {
	var entries []searchEntry
	for slug, chunkSet := range candidates {
		if filter.DocSlug != "" && slug != filter.DocSlug {
			continue
		}
		meta, metaErr := e.chunkStore.ReadMeta(slug)
		if metaErr != nil {
			continue
		}
		if e.chunkStore.IsTombstoned(slug) {
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
		index, idxErr := e.chunkStore.ReadChunksIndex(slug)
		if idxErr != nil || index == nil {
			e.logger.WithModule("search").Debugf("collectEntriesFromCandidates: skipping doc %q (index missing or corrupt)", slug)
			continue
		}
		for _, ce := range index.Chunks {
			if !chunkSet[ce.ID] {
				continue
			}
			if filter.Section != "" && !strings.Contains(ce.Section, filter.Section) {
				continue
			}
			entries = append(entries, searchEntry{
				docSlug:        slug,
				chunkID:        ce.ID,
				terms:          knowledge.TermFreqsToMap(ce.Terms),
				termLen:        ce.TermCount,
				section:        ce.Section,
				offset:         ce.Offset,
				sourceType:     meta.SourceType,
				title:          meta.Title,
				originalName:   meta.OriginalName,
				pageStart:      ce.PageStart,
				pageEnd:        ce.PageEnd,
				vector:         ce.Vector,
				sectionRole:    ce.SectionRole,
				sectionChunkID: ce.SectionChunkID,
				isPaper:        meta.IsPaper,
			})
		}
	}
	return entries, nil
}

// ── Inverted index ──────────────────────────────────────────────────────────

func (e *Engine) loadInvertedIndex() (*knowledge.InvertedIndex, error) {
	if e.backend == nil {
		return nil, nil
	}
	return e.backend.ReadInvertedIndex(e.kbName)
}

func (e *Engine) queryCandidates(queryTerms []string) (map[string]map[string]bool, error) {
	if e.backend == nil {
		return nil, nil
	}
	idx, err := e.loadInvertedIndex()
	if err != nil || idx == nil {
		return nil, nil
	}
	candidates := make(map[string]map[string]bool)
	for _, term := range queryTerms {
		for _, p := range idx.Index[term] {
			if candidates[p.DocSlug] == nil {
				candidates[p.DocSlug] = make(map[string]bool)
			}
			candidates[p.DocSlug][p.ChunkID] = true
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	e.logger.WithModule("store").Debugf("queryCandidates: queryTerms=%d candidates=%d", len(queryTerms), len(candidates))
	return candidates, nil
}
