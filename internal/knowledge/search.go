package knowledge

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"knowledge-mcp/internal/retrieval"
)

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
	HitIDs     []string      `json:"hit_ids,omitempty"`     // returned chunk IDs in ranked order
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
	kd := s.knowledgeDir()

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

	// Fallback: full scan (existing logic, unchanged).
	docDirs, err := listDocDirs(kd)
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
					docSlug:    slug,
					chunkID:    id,
					text:       text,
					terms:      retrieval.Counts(tokens),
					termLen:    len(tokens),
					sourceType: meta.SourceType,
					isPaper:    meta.IsPaper,
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
// ranked hits. It first tries the per-document CHUNKS.toml index for
// pre-computed term frequencies; documents without an index fall back to
// reading and tokenising every chunk file.
//
// An optional SearchFilter can be passed to narrow results by doc slug, source
// type, or section. When no filter is passed, all documents are searched.
//
// The limit caps the number of results; hits below 15% of the top score are
// trimmed via retrieval.KeepTopRelativeScore. Chunk text is loaded for all
// surviving candidates (for reranker and snippet), then the optional reranker
// re-scores the top candidates before the final cap.
func (s *Store) Search(query string, limit int, filters ...SearchFilter) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 8
	}

	// Query rewriting: if a QueryRewriter is configured, expand the query
	// with synonyms and merge all variants into a single set of unique terms.
	rewritten := s.rewrittenQueries(query)

	queryTerms, err := retrieval.QueryTerms(rewritten)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	// Resolve filter.
	var filter SearchFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

	entries, err := s.collectEntries(filter, queryTerms)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// G14: coarse-to-fine filter — reduce entries to those in top-3 sections.
	if filter.Coarse {
		entries, err = s.coarseToFineFilter(query, entries)
		if err != nil {
			// Non-fatal: fall back to unfiltered entries.
		}
		if len(entries) == 0 {
			return nil, nil
		}
	}

	// Phase 2: build document-frequency map and compute average length.
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

	// Phase 3: score each entry with BM25.
	type ranked struct {
		entry searchEntry
		score float64
	}
	var results []ranked
	for i, e := range entries {
		score := retrieval.BM25Score(docs[i], lengths[i], queryTerms, df, len(entries), avgLen)
		if score > 0 {
			results = append(results, ranked{entry: e, score: score})
		}
	}

	// Phase 3.5: abstract score boost. Chunks from the "abstract" section of
	// paper-like documents receive a modest boost (×AbstractBoost) because
	// abstracts condense the core contribution of a paper (G13).
	for i := range results {
		if results[i].entry.isPaper && results[i].entry.sectionRole == "abstract" {
			results[i].score *= s.AbstractBoost
		}
	}

	// Phase 4: sort descending by score.
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	// Phase 5: trim low-scoring noise.
	results = retrieval.KeepTopRelativeScore(results, 0.15, func(r ranked) float64 {
		return r.score
	})

	// Phase 6: load chunk text for index-path results (needed for reranker and snippet).
	for i := range results {
		if results[i].entry.text == "" {
			text, readErr := s.ReadChunk(results[i].entry.docSlug, results[i].entry.chunkID)
			if readErr != nil {
				text = "" // snippet will reflect the failure
			}
			results[i].entry.text = text
		}
	}

	// Phase 7: reranker (optional). If a Reranker is configured, re-score the
	// top results using a cross-encoder for improved precision.
	if s.reranker != nil && len(results) > 0 {
		entries := make([]searchEntry, len(results))
		scores := make([]float64, len(results))
		for i, r := range results {
			entries[i] = r.entry
			scores[i] = r.score
		}
		newEntries, newScores := s.rerankTop(query, entries, scores, limit)
		results = make([]ranked, len(newEntries))
		for i := range newEntries {
			results[i] = ranked{entry: newEntries[i], score: newScores[i]}
		}
	}

	// Phase 8: cap to limit.
	if len(results) > limit {
		results = results[:limit]
	}

	// Phase 9: convert to SearchHit slice with section/offset metadata.
	hits := make([]SearchHit, len(results))
	for i, r := range results {
		hits[i] = SearchHit{
			Score:       r.score,
			DocSlug:     r.entry.docSlug,
			ChunkID:     r.entry.chunkID,
			Snippet:     retrieval.MakeSnippet(r.entry.text, query, queryTerms, 200),
			Section:     r.entry.section,
			Offset:      r.entry.offset,
			SectionRole: r.entry.sectionRole,
		}
	}
	// Phase 9a: deduplicate overlapping snippets (G9).
	hits = deduplicateSnippets(hits)

	// Phase 9b: section hint — when multiple chunks from the same section appear
	// in the results, annotate them so the caller knows to read the full section.
	type sectionKey struct{ doc, heading string }
	secCount := make(map[sectionKey]int)
	for _, h := range hits {
		if h.Section != "" {
			secCount[sectionKey{h.DocSlug, h.Section}]++
		}
	}
	for i := range hits {
		if hits[i].Section != "" && secCount[sectionKey{hits[i].DocSlug, hits[i].Section}] >= 2 {
			hits[i].SectionHint = fmt.Sprintf("Multiple hits in section '%s'. Consider reading with level=section for full context.", hits[i].Section)
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
			hitIDs[i] = hits[i].ChunkID
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
	if limit <= 0 {
		limit = 8
	}

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

	entries, err := s.collectEntries(filter, queryTerms)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// G14: coarse-to-fine filter — reduce entries to those in top-3 sections.
	if filter.Coarse {
		entries, err = s.coarseToFineFilter(query, entries)
		if err != nil {
			// Non-fatal: fall back to unfiltered entries.
		}
		if len(entries) == 0 {
			return nil, nil
		}
	}

	// Check if any entry has a vector.
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

	// Phase 3: embedding scoring (if vectors are available).
	if hasVectors && s.embedder != nil {
		queryVec, embedErr := s.embedder.Embed(nil, []string{query})
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
	if s.reranker != nil && len(results) > 0 {
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
		hits[i] = SearchHit{
			Score:       r.rrfScore,
			DocSlug:     r.entry.docSlug,
			ChunkID:     r.entry.chunkID,
			Snippet:     retrieval.MakeSnippet(r.entry.text, query, queryTerms, 200),
			Section:     r.entry.section,
			Offset:      r.entry.offset,
			SectionRole: r.entry.sectionRole,
		}
	}
	// Phase 10a: deduplicate overlapping snippets (G9).
	hits = deduplicateSnippets(hits)

	// Phase 10b: section hint — when multiple chunks from the same section appear
	// in the results, annotate them so the caller knows to read the full section.
	type hsSectionKey struct{ doc, heading string }
	hsSecCount := make(map[hsSectionKey]int)
	for _, h := range hits {
		if h.Section != "" {
			hsSecCount[hsSectionKey{h.DocSlug, h.Section}]++
		}
	}
	for i := range hits {
		if hits[i].Section != "" && hsSecCount[hsSectionKey{hits[i].DocSlug, hits[i].Section}] >= 2 {
			hits[i].SectionHint = fmt.Sprintf("Multiple hits in section '%s'. Consider reading with level=section for full context.", hits[i].Section)
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
			hitIDs[i] = hits[i].ChunkID
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

// SearchDocuments performs document-level retrieval using MaxP aggregation.
// It first searches chunks (3×limit to get enough coverage), groups results
// by DocSlug, takes the highest chunk score per document (MaxP), and returns
// documents sorted by that score. Each document includes its top-3 chunks.
//
// Internally uses HybridSearch when an embedder is configured, or Search
// (pure BM25) otherwise.
// An optional SearchFilter can be passed to narrow results.
func (s *Store) SearchDocuments(query string, limit int, filters ...SearchFilter) ([]DocumentHit, error) {
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
		return nil, nil
	}

	// Group by DocSlug, track max score and top-3 chunks.
	type docGroup struct {
		maxScore float64
		chunks   []SearchHit
	}
	groups := map[string]*docGroup{}
	for _, h := range hits {
		g, ok := groups[h.DocSlug]
		if !ok {
			g = &docGroup{maxScore: h.Score}
			groups[h.DocSlug] = g
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

	rerankScores, rerankErr := s.reranker.Rerank(nil, query, texts)
	if rerankErr != nil || len(rerankScores) != n {
		return entries[:n], scores[:n]
	}

	// Sort candidates by reranker score descending.
	type reranked struct {
		entry searchEntry
		score float64
	}
	combined := make([]reranked, n)
	for i := 0; i < n; i++ {
		combined[i] = reranked{entry: entries[i], score: rerankScores[i]}
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

// resolveSourceType returns the source type for a slug, but only reads meta
// when the filter actually needs it (to avoid unnecessary I/O in the common
// no-filter path). Returns empty string when not needed.
func resolveSourceType(s *Store, slug string, filter SearchFilter) string {
	if filter.SourceType == "" {
		return "" // not needed by any filter
	}
	meta, err := s.ReadMeta(slug)
	if err != nil {
		return ""
	}
	return meta.SourceType
}

// rewrittenQueries applies the configured QueryRewriter and merges all
// rewritten query variants into a single query string for tokenisation.
// When no rewriter is configured, the original query is returned as-is.
func (s *Store) rewrittenQueries(query string) string {
	if s.rewriter == nil {
		return query
	}
	variants := s.rewriter.Rewrite(query)
	if len(variants) == 0 {
		return query
	}
	// Merge all variants into a single query string. Duplicate terms are
	// removed during tokenisation by retrieval.QueryTerms → retrieval.Unique.
	return strings.Join(variants, " ")
}

// listDocDirs returns the names of all document subdirectories under the
// knowledge directory.
func listDocDirs(kd string) ([]string, error) {
	entries, err := readDirNames(kd)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, name := range entries {
		if name == "INDEX.md" {
			continue
		}
		dirs = append(dirs, name)
	}
	return dirs, nil
}

// readDirNames is a thin wrapper around os.ReadDir that returns entry names.
func readDirNames(dir string) ([]string, error) {
	des, err := readDirSafe(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(des))
	for i, d := range des {
		names[i] = d.Name()
	}
	return names, nil
}

// readDirSafe is os.ReadDir but returns nil slice for non-existent dirs.
func readDirSafe(dir string) ([]os.DirEntry, error) {
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return des, err
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
			if hits[i].DocSlug != hits[j].DocSlug {
				continue
			}
			if snippetJaccard(hits[i].Snippet, hits[j].Snippet) >= dedupJaccardThreshold {
				// Mark the lower-scoring hit as a duplicate.
				if hits[i].Score >= hits[j].Score {
					hits[j].DuplicateOf = hits[i].ChunkID
				} else {
					hits[i].DuplicateOf = hits[j].ChunkID
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
