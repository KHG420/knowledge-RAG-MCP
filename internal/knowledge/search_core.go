// Deprecated: The search methods in this file are legacy fallbacks used only
// when searchEngine is nil (tests). Production paths use internal/knowledge/search/Engine.
package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"knowledge-mcp/internal/cache"
	"knowledge-mcp/internal/knowledge/search/retrieval"
)

func (s *Store) Search(query string, limit int, filters ...SearchFilter) ([]SearchHit, error) {
	// Resolve filter.
	var filter SearchFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

	// Delegate to search engine when available (REFACTOR_PLAN Phase 3.2).
	if s.searchEngine != nil {
		return s.searchEngine.Search(context.Background(), query, limit, filter)
	}

	// Legacy path — retained for unit tests that construct Store without engine.
	// Check query cache.
	if s.cacheEnabled() && limit > 0 {
		qhash := cache.QueryHash(query, "bm25", limit, filter.SourceType, filter.Section)
		ckey := cache.QueryKey(s.kbName, qhash)
		if raw, cerr := s.cacheClient.Get(context.Background(), ckey); cerr == nil && raw != nil {
			var hits []SearchHit
			if json.Unmarshal(raw, &hits) == nil {
				s.logger.WithModule("cache").Debugf("query HIT: mode=bm25 query=%q hash=%s hits=%d", query, qhash, len(hits))
				return hits, nil
			}
		}
		s.logger.WithModule("cache").Debugf("query MISS: mode=bm25 query=%q hash=%s", query, qhash)
	}

	hits, err := s.searchImpl(query, limit, filter)
	if err != nil {
		return hits, err
	}

	// Store in query cache.
	if s.cacheEnabled() && limit > 0 && len(hits) > 0 {
		qhash := cache.QueryHash(query, "bm25", limit, filter.SourceType, filter.Section)
		ckey := cache.QueryKey(s.kbName, qhash)
		if raw, jerr := json.Marshal(hits); jerr == nil {
			if setErr := s.cacheClient.Set(context.Background(), ckey, raw, s.queryCacheTTL); setErr != nil {
				s.logger.WithModule("cache").Warnf("query SET failed: mode=bm25 query=%q err=%v", query, setErr)
			} else {
				s.logger.WithModule("cache").Debugf("query SET: mode=bm25 query=%q hash=%s hits=%d ttl=%v", query, qhash, len(hits), s.queryCacheTTL)
			}
		}
	}

	return hits, nil
}

// searchImpl contains the actual BM25 scoring pipeline, separated from the
// cache-aware Search wrapper so the cache doesn't interfere with internal calls.
func (s *Store) searchImpl(query string, limit int, filter SearchFilter) ([]SearchHit, error) {
	start := time.Now()
	log := s.logger.WithModule("search")
	log.Debugf("searchImpl: query=%q limit=%d kb=%q", query, limit, s.kbName)
	defer func() {
		log.Debugf("searchImpl done in %v", time.Since(start))
	}()

	if limit <= 0 {
		limit = 8
	}

	// Query rewriting — triage-aware, BM25-only path.
	bm25QueryStr := s.bm25Query(query)
	queryTerms, err := retrieval.QueryTerms(bm25QueryStr)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	entries, err := s.collectEntries(filter, queryTerms)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// G14: coarse-to-fine
	if filter.Coarse {
		log.Debugf("coarseToFineFilter: entries before=%d", len(entries))
		entries, err = s.coarseToFineFilter(query, entries)
		if err != nil {
			log.Warnf("coarseToFineFilter failed: %v, falling back to unfiltered", err)
		}
		log.Debugf("coarseToFineFilter: entries after=%d", len(entries))
		if len(entries) == 0 {
			return nil, nil
		}
	}

	// Phase 2: BM25 scoring
	docs := make([]map[string]int, len(entries))
	lengths := make([]int, len(entries))
	var totalLen int
	for i, e := range entries {
		docs[i] = e.terms
		lengths[i] = e.termLen
		totalLen += e.termLen
	}
	df := retrieval.DocumentFrequency(docs)
	avgLen := float64(totalLen) / float64(len(entries))

	var results []rankedEntry
	for i, e := range entries {
		score := retrieval.BM25Score(docs[i], lengths[i], queryTerms, df, len(entries), avgLen)
		if score > 0 {
			results = append(results, rankedEntry{entry: e, score: score})
		}
	}

	// Abstract boost
	for i := range results {
		if results[i].entry.isPaper && results[i].entry.sectionRole == "abstract" {
			results[i].score *= s.AbstractBoost
		}
	}

	// Partial sort + noise trim
	topN := limit * 5
	if topN < 200 {
		topN = 200
	}
	results = partialSortTopK(results, topN)
	results = retrieval.KeepTopRelativeScore(results, 0.15, func(r rankedEntry) float64 { return r.score })

	// Load chunk text
	for i := range results {
		if results[i].entry.text == "" {
			text, readErr := s.ReadChunk(results[i].entry.docSlug, results[i].entry.chunkID)
			if readErr != nil {
				text = ""
			}
			results[i].entry.text = text
		}
	}

	// Reranker
	if s.reranker != nil && len(results) > 0 {
		if s.gpuScheduler != nil {
			restore := s.gpuScheduler.PrepareForReranking()
			defer restore()
		}
		entries2 := make([]searchEntry, len(results))
		scores := make([]float64, len(results))
		for i, r := range results {
			entries2[i] = r.entry
			scores[i] = r.score
		}
		newEntries, newScores := s.rerankTop(query, entries2, scores, limit)
		results = make([]rankedEntry, len(newEntries))
		for i := range newEntries {
			results[i] = rankedEntry{entry: newEntries[i], score: newScores[i]}
		}
	}

	// Cap
	if len(results) > limit {
		results = results[:limit]
	}

	// Convert to SearchHit
	hits := make([]SearchHit, len(results))
	for i, r := range results {
		cid := fmt.Sprintf("%s_%s", r.entry.docSlug, r.entry.chunkID)
		hits[i] = SearchHit{
			Score:      r.score,
			Document:   DocumentInfo{ID: r.entry.docSlug, Title: r.entry.title, OriginalName: r.entry.originalName, Type: r.entry.sourceType},
			Location:   LocationInfo{ChunkID: r.entry.chunkID, Section: r.entry.section, Offset: r.entry.offset, PageStart: r.entry.pageStart, PageEnd: r.entry.pageEnd},
			Content:    HitContent{Snippet: retrieval.MakeSnippet(r.entry.text, query, queryTerms, 200), SectionRole: r.entry.sectionRole},
			CitationID: cid,
		}
	}

	// Deduplicate + section hint
	hits = deduplicateSnippets(hits)
	type sectionKey struct{ doc, heading string }
	secCount := make(map[sectionKey]int)
	for _, h := range hits {
		if h.Location.Section != "" {
			secCount[sectionKey{h.Document.ID, h.Location.Section}]++
		}
	}
	for i := range hits {
		if hits[i].Location.Section != "" && secCount[sectionKey{hits[i].Document.ID, hits[i].Location.Section}] >= 2 {
			hits[i].SectionHint = fmt.Sprintf("Multiple hits in section '%s'. Consider reading with level=section for full context.", hits[i].Location.Section)
		}
	}

	// Search log
	if s.searchLogger != nil {
		topN2 := 3
		if len(hits) < topN2 {
			topN2 = len(hits)
		}
		topScores := make([]float64, topN2)
		hitIDs := make([]string, topN2)
		for i := 0; i < topN2; i++ {
			topScores[i] = hits[i].Score
			hitIDs[i] = hits[i].Location.ChunkID
		}
		s.searchLogger.LogSearch(SearchLogEntry{
			Query:     query,
			HitCount:  len(hits),
			HitIDs:    hitIDs,
			TopScores: topScores,
			Filter:    &filter,
			Timestamp: time.Now(),
		})
	}

	return hits, nil
}

// SearchBM25 performs a pure BM25 keyword search without vector retrieval or
// cross-encoder reranking. It is intended for search-debug and scenarios where
// raw lexical scores are needed.
func (s *Store) SearchBM25(query string, limit int) ([]SearchHit, error) {
	// Delegate to search engine when available.
	if s.searchEngine != nil {
		return s.searchEngine.SearchBM25(context.Background(), query, limit, SearchFilter{})
	}

	// Legacy path.
	start := time.Now()
	log := s.logger.WithModule("search")
	log.Debugf("SearchBM25: query=%q limit=%d kb=%q", query, limit, s.kbName)
	defer func() {
		log.Debugf("SearchBM25 done in %v", time.Since(start))
	}()

	if limit <= 0 {
		limit = 8
	}

	bm25QueryStr := s.bm25Query(query)
	queryTerms, err := retrieval.QueryTerms(bm25QueryStr)
	if err != nil {
		return nil, fmt.Errorf("search bm25: %w", err)
	}

	entries, err := s.collectEntries(SearchFilter{}, queryTerms)
	if err != nil {
		return nil, fmt.Errorf("search bm25: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// BM25 scoring.
	docs := make([]map[string]int, len(entries))
	lengths := make([]int, len(entries))
	var totalLen int
	for i, e := range entries {
		docs[i] = e.terms
		lengths[i] = e.termLen
		totalLen += e.termLen
	}
	df := retrieval.DocumentFrequency(docs)
	avgLen := float64(totalLen) / float64(len(entries))

	var results []rankedEntry
	for i, e := range entries {
		score := retrieval.BM25Score(docs[i], lengths[i], queryTerms, df, len(entries), avgLen)
		if score > 0 {
			results = append(results, rankedEntry{entry: e, score: score})
		}
	}

	// Abstract boost.
	for i := range results {
		if results[i].entry.isPaper && results[i].entry.sectionRole == "abstract" {
			results[i].score *= s.AbstractBoost
		}
	}

	// Partial sort + noise trim.
	topN := limit * 5
	if topN < 200 {
		topN = 200
	}
	results = partialSortTopK(results, topN)
	results = retrieval.KeepTopRelativeScore(results, 0.15, func(r rankedEntry) float64 { return r.score })

	// Load chunk text for snippet generation.
	for i := range results {
		if results[i].entry.text == "" {
			text, readErr := s.ReadChunk(results[i].entry.docSlug, results[i].entry.chunkID)
			if readErr != nil {
				text = ""
			}
			results[i].entry.text = text
		}
	}

	// Cap to limit.
	if len(results) > limit {
		results = results[:limit]
	}

	// Convert to SearchHit.
	hits := make([]SearchHit, len(results))
	for i, r := range results {
		cid := fmt.Sprintf("%s_%s", r.entry.docSlug, r.entry.chunkID)
		hits[i] = SearchHit{
			Score: r.score,
			Document: DocumentInfo{
				ID:           r.entry.docSlug,
				Title:        r.entry.title,
				OriginalName: r.entry.originalName,
				Type:         r.entry.sourceType,
			},
			Location: LocationInfo{
				ChunkID:   r.entry.chunkID,
				Section:   r.entry.section,
				Offset:    r.entry.offset,
				PageStart: r.entry.pageStart,
				PageEnd:   r.entry.pageEnd,
			},
			Content: HitContent{
				Snippet:     retrieval.MakeSnippet(r.entry.text, query, queryTerms, 200),
				SectionRole: r.entry.sectionRole,
			},
			CitationID: cid,
		}
	}

	// Deduplicate + section hint.
	hits = deduplicateSnippets(hits)
	type sectionKey struct{ doc, heading string }
	secCount := make(map[sectionKey]int)
	for _, h := range hits {
		if h.Location.Section != "" {
			secCount[sectionKey{h.Document.ID, h.Location.Section}]++
		}
	}
	for i := range hits {
		if hits[i].Location.Section != "" && secCount[sectionKey{hits[i].Document.ID, hits[i].Location.Section}] >= 2 {
			hits[i].SectionHint = fmt.Sprintf("Multiple hits in section '%s'. Consider reading with level=section for full context.", hits[i].Location.Section)
		}
	}

	return hits, nil
}

// SearchAll searches across all knowledge bases and merges results.
// When no kbName is set, it traverses every KB and combines hits sorted
// by score descending, capped at limit.
func (s *Store) SearchAll(query string, limit int, filters ...SearchFilter) ([]SearchHit, error) {
	s.logger.WithModule("search").Debugf("SearchAll: query=%q limit=%d", query, limit)
	kbs, err := s.ListKBs()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 8
	}
	type scoredHit struct {
		hit   SearchHit
		score float64
	}
	var all []scoredHit
	seen := map[string]bool{}
	for _, kb := range kbs {
		kbStore := s.WithKB(kb)
		hits, err := kbStore.Search(query, limit, filters...)
		if err != nil {
			continue
		}
		for _, h := range hits {
			key := VectorID(h.Document.ID, h.Location.ChunkID)
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, scoredHit{hit: h, score: h.Score})
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].score > all[j].score
	})
	if len(all) > limit {
		all = all[:limit]
	}
	result := make([]SearchHit, len(all))
	for i, sh := range all {
		result[i] = sh.hit
	}
	return result, nil
}

// HybridSearch runs a combined BM25 + dense embedding search using Reciprocal
// Rank Fusion (RRF). It searches all chunks, computes both BM25 scores and
// cosine similarity with the query embedding, then fuses the rankings.
//
// Documents without vectors (no embedder configured at upload time) are scored
// with BM25 only. The method falls back to pure BM25 when the store has no
// embedder set.
//
// An optional SearchFilter can be passed to narrow results by doc slug, source
// type, or section.
//
// The limit caps the number of results; hits below 15% of the top BM25 score
// are trimmed before fusion.
func (s *Store) HybridSearch(query string, limit int, filters ...SearchFilter) ([]SearchHit, error) {
	// Resolve filter.
	var filter SearchFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

	// Delegate to search engine when available.
	if s.searchEngine != nil {
		return s.searchEngine.HybridSearch(context.Background(), query, limit, filter)
	}

	// Legacy path.
	log := s.logger.WithModule("search")
	log.Debugf("HybridSearch: query=%q limit=%d kb=%q embedder=%v", query, limit, s.kbName, s.embedder != nil)

	if limit <= 0 {
		limit = 8
	}

	// v4: Dynamic retrieval budget based on query complexity (pure rules, 0 LLM cost).
	qf := analyzeQuery(query)
	bm25N, vecBeam, rerankN, budgetReturn := retrievalBudget(qf)
	if limit > budgetReturn {
		// User asked for more than the budget recommends — respect the user's
		// limit, but use budget for internal stages (beam, rerank pool).
	}
	log.Debugf("HybridSearch: budget=%s bm25N=%d vecBeam=%d rerankN=%d return=%d",
		retrievalBudgetLabel(qf), bm25N, vecBeam, rerankN, budgetReturn)

	// Query rewriting — triage-aware, BM25 and vector paths separated.
	bm25QueryStr := s.bm25Query(query)
	vectorQueryStr := s.vectorQuery(query)

	queryTerms, err := retrieval.QueryTerms(bm25QueryStr)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}

	// Check query cache.
	if s.cacheEnabled() && limit > 0 {
		qhash := cache.QueryHash(query, "hybrid", limit, filter.SourceType, filter.Section)
		ckey := cache.QueryKey(s.kbName, qhash)
		if raw, cerr := s.cacheClient.Get(context.Background(), ckey); cerr == nil && raw != nil {
			var cachedHits []SearchHit
			if json.Unmarshal(raw, &cachedHits) == nil {
				log.Infof("query HIT  key=%s query=%q hash=%s hits=%d", ckey, query, qhash, len(cachedHits))
				return cachedHits, nil
			}
		}
		log.Infof("query MISS key=%s query=%q hash=%s", ckey, query, qhash)
	}

	entries, err := s.collectEntries(filter, queryTerms)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// G14: coarse-to-fine filter — reduce entries to those in top-3 sections.
	if filter.Coarse {
		log.Debugf("coarseToFineFilter: entries before=%d", len(entries))
		entries, err = s.coarseToFineFilter(query, entries)
		if err != nil {
			// Non-fatal: fall back to unfiltered entries.
			log.Warnf("coarseToFineFilter failed: %v, falling back to unfiltered", err)
		}
		log.Debugf("coarseToFineFilter: entries after=%d", len(entries))
		if len(entries) == 0 {
			return nil, nil
		}
	}

	// =========================================================================
	// Phase 1.5: vector-side independent recall via HNSW ANN.
	//
	// The dense path does its own ANN search over *all* indexed vectors —
	// it is NOT limited to the BM25 candidate set.  Vector-only candidates
	// (semantically relevant but lacking keyword overlap) are merged into
	// the candidate pool so they can participate in RRF fusion.
	//
	// cosByKey is pre-computed here and reused in Phase 3 so we don't
	// repeat the embedding API call or ANN search.
	// =========================================================================
	var cosByKey map[string]float64 // key = "slug/chunkID" → cosine score

	if s.embedder != nil {
		s.EnsureVectorIndex()
	}
	needDense := s.embedder != nil && s.vectorIndex != nil && s.vectorIndex.Len() > 0

	if needDense {
		// Coordinate GPU: load embedding model, sleep reranker.
		var restoreEmb func()
		if s.gpuScheduler != nil {
			restoreEmb = s.gpuScheduler.PrepareForEmbedding()
		}

		queryVec, embedErr := s.embedder.Embed(context.Background(), []string{vectorQueryStr})
		if embedErr == nil && len(queryVec) > 0 && len(queryVec[0]) > 0 {
			qVec64 := make([]float64, len(queryVec[0]))
			for j, v := range queryVec[0] {
				qVec64[j] = float64(v)
			}

			// v4: Budget-based beam width from query complexity.
			vecK := vecBeam
			if vecK > s.vectorIndex.Len() {
				vecK = s.vectorIndex.Len()
			}

			hits := s.vectorIndex.Search(qVec64, vecK)
			cosByKey = make(map[string]float64, len(hits))
			for _, h := range hits {
				cosByKey[h.ID] = h.Score
			}
			log.Debugf("[search] hybrid: vector recall returned %d hits (beam=%d)", len(hits), vecK)

			// Merge vector-only candidates into the BM25 candidate set.
			existingKeys := make(map[string]bool, len(entries))
			for _, e := range entries {
				existingKeys[VectorID(e.docSlug, e.chunkID)] = true
			}
			merged := 0
			for _, h := range hits {
				if !existingKeys[h.ID] {
				parts := strings.SplitN(h.ID, "/", 2)
				if len(parts) == 2 {
						entries = append(entries, searchEntry{
							docSlug: parts[0],
							chunkID: parts[1],
						})
						merged++
					}
				}
			}
			if merged > 0 {
				log.Debugf("[search] hybrid: merged %d vector-only candidates (total=%d)", merged, len(entries))
			}
		} else {
			log.Infof("[search] hybrid: embedding failed, dense recall skipped")
			needDense = false
		}

		// Release GPU scheduler lock — embedding work is done.
		// Must release before Phase 8 (reranking) can acquire the lock.
		if restoreEmb != nil {
			restoreEmb()
		}
	}

	// Check if any entry carries its own vector (for brute-force fallback).
	hasVectors := false
	for _, e := range entries {
		if len(e.vector) > 0 {
			hasVectors = true
			break
		}
	}

	// Phase 2: BM25 scoring.
	docs := make([]map[string]int, len(entries))
	lengths := make([]int, len(entries))
	var totalLen int
	for i, e := range entries {
		docs[i] = e.terms
		lengths[i] = e.termLen
		totalLen += e.termLen
	}
	df := retrieval.DocumentFrequency(docs)
	avgLen := float64(totalLen) / float64(len(entries))

	type hybridRanked struct {
		entry     searchEntry
		bm25Score float64
		cosScore  float64
		rrfScore  float64
	}
	scored := make([]hybridRanked, len(entries))
	for i, e := range entries {
		scored[i] = hybridRanked{
			entry:     e,
			bm25Score: retrieval.BM25Score(docs[i], lengths[i], queryTerms, df, len(entries), avgLen),
		}
	}

	// Phase 3: dense scoring — reuse cosByKey pre-computed in Phase 1.5.
	// When cosByKey is populated, the ANN search has already been done and we
	// just map scores onto the scored slice.  Otherwise we fall back to a
	// fresh brute-force scan over entries that carry their own vectors.
	if len(cosByKey) > 0 {
		needFallback := false
		for i := range scored {
			key := VectorID(scored[i].entry.docSlug, scored[i].entry.chunkID)
			if s, ok := cosByKey[key]; ok {
				scored[i].cosScore = s
			} else if len(scored[i].entry.vector) > 0 {
				needFallback = true
			}
		}
		if needFallback {
			// Rare path: a few entries have vectors but fell outside the
			// ANN beam.  Do a targeted brute-force for just those entries.
			queryVec, embedErr := s.embedder.Embed(context.Background(), []string{vectorQueryStr})
			if embedErr == nil && len(queryVec) > 0 && len(queryVec[0]) > 0 {
				qVec64 := make([]float64, len(queryVec[0]))
				for j, v := range queryVec[0] {
					qVec64[j] = float64(v)
				}
				for i := range scored {
					if scored[i].cosScore == 0 && len(scored[i].entry.vector) > 0 {
						scored[i].cosScore = cosineSimilarity(scored[i].entry.vector, qVec64)
					}
				}
			}
		}
		s.logger.Debugf("[search] hybrid: dense scoring done (cosByKey=%d fallback=%v)", len(cosByKey), needFallback)
	} else if hasVectors && s.embedder != nil {
		// No vector index available; brute-force every entry that has a vector.
		var restoreEmb2 func()
		if s.gpuScheduler != nil {
			restoreEmb2 = s.gpuScheduler.PrepareForEmbedding()
		}
		queryVec, embedErr := s.embedder.Embed(context.Background(), []string{vectorQueryStr})
		if embedErr == nil && len(queryVec) > 0 && len(queryVec[0]) > 0 {
			qVec64 := make([]float64, len(queryVec[0]))
			for j, v := range queryVec[0] {
				qVec64[j] = float64(v)
			}
			for i := range scored {
				if len(scored[i].entry.vector) > 0 {
					scored[i].cosScore = cosineSimilarity(scored[i].entry.vector, qVec64)
				}
			}
		}
		if restoreEmb2 != nil {
			restoreEmb2()
		}
		s.logger.Debugf("[search] hybrid: brute-force cos_scores for %d candidates (no index)", len(scored))
	} else {
		s.logger.Debugf("[search] hybrid: dense scoring skipped (no vectors or no embedder)")
	}

	// Phase 3.5: abstract score boost. Chunks from the "abstract" section of
	// paper-like documents receive a modest BM25 boost (×AbstractBoost) so the
	// boost is factored into RRF fusion (G13).
	for i := range scored {
		if scored[i].entry.isPaper && scored[i].entry.sectionRole == "abstract" {
			scored[i].bm25Score *= s.AbstractBoost
		}
	}

	// Phase 4: RRF fusion with adaptive weighting (G5).
	// The alpha weight adapts to query type: conceptual queries boost the
	// dense side, factual queries boost BM25.
	alpha := adaptiveRRFWeight(query)

	// Sort by BM25 score descending for BM25 rank.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].bm25Score > scored[j].bm25Score
	})
	for i := range scored {
		if scored[i].bm25Score > 0 {
			scored[i].rrfScore += alpha * (1.0 / (rrfK + float64(i)))
		}
	}

	// Sort by cosine score descending for dense rank.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].cosScore > scored[j].cosScore
	})
	for i := range scored {
		if scored[i].cosScore > 0 {
			scored[i].rrfScore += (1 - alpha) * (1.0 / (rrfK + float64(i)))
		}
	}

	// Phase 5: sort by final RRF score descending.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].rrfScore > scored[j].rrfScore
	})

	// Phase 6: keep only entries with non-zero RRF score.
	var results []hybridRanked
	for _, r := range scored {
		if r.rrfScore > 0 {
			results = append(results, r)
		}
	}

	// Phase 7: trim low-scoring noise.
	if len(results) > 0 {
		top := results[0].rrfScore
		cutoff := 0.15
		trimmed := results[:0]
		for i, r := range results {
			if i == 0 || r.rrfScore >= top*cutoff {
				trimmed = append(trimmed, r)
			}
		}
		results = trimmed
	}

	// Phase 8: reranker (optional). Applied BEFORE the final cap so the
	// cross-encoder sees the full candidate pool (up to rerankCandidateLimit).
	// When no reranker is configured, this is a no-op.
	s.logger.Debugf("[search] hybrid: reranking phase reranker=%v candidates=%d", s.reranker != nil, len(results))
	if s.reranker != nil && len(results) > 0 {
		// Coordinate GPU: switch from embedding to reranker mode.
		if s.gpuScheduler != nil {
			restoreRerank := s.gpuScheduler.PrepareForReranking()
			defer restoreRerank()
		}
		entries := make([]searchEntry, len(results))
		scores := make([]float64, len(results))
		for i, r := range results {
			entries[i] = r.entry
			scores[i] = r.rrfScore
		}
		newEntries, newScores := s.rerankTop(query, entries, scores, limit)
		results = make([]hybridRanked, len(newEntries))
		for i := range newEntries {
			results[i] = hybridRanked{entry: newEntries[i], rrfScore: newScores[i]}
		}
		s.logger.Debugf("[search] hybrid: reranking done results=%d", len(newEntries))
	} else {
		if s.reranker == nil {
			s.logger.Debugf("[search] hybrid: reranking skipped (no reranker configured)")
		} else {
			s.logger.Debugf("[search] hybrid: reranking skipped (no results to rerank)")
		}
	}

	// Phase 9: cap to limit — AFTER rerank so cross-encoder sees all candidates.
	if len(results) > limit {
		results = results[:limit]
	}

	// Phase 9: read chunk text for index-path results.
	for i := range results {
		if results[i].entry.text == "" {
			text, readErr := s.ReadChunk(results[i].entry.docSlug, results[i].entry.chunkID)
			if readErr != nil {
				text = ""
			}
			results[i].entry.text = text
		}
	}

	// Phase 10: convert to SearchHit slice.
	hits := make([]SearchHit, len(results))
	for i, r := range results {
		cid := fmt.Sprintf("%s_%s", r.entry.docSlug, r.entry.chunkID)
		hits[i] = SearchHit{
			Score: r.rrfScore,
			Document: DocumentInfo{
				ID:           r.entry.docSlug,
				Title:        r.entry.title,
				OriginalName: r.entry.originalName,
				Type:         r.entry.sourceType,
			},
			Location: LocationInfo{
				ChunkID:   r.entry.chunkID,
				Section:   r.entry.section,
				Offset:    r.entry.offset,
				PageStart: r.entry.pageStart,
				PageEnd:   r.entry.pageEnd,
			},
			Content: HitContent{
				Snippet:     retrieval.MakeSnippet(r.entry.text, query, queryTerms, 200),
				SectionRole: r.entry.sectionRole,
			},
			CitationID: cid,
		}
	}
	// Phase 10a: deduplicate overlapping snippets (G9).
	before := len(hits)
	hits = deduplicateSnippets(hits)
	dupCount := 0
	for _, h := range hits {
		if h.DuplicateOf != "" {
			dupCount++
		}
	}
	if dupCount > 0 {
		log.Debugf("deduplicateSnippets: total=%d duplicates=%d", before, dupCount)
	}

	// Phase 10b: section hint — when multiple chunks from the same section appear
	// in the results, annotate them so the caller knows to read the full section.
	type hsSectionKey struct{ doc, heading string }
	hsSecCount := make(map[hsSectionKey]int)
	for _, h := range hits {
		if h.Location.Section != "" {
			hsSecCount[hsSectionKey{h.Document.ID, h.Location.Section}]++
		}
	}
	for i := range hits {
		if hits[i].Location.Section != "" && hsSecCount[hsSectionKey{hits[i].Document.ID, hits[i].Location.Section}] >= 2 {
			hits[i].SectionHint = fmt.Sprintf("Multiple hits in section '%s'. Consider reading with level=section for full context.", hits[i].Location.Section)
		}
	}

	// G11: log search if a logger is configured.
	if s.searchLogger != nil {
		topN := 3
		if len(hits) < topN {
			topN = len(hits)
		}
		topScores := make([]float64, topN)
		hitIDs := make([]string, topN)
		for i := 0; i < topN; i++ {
			topScores[i] = hits[i].Score
			hitIDs[i] = hits[i].Location.ChunkID
		}
		s.searchLogger.LogSearch(SearchLogEntry{
			Query:     query,
			HitCount:  len(hits),
			HitIDs:    hitIDs,
			TopScores: topScores,
			Filter:    &filter,
			Timestamp: time.Now(),
		})
	}
	// Store in query cache.
	if s.cacheEnabled() && limit > 0 && len(hits) > 0 {
		qhash := cache.QueryHash(query, "hybrid", limit, filter.SourceType, filter.Section)
		ckey := cache.QueryKey(s.kbName, qhash)
		if raw, jerr := json.Marshal(hits); jerr == nil {
			if setErr := s.cacheClient.Set(context.Background(), ckey, raw, s.queryCacheTTL); setErr != nil {
				log.Warnf("query SET failed: key=%s query=%q err=%v", ckey, query, setErr)
			} else {
				log.Infof("query SET  key=%s query=%q hash=%s hits=%d ttl=%v size=%d", ckey, query, qhash, len(hits), s.queryCacheTTL, len(raw))
			}
		}
	}
	return hits, nil
}

// SearchVector performs a pure dense (ANN) vector search using the HNSW index.
// It is intended for search-debug and scenarios where raw semantic similarity
// scores are needed. Returns an error when no embedder or vector index is
// configured.
