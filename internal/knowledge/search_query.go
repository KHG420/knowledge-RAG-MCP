package knowledge

import (
	"strings"
)

func (s *Store) bm25Query(query string) string {
	log := s.logger.WithModule("search")
	qf := analyzeQuery(query)
	triage := TriageQuery(qf)

	var variants []string
	var rewriterName string

	switch triage {
	case TriageComplex:
		// Complex queries: use LLM rewriter if available, fall back to synonym.
		if s.llmRewriter != nil {
			variants = s.llmRewriter.Rewrite(query)
			rewriterName = "llm"
		} else if s.synonymRewriter != nil {
			variants = s.synonymRewriter.Rewrite(query)
			rewriterName = "synonym"
		}
	default: // TriageSimple, TriageMedium
		// Simple/medium queries: dictionary-only expansion (fast, stable).
		if s.synonymRewriter != nil {
			variants = s.synonymRewriter.Rewrite(query)
			rewriterName = "synonym"
		}
	}

	if len(variants) == 0 {
		log.Debugf("bm25Query: query=%q triage=%v terms=%d rewriter=none → no rewrite", query, triage, qf.TermCount)
		return query
	}

	result := strings.Join(variants, " ")
	log.Debugf("bm25Query: query=%q triage=%v terms=%d rewriter=%s variants=%d → %q",
		query, triage, qf.TermCount, rewriterName, len(variants), result)
	return result
}

// vectorQuery returns the query string used for vector embedding. It includes
// the original query plus dictionary related_terms for extra semantic signal,
// helping the dense retriever find conceptually relevant documents even when
// they use different terminology.
//
// Unlike bm25Query, related_terms are safe here because embedding models handle
// semantic similarity natively — loosely-related terms help rather than hurt.
func (s *Store) vectorQuery(query string) string {
	log := s.logger.WithModule("search")
	related := s.GetDictionaryRelatedTerms()
	if len(related) == 0 {
		log.Debugf("vectorQuery: query=%q → no related terms", query)
		return query
	}
	result := query + " " + strings.Join(related, " ")
	log.Debugf("vectorQuery: query=%q related=%d → %q", query, len(related), result)
	return result
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
