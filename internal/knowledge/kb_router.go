package knowledge

import (
	"context"
	"sort"
	"strings"
)

// KBCandidate represents a single knowledge base with its routing score.
type KBCandidate struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// KBRouteResult holds the full routing output: all candidates sorted by score
// and the subset of KBs selected for retrieval.
type KBRouteResult struct {
	Candidates []KBCandidate `json:"candidates"` // all KBs, sorted by score descending
	Selected   []string      `json:"selected"`   // 1–3 KBs actually used for retrieval
}

// kbDesc holds the name and description text for one knowledge base.
type kbDesc struct {
	Name string
	Desc string
}

// KBRouter scores knowledge bases against a query and selects the best
// subset for retrieval. It uses a four-dimension weighted score:
//
//	keyword    (0.35) — term overlap between query and KB name/description
//	embedding  (0.35) — cosine similarity of query vector vs KB description vector
//	desc       (0.15) — description substring match bonus
//	constraint (0.15) — domain-specific routing constraints
//
// The router never calls an LLM; all scoring is rule-based or embedding-based.
type KBRouter struct {
	embedder    Embedder            // optional: enables embedding scoring
	constraints map[string][]string // KB name → constraint keywords
	kbDescs     []kbDesc            // cached KB name+description list
}

// NewKBRouter creates a KBRouter. If embedder is nil, the embedding dimension
// falls back to keyword scoring, so the router still works but is less precise.
func NewKBRouter(embedder Embedder) *KBRouter {
	return &KBRouter{
		embedder:    embedder,
		constraints: defaultConstraints(),
	}
}

// defaultConstraints defines domain-specific routing keywords that boost
// certain KBs when the query contains those keywords.
func defaultConstraints() map[string][]string {
	return map[string][]string{
		"ship_motion": {
			"横摇", "roll", "阻尼", "damping", "舭龙骨", "bilge keel",
			"参数横摇", "parametric roll", "同步横摇", "synchronous roll",
			"减摇", "stabilizer", "耐波性", "seakeeping", "倾覆", "capsize",
			"CFD", "Ikeda", "遭遇频率", "encounter frequency",
			"非线性", "nonlinear", "横浪", "beam sea", "横甩", "broaching",
		},
	}
}

// SetKBDescs updates the cached KB name/description list. Call this after
// creating or deleting KBs.
func (r *KBRouter) SetKBDescs(descs []kbDesc) {
	r.kbDescs = descs
}

// Route scores every candidate KB against the query and returns the routing
// result. When the score gap between the top-1 and top-2 KB exceeds 0.25,
// only the top KB is selected. Otherwise up to the top 3 KBs are selected
// for multi-KB joint retrieval.
//
// If candidates is empty, it uses the cached kbDescs list.
func (r *KBRouter) Route(ctx context.Context, query string, candidates []string) *KBRouteResult {
	if len(candidates) == 0 {
		candidates = make([]string, len(r.kbDescs))
		for i, d := range r.kbDescs {
			candidates[i] = d.Name
		}
	}

	// Build a lookup for descriptions.
	descMap := make(map[string]string, len(r.kbDescs))
	for _, d := range r.kbDescs {
		descMap[d.Name] = d.Desc
	}

	lowerQuery := strings.ToLower(query)
	queryTerms := tokenizeForRoute(lowerQuery)

	// Compute embedding similarity if embedder is available.
	var queryVec []float64
	if r.embedder != nil {
		vecs, err := r.embedder.Embed(ctx, []string{query})
		if err == nil && len(vecs) == 1 && len(vecs[0]) > 0 {
			queryVec = make([]float64, len(vecs[0]))
			for i, v := range vecs[0] {
				queryVec[i] = float64(v)
			}
		}
	}

	type scoredKB struct {
		name       string
		keyword    float64
		embedding  float64
		desc       float64
		constraint float64
		total      float64
	}

	scored := make([]scoredKB, 0, len(candidates))
	for _, name := range candidates {
		desc := descMap[name]
		lowerName := strings.ToLower(name)

		// 1. Keyword score (0.35): term overlap with KB name + description.
		keywordScore := keywordOverlap(queryTerms, lowerName, strings.ToLower(desc))

		// 2. Embedding score (0.35): cosine similarity.
		embedScore := 0.0
		if len(queryVec) > 0 && desc != "" {
			descVecs, err := r.embedder.Embed(ctx, []string{desc})
			if err == nil && len(descVecs) == 1 && len(descVecs[0]) > 0 {
				descVec := make([]float64, len(descVecs[0]))
				for i, v := range descVecs[0] {
					descVec[i] = float64(v)
				}
				embedScore = cosineSimilarity(queryVec, descVec)
			}
		}
		// Normalise embedding score to [0, 1]. Cosine similarity is [-1, 1];
		// clamp negatives and take (cos+1)/2 so typical values map nicely.
		if embedScore < 0 {
			embedScore = 0
		}

		// 3. Description score (0.15): substring match bonus.
		descScore := 0.0
		if desc != "" {
			lowerDesc := strings.ToLower(desc)
			for _, term := range queryTerms {
				if len(term) >= 2 && strings.Contains(lowerDesc, term) {
					descScore += 0.15 / float64(len(queryTerms))
				}
			}
			if descScore > 1.0 {
				descScore = 1.0
			}
		}

		// 4. Constraint score (0.15): domain keywords.
		constraintScore := 0.0
		if keywords, ok := r.constraints[name]; ok {
			matchCount := 0
			for _, kw := range keywords {
				if strings.Contains(lowerQuery, strings.ToLower(kw)) {
					matchCount++
				}
			}
			if matchCount > 0 {
				constraintScore = float64(matchCount) / float64(len(keywords))
				// Scale: up to 5 matches saturates the 0.15 weight.
				if constraintScore > 1.0 {
					constraintScore = 1.0
				}
			}
		}

		total := 0.35*keywordScore + 0.35*embedScore + 0.15*descScore + 0.15*constraintScore

		scored = append(scored, scoredKB{
			name:       name,
			keyword:    keywordScore,
			embedding:  embedScore,
			desc:       descScore,
			constraint: constraintScore,
			total:      total,
		})
	}

	// Sort by total score descending.
	sort.Slice(scored, func(i, j int) bool { return scored[i].total > scored[j].total })

	result := &KBRouteResult{}
	for _, s := range scored {
		result.Candidates = append(result.Candidates, KBCandidate{Name: s.name, Score: s.total})
	}

	// Decision logic: gap-based selection.
	if len(scored) == 0 {
		return result
	}
	if len(scored) == 1 {
		result.Selected = []string{scored[0].name}
		return result
	}

	top1, top2 := scored[0].total, scored[1].total
	if top1-top2 > 0.25 {
		result.Selected = []string{scored[0].name}
	} else {
		n := min(3, len(scored))
		for i := 0; i < n; i++ {
			result.Selected = append(result.Selected, scored[i].name)
		}
	}

	return result
}

// tokenizeForRoute splits a query into lowercase tokens for keyword matching.
func tokenizeForRoute(text string) []string {
	// Simple whitespace + punctuation split.
	fields := strings.Fields(text)
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, ",.;:!?()[]{}<>\"'")
		if len(f) > 0 && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// keywordOverlap computes a normalised keyword overlap score [0, 1] between
// the query terms and the target text (KB name + description).
func keywordOverlap(queryTerms []string, name, desc string) float64 {
	if len(queryTerms) == 0 {
		return 0
	}
	combined := name + " " + desc
	combinedLower := strings.ToLower(combined)
	matched := 0
	for _, term := range queryTerms {
		if len(term) >= 2 && strings.Contains(combinedLower, term) {
			matched++
		}
	}
	return float64(matched) / float64(len(queryTerms))
}
