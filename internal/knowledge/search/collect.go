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

	// G7: try inverted-index fast path. A failed or absent inverted index is not
	// proof that the KB is empty: fall through to the full scan, whose own
	// read errors are propagated below. This keeps the optimization a pure
	// optimization — correctness never depends on the index being readable.
	if len(queryTerms) > 0 && e.backend != nil {
		candidates, candErr := e.queryCandidates(queryTerms)
		if candErr != nil {
			log.Debugf("collectEntries: inverted index unavailable (%v); falling back to full scan", candErr)
		} else if candidates != nil {
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
			return nil, fmt.Errorf("read meta %q: %w", slug, metaErr)
		}
		if meta == nil {
			return nil, fmt.Errorf("read meta %q: empty metadata", slug)
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
			return nil, fmt.Errorf("read chunks index %q: %w", slug, idxErr)
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
			// Legitimate fallback: a document with no stored chunk index is
			// still searchable as long as its raw chunks are readable.
			ids, listErr := e.chunkStore.ListChunks(slug)
			if listErr != nil {
				return nil, fmt.Errorf("list chunks %q: %w", slug, listErr)
			}
			for _, id := range ids {
				text, readErr := e.chunkStore.ReadChunk(slug, id)
				if readErr != nil {
					return nil, fmt.Errorf("read chunk %q/%q: %w", slug, id, readErr)
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
			return nil, fmt.Errorf("read meta %q: %w", slug, metaErr)
		}
		if meta == nil {
			return nil, fmt.Errorf("read meta %q: empty metadata", slug)
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
			return nil, fmt.Errorf("read chunks index %q: %w", slug, idxErr)
		}
		if index == nil {
			// The document is present in the inverted index, so its chunk
			// index is expected to exist. Treating it as "no match" would
			// silently under-report a real storage fault.
			return nil, fmt.Errorf("read chunks index %q: index missing for indexed candidate", slug)
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
	if err != nil {
		return nil, fmt.Errorf("load inverted index: %w", err)
	}
	if idx == nil {
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
