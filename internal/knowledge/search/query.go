package search

import (
	"strings"

	"knowledge-mcp/internal/knowledge"
)

// ── Query rewriting ─────────────────────────────────────────────────────────

// bm25Query returns the BM25 query string after triage-aware rewriting.
func (e *Engine) bm25Query(query string) string {
	log := e.logger.WithModule("search")
	qf := analyzeQuery(query)
	triage := knowledge.TriageQuery(qf)

	var variants []string
	var rewriterName string

	switch triage {
	case knowledge.TriageComplex:
		if e.llmRewriter != nil {
			variants = e.llmRewriter.Rewrite(query)
			rewriterName = "llm"
		} else if e.synonymRewriter != nil {
			variants = e.synonymRewriter.Rewrite(query)
			rewriterName = "synonym"
		}
	default: // TriageSimple, TriageMedium
		if e.synonymRewriter != nil {
			variants = e.synonymRewriter.Rewrite(query)
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

// vectorQuery returns the query string used for vector embedding, appending
// dictionary related_terms for extra semantic signal.
func (e *Engine) vectorQuery(query string) string {
	log := e.logger.WithModule("search")
	related := e.dictRelatedTerms
	if len(related) == 0 {
		log.Debugf("vectorQuery: query=%q → no related terms", query)
		return query
	}
	result := query + " " + strings.Join(related, " ")
	log.Debugf("vectorQuery: query=%q related=%d → %q", query, len(related), result)
	return result
}

// ── G5: Adaptive RRF weighting ──────────────────────────────────────────────

func adaptiveRRFWeight(query string) float64 {
	qtype := detectQueryType(query)
	switch qtype {
	case "conceptual":
		return 0.4
	case "factual":
		return 0.6
	default:
		return 0.5
	}
}

// ── v4: Pure-rule complexity & retrieval budget ─────────────────────────────

// analyzeQuery extracts structural features from a query string using pure text rules.
func analyzeQuery(query string) knowledge.QueryFeatures {
	lower := strings.ToLower(query)
	terms := strings.Fields(query)

	return knowledge.QueryFeatures{
		TermCount: len(terms),
		HasComparison: strings.Contains(lower, "比较") || strings.Contains(lower, "对比") ||
			strings.Contains(lower, "差异") || strings.Contains(lower, "vs") ||
			strings.Contains(lower, "compare") || strings.Contains(lower, "versus") ||
			strings.Contains(lower, "区别"),
		HasMethod: strings.Contains(lower, "方法") || strings.Contains(lower, "method") ||
			strings.Contains(lower, "模型") ||
			strings.Contains(lower, "计算") || strings.Contains(lower, "compute") ||
			strings.Contains(lower, "算法") || strings.Contains(lower, "algorithm"),
		HasMultiConcept: strings.Contains(lower, "和") || strings.Contains(lower, "与") ||
			strings.Contains(lower, "and") || strings.Contains(lower, "+") ||
			strings.Contains(lower, "以及") || strings.Contains(lower, "同时"),
	}
}

func retrievalBudget(qf knowledge.QueryFeatures) (bm25N, vecBeam, rerankN, returnN int) {
	switch {
	case qf.TermCount > 12 || (qf.TermCount > 6 && qf.HasComparison):
		return 80, 200, 100, 10
	case qf.TermCount > 6 || qf.HasMethod || qf.HasMultiConcept:
		return 60, 120, 60, 8
	default:
		return 40, 80, 40, 8
	}
}

func retrievalBudgetLabel(qf knowledge.QueryFeatures) string {
	switch {
	case qf.TermCount > 12 || (qf.TermCount > 6 && qf.HasComparison):
		return "complex"
	case qf.TermCount > 6 || qf.HasMethod || qf.HasMultiConcept:
		return "medium"
	default:
		return "simple"
	}
}

func detectQueryType(query string) string {
	fields := strings.Fields(query)
	if len(fields) < 2 {
		return "factual"
	}

	trimmed := strings.TrimSpace(query)
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '?' {
		return "conceptual"
	}

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
