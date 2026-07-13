package knowledge

import "strings"

// QueryRewriter transforms a user query before it is passed to the search
// engine. Implementations may add synonyms, expand abbreviations, or generate
// multiple query variants to improve recall.
type QueryRewriter interface {
	// Rewrite takes a raw user query and returns one or more query variants.
	// When multiple variants are returned, the search engine tokenises and
	// scores them together (union of terms), increasing the chance of matching
	// documents that use different terminology.
	Rewrite(query string) []string
}

// NoopRewriter returns the query unchanged.
type NoopRewriter struct{}

func (NoopRewriter) Rewrite(query string) []string {
	if query == "" {
		return nil
	}
	return []string{query}
}

// SynonymRewriter expands common domain-specific terms with their synonyms.
// It uses a built-in synonym map covering topics relevant to the Reasonix
// project (RAG, embeddings, NLP, ML, and CJK-related terms).
type SynonymRewriter struct {
	m map[string][]string // term → synonyms
}

// NewSynonymRewriter returns a SynonymRewriter with a built-in synonym map.
func NewSynonymRewriter() *SynonymRewriter {
	// Copy the built-in map so per-instance additions don't leak.
	m := make(map[string][]string, len(builtinSynonyms))
	for k, v := range builtinSynonyms {
		m[k] = append([]string{}, v...)
	}
	return &SynonymRewriter{m: m}
}

// AddSynonym registers a custom synonym pair: given the canonical term,
// searching for it will also match the synonym (and vice versa).
func (r *SynonymRewriter) AddSynonym(term, synonym string) {
	term = strings.ToLower(term)
	synonym = strings.ToLower(synonym)
	r.m[term] = append(r.m[term], synonym)
}

// Rewrite expands the query by adding synonym alternatives. The original query
// is always included as the first variant. Matching is case-insensitive.
func (r *SynonymRewriter) Rewrite(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}

	// Build a set of unique expanded queries.
	seen := map[string]bool{query: true}
	results := []string{query}

	lower := strings.ToLower(query)
	// Check each term in the synonym map.
	for term, synonyms := range r.m {
		if !strings.Contains(lower, term) {
			continue
		}
		for _, syn := range synonyms {
			expanded := ciReplace(query, term, syn, 1)
			if !seen[expanded] {
				seen[expanded] = true
				results = append(results, expanded)
			}
			// Also try the reverse: replace a synonym in the query with the
			// canonical term.
			if strings.Contains(lower, syn) {
				rev := ciReplace(query, syn, term, 1)
				if !seen[rev] {
					seen[rev] = true
					results = append(results, rev)
				}
			}
		}
	}
	return results
}

// ciReplace performs a case-insensitive replacement of old with new in s.
// It replaces at most n occurrences (n < 0 means unlimited).
func ciReplace(s, old, new string, n int) string {
	lower := strings.ToLower(s)
	lowerOld := strings.ToLower(old)
	var b strings.Builder
	written := 0
	for i := 0; i < n || n < 0; i++ {
		idx := strings.Index(lower[written:], lowerOld)
		if idx < 0 {
			break
		}
		idx += written // translate to full-s offset
		b.WriteString(s[written:idx])
		b.WriteString(new)
		written = idx + len(old)
	}
	b.WriteString(s[written:])
	return b.String()
}

// builtinSynonyms contains domain-specific synonym pairs. Each canonical term
// maps to its common synonyms. All keys and values must be lowercase.
var builtinSynonyms = map[string][]string{
	// RAG / Retrieval
	"rag":       {"retrieval augmented generation", "retrieval-augmented generation"},
	"retrieval": {"search", "retrieve"},
	"bm25":      {"bm-25", "bm 25"},
	"reranker":  {"re-ranker", "cross-encoder", "re-ranking"},
	"embedding": {"vector", "embed", "dense retrieval"},
	"chunk":     {"segment", "fragment", "passage"},
	"chunking":  {"segmentation", "splitting", "text splitting"},

	// ML / AI
	"llm":              {"large language model", "language model"},
	"machine learning": {"ml"},
	"neural":           {"neural network", "deep learning"},

	// Knowledge management
	"knowledge": {"know"},
}

// SetRewriter configures the query rewriter on the store. A nil rewriter is
// treated as NoopRewriter.
func (s *Store) SetRewriter(r QueryRewriter) {
	s.rewriter = r
}
