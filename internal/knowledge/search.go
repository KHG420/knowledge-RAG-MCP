package knowledge

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"knowledge-mcp/internal/cache"
	"knowledge-mcp/internal/retrieval"
)

// rankedEntry pairs a search entry with its BM25 / hybrid score for sorting.
type rankedEntry struct {
	entry searchEntry
	score float64
}

const dedupJaccardThreshold = 0.6 // Jaccard similarity threshold for snippet dedup (G9)

const rrfK = 60.0 // RRF constant for hybrid search fusion

// SearchLogger is an optional interface for recording search queries and their
// results for telemetry, tuning, and feedback. A nil logger is silently ignored.
type SearchLogger interface {
	LogSearch(entry SearchLogEntry)
}

// SearchLogEntry captures a single search query and its top results.
type SearchLogEntry struct {
	Query      string        `json:"query"`
	HitCount   int           `json:"hit_count"`
	HitIDs     []string      `json:"hit_ids,omitempty"` // returned chunk IDs in ranked order
	TopScores  []float64     `json:"top_scores,omitempty"`
	JudgedHits []string      `json:"judged_hits,omitempty"` // human-annotated relevant chunk IDs
	Filter     *SearchFilter `json:"filter,omitempty"`
	Timestamp  time.Time     `json:"timestamp"`
}

// searchEntry is a unified representation of one chunk during scoring. When the
// index path is used, text is empty until snippet generation; when the fallback
// path is used, text is populated from the chunk file and tokens are computed
// on the fly.
type searchEntry struct {
	docSlug        string
	chunkID        string
	text           string         // chunk content (only populated in fallback or for snippet)
	terms          map[string]int // term frequencies (from index or computed)
	termLen        int            // total token count
	section        string         // from CHUNKS.toml metadata
	offset         int            // from CHUNKS.toml metadata
	sourceType     string         // from meta.json
	title          string         // from meta.json (human-readable document title)
	originalName   string         // from meta.json (original filename)
	pageStart      int            // from CHUNKS.toml (PDF page number, 1-based, 0 = unknown)
	pageEnd        int            // from CHUNKS.toml (PDF page number, 1-based, 0 = unknown)
	vector         []float64      // dense embedding vector (from CHUNKS.toml, if available)
	sectionRole    string         // classified section role (C2), e.g. "abstract", "introduction"
	sectionChunkID string         // G14: parent section chunk ID (e.g. "S00"), for coarse-to-fine search
	isPaper        bool           // G13: true when the parent document is an academic paper
}

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
					terms:          termFreqsToMap(e.Terms),
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
				terms:          termFreqsToMap(e.Terms),
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

// Search runs a BM25 query across all chunks in the knowledge base and returns
// ranked hits.
//
// Results are cached in the configured cache backend (if enabled).
func (s *Store) Search(query string, limit int, filters ...SearchFilter) ([]SearchHit, error) {
	// Resolve filter.
	var filter SearchFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

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

	// Query rewriting
	rewritten := s.rewrittenQueries(query)
	queryTerms, err := retrieval.QueryTerms(rewritten)
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
	start := time.Now()
	log := s.logger.WithModule("search")
	log.Debugf("SearchBM25: query=%q limit=%d kb=%q", query, limit, s.kbName)
	defer func() {
		log.Debugf("SearchBM25 done in %v", time.Since(start))
	}()

	if limit <= 0 {
		limit = 8
	}

	rewritten := s.rewrittenQueries(query)
	queryTerms, err := retrieval.QueryTerms(rewritten)
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
			key := h.Document.ID + "/" + h.Location.ChunkID
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
	_ = bm25N // reserved for future per-stage BM25 cap
	log.Debugf("HybridSearch: budget=%s bm25N=%d vecBeam=%d rerankN=%d return=%d",
		retrievalBudgetLabel(qf), bm25N, vecBeam, rerankN, budgetReturn)

	// Query rewriting.
	rewritten := s.rewrittenQueries(query)

	queryTerms, err := retrieval.QueryTerms(rewritten)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}

	var filter SearchFilter
	if len(filters) > 0 {
		filter = filters[0]
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

		queryVec, embedErr := s.embedder.Embed(context.Background(), []string{query})
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
			log.Infof("[search] hybrid: vector recall returned %d hits (beam=%d)", len(hits), vecK)

			// Merge vector-only candidates into the BM25 candidate set.
			existingKeys := make(map[string]bool, len(entries))
			for _, e := range entries {
				existingKeys[e.docSlug+"/"+e.chunkID] = true
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
				log.Infof("[search] hybrid: merged %d vector-only candidates (total=%d)", merged, len(entries))
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
			key := scored[i].entry.docSlug + "/" + scored[i].entry.chunkID
			if s, ok := cosByKey[key]; ok {
				scored[i].cosScore = s
			} else if len(scored[i].entry.vector) > 0 {
				needFallback = true
			}
		}
		if needFallback {
			// Rare path: a few entries have vectors but fell outside the
			// ANN beam.  Do a targeted brute-force for just those entries.
			queryVec, embedErr := s.embedder.Embed(context.Background(), []string{query})
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
		s.logger.Infof("[search] hybrid: dense scoring done (cosByKey=%d fallback=%v)", len(cosByKey), needFallback)
	} else if hasVectors && s.embedder != nil {
		// No vector index available; brute-force every entry that has a vector.
		var restoreEmb2 func()
		if s.gpuScheduler != nil {
			restoreEmb2 = s.gpuScheduler.PrepareForEmbedding()
		}
		queryVec, embedErr := s.embedder.Embed(context.Background(), []string{query})
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
		s.logger.Infof("[search] hybrid: brute-force cos_scores for %d candidates (no index)", len(scored))
	} else {
		s.logger.Infof("[search] hybrid: dense scoring skipped (no vectors or no embedder)")
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
	s.logger.Infof("[search] hybrid: reranking phase reranker=%v candidates=%d", s.reranker != nil, len(results))
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
		s.logger.Infof("[search] hybrid: reranking done results=%d", len(newEntries))
	} else {
		if s.reranker == nil {
			s.logger.Infof("[search] hybrid: reranking skipped (no reranker configured)")
		} else {
			s.logger.Infof("[search] hybrid: reranking skipped (no results to rerank)")
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
func (s *Store) SearchVector(query string, limit int) ([]SearchHit, error) {
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

	queryVec, embedErr := s.embedder.Embed(context.Background(), []string{query})
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
	log.Infof("[search] vector: ANN returned %d hits (beam=%d)", len(hits), vecK)

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

		parts := strings.SplitN(h.ID, "/", 2)
		if len(parts) != 2 {
			continue
		}
		slug, chunkID := parts[0], parts[1]

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
func (s *Store) coarseToFineFilter(query string, entries []searchEntry) ([]searchEntry, error) {
	if len(entries) == 0 {
		return entries, nil
	}

	// Collect unique (docSlug, sectionChunkID) pairs from entries.
	type secKey struct {
		slug  string
		secID string
	}
	secSet := map[secKey]bool{}
	for _, e := range entries {
		if e.sectionChunkID != "" {
			secSet[secKey{e.docSlug, e.sectionChunkID}] = true
		}
	}
	if len(secSet) == 0 {
		return entries, nil // no section info — return all
	}

	// Build section-level search entries by reading section chunk files.
	type secEntry struct {
		key     secKey
		terms   map[string]int
		termLen int
	}
	var secEntries []secEntry
	for key := range secSet {
		content, readErr := s.ReadSectionChunk(key.slug, key.secID)
		if readErr != nil {
			continue
		}
		tokens := retrieval.Tokens(content)
		secEntries = append(secEntries, secEntry{
			key:     key,
			terms:   retrieval.Counts(tokens),
			termLen: len(tokens),
		})
	}
	if len(secEntries) == 0 {
		return entries, nil
	}

	// Tokenise the query for BM25 scoring.
	queryTerms, err := retrieval.QueryTerms(query)
	if err != nil || len(queryTerms) == 0 {
		return entries, nil
	}

	// Score sections with BM25.
	docs := make([]map[string]int, len(secEntries))
	lengths := make([]int, len(secEntries))
	totalLen := 0
	for i, se := range secEntries {
		docs[i] = se.terms
		lengths[i] = se.termLen
		totalLen += se.termLen
	}
	df := retrieval.DocumentFrequency(docs)
	avgLen := float64(totalLen) / float64(len(secEntries))

	type scoredSec struct {
		key   secKey
		score float64
	}
	var scored []scoredSec
	for i, se := range secEntries {
		score := retrieval.BM25Score(docs[i], lengths[i], queryTerms, df, len(secEntries), avgLen)
		if score > 0 {
			scored = append(scored, scoredSec{key: se.key, score: score})
		}
	}

	// Sort by score descending, take top 3.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	topN := 3
	if len(scored) < topN {
		topN = len(scored)
	}
	topSections := map[secKey]bool{}
	for i := 0; i < topN; i++ {
		topSections[scored[i].key] = true
	}

	// Filter entries: keep entries without sectionChunkID (no section info)
	// and entries whose sectionChunkID is in the top sections.
	filtered := make([]searchEntry, 0, len(entries))
	for _, e := range entries {
		if e.sectionChunkID == "" || topSections[secKey{e.docSlug, e.sectionChunkID}] {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// rerankTop re-scores the top entries using the configured Reranker (cross-encoder)
// for improved precision. It returns entries and scores in reranker order.
// When no reranker is configured or the reranker errors, returns entries/scores
// capped to the candidate limit unchanged.
func (s *Store) rerankTop(query string, entries []searchEntry, scores []float64, limit int) ([]searchEntry, []float64) {
	if s.reranker == nil || len(entries) == 0 {
		return entries, scores
	}

	// candLimit controls how many top-N BM25 results are fed to the reranker.
	// When RerankCandidateLimit is set (>0), use it. Otherwise fall back to
	// the legacy heuristic (limit×2, min 20).
	candLimit := s.rerankCandidateLimit
	if candLimit <= 0 {
		candLimit = limit * 2
		if candLimit < 20 {
			candLimit = 20
		}
	}
	n := len(entries)
	if n > candLimit {
		n = candLimit
	}

	// Load chunk text for the candidate entries.
	texts := make([]string, n)
	for i := 0; i < n; i++ {
		if entries[i].text == "" {
			text, readErr := s.ReadChunk(entries[i].docSlug, entries[i].chunkID)
			if readErr != nil {
				text = ""
			}
			entries[i].text = text
		}
		texts[i] = entries[i].text
	}

	// Check rerank memory cache to avoid repeated HTTP calls.
	rerankKey := rerankCacheKey(query, texts[:n])
	if s.rerankCache != nil {
		s.rerankCacheMu.RLock()
		if cachedScores, ok := s.rerankCache[rerankKey]; ok {
			s.rerankCacheMu.RUnlock()
			s.logger.Debugf("[rerank] cache hit: query=%q candidates=%d", query, n)
			return sortByRerankScores(entries[:n], cachedScores)
		}
		s.rerankCacheMu.RUnlock()
	}

	s.logger.Debugf("[rerank] rerankTop query=%q candidates=%d candLimit=%d", query, len(entries), candLimit)

	// Determine timeout from the reranker's HTTP client timeout, with 5s buffer
	// to avoid context deadline racing with the HTTP client timeout.
	rerankTimeout := 30 * time.Second
	if ir, ok := s.reranker.(*InfinityReranker); ok {
		rerankTimeout = ir.Timeout() + 5*time.Second
	}
	rerankCtx, rerankCancel := context.WithTimeout(context.Background(), rerankTimeout)
	defer rerankCancel()

	// Determine batch size: default 20, overridable via SetRerankBatchSize or RERANK_BATCH_SIZE env.
	batchSize := s.rerankBatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	if envBatch := os.Getenv("RERANK_BATCH_SIZE"); envBatch != "" {
		if v, err := strconv.Atoi(envBatch); err == nil && v > 0 {
			batchSize = v
		}
	}

	// Batch candidates to avoid timeouts on slow reranker models.
	rerankScores := make([]float64, 0, n)
	for batchStart := 0; batchStart < n; batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > n {
			batchEnd = n
		}
		batchScores, batchErr := s.reranker.Rerank(rerankCtx, query, texts[batchStart:batchEnd])
		if batchErr != nil || len(batchScores) != batchEnd-batchStart {
			s.logger.Warnf("[rerank] rerankTop failed: %v (scores=%d, expected=%d), trying vector similarity fallback", batchErr, len(batchScores), batchEnd-batchStart)

			// Vector similarity fallback: embed the query and score each candidate
			// via cosine similarity. This gives semantic relevance even when the
			// cross-encoder is unavailable.
			if s.embedder != nil {
				// Check if any candidate has a stored vector.
				hasVec := false
				for i := 0; i < n && !hasVec; i++ {
					if len(entries[i].vector) > 0 {
						hasVec = true
					}
				}
				if hasVec {
					qVec, embedErr := s.embedder.Embed(rerankCtx, []string{query})
					if embedErr == nil && len(qVec) == 1 && len(qVec[0]) > 0 {
						qVec64 := make([]float64, len(qVec[0]))
						for i, v := range qVec[0] {
							qVec64[i] = float64(v)
						}
						vecScores := make([]float64, n)
						for i := 0; i < n; i++ {
							vecScores[i] = cosineSimilarity(qVec64, entries[i].vector)
						}
						s.logger.Infof("[rerank] using vector similarity fallback for %d candidates", n)
						return entries[:n], vecScores
					}
				}
			}
			s.logger.Warnf("[rerank] vector similarity fallback failed, using BM25 scores")
			return entries[:n], scores[:n]
		}
		rerankScores = append(rerankScores, batchScores...)
	}

	s.logger.Debugf("[rerank] rerankTop done candidates=%d batchSize=%d batches=%d", n, batchSize, (n+batchSize-1)/batchSize)

	// Store in memory cache for future identical queries.
	s.cacheRerankResult(rerankKey, rerankScores)

	// Sort candidates by reranker score descending.
	return sortByRerankScores(entries[:n], rerankScores)
}

// sortByRerankScores reorders entries by reranker scores descending.
func sortByRerankScores(entries []searchEntry, scores []float64) ([]searchEntry, []float64) {
	n := len(entries)
	type reranked struct {
		entry searchEntry
		score float64
	}
	combined := make([]reranked, n)
	for i := 0; i < n; i++ {
		combined[i] = reranked{entry: entries[i], score: scores[i]}
	}
	sort.Slice(combined, func(i, j int) bool {
		return combined[i].score > combined[j].score
	})
	outEntries := make([]searchEntry, n)
	outScores := make([]float64, n)
	for i := 0; i < n; i++ {
		outEntries[i] = combined[i].entry
		outScores[i] = combined[i].score
	}
	return outEntries, outScores
}

// rerankCacheKey computes a stable hash key from the query and candidate texts.
func rerankCacheKey(query string, texts []string) string {
	h := sha256.New()
	h.Write([]byte(query))
	h.Write([]byte{0})
	for _, t := range texts {
		h.Write([]byte(t))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// cacheRerankResult stores rerank scores in the memory cache with size bounding.
func (s *Store) cacheRerankResult(key string, scores []float64) {
	s.rerankCacheMu.Lock()
	defer s.rerankCacheMu.Unlock()
	if s.rerankCache == nil {
		s.rerankCache = make(map[string][]float64)
	}
	// Bound cache to prevent unbounded memory growth — evict oldest half when
	// exceeding 256 entries.
	const maxEntries = 256
	if len(s.rerankCache) >= maxEntries {
		n := 0
		for k := range s.rerankCache {
			delete(s.rerankCache, k)
			n++
			if n >= maxEntries/2 {
				break
			}
		}
	}
	s.rerankCache[key] = scores
}

// rewrittenQueries applies the configured QueryRewriter and merges all
// rewritten query variants into a single query string for tokenisation.
// When no rewriter is configured, the original query is returned as-is.
//
// v4: Also appends dictionary related_terms as extra keywords for vector recall.
func (s *Store) rewrittenQueries(query string) string {
	base := query
	if s.rewriter != nil {
		variants := s.rewriter.Rewrite(query)
		if len(variants) > 0 {
			// Merge all variants into a single query string for BM25 tokenisation.
			base = strings.Join(variants, " ")
		}
	}
	// Append related terms from loaded dictionaries for extra semantic signal.
	related := s.GetDictionaryRelatedTerms()
	if len(related) > 0 {
		base = base + " " + strings.Join(related, " ")
	}
	return base
}

// -------------------- G5: Adaptive RRF weighting --------------------

// adaptiveRRFWeight returns the BM25 weight (α) for RRF fusion based on query
// characteristics. Conceptual queries (questions, verbs) get a higher dense-side
// weight (lower α), while factual queries (noun-heavy) get a higher BM25 weight
// (higher α). Balanced queries use 0.5 (equal weighting, matching the default
// RRF behaviour).
func adaptiveRRFWeight(query string) float64 {
	qtype := detectQueryType(query)
	switch qtype {
	case "conceptual":
		return 0.4 // boost dense side: α=0.4 → dense weight = 0.6
	case "factual":
		return 0.6 // boost BM25 side: α=0.6 → BM25 gets 0.6
	default:
		return 0.5 // balanced: equal weights
	}
}

// -------------------- v4: Pure-rule complexity & retrieval budget --------------------

// QueryFeatures captures lightweight structural characteristics of a query
// for complexity classification without any LLM call.
type QueryFeatures struct {
	TermCount       int
	HasComparison   bool
	HasMethod       bool
	HasMultiConcept bool
}

// analyzeQuery extracts structural features from a query string using pure
// text rules — zero extra allocations beyond string ops.
func analyzeQuery(query string) QueryFeatures {
	lower := strings.ToLower(query)
	terms := strings.Fields(query)

	return QueryFeatures{
		TermCount: len(terms),
		HasComparison: strings.Contains(lower, "比较") || strings.Contains(lower, "对比") ||
			strings.Contains(lower, "差异") || strings.Contains(lower, "vs") ||
			strings.Contains(lower, "compare") || strings.Contains(lower, "versus") ||
			strings.Contains(lower, "区别"),
		HasMethod: strings.Contains(lower, "方法") || strings.Contains(lower, "method") ||
			strings.Contains(lower, "模型") || strings.Contains(lower, "模型") ||
			strings.Contains(lower, "计算") || strings.Contains(lower, "compute") ||
			strings.Contains(lower, "算法") || strings.Contains(lower, "algorithm"),
		HasMultiConcept: strings.Contains(lower, "和") || strings.Contains(lower, "与") ||
			strings.Contains(lower, "and") || strings.Contains(lower, "+") ||
			strings.Contains(lower, "以及") || strings.Contains(lower, "同时"),
	}
}

// retrievalBudget maps query complexity to search-stage capacities.
//
//	           BM25 top-N  Vector beam  Rerank N  返回
//	simple         40           80         40       8
//	medium         60          120         60       8
//	complex        80          200        100      10
func retrievalBudget(qf QueryFeatures) (bm25N, vecBeam, rerankN, returnN int) {
	switch {
	case qf.TermCount > 12 || (qf.TermCount > 6 && qf.HasComparison):
		return 80, 200, 100, 10
	case qf.TermCount > 6 || qf.HasMethod || qf.HasMultiConcept:
		return 60, 120, 60, 8
	default:
		return 40, 80, 40, 8
	}
}

// retrievalBudgetLabel returns a human-readable complexity label for logging.
func retrievalBudgetLabel(qf QueryFeatures) string {
	switch {
	case qf.TermCount > 12 || (qf.TermCount > 6 && qf.HasComparison):
		return "complex"
	case qf.TermCount > 6 || qf.HasMethod || qf.HasMultiConcept:
		return "medium"
	default:
		return "simple"
	}
}

// detectQueryType classifies a search query as "conceptual", "factual", or
// "balanced" using lightweight heuristics on the query text.
//
//   - Conceptual: ends with '?' or has a high verb-to-word ratio (questions,
//     how-to, explanations)
//   - Factual: high proportion of noun-like tokens (names, technical terms)
//   - Balanced: everything else
func detectQueryType(query string) string {
	fields := strings.Fields(query)
	if len(fields) < 2 {
		// Very short queries are treated as factual (likely a term lookup).
		return "factual"
	}

	// Check for question marker.
	trimmed := strings.TrimSpace(query)
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '?' {
		return "conceptual"
	}

	// Count common English verbs/auxiliaries as a proxy for "conceptual".
	verbs := map[string]bool{
		"is": true, "are": true, "was": true, "were": true,
		"has": true, "have": true, "had": true,
		"do": true, "does": true, "did": true,
		"can": true, "could": true, "will": true, "would": true,
		"shall": true, "should": true, "may": true, "might": true,
		"need": true, "want": true, "know": true, "use": true,
		"how": true, "why": true, "what": true, "which": true,
		"explain": true, "describe": true, "compare": true,
		"define": true, "list": true, "find": true, "show": true,
		"tell": true, "give": true, "write": true, "make": true,
		"get": true, "set": true, "create": true, "build": true,
		"generate": true, "implement": true, "configure": true,
	}

	verbCount := 0
	nounLike := 0
	for _, f := range fields {
		low := strings.ToLower(f)
		if verbs[low] {
			verbCount++
			continue
		}
		// Heuristic: longer lowercase words that aren't verbs are likely
		// nouns or technical terms.
		if len(low) > 3 {
			nounLike++
		}
	}

	verbRatio := float64(verbCount) / float64(len(fields))
	nounRatio := float64(nounLike) / float64(len(fields))

	if verbRatio >= 0.25 {
		return "conceptual"
	}
	if nounRatio >= 0.6 {
		return "factual"
	}
	return "balanced"
}

// -------------------- G9: Snippet deduplication --------------------

// deduplicateSnippets marks approximate-duplicate hits within the same document
// by setting the DuplicateOf field. Two hits are considered duplicates when they
// share the same DocSlug and their snippets have a Jaccard similarity ≥
// dedupJaccardThreshold (0.6). The lower-scoring hit is marked as a duplicate;
// the higher-scoring one is kept as canonical.
//
// The function does NOT remove entries; it only marks duplicates so callers
// can decide whether to filter them out in the UI.
func deduplicateSnippets(hits []SearchHit) []SearchHit {
	for i := 0; i < len(hits); i++ {
		if hits[i].DuplicateOf != "" {
			continue // already marked
		}
		for j := i + 1; j < len(hits); j++ {
			if hits[j].DuplicateOf != "" {
				continue
			}
			if hits[i].Document.ID != hits[j].Document.ID {
				continue
			}
			if snippetJaccard(hits[i].Content.Snippet, hits[j].Content.Snippet) >= dedupJaccardThreshold {
				// Mark the lower-scoring hit as a duplicate.
				if hits[i].Score >= hits[j].Score {
					hits[j].DuplicateOf = hits[i].Location.ChunkID
				} else {
					hits[i].DuplicateOf = hits[j].Location.ChunkID
					break // hits[i] is now a duplicate; no need to check further for i
				}
			}
		}
	}
	return hits
}

// snippetJaccard computes the Jaccard similarity between two snippet strings
// using word-level tokenisation (strings.Fields). Returns 0 for empty inputs.
func snippetJaccard(a, b string) float64 {
	tokensA := strings.Fields(a)
	tokensB := strings.Fields(b)
	if len(tokensA) == 0 && len(tokensB) == 0 {
		return 0
	}

	setA := make(map[string]bool, len(tokensA))
	for _, t := range tokensA {
		setA[t] = true
	}

	intersection := 0
	setB := make(map[string]bool, len(tokensB))
	for _, t := range tokensB {
		setB[t] = true
		if setA[t] {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// matchesMetaTags returns true when the document passes the tag filter:
// if filterTags is empty, all documents match (no filter applied);
// otherwise the document must have at least one tag that equal-folds to one of
// the filter tags.
func matchesMetaTags(docTags, filterTags []string) bool {
	if len(filterTags) == 0 {
		return true
	}
	for _, dt := range docTags {
		for _, ft := range filterTags {
			if strings.EqualFold(dt, ft) {
				return true
			}
		}
	}
	return false
}

// --- top-K partial sort (min-heap) ---

// topKHeap maintains the k highest scored items using a min-heap.
type topKHeap struct {
	items []rankedEntry
	k     int
}

func (h topKHeap) Len() int           { return len(h.items) }
func (h topKHeap) Less(i, j int) bool { return h.items[i].score < h.items[j].score }
func (h topKHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *topKHeap) Push(x any)        { h.items = append(h.items, x.(rankedEntry)) }
func (h *topKHeap) Pop() any {
	old := h.items
	n := len(old)
	x := old[n-1]
	h.items = old[:n-1]
	return x
}

// partialSortTopK keeps the top k highest-scored items and sorts them
// descending. When len(results) ≤ k it does a full sort; for large
// collections it uses a bounded min-heap to avoid O(n log n) sorting.
func partialSortTopK(results []rankedEntry, k int) []rankedEntry {
	if k <= 0 || len(results) <= k {
		sort.Slice(results, func(i, j int) bool {
			return results[i].score > results[j].score
		})
		return results
	}
	h := &topKHeap{items: make([]rankedEntry, 0, k), k: k}
	heap.Init(h)
	for i := range results {
		if h.Len() < k {
			heap.Push(h, results[i])
		} else if results[i].score > h.items[0].score {
			h.items[0] = results[i]
			heap.Fix(h, 0)
		}
	}
	// Extract in descending order.
	out := make([]rankedEntry, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(h).(rankedEntry)
	}
	return out
}
