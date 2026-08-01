package knowledge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// SynonymCandidate represents a potential synonym pair discovered from
// search-log analysis. It captures the user's query term, a candidate
// synonym found in clicked documents, and a confidence score.
type SynonymCandidate struct {
	QueryTerm    string  `json:"query_term"`    // term from the user's query
	CandidateTerm string `json:"candidate_term"` // term found in clicked documents
	CoOccurCount int     `json:"co_occur_count"` // number of times they co-occurred
	Score        float64 `json:"score"`          // confidence score (0-1)
	ExampleQuery string  `json:"example_query"`  // example query where this was found
}

// MineSynonymsFromLog reads a search log JSONL file and discovers candidate
// synonym pairs by analyzing co-occurrence between user query terms and
// high-frequency terms in the top-ranked documents they clicked/viewed.
//
// The miner does NOT access chunk content on its own — it needs a termProvider
// to extract representative terms from hit documents. For a simple first pass
// without chunk content access, set termProvider to nil; the miner will fall
// back to analyzing query-term co-occurrence across multiple log entries.
//
//	minResults: minimum number of results to return (use 0 for all candidates)
func MineSynonymsFromLog(logPath string, minScore float64, minResults int) ([]SynonymCandidate, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("open search log: %w", err)
	}
	defer f.Close()

	// Phase 1: collect all queries and their hit document slugs.
	type logEntry struct {
		query   string
		hitSlugs []string
	}
	var entries []logEntry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 10<<20) // 10 MB max line
	for scanner.Scan() {
		var e SearchLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue // skip malformed lines
		}
		if e.Query == "" || len(e.HitIDs) == 0 {
			continue
		}

		slugs := make([]string, 0, len(e.HitIDs))
		for _, hid := range e.HitIDs {
			// HitID format: "slug/chunkID" — extract slug.
			if idx := strings.LastIndex(hid, "/"); idx > 0 {
				slug := hid[:idx]
				slugs = append(slugs, slug)
			}
		}
		if len(slugs) > 0 {
			entries = append(entries, logEntry{query: e.Query, hitSlugs: slugs})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan search log: %w", err)
	}

	if len(entries) < 2 {
		return nil, nil // not enough data
	}

	// Phase 2: build inverted index: query-term → {document-slug → count}
	// and document-term → {query → count} for co-occurrence scoring.
	// queryIndex: term (from query) → document slugs where it appears in hits
	queryIndex := make(map[string]map[string]int) // term → slug → count
	// docIndex: document slug → set of query terms that led to it
	docIndex := make(map[string]map[string]int) // slug → query term → count

	for _, entry := range entries {
		queryTerms := extractKeyTerms(entry.query)
		for _, slug := range entry.hitSlugs {
			for _, qt := range queryTerms {
				if queryIndex[qt] == nil {
					queryIndex[qt] = make(map[string]int)
				}
				queryIndex[qt][slug]++

				if docIndex[slug] == nil {
					docIndex[slug] = make(map[string]int)
				}
				docIndex[slug][qt]++
			}
		}
	}

	// Phase 3: discover candidate pairs.
	// For each document slug, find all query terms that led to it.
	// Pairs of query terms that frequently lead to the SAME document
	// are candidates for synonymy.
	type pairKey struct {
		a, b string
	}
	pairScores := make(map[pairKey]struct {
		count int
		example string
	})

	for _, qterms := range docIndex {
		termList := make([]string, 0, len(qterms))
		for t := range qterms {
			termList = append(termList, t)
		}
		// For each pair of query terms that share this document...
		for i := 0; i < len(termList); i++ {
			for j := i + 1; j < len(termList); j++ {
				a, b := termList[i], termList[j]
				if a > b {
					a, b = b, a // canonical order
				}
				pk := pairKey{a, b}
				entry := pairScores[pk]
				entry.count++
				if entry.example == "" {
					// Find an example query that contains one of the terms.
					for _, le := range entries {
						if strings.Contains(strings.ToLower(le.query), strings.ToLower(a)) ||
							strings.Contains(strings.ToLower(le.query), strings.ToLower(b)) {
							entry.example = le.query
							break
						}
					}
				}
				pairScores[pk] = entry
			}
		}
	}

	// Phase 4: score and filter candidates.
	var candidates []SynonymCandidate
	for pk, data := range pairScores {
		if data.count < 2 {
			continue // need at least 2 co-occurrences
		}
		// Score based on co-occurrence count relative to total entries.
		score := float64(data.count) / float64(len(entries))
		if score < minScore {
			continue
		}
		candidates = append(candidates, SynonymCandidate{
			QueryTerm:     pk.a,
			CandidateTerm: pk.b,
			CoOccurCount:  data.count,
			Score:         score,
			ExampleQuery:  data.example,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	if minResults > 0 && len(candidates) > minResults {
		candidates = candidates[:minResults]
	}

	return candidates, nil
}

// extractKeyTerms splits a query into meaningful terms, filtering out
// stopwords and very short tokens. It's a simplified version suitable
// for co-occurrence mining — not a full NLP pipeline.
func extractKeyTerms(query string) []string {
	// Split on whitespace and CJK boundaries.
	raw := strings.Fields(query)
	seen := make(map[string]bool)
	var terms []string

	for _, t := range raw {
		t = strings.ToLower(strings.Trim(t, ",.!?;:，。！？；：\"'（）()[]{}"))
		if len(t) < 2 {
			continue
		}
		// Skip common stopwords.
		if isStopword(t) {
			continue
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		terms = append(terms, t)
	}
	return terms
}

// isStopword returns true for common English and Chinese stopwords that
// are unlikely to be useful domain terms.
func isStopword(s string) bool {
	stopwords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "can": true, "shall": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"with": true, "at": true, "by": true, "from": true, "as": true,
		"into": true, "through": true, "during": true, "before": true,
		"after": true, "above": true, "below": true, "between": true,
		"and": true, "but": true, "or": true, "nor": true, "not": true,
		"so": true, "yet": true, "both": true, "either": true, "neither": true,
		"each": true, "every": true, "all": true, "any": true, "few": true,
		"more": true, "most": true, "other": true, "some": true, "such": true,
		"no": true, "only": true, "own": true, "same": true, "than": true,
		"too": true, "very": true, "just": true, "about": true, "also": true,
		"how": true, "what": true, "which": true, "who": true, "whom": true,
		"whose": true, "why": true, "when": true, "where": true,
		"this": true, "that": true, "these": true, "those": true,
		"it": true, "its": true, "he": true, "she": true, "they": true,
		"them": true, "their": true, "we": true, "you": true, "i": true,
		"me": true, "my": true, "your": true, "our": true,
		"的": true, "了": true, "在": true, "是": true, "我": true,
		"有": true, "和": true, "就": true, "不": true, "人": true,
		"都": true, "一": true, "个": true, "上": true, "也": true,
		"很": true, "到": true, "说": true, "要": true, "去": true,
		"你": true, "会": true, "着": true, "没有": true, "看": true,
		"好": true, "自己": true, "这": true, "他": true, "她": true,
		"它": true, "们": true, "那": true, "什么": true, "怎么": true,
		"如何": true, "为什么": true, "可以": true, "还是": true,
		"或者": true, "应该": true, "能够": true, "可能": true,
		"已经": true, "因为": true, "所以": true, "但是": true,
		"如果": true, "虽然": true, "而且": true, "然后": true,
		"计算": true, "方法": true, "模型": true, "问题": true,
		"进行": true, "使用": true, "通过": true, "需要": true,
		"以及": true, "同时": true, "比较": true, "差异": true,
		"区别": true, "不同": true,
	}
	return stopwords[s]
}

// FormatSynonymCandidates returns a human-readable report of discovered
// synonym candidates, suitable for review by a domain expert before
// adding to the dictionary.
func FormatSynonymCandidates(candidates []SynonymCandidate) string {
	if len(candidates) == 0 {
		return "No synonym candidates found (insufficient search data or no co-occurrence patterns).\n"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Discovered %d synonym candidates from search log:\n\n", len(candidates)))
	b.WriteString("Score   Co-occur   Query Term          Candidate Term       Example Query\n")
	b.WriteString("------  ---------  ------------------  -------------------  -------------\n")

	for _, c := range candidates {
		b.WriteString(fmt.Sprintf("%.3f   %-9d  %-18s  %-20s  %s\n",
			c.Score, c.CoOccurCount,
			truncate(c.QueryTerm, 18),
			truncate(c.CandidateTerm, 20),
			truncate(c.ExampleQuery, 50),
		))
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
