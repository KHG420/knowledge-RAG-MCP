package knowledge

import (
	"context"
	"strings"
)

// TextCompleter is a minimal interface for calling an LLM to generate text.
// Implementations may wrap the OpenAI-compatible chat API, a local model, or
// a test mock. This keeps the LLM integration decoupled from any specific
// provider package.
type TextCompleter interface {
	// Complete sends a prompt and returns the model's text response.
	// The implementation is responsible for context cancellation and timeouts.
	Complete(ctx context.Context, prompt string) (string, error)
}

// LLMQueryRewriter uses an LLM (TextCompleter) to generate alternative phrasings
// of the user's search query, improving recall for queries where the best terms
// are not obvious from the document text.
//
// The original query is always returned as the first variant. LLM-generated
// variants follow. If the LLM call fails or returns empty, the fallback rewriter
// (default: SynonymRewriter) is used, so the system degrades gracefully when the
// LLM is unavailable.
type LLMQueryRewriter struct {
	completer TextCompleter
	fallback  QueryRewriter
	prompt    string
}

// NewLLMQueryRewriter returns an LLMQueryRewriter backed by the given
// TextCompleter. The fallback rewriter defaults to NewSynonymRewriter().
// The default LLM prompt asks for 2-4 alternative phrasings without commentary.
func NewLLMQueryRewriter(completer TextCompleter) *LLMQueryRewriter {
	return &LLMQueryRewriter{
		completer: completer,
		fallback:  NewSynonymRewriter(),
		prompt: "You are a query expansion assistant. Given a user's search " +
			"query, generate 2-4 alternative phrasings that express the same " +
			"information need using different terminology. Return only the " +
			"alternative queries, one per line, no numbering or commentary.",
	}
}

// WithFallback sets the fallback rewriter used when the LLM is unavailable.
// A nil value is treated as NoopRewriter (original query only).
func (r *LLMQueryRewriter) WithFallback(fb QueryRewriter) *LLMQueryRewriter {
	if fb == nil {
		r.fallback = NoopRewriter{}
	} else {
		r.fallback = fb
	}
	return r
}

// WithPrompt overrides the default LLM prompt template. The user query is
// appended to this prompt on a new line.
func (r *LLMQueryRewriter) WithPrompt(p string) *LLMQueryRewriter {
	r.prompt = p
	return r
}

// Rewrite returns the original query followed by LLM-generated alternatives.
// If the LLM call fails, times out, or returns no usable output, the fallback
// rewriter is used instead.
func (r *LLMQueryRewriter) Rewrite(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}

	variants, llmErr := r.llmRewrite(query)
	if llmErr != nil || len(variants) <= 1 {
		// LLM failed or returned nothing beyond the original query;
		// fall back to the configured rewriter.
		return r.fallback.Rewrite(query)
	}
	return variants
}

// llmRewrite attempts to generate query variants via the LLM, always
// guaranteeing the original query is first. Returns a non-nil error on
// any LLM failure (network, timeout, empty response).
func (r *LLMQueryRewriter) llmRewrite(query string) ([]string, error) {
	fullPrompt := r.prompt + "\n" + query
	resp, err := r.completer.Complete(context.Background(), fullPrompt)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(resp, "\n")
	var variants []string
	seen := map[string]bool{query: true}

	for _, line := range lines {
		line = cleanVariant(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		variants = append(variants, line)
	}

	if len(variants) == 0 {
		return nil, nil // empty response — caller should fall back
	}

	// Prepend the original query.
	result := make([]string, 0, len(variants)+1)
	result = append(result, query)
	result = append(result, variants...)
	return result, nil
}

// cleanVariant strips common list prefixes (numbering, bullets) and surrounding
// quotation marks from a query variant line returned by the LLM.
func cleanVariant(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Remove common list prefixes: "1. ", "- ", "* ", "• "
	for _, prefix := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimSpace(s[len(prefix):])
			break
		}
	}
	// Numbered prefixes: "1.", "1)", "1)"
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		if i+1 < len(s) && (s[i+1] == '.' || s[i+1] == ')' || s[i+1] == ']') {
			s = strings.TrimSpace(s[i+2:])
			break
		}
	}
	// Strip surrounding quotation marks.
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') ||
			(s[0] == '`' && s[len(s)-1] == '`') {
			s = s[1 : len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}
