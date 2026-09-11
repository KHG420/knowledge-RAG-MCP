package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strconv"
	"time"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/search/retrieval"
)

// ── Coarse-to-fine filter (G14) ─────────────────────────────────────────────

func (e *Engine) coarseToFineFilter(query string, entries []searchEntry) ([]searchEntry, error) {
	if len(entries) == 0 {
		return entries, nil
	}

	type secKey struct {
		slug  string
		secID string
	}
	secSet := map[secKey]bool{}
	for _, en := range entries {
		if en.sectionChunkID != "" {
			secSet[secKey{en.docSlug, en.sectionChunkID}] = true
		}
	}
	if len(secSet) == 0 {
		return entries, nil
	}

	type secEntry struct {
		key     secKey
		terms   map[string]int
		termLen int
	}
	var secEntries []secEntry
	for key := range secSet {
		content, readErr := e.chunkStore.ReadSectionChunk(key.slug, key.secID)
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

	queryTerms, err := retrieval.QueryTerms(query)
	if err != nil || len(queryTerms) == 0 {
		return entries, nil
	}

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

	filtered := make([]searchEntry, 0, len(entries))
	for _, en := range entries {
		if en.sectionChunkID == "" || topSections[secKey{en.docSlug, en.sectionChunkID}] {
			filtered = append(filtered, en)
		}
	}
	return filtered, nil
}

// ── Reranking ──────────────────────────────────────────────────────────────

func (e *Engine) rerankTop(query string, entries []searchEntry, scores []float64, limit int) ([]searchEntry, []float64) {
	if e.reranker == nil || len(entries) == 0 {
		return entries, scores
	}

	candLimit := e.rerankCandidateLimit
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

	texts := make([]string, n)
	for i := 0; i < n; i++ {
		if entries[i].text == "" {
			text, readErr := e.chunkStore.ReadChunk(entries[i].docSlug, entries[i].chunkID)
			if readErr != nil {
				text = ""
			}
			entries[i].text = text
		}
		texts[i] = entries[i].text
	}

	rerankKey := rerankCacheKey(query, texts[:n])
	if e.rerankState != nil {
		e.rerankState.Lock()
		if e.rerankState.Cache() != nil {
			if cachedScores, ok := e.rerankState.Cache()[rerankKey]; ok {
				e.rerankState.Unlock()
				e.logger.Debugf("[rerank] cache hit: query=%q candidates=%d", query, n)
				return sortByRerankScores(entries[:n], cachedScores)
			}
		}
		e.rerankState.Unlock()
	}

	e.logger.Debugf("[rerank] rerankTop query=%q candidates=%d candLimit=%d", query, len(entries), candLimit)

	rerankTimeout := 30 * time.Second
	if ir, ok := e.reranker.(*knowledge.InfinityReranker); ok {
		rerankTimeout = ir.Timeout() + 5*time.Second
	}
	rerankCtx, rerankCancel := context.WithTimeout(context.Background(), rerankTimeout)
	defer rerankCancel()

	batchSize := e.rerankBatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	if envBatch := os.Getenv("RERANK_BATCH_SIZE"); envBatch != "" {
		if v, err := strconv.Atoi(envBatch); err == nil && v > 0 {
			batchSize = v
		}
	}

	rerankScores := make([]float64, 0, n)
	for batchStart := 0; batchStart < n; batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > n {
			batchEnd = n
		}
		batchScores, batchErr := e.reranker.Rerank(rerankCtx, query, texts[batchStart:batchEnd])
		if batchErr != nil || len(batchScores) != batchEnd-batchStart {
			e.logger.Warnf("[rerank] rerankTop failed: %v (scores=%d, expected=%d), trying vector similarity fallback", batchErr, len(batchScores), batchEnd-batchStart)

			if e.embedder != nil {
				hasVec := false
				for i := 0; i < n && !hasVec; i++ {
					if len(entries[i].vector) > 0 {
						hasVec = true
					}
				}
				if hasVec {
					qVec, embedErr := e.embedder.Embed(rerankCtx, []string{query})
					if embedErr == nil && len(qVec) == 1 && len(qVec[0]) > 0 {
						qVec64 := make([]float64, len(qVec[0]))
						for i, v := range qVec[0] {
							qVec64[i] = float64(v)
						}
						vecScores := make([]float64, n)
						for i := 0; i < n; i++ {
							vecScores[i] = cosineSimilarity(qVec64, entries[i].vector)
						}
						e.logger.Infof("[rerank] using vector similarity fallback for %d candidates", n)
						return entries[:n], vecScores
					}
				}
			}
			e.logger.Warnf("[rerank] vector similarity fallback failed, using BM25 scores")
			return entries[:n], scores[:n]
		}
		rerankScores = append(rerankScores, batchScores...)
	}

	e.logger.Debugf("[rerank] rerankTop done candidates=%d batchSize=%d batches=%d", n, batchSize, (n+batchSize-1)/batchSize)

	e.cacheRerankResult(rerankKey, rerankScores)

	return sortByRerankScores(entries[:n], rerankScores)
}

func sortByRerankScores(entries []searchEntry, scores []float64) ([]searchEntry, []float64) {
	n := len(entries)
	type reranked struct {
		entry searchEntry
		score float64
	}
	combined := make([]reranked, n)
	for i := 0; i < n; i++ {
		score := scores[i]
		if entries[i].sectionRole == "references" {
			score *= 0.8
		}
		combined[i] = reranked{entry: entries[i], score: score}
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

func (e *Engine) cacheRerankResult(key string, scores []float64) {
	if e.rerankState == nil {
		return
	}
	e.rerankState.WLock()
	defer e.rerankState.WUnlock()
	cache := e.rerankState.Cache()
	if cache == nil {
		e.rerankState.SetCache(make(map[string][]float64))
		cache = e.rerankState.Cache()
	}
	const maxEntries = 256
	if len(cache) >= maxEntries {
		n := 0
		for k := range cache {
			delete(cache, k)
			n++
			if n >= maxEntries/2 {
				break
			}
		}
	}
	cache[key] = scores
}
