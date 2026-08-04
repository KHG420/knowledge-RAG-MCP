// Deprecated: SearchVector/SearchDocuments are legacy fallbacks (searchEngine==nil).
// Production paths use internal/knowledge/search/Engine.
package knowledge

import (
	"context"
	"fmt"
	"sort"
	"time"
	"knowledge-mcp/internal/knowledge/search/retrieval"
)

func (s *Store) SearchVector(query string, limit int) ([]SearchHit, error) {
	// Delegate to search engine when available.
	if s.searchEngine != nil {
		return s.searchEngine.SearchVector(context.Background(), query, limit, SearchFilter{})
	}

	// Legacy path.
	log := s.logger.WithModule("search")
	log.Debugf("SearchVector: query=%q limit=%d kb=%q embedder=%v", query, limit, s.kbName, s.embedder != nil)

	if limit <= 0 {
		limit = 8
	}

	if s.embedder == nil {
		return nil, fmt.Errorf("vector search unavailable: no embedding model configured (set embedder in config)")
	}
	s.EnsureVectorIndex()
	if s.vectorIndex == nil || s.vectorIndex.Len() == 0 {
		return nil, fmt.Errorf("vector search unavailable: vector index is empty (upload documents with an embedder configured to populate it)")
	}

	// Coordinate GPU: prepare for embedding.
	if s.gpuScheduler != nil {
		restoreVec := s.gpuScheduler.PrepareForEmbedding()
		defer restoreVec()
	}

	queryVec, embedErr := s.embedder.Embed(context.Background(), []string{s.vectorQuery(query)})
	if embedErr != nil || len(queryVec) == 0 || len(queryVec[0]) == 0 {
		return nil, fmt.Errorf("search vector: embed failed: %w", embedErr)
	}
	qVec64 := make([]float64, len(queryVec[0]))
	for j, v := range queryVec[0] {
		qVec64[j] = float64(v)
	}

	// Wide beam for good recall.
	vecK := limit * 20
	if vecK < 300 {
		vecK = 300
	}
	if vecK > s.vectorIndex.Len() {
		vecK = s.vectorIndex.Len()
	}

	hits := s.vectorIndex.Search(qVec64, vecK)
	log.Debugf("[search] vector: ANN returned %d hits (beam=%d)", len(hits), vecK)

	// Build SearchHit results from vector hits.
	results := make([]SearchHit, 0, len(hits))
	seen := make(map[string]bool, len(hits))
	for _, h := range hits {
		if h.ID == "" || h.Score <= 0 {
			continue
		}
		// Deduplicate by slug/chunkID.
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true

		slug, chunkID, ok := ParseVectorID(h.ID)
		if !ok {
			continue
		}

		// Skip tombstoned documents (soft-deleted).
		if s.IsTombstoned(slug) {
			continue
		}

		text, readErr := s.ReadChunk(slug, chunkID)
		if readErr != nil {
			text = ""
		}

		// Re-tokenise for snippet generation.
		queryTerms, _ := retrieval.QueryTerms(query)

		results = append(results, SearchHit{
			Score: h.Score,
			Document: DocumentInfo{
				ID: slug,
			},
			Location: LocationInfo{
				ChunkID: chunkID,
			},
			Content: HitContent{
				Snippet: retrieval.MakeSnippet(text, query, queryTerms, 200),
			},
			CitationID: fmt.Sprintf("%s_%s", slug, chunkID),
		})

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// SearchDocuments performs document-level retrieval using MaxP aggregation.
// It first searches chunks (3×limit to get enough coverage), groups results
// by DocSlug, takes the highest chunk score per document (MaxP), and returns
// documents sorted by that score. Each document includes its top-3 chunks.
//
// Internally uses HybridSearch when an embedder is configured, or Search
// (pure BM25) otherwise.
// An optional SearchFilter can be passed to narrow results.
func (s *Store) SearchDocuments(query string, limit int, filters ...SearchFilter) ([]DocumentHit, error) {
	// Resolve filter.
	var filter SearchFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

	// Delegate to search engine when available.
	if s.searchEngine != nil {
		return s.searchEngine.SearchDocuments(context.Background(), query, limit, filter)
	}

	// Legacy path.
	log := s.logger.WithModule("search")
	log.Debugf("SearchDocuments: query=%q limit=%d kb=%q embedder=%v", query, limit, s.kbName, s.embedder != nil)
	start := time.Now()

	if limit <= 0 {
		limit = 8
	}

	// Fetch 3×limit chunks for broader coverage across documents.
	chunkLimit := limit * 3
	var hits []SearchHit
	var err error
	if s.embedder != nil {
		hits, err = s.HybridSearch(query, chunkLimit, filters...)
	} else {
		hits, err = s.Search(query, chunkLimit, filters...)
	}
	if err != nil {
		return nil, fmt.Errorf("search documents: %w", err)
	}
	if len(hits) == 0 {
		log.Debugf("SearchDocuments done: query=%q hits=0 elapsed=%v", query, time.Since(start))
		return nil, nil
	}

	// Group by DocSlug, track max score and top-3 chunks.
	type docGroup struct {
		maxScore float64
		chunks   []SearchHit
	}
	groups := map[string]*docGroup{}
	for _, h := range hits {
		g, ok := groups[h.Document.ID]
		if !ok {
			g = &docGroup{maxScore: h.Score}
			groups[h.Document.ID] = g
		}
		if h.Score > g.maxScore {
			g.maxScore = h.Score
		}
		g.chunks = append(g.chunks, h)
	}

	// Build result sorted by MaxP descending.
	docs := make([]DocumentHit, 0, len(groups))
	for slug, g := range groups {
		meta, metaErr := s.ReadMeta(slug)
		if metaErr != nil {
			continue
		}
		// Keep top-3 chunks per doc, sorted by score.
		sort.Slice(g.chunks, func(i, j int) bool {
			return g.chunks[i].Score > g.chunks[j].Score
		})
		topN := 3
		if len(g.chunks) < topN {
			topN = len(g.chunks)
		}
		chunks := make([]SearchHit, topN)
		copy(chunks, g.chunks[:topN])
		docs = append(docs, DocumentHit{
			Score:     g.maxScore,
			DocSlug:   slug,
			DocMeta:   meta,
			TopChunks: chunks,
		})
	}
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Score > docs[j].Score
	})
	if len(docs) > limit {
		docs = docs[:limit]
	}
	log.Debugf("SearchDocuments done: query=%q docs=%d elapsed=%v", query, len(docs), time.Since(start))
	return docs, nil
}

// G14: coarseToFineFilter implements two-phase coarse-to-fine search.
// Phase 1: score section-level chunks with BM25 and select top-3 sections.
// Phase 2: filter fine-grained entries to only those whose sectionChunkID
// matches one of the top section IDs. Entries without sectionChunkID are
// always kept as candidates.
//
// Query expansion (rewriting) is NOT applied here since it was already
// applied by the caller before calling this method.
