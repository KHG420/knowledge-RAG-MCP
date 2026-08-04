// Package search implements the retrieval pipeline: BM25, vector, hybrid search,
// coarse-to-fine filtering, and reranking. It is extracted from the Store God
// Object (REFACTOR_PLAN Phase 3.2).
//
// Engine implements knowledge.Searcher and depends on knowledge.ChunkStore for
// data access, knowledge.StorageBackend for inverted index operations, and
// cache.Cache for result caching.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
	"knowledge-mcp/internal/knowledge/search/retrieval"
)

// Engine manages the full retrieval pipeline: BM25, vector, hybrid,
// coarse-to-fine filtering, and reranking.
//
// It implements knowledge.Searcher.
type Engine struct {
	// ── Data access ──
	chunkStore knowledge.ChunkStore     // read-side access to chunk storage
	backend    knowledge.StorageBackend // for inverted index operations

	// ── KB identity ──
	kbName string // current knowledge base name

	// ── Embedding & reranking ──
	embedder knowledge.Embedder
	reranker knowledge.Reranker

	// ── GPU scheduler ──
	gpuScheduler *knowledge.GPUScheduler // shared GPU resource management

	// ── Query rewriting ──
	synonymRewriter *knowledge.SynonymRewriter  // always-on dictionary-based expansion
	llmRewriter     *knowledge.LLMQueryRewriter // optional LLM rewriter for complex queries

	// ── Dictionary ──
	dictRelatedTerms []string // related_terms from dictionaries/*.yaml

	// ── Rerank params ──
	rerankCandidateLimit int // max candidates fed to reranker (default 100)
	rerankBatchSize      int // max documents per reranker request (default 20)

	// ── Vector index ──
	vectorIndex *knowledge.HNSWIndex          // per-KB vector index for fast ANN search
	vecState    *knowledge.VectorIndexState   // shared vector index cache
	rerankState *knowledge.RerankCacheState   // shared rerank result cache

	// ── Search settings ──
	searchMode    string  // "hybrid", "bm25", "vector"
	rerankEnabled bool    // whether reranker is enabled
	rrfK          float64 // RRF fusion constant (default 60)
	bm25K1        float64 // BM25 k1 parameter (default 1.2)
	bm25B         float64 // BM25 b parameter  (default 0.75)
	abstractBoost float64 // G13: multiplier for abstract-section chunks (default 1.1)

	// ── Logging ──
	searchLogger knowledge.SearchLogger

	// ── Cache ──
	cacheClient   knowledge.CacheClient
	queryCacheTTL time.Duration

	// ── Infrastructure ──
	logger *logging.Logger
	mu     *sync.Mutex
}

// New creates an Engine with sensible defaults.
func New(mu *sync.Mutex, logger *logging.Logger) *Engine {
	return &Engine{
		searchMode:          "hybrid",
		rerankEnabled:       true,
		rrfK:                60.0,
		bm25K1:              1.2,
		bm25B:               0.75,
		rerankCandidateLimit: 100,
		rerankBatchSize:      20,
		abstractBoost:        1.1,
		queryCacheTTL:        5 * time.Minute,
		logger:               logger,
		mu:                   mu,
	}
}

// ── Wiring ───────────────────────────────────────────────────────────────────

// SetChunkStore sets the ChunkStore for data access during search.
func (e *Engine) SetChunkStore(cs knowledge.ChunkStore) { e.chunkStore = cs }

// SetBackend sets a StorageBackend for inverted index operations.
func (e *Engine) SetBackend(b knowledge.StorageBackend) { e.backend = b }

// SetGPUScheduler sets the shared GPU scheduler.
func (e *Engine) SetGPUScheduler(gs *knowledge.GPUScheduler) { e.gpuScheduler = gs }

// SetKBName sets the current knowledge base name.
func (e *Engine) SetKBName(name string) { e.kbName = name }

// KBName returns the current knowledge base name.
func (e *Engine) KBName() string { return e.kbName }

// ── Embedder ─────────────────────────────────────────────────────────────────

// SetEmbedder configures the vector embedder.
func (e *Engine) SetEmbedder(emb knowledge.Embedder) { e.embedder = emb }

// Embedder returns the configured vector embedder, or nil if none.
func (e *Engine) Embedder() knowledge.Embedder { return e.embedder }

// EmbedderInfo returns information about the configured embedder.
func (e *Engine) EmbedderInfo() map[string]any {
	if e.embedder == nil {
		return nil
	}
	info := map[string]any{"dim": e.embedder.Dim()}
	if oe, ok := e.embedder.(*knowledge.OpenAIEmbedder); ok {
		info["endpointURL"] = oe.EndpointURL()
		info["model"] = oe.Model()
	}
	return info
}

// ── Reranker ─────────────────────────────────────────────────────────────────

// SetReranker configures the cross-encoder reranker.
func (e *Engine) SetReranker(r knowledge.Reranker) { e.reranker = r }

// SetRerankCandidateLimit sets the max candidates passed to the reranker.
func (e *Engine) SetRerankCandidateLimit(n int) { e.rerankCandidateLimit = n }

// SetRerankBatchSize sets the max documents per reranker request.
func (e *Engine) SetRerankBatchSize(n int) { e.rerankBatchSize = n }

// RerankerInfo returns information about the configured reranker.
func (e *Engine) RerankerInfo() map[string]any {
	if e.reranker == nil {
		return nil
	}
	if ir, ok := e.reranker.(*knowledge.InfinityReranker); ok {
		return map[string]any{
			"endpointURL": ir.EndpointURL(),
			"model":       ir.Model(),
		}
	}
	return map[string]any{"type": "custom"}
}

// RerankCandidateLimit returns the configured reranker candidate limit.
func (e *Engine) RerankCandidateLimit() int { return e.rerankCandidateLimit }

// ── Search logger ────────────────────────────────────────────────────────────

// SetSearchLogger configures an optional search logger.
func (e *Engine) SetSearchLogger(l knowledge.SearchLogger) { e.searchLogger = l }

// ── Dictionary injection ─────────────────────────────────────────────────────

// SetDictionaryRelatedTerms sets related terms from dictionaries for query expansion.
func (e *Engine) SetDictionaryRelatedTerms(terms []string) { e.dictRelatedTerms = terms }

// SetSynonymRewriter injects the synonym-based query rewriter.
func (e *Engine) SetSynonymRewriter(rw *knowledge.SynonymRewriter) { e.synonymRewriter = rw }

// SetLLMRewriter injects the LLM-based query rewriter.
func (e *Engine) SetLLMRewriter(rw *knowledge.LLMQueryRewriter) { e.llmRewriter = rw }

// ── Search mode & tunables ──────────────────────────────────────────────────

// GetSearchMode returns the current search mode.
func (e *Engine) GetSearchMode() string { return e.searchMode }

// SetSearchMode sets the search mode ("bm25", "vector", "hybrid").
func (e *Engine) SetSearchMode(mode string) { e.searchMode = mode }

// SetRRFK sets the RRF fusion constant.
func (e *Engine) SetRRFK(k float64) { e.rrfK = k }

// GetRRFK returns the RRF fusion constant.
func (e *Engine) GetRRFK() float64 { return e.rrfK }

// SetBM25K1 sets the BM25 k1 parameter.
func (e *Engine) SetBM25K1(k1 float64) { e.bm25K1 = k1 }

// GetBM25K1 returns the BM25 k1 parameter.
func (e *Engine) GetBM25K1() float64 { return e.bm25K1 }

// SetBM25B sets the BM25 b parameter.
func (e *Engine) SetBM25B(b float64) { e.bm25B = b }

// GetBM25B returns the BM25 b parameter.
func (e *Engine) GetBM25B() float64 { return e.bm25B }

// SetAbstractBoost sets the abstract-section chunk score multiplier.
func (e *Engine) SetAbstractBoost(b float64) { e.abstractBoost = b }

// ── Cache coordination ──────────────────────────────────────────────────────

// SetCacheClient configures the cache backend for search result caching.
func (e *Engine) SetCacheClient(client knowledge.CacheClient) { e.cacheClient = client }

// SetQueryCacheTTL sets the TTL for exact query cache entries.
func (e *Engine) SetQueryCacheTTL(d time.Duration) { e.queryCacheTTL = d }

// cacheEnabled reports whether the cache layer is available.
func (e *Engine) cacheEnabled() bool { return e.cacheClient != nil }

// InvalidateDoc removes all cached search results that may contain the given document slug.
func (e *Engine) InvalidateDoc(docSlug string) {
	if e.cacheClient == nil {
		return
	}
	_, _ = e.cacheClient.DeletePattern(context.Background(), "*:"+docSlug+":*")
}

// ── Vector index access ─────────────────────────────────────────────────────

// VectorIndex returns the current HNSW vector index (may be nil).
func (e *Engine) VectorIndex() *knowledge.HNSWIndex { return e.vectorIndex }

// SetVectorIndex replaces the vector index (used by Store.EnsureVectorIndex).
func (e *Engine) SetVectorIndex(idx *knowledge.HNSWIndex) { e.vectorIndex = idx }

// VecState returns the shared vector index state for cross-KB sharing.
func (e *Engine) VecState() *knowledge.VectorIndexState { return e.vecState }

// SetVecState sets the shared vector index state.
func (e *Engine) SetVecState(vs *knowledge.VectorIndexState) { e.vecState = vs }

// RerankState returns the shared rerank cache state.
func (e *Engine) RerankState() *knowledge.RerankCacheState { return e.rerankState }

// SetRerankState sets the shared rerank cache state.
func (e *Engine) SetRerankState(rs *knowledge.RerankCacheState) { e.rerankState = rs }

// ── Inverted index (write paths) ──────────────────────────────────────────

// RebuildInvertedIndex scans all documents in the current KB and rebuilds the
// global inverted index from scratch. Non-fatal: individual document failures
// are skipped.
func (e *Engine) RebuildInvertedIndex() error {
	if e.backend == nil {
		return nil
	}
	start := time.Now()
	slugs, err := e.backend.ListDocSlugs(e.kbName)
	if err != nil {
		return err
	}
	idx := knowledge.NewInvertedIndex()
	for _, slug := range slugs {
		if e.chunkStore == nil {
			continue
		}
		index, idxErr := e.chunkStore.ReadChunksIndex(slug)
		if idxErr != nil || index == nil {
			continue
		}
		for _, entry := range index.Chunks {
			for _, tf := range entry.Terms {
				idx.Index[tf.Term] = append(idx.Index[tf.Term], knowledge.Posting{
					DocSlug: slug,
					ChunkID: entry.ID,
					TF:      tf.Count,
				})
			}
		}
	}
	e.logger.WithModule("search").Debugf("RebuildInvertedIndex: docs=%d terms=%d elapsed=%v", len(slugs), len(idx.Index), time.Since(start))
	return e.backend.WriteInvertedIndex(e.kbName, idx)
}

// UpdateInvertedIndex removes all postings for the given document and re-adds
// entries from its current chunks index. This is called after WriteChunksIndex
// to keep the inverted index in sync.
func (e *Engine) UpdateInvertedIndex(docSlug string) error {
	if e.backend == nil {
		return nil
	}
	idx, err := e.loadInvertedIndex()
	if err != nil || idx == nil {
		idx = knowledge.NewInvertedIndex()
	}

	// Remove all existing postings for this document.
	for term, postings := range idx.Index {
		filtered := postings[:0]
		for _, p := range postings {
			if p.DocSlug != docSlug {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			delete(idx.Index, term)
		} else {
			idx.Index[term] = filtered
		}
	}

	// Add new postings from current chunks index.
	if e.chunkStore != nil {
		chunksIndex, idxErr := e.chunkStore.ReadChunksIndex(docSlug)
		if idxErr == nil && chunksIndex != nil {
			for _, entry := range chunksIndex.Chunks {
				for _, tf := range entry.Terms {
					idx.Index[tf.Term] = append(idx.Index[tf.Term], knowledge.Posting{
						DocSlug: docSlug,
						ChunkID: entry.ID,
						TF:      tf.Count,
					})
				}
			}
		}
	}

	return e.backend.WriteInvertedIndex(e.kbName, idx)
}

// =============================================================================
// Core Search Methods — migrated from Store
// =============================================================================

// Search performs a pure BM25 keyword search with query rewriting, coarse-to-fine
// filtering, and optional cross-encoder reranking.
func (e *Engine) Search(ctx context.Context, question string, limit int, filter knowledge.SearchFilter) ([]knowledge.SearchHit, error) {
	if limit <= 0 {
		limit = 8
	}

	// Check query cache.
	if e.cacheEnabled() && limit > 0 {
		qhash := cacheQueryHash(question, "bm25", limit, filter.SourceType, filter.Section)
		ckey := cacheQueryKey(e.kbName, qhash)
		if raw, cerr := e.cacheClient.Get(ctx, ckey); cerr == nil && raw != nil {
			var hits []knowledge.SearchHit
			if json.Unmarshal(raw, &hits) == nil {
				e.logger.WithModule("cache").Debugf("query HIT: mode=bm25 query=%q hash=%s hits=%d", question, qhash, len(hits))
				return hits, nil
			}
		}
		e.logger.WithModule("cache").Debugf("query MISS: mode=bm25 query=%q hash=%s", question, qhash)
	}

	hits, err := e.searchImpl(ctx, question, limit, filter)
	if err != nil {
		return hits, err
	}

	// Store in query cache.
	if e.cacheEnabled() && limit > 0 && len(hits) > 0 {
		qhash := cacheQueryHash(question, "bm25", limit, filter.SourceType, filter.Section)
		ckey := cacheQueryKey(e.kbName, qhash)
		if raw, jerr := json.Marshal(hits); jerr == nil {
			if setErr := e.cacheClient.Set(ctx, ckey, raw, e.queryCacheTTL); setErr != nil {
				e.logger.WithModule("cache").Warnf("query SET failed: mode=bm25 query=%q err=%v", question, setErr)
			} else {
				e.logger.WithModule("cache").Debugf("query SET: mode=bm25 query=%q hash=%s hits=%d ttl=%v", question, qhash, len(hits), e.queryCacheTTL)
			}
		}
	}
	return hits, nil
}

// searchImpl contains the actual BM25 scoring pipeline.
func (e *Engine) searchImpl(ctx context.Context, question string, limit int, filter knowledge.SearchFilter) ([]knowledge.SearchHit, error) {
	start := time.Now()
	log := e.logger.WithModule("search")
	log.Debugf("searchImpl: query=%q limit=%d kb=%q", question, limit, e.kbName)
	defer func() {
		log.Debugf("searchImpl done in %v", time.Since(start))
	}()

	bm25QueryStr := e.bm25Query(question)
	queryTerms, err := retrieval.QueryTerms(bm25QueryStr)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	entries, err := e.collectEntries(filter, queryTerms)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// G14: coarse-to-fine
	if filter.Coarse {
		log.Debugf("coarseToFineFilter: entries before=%d", len(entries))
		entries, err = e.coarseToFineFilter(question, entries)
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
	for i, en := range entries {
		docs[i] = en.terms
		lengths[i] = en.termLen
		totalLen += en.termLen
	}
	df := retrieval.DocumentFrequency(docs)
	avgLen := float64(totalLen) / float64(len(entries))

	var results []rankedEntry
	for i, en := range entries {
		score := retrieval.BM25Score(docs[i], lengths[i], queryTerms, df, len(entries), avgLen)
		if score > 0 {
			results = append(results, rankedEntry{entry: en, score: score})
		}
	}

	// Abstract boost
	for i := range results {
		if results[i].entry.isPaper && results[i].entry.sectionRole == "abstract" {
			results[i].score *= e.abstractBoost
		}
	}

	topN := limit * 5
	if topN < 200 {
		topN = 200
	}
	results = partialSortTopK(results, topN)
	results = keepTopRelativeScore(results, 0.15)

	// Load chunk text
	for i := range results {
		if results[i].entry.text == "" {
			text, readErr := e.chunkStore.ReadChunk(results[i].entry.docSlug, results[i].entry.chunkID)
			if readErr != nil {
				text = ""
			}
			results[i].entry.text = text
		}
	}

	// Reranker
	if e.reranker != nil && len(results) > 0 {
		if e.gpuScheduler != nil {
			restore := e.gpuScheduler.PrepareForReranking()
			defer restore()
		}
		entries2 := make([]searchEntry, len(results))
		scores := make([]float64, len(results))
		for i, r := range results {
			entries2[i] = r.entry
			scores[i] = r.score
		}
		newEntries, newScores := e.rerankTop(question, entries2, scores, limit)
		results = make([]rankedEntry, len(newEntries))
		for i := range newEntries {
			results[i] = rankedEntry{entry: newEntries[i], score: newScores[i]}
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}

	// Convert to SearchHit
	hits := e.entriesToHits(results, question, queryTerms)
	return hits, nil
}

// SearchBM25 performs a pure BM25 keyword search without vector retrieval or reranking.
func (e *Engine) SearchBM25(ctx context.Context, question string, limit int, filter knowledge.SearchFilter) ([]knowledge.SearchHit, error) {
	start := time.Now()
	log := e.logger.WithModule("search")
	log.Debugf("SearchBM25: query=%q limit=%d kb=%q", question, limit, e.kbName)
	defer func() {
		log.Debugf("SearchBM25 done in %v", time.Since(start))
	}()

	if limit <= 0 {
		limit = 8
	}

	bm25QueryStr := e.bm25Query(question)
	queryTerms, err := retrieval.QueryTerms(bm25QueryStr)
	if err != nil {
		return nil, fmt.Errorf("search bm25: %w", err)
	}

	entries, err := e.collectEntries(knowledge.SearchFilter{}, queryTerms)
	if err != nil {
		return nil, fmt.Errorf("search bm25: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	docs := make([]map[string]int, len(entries))
	lengths := make([]int, len(entries))
	var totalLen int
	for i, en := range entries {
		docs[i] = en.terms
		lengths[i] = en.termLen
		totalLen += en.termLen
	}
	df := retrieval.DocumentFrequency(docs)
	avgLen := float64(totalLen) / float64(len(entries))

	var results []rankedEntry
	for i, en := range entries {
		score := retrieval.BM25Score(docs[i], lengths[i], queryTerms, df, len(entries), avgLen)
		if score > 0 {
			results = append(results, rankedEntry{entry: en, score: score})
		}
	}

	for i := range results {
		if results[i].entry.isPaper && results[i].entry.sectionRole == "abstract" {
			results[i].score *= e.abstractBoost
		}
	}

	topN := limit * 5
	if topN < 200 {
		topN = 200
	}
	results = partialSortTopK(results, topN)
	results = keepTopRelativeScore(results, 0.15)

	for i := range results {
		if results[i].entry.text == "" {
			text, readErr := e.chunkStore.ReadChunk(results[i].entry.docSlug, results[i].entry.chunkID)
			if readErr != nil {
				text = ""
			}
			results[i].entry.text = text
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}

	return e.entriesToHits(results, question, queryTerms), nil
}

// SearchAll searches across all knowledge bases and merges results.
// This is a facade-level concern — cross-KB coordination requires KB listing
// which is owned by KBAdmin, not the search engine. Store.SearchAll handles
// this by iterating KBs and delegating per-KB searches to Engine.Search.
func (e *Engine) SearchAll(ctx context.Context, question string, limit int, filter knowledge.SearchFilter) ([]knowledge.SearchHit, error) {
	// Cross-KB coordination requires KB listing which is owned by Store/KBAdmin.
	// Store.SearchAll handles this by delegating per-KB searches to Engine.Search.
	return nil, nil
}

// HybridSearch runs combined BM25 + dense embedding search using RRF fusion.
func (e *Engine) HybridSearch(ctx context.Context, question string, limit int, filter knowledge.SearchFilter) ([]knowledge.SearchHit, error) {
	log := e.logger.WithModule("search")
	log.Debugf("HybridSearch: query=%q limit=%d kb=%q embedder=%v", question, limit, e.kbName, e.embedder != nil)

	if limit <= 0 {
		limit = 8
	}

	qf := analyzeQuery(question)
	bm25N, vecBeam, rerankN, budgetReturn := retrievalBudget(qf)
	if limit > budgetReturn {
		// respect user limit, use budget for internal stages
	}
	log.Debugf("HybridSearch: budget=%s bm25N=%d vecBeam=%d rerankN=%d return=%d",
		retrievalBudgetLabel(qf), bm25N, vecBeam, rerankN, budgetReturn)

	bm25QueryStr := e.bm25Query(question)
	vectorQueryStr := e.vectorQuery(question)

	queryTerms, err := retrieval.QueryTerms(bm25QueryStr)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}

	// Check query cache.
	if e.cacheEnabled() && limit > 0 {
		qhash := cacheQueryHash(question, "hybrid", limit, filter.SourceType, filter.Section)
		ckey := cacheQueryKey(e.kbName, qhash)
		if raw, cerr := e.cacheClient.Get(ctx, ckey); cerr == nil && raw != nil {
			var cachedHits []knowledge.SearchHit
			if json.Unmarshal(raw, &cachedHits) == nil {
				log.Infof("query HIT  key=%s query=%q hash=%s hits=%d", ckey, question, qhash, len(cachedHits))
				return cachedHits, nil
			}
		}
		log.Infof("query MISS key=%s query=%q hash=%s", ckey, question, qhash)
	}

	entries, err := e.collectEntries(filter, queryTerms)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// G14: coarse-to-fine filter
	if filter.Coarse {
		log.Debugf("coarseToFineFilter: entries before=%d", len(entries))
		entries, err = e.coarseToFineFilter(question, entries)
		if err != nil {
			log.Warnf("coarseToFineFilter failed: %v, falling back to unfiltered", err)
		}
		log.Debugf("coarseToFineFilter: entries after=%d", len(entries))
		if len(entries) == 0 {
			return nil, nil
		}
	}

	// Phase 1.5: vector-side independent recall via HNSW ANN.
	var cosByKey map[string]float64

	if e.embedder != nil && e.vecState != nil {
		// EnsureVectorIndex is handled by Store — engine assumes the index is already built.
	}
	needDense := e.embedder != nil && e.vectorIndex != nil && e.vectorIndex.Len() > 0

	if needDense {
		var restoreEmb func()
		if e.gpuScheduler != nil {
			restoreEmb = e.gpuScheduler.PrepareForEmbedding()
		}

		queryVec, embedErr := e.embedder.Embed(ctx, []string{vectorQueryStr})
		if embedErr == nil && len(queryVec) > 0 && len(queryVec[0]) > 0 {
			qVec64 := make([]float64, len(queryVec[0]))
			for j, v := range queryVec[0] {
				qVec64[j] = float64(v)
			}

			vecK := vecBeam
			if vecK > e.vectorIndex.Len() {
				vecK = e.vectorIndex.Len()
			}

			hits := e.vectorIndex.Search(qVec64, vecK)
			cosByKey = make(map[string]float64, len(hits))
			for _, h := range hits {
				cosByKey[h.ID] = h.Score
			}
			log.Debugf("[search] hybrid: vector recall returned %d hits (beam=%d)", len(hits), vecK)

			existingKeys := make(map[string]bool, len(entries))
			for _, en := range entries {
				existingKeys[knowledge.VectorID(en.docSlug, en.chunkID)] = true
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

		if restoreEmb != nil {
			restoreEmb()
		}
	}

	hasVectors := false
	for _, en := range entries {
		if len(en.vector) > 0 {
			hasVectors = true
			break
		}
	}

	// Phase 2: BM25 scoring.
	docs := make([]map[string]int, len(entries))
	lengths := make([]int, len(entries))
	var totalLen int
	for i, en := range entries {
		docs[i] = en.terms
		lengths[i] = en.termLen
		totalLen += en.termLen
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
	for i, en := range entries {
		scored[i] = hybridRanked{
			entry:     en,
			bm25Score: retrieval.BM25Score(docs[i], lengths[i], queryTerms, df, len(entries), avgLen),
		}
	}

	// Phase 3: dense scoring.
	if len(cosByKey) > 0 {
		needFallback := false
		for i := range scored {
			key := knowledge.VectorID(scored[i].entry.docSlug, scored[i].entry.chunkID)
			if s, ok := cosByKey[key]; ok {
				scored[i].cosScore = s
			} else if len(scored[i].entry.vector) > 0 {
				needFallback = true
			}
		}
		if needFallback {
			queryVec, embedErr := e.embedder.Embed(ctx, []string{vectorQueryStr})
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
		e.logger.Debugf("[search] hybrid: dense scoring done (cosByKey=%d fallback=%v)", len(cosByKey), needFallback)
	} else if hasVectors && e.embedder != nil {
		var restoreEmb2 func()
		if e.gpuScheduler != nil {
			restoreEmb2 = e.gpuScheduler.PrepareForEmbedding()
		}
		queryVec, embedErr := e.embedder.Embed(ctx, []string{vectorQueryStr})
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
		e.logger.Debugf("[search] hybrid: brute-force cos_scores for %d candidates (no index)", len(scored))
	} else {
		e.logger.Debugf("[search] hybrid: dense scoring skipped (no vectors or no embedder)")
	}

	// Phase 3.5: abstract boost.
	for i := range scored {
		if scored[i].entry.isPaper && scored[i].entry.sectionRole == "abstract" {
			scored[i].bm25Score *= e.abstractBoost
		}
	}

	// Phase 4: RRF fusion.
	alpha := adaptiveRRFWeight(question)

	sort.Slice(scored, func(i, j int) bool { return scored[i].bm25Score > scored[j].bm25Score })
	for i := range scored {
		if scored[i].bm25Score > 0 {
			scored[i].rrfScore += alpha * (1.0 / (rrfK + float64(i)))
		}
	}

	sort.Slice(scored, func(i, j int) bool { return scored[i].cosScore > scored[j].cosScore })
	for i := range scored {
		if scored[i].cosScore > 0 {
			scored[i].rrfScore += (1 - alpha) * (1.0 / (rrfK + float64(i)))
		}
	}

	sort.Slice(scored, func(i, j int) bool { return scored[i].rrfScore > scored[j].rrfScore })

	var results []hybridRanked
	for _, r := range scored {
		if r.rrfScore > 0 {
			results = append(results, r)
		}
	}

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

	// Phase 8: reranker.
	e.logger.Debugf("[search] hybrid: reranking phase reranker=%v candidates=%d", e.reranker != nil, len(results))
	if e.reranker != nil && len(results) > 0 {
		if e.gpuScheduler != nil {
			restoreRerank := e.gpuScheduler.PrepareForReranking()
			defer restoreRerank()
		}
		rentries := make([]searchEntry, len(results))
		rscores := make([]float64, len(results))
		for i, r := range results {
			rentries[i] = r.entry
			rscores[i] = r.rrfScore
		}
		newEntries, newScores := e.rerankTop(question, rentries, rscores, limit)
		results = make([]hybridRanked, len(newEntries))
		for i := range newEntries {
			results[i] = hybridRanked{entry: newEntries[i], rrfScore: newScores[i]}
		}
		e.logger.Debugf("[search] hybrid: reranking done results=%d", len(newEntries))
	} else {
		if e.reranker == nil {
			e.logger.Debugf("[search] hybrid: reranking skipped (no reranker configured)")
		} else {
			e.logger.Debugf("[search] hybrid: reranking skipped (no results to rerank)")
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}

	// Read chunk text.
	for i := range results {
		if results[i].entry.text == "" {
			text, readErr := e.chunkStore.ReadChunk(results[i].entry.docSlug, results[i].entry.chunkID)
			if readErr != nil {
				text = ""
			}
			results[i].entry.text = text
		}
	}

	// Convert to SearchHit.
	hits := make([]knowledge.SearchHit, len(results))
	for i, r := range results {
		cid := fmt.Sprintf("%s_%s", r.entry.docSlug, r.entry.chunkID)
		hits[i] = knowledge.SearchHit{
			Score: r.rrfScore,
			Document: knowledge.DocumentInfo{
				ID:           r.entry.docSlug,
				Title:        r.entry.title,
				OriginalName: r.entry.originalName,
				Type:         r.entry.sourceType,
			},
			Location: knowledge.LocationInfo{
				ChunkID:   r.entry.chunkID,
				Section:   r.entry.section,
				Offset:    r.entry.offset,
				PageStart: r.entry.pageStart,
				PageEnd:   r.entry.pageEnd,
			},
			Content: knowledge.HitContent{
				Snippet:     retrieval.MakeSnippet(r.entry.text, question, queryTerms, 200),
				SectionRole: r.entry.sectionRole,
			},
			CitationID: cid,
		}
	}

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

	if e.searchLogger != nil {
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
		e.searchLogger.LogSearch(knowledge.SearchLogEntry{
			Query:     question,
			HitCount:  len(hits),
			HitIDs:    hitIDs,
			TopScores: topScores,
			Filter:    &filter,
			Timestamp: time.Now(),
		})
	}

	if e.cacheEnabled() && limit > 0 && len(hits) > 0 {
		qhash := cacheQueryHash(question, "hybrid", limit, filter.SourceType, filter.Section)
		ckey := cacheQueryKey(e.kbName, qhash)
		if raw, jerr := json.Marshal(hits); jerr == nil {
			if setErr := e.cacheClient.Set(ctx, ckey, raw, e.queryCacheTTL); setErr != nil {
				log.Warnf("query SET failed: key=%s query=%q err=%v", ckey, question, setErr)
			} else {
				log.Infof("query SET  key=%s query=%q hash=%s hits=%d ttl=%v size=%d", ckey, question, qhash, len(hits), e.queryCacheTTL, len(raw))
			}
		}
	}
	return hits, nil
}

// SearchVector performs a pure dense (ANN) vector search using the HNSW index.
func (e *Engine) SearchVector(ctx context.Context, question string, limit int, filter knowledge.SearchFilter) ([]knowledge.SearchHit, error) {
	log := e.logger.WithModule("search")
	log.Debugf("SearchVector: query=%q limit=%d kb=%q embedder=%v", question, limit, e.kbName, e.embedder != nil)

	if limit <= 0 {
		limit = 8
	}

	if e.embedder == nil {
		return nil, fmt.Errorf("vector search unavailable: no embedding model configured (set embedder in config)")
	}
	if e.vectorIndex == nil || e.vectorIndex.Len() == 0 {
		return nil, fmt.Errorf("vector search unavailable: vector index is empty (upload documents with an embedder configured to populate it)")
	}

	if e.gpuScheduler != nil {
		restoreVec := e.gpuScheduler.PrepareForEmbedding()
		defer restoreVec()
	}

	queryVec, embedErr := e.embedder.Embed(ctx, []string{e.vectorQuery(question)})
	if embedErr != nil || len(queryVec) == 0 || len(queryVec[0]) == 0 {
		return nil, fmt.Errorf("search vector: embed failed: %w", embedErr)
	}
	qVec64 := make([]float64, len(queryVec[0]))
	for j, v := range queryVec[0] {
		qVec64[j] = float64(v)
	}

	vecK := limit * 20
	if vecK < 300 {
		vecK = 300
	}
	if vecK > e.vectorIndex.Len() {
		vecK = e.vectorIndex.Len()
	}

	hits := e.vectorIndex.Search(qVec64, vecK)
	log.Debugf("[search] vector: ANN returned %d hits (beam=%d)", len(hits), vecK)

	results := make([]knowledge.SearchHit, 0, len(hits))
	seen := make(map[string]bool, len(hits))
	for _, h := range hits {
		if h.ID == "" || h.Score <= 0 {
			continue
		}
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true

		slug, chunkID, ok := knowledge.ParseVectorID(h.ID)
		if !ok {
			continue
		}

		if e.chunkStore.IsTombstoned(slug) {
			continue
		}

		text, readErr := e.chunkStore.ReadChunk(slug, chunkID)
		if readErr != nil {
			text = ""
		}

		queryTerms, _ := retrieval.QueryTerms(question)

		results = append(results, knowledge.SearchHit{
			Score: h.Score,
			Document: knowledge.DocumentInfo{
				ID: slug,
			},
			Location: knowledge.LocationInfo{
				ChunkID: chunkID,
			},
			Content: knowledge.HitContent{
				Snippet: retrieval.MakeSnippet(text, question, queryTerms, 200),
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
func (e *Engine) SearchDocuments(ctx context.Context, question string, limit int, filter knowledge.SearchFilter) ([]knowledge.DocumentHit, error) {
	log := e.logger.WithModule("search")
	log.Debugf("SearchDocuments: query=%q limit=%d kb=%q embedder=%v", question, limit, e.kbName, e.embedder != nil)
	start := time.Now()

	if limit <= 0 {
		limit = 8
	}

	chunkLimit := limit * 3
	var hits []knowledge.SearchHit
	var err error
	if e.embedder != nil {
		hits, err = e.HybridSearch(ctx, question, chunkLimit, filter)
	} else {
		hits, err = e.Search(ctx, question, chunkLimit, filter)
	}
	if err != nil {
		return nil, fmt.Errorf("search documents: %w", err)
	}
	if len(hits) == 0 {
		log.Debugf("SearchDocuments done: query=%q hits=0 elapsed=%v", question, time.Since(start))
		return nil, nil
	}

	type docGroup struct {
		maxScore float64
		chunks   []knowledge.SearchHit
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

	docs := make([]knowledge.DocumentHit, 0, len(groups))
	for slug, g := range groups {
		meta, metaErr := e.chunkStore.ReadMeta(slug)
		if metaErr != nil {
			continue
		}
		sort.Slice(g.chunks, func(i, j int) bool {
			return g.chunks[i].Score > g.chunks[j].Score
		})
		topN := 3
		if len(g.chunks) < topN {
			topN = len(g.chunks)
		}
		chunks := make([]knowledge.SearchHit, topN)
		copy(chunks, g.chunks[:topN])
		docs = append(docs, knowledge.DocumentHit{
			Score:     g.maxScore,
			DocSlug:   slug,
			DocMeta:   *meta,
			TopChunks: chunks,
		})
	}
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Score > docs[j].Score
	})
	if len(docs) > limit {
		docs = docs[:limit]
	}
	log.Debugf("SearchDocuments done: query=%q docs=%d elapsed=%v", question, len(docs), time.Since(start))
	return docs, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// entriesToHits converts ranked entries to SearchHit results.
func (e *Engine) entriesToHits(results []rankedEntry, query string, queryTerms []string) []knowledge.SearchHit {
	hits := make([]knowledge.SearchHit, len(results))
	for i, r := range results {
		cid := fmt.Sprintf("%s_%s", r.entry.docSlug, r.entry.chunkID)
		hits[i] = knowledge.SearchHit{
			Score: r.score,
			Document: knowledge.DocumentInfo{
				ID:           r.entry.docSlug,
				Title:        r.entry.title,
				OriginalName: r.entry.originalName,
				Type:         r.entry.sourceType,
			},
			Location: knowledge.LocationInfo{
				ChunkID:   r.entry.chunkID,
				Section:   r.entry.section,
				Offset:    r.entry.offset,
				PageStart: r.entry.pageStart,
				PageEnd:   r.entry.pageEnd,
			},
			Content: knowledge.HitContent{
				Snippet:     retrieval.MakeSnippet(r.entry.text, query, queryTerms, 200),
				SectionRole: r.entry.sectionRole,
			},
			CitationID: cid,
		}
	}

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

	if e.searchLogger != nil {
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
		e.searchLogger.LogSearch(knowledge.SearchLogEntry{
			Query:     query,
			HitCount:  len(hits),
			HitIDs:    hitIDs,
			TopScores: topScores,
			Timestamp: time.Now(),
		})
	}

	return hits
}

// keepTopRelativeScore trims entries whose score falls below a fraction of the top score.
func keepTopRelativeScore(results []rankedEntry, fraction float64) []rankedEntry {
	if len(results) == 0 {
		return results
	}
	threshold := results[0].score * fraction
	cutoff := len(results)
	for i, r := range results {
		if r.score < threshold {
			cutoff = i
			break
		}
	}
	return results[:cutoff]
}

// ── Cache helpers ────────────────────────────────────────────────────────────

func cacheQueryHash(query, mode string, limit int, sourceType, section string) string {
	// Simple hash using the query, mode, limit, and filter fields.
	return fmt.Sprintf("%s|%s|%d|%s|%s", query, mode, limit, sourceType, section)
}

func cacheQueryKey(kbName, hash string) string {
	return fmt.Sprintf("query:%s:%s", kbName, hash)
}

// Ensure knowledge.Searcher interface is satisfied.
var _ knowledge.Searcher = (*Engine)(nil)
