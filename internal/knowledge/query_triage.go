package knowledge

import "strings"

// QueryTriage classifies a search query into one of three complexity tiers,
// controlling which rewrite strategy is applied. The classification uses
// ONLY pure text rules — zero LLM calls, zero allocations beyond string ops.
//
// Design rationale (from optimization plan):
//
//	70% simple  → dictionary expansion only       (fast, stable)
//	25% medium  → SynonymRewriter variants          (cheap, reliable)
//	 5% complex → LLMQueryRewriter semantic rewrite (expensive, worth it)
type QueryTriage int

const (
	// TriageSimple: short, single-concept queries like "横摇阻尼计算".
	// Strategy: dictionary exact_synonyms + BM25, no LLM, no multi-variant.
	TriageSimple QueryTriage = iota

	// TriageMedium: multi-concept or method-oriented queries like
	// "横摇阻尼的Ikeda方法计算". Strategy: SynonymRewriter multi-variant.
	TriageMedium

	// TriageComplex: long, comparative, or multi-faceted queries like
	// "比较Ikeda方法和CFD方法在横摇阻尼计算中的差异".
	// Strategy: LLMQueryRewriter semantic expansion.
	TriageComplex
)

// String returns a human-readable label for logging.
func (t QueryTriage) String() string {
	switch t {
	case TriageSimple:
		return "simple"
	case TriageMedium:
		return "medium"
	case TriageComplex:
		return "complex"
	default:
		return "unknown"
	}
}

// TriageQuery classifies a raw user query into one of three complexity tiers.
// It reuses the structural features already computed by analyzeQuery (term count,
// comparison markers, method markers, multi-concept markers) so there is zero
// additional cost when called from the search path.
//
// Rules (conservative — err toward higher complexity):
//
//	Complex:  >12 terms, OR (>6 terms AND has comparison markers)
//	Medium:   >6 terms, OR has method markers, OR has multi-concept markers
//	Simple:   everything else (short, single-concept queries)
func TriageQuery(qf QueryFeatures) QueryTriage {
	switch {
	case qf.TermCount > 12 || (qf.TermCount > 6 && qf.HasComparison):
		return TriageComplex
	case qf.TermCount > 6 || qf.HasMethod || qf.HasMultiConcept:
		return TriageMedium
	default:
		return TriageSimple
	}
}

// TriageQueryStr is a convenience wrapper that parses and classifies a query
// string in one call. Use TriageQuery(analyzeQuery(q)) when you already have
// QueryFeatures computed.
func TriageQueryStr(query string) QueryTriage {
	return TriageQuery(analyzeQuery(query))
}

// queryTriageMarkers returns a set of comparison/discourse markers that indicate
// a query is likely comparative or multi-faceted. This is used by the triage
// logic to ensure we don't miss conceptual comparison queries.
//
// The markers cover both Chinese and English:
//
//	中文: 比较、对比、差异、区别、优缺点、不同、哪个更好
//	英文: compare, versus, vs, difference, better, which, pros and cons
var queryTriageComparisonMarkers = []string{
	"比较", "对比", "差异", "区别", "优缺点",
	"不同", "哪个更好", "哪个更", "如何选择",
	"compare", "versus", " difference", "better",
	"which", "pros", "cons", "tradeoff", "trade-off",
}

// HasComparisonMarkers is a helper that checks whether the query contains any
// known comparison/discourse marker. It can be used to extend analyzeQuery
// without modifying its existing logic.
func HasComparisonMarkers(query string) bool {
	lower := strings.ToLower(query)
	for _, m := range queryTriageComparisonMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}
