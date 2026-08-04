package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strconv"
	"time"
	"knowledge-mcp/internal/retrieval"
)

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
	if s.rerankState.cache != nil {
		s.rerankState.mu.RLock()
		if cachedScores, ok := s.rerankState.cache[rerankKey]; ok {
			s.rerankState.mu.RUnlock()
			s.logger.Debugf("[rerank] cache hit: query=%q candidates=%d", query, n)
			return sortByRerankScores(entries[:n], cachedScores)
		}
		s.rerankState.mu.RUnlock()
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
	s.rerankState.mu.Lock()
	defer s.rerankState.mu.Unlock()
	if s.rerankState.cache == nil {
		s.rerankState.cache = make(map[string][]float64)
	}
	// Bound cache to prevent unbounded memory growth — evict oldest half when
	// exceeding 256 entries.
	const maxEntries = 256
	if len(s.rerankState.cache) >= maxEntries {
		n := 0
		for k := range s.rerankState.cache {
			delete(s.rerankState.cache, k)
			n++
			if n >= maxEntries/2 {
				break
			}
		}
	}
	s.rerankState.cache[key] = scores
}

// bm25Query returns the query string used for BM25 tokenisation. It applies
// triage-aware rewrite: simple/medium queries use dictionary-only expansion,
// complex queries use LLM semantic expansion (falling back to dictionary when
// no LLM is configured).
//
// IMPORTANT: related_terms from dictionaries are NOT appended here — they are
// added only to the vector query (see vectorQuery) to avoid polluting BM25's
// exact keyword matching with loosely-related terms.
