package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// GenerateDictPrompt is the prompt template used to ask an LLM to extract
// domain-specific synonyms from a batch of document text. The LLM is expected
// to return a structured list of term→synonym mappings.
//
// This is designed for OFFLINE use (during knowledge base building), NOT for
// per-query expansion.
const GenerateDictPrompt = `You are a domain terminology extraction assistant. Given text excerpts
from academic/technical documents in a specialized field, extract domain-specific
terms and their synonyms/alternative expressions.

For each term, provide:
1. The canonical term (preferred form)
2. Exact synonyms (terms that mean exactly the same thing, including translations)
3. Related terms (broader/narrower/method-related, but NOT exact synonyms)

Return the result as a JSON array:
[
  {
    "term": "横摇阻尼",
    "exact_synonyms": ["roll damping", "roll damping coefficient"],
    "related_terms": ["Ikeda method", "bilge keel", "eddy damping"]
  }
]

Rules:
- Include both Chinese and English terms
- exact_synonyms must be truly synonymous (can replace each other in a query)
- related_terms are conceptually related but not identical
- Do not include generic stopwords or common academic phrases
- Limit to 20 most important terms from the provided text

Document excerpts:
%s`

// DictGenerationResult is the structured output from the LLM dictionary generation.
type DictGenerationResult struct {
	Term          string   `json:"term"`
	ExactSynonyms []string `json:"exact_synonyms"`
	RelatedTerms  []string `json:"related_terms"`
}

// GenerateDictionaryFromChunks uses an LLM (TextCompleter) to batch-extract
// domain terminology from a list of chunk texts. It sends chunks in batches
// to stay within context limits, then merges and deduplicates results.
//
// The completer should point to a capable model (e.g., deepseek-v4-flash or
// GPT-4) since term extraction requires domain understanding. This function
// is intended for OFFLINE use during knowledge base construction — NOT for
// online query-time expansion.
//
//	maxChunksPerBatch: number of chunks to send per LLM call (default 20)
//	maxTerms: maximum total terms to return after dedup (0 = unlimited)
func GenerateDictionaryFromChunks(
	completer TextCompleter,
	chunkTexts []string,
	maxChunksPerBatch int,
	maxTerms int,
) ([]DictGenerationResult, error) {
	if completer == nil {
		return nil, fmt.Errorf("generate dictionary: completer is nil")
	}
	if len(chunkTexts) == 0 {
		return nil, nil
	}
	if maxChunksPerBatch <= 0 {
		maxChunksPerBatch = 20
	}

	var allResults []DictGenerationResult
	seen := make(map[string]bool)

	for i := 0; i < len(chunkTexts); i += maxChunksPerBatch {
		end := i + maxChunksPerBatch
		if end > len(chunkTexts) {
			end = len(chunkTexts)
		}
		batch := chunkTexts[i:end]
		batchText := strings.Join(batch, "\n---\n")
		if len(batchText) > 32000 {
			batchText = batchText[:32000] // hard cap for API limits
		}

		prompt := fmt.Sprintf(GenerateDictPrompt, batchText)
		// We can't use json.Unmarshal directly since the LLM might include
		// markdown fences. Use a simple JSON extraction approach.
		resp, err := completer.Complete(context.Background(), prompt)
		if err != nil {
			// Non-fatal: skip this batch on error.
			continue
		}

		batchResults := extractJSONDictResults(resp)
		for _, r := range batchResults {
			key := strings.ToLower(r.Term)
			if seen[key] {
				continue
			}
			seen[key] = true
			allResults = append(allResults, r)
		}
	}

	// Sort by number of synonyms (more synonyms = more important term).
	sort.Slice(allResults, func(i, j int) bool {
		return len(allResults[i].ExactSynonyms) > len(allResults[j].ExactSynonyms)
	})

	if maxTerms > 0 && len(allResults) > maxTerms {
		allResults = allResults[:maxTerms]
	}

	return allResults, nil
}

// extractJSONDictResults attempts to parse JSON dict entries from an LLM
// response that may include markdown code fences or extra commentary.
func extractJSONDictResults(resp string) []DictGenerationResult {
	// Strip markdown fences.
	cleaned := resp
	if idx := strings.Index(cleaned, "```json"); idx >= 0 {
		cleaned = cleaned[idx+7:]
		if end := strings.Index(cleaned, "```"); end >= 0 {
			cleaned = cleaned[:end]
		}
	} else if idx := strings.Index(cleaned, "```"); idx >= 0 {
		cleaned = cleaned[idx+3:]
		if end := strings.Index(cleaned, "```"); end >= 0 {
			cleaned = cleaned[:end]
		}
	}
	// Try to find the JSON array.
	start := strings.Index(cleaned, "[")
	if start < 0 {
		return nil
	}
	end := strings.LastIndex(cleaned, "]")
	if end < 0 || end <= start {
		return nil
	}
	jsonStr := cleaned[start : end+1]

	// Use a simple manual parser to avoid depending on encoding/json for
	// potentially malformed LLM output. We only need the basic structure.
	return parseSimpleDictJSON(jsonStr)
}

// parseSimpleDictJSON is a lightweight JSON array parser that handles only
// the DictGenerationResult structure. It's more forgiving than encoding/json
// for LLM-generated text (trailing commas, minor formatting issues).
func parseSimpleDictJSON(jsonStr string) []DictGenerationResult {
	var results []DictGenerationResult
	jsonStr = strings.TrimSpace(jsonStr)

	// Find each {...} object in the array.
	depth := 0
	objStart := -1
	for i := 0; i < len(jsonStr); i++ {
		switch jsonStr[i] {
		case '{':
			if depth == 0 {
				objStart = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && objStart >= 0 {
				obj := jsonStr[objStart : i+1]
				if r := parseOneDictObj(obj); r.Term != "" {
					results = append(results, r)
				}
				objStart = -1
			}
		}
	}
	return results
}

// parseOneDictObj parses a single JSON object with "term", "exact_synonyms",
// and "related_terms" fields. Uses simple string operations; robust to minor
// formatting issues.
func parseOneDictObj(obj string) DictGenerationResult {
	var r DictGenerationResult

	r.Term = extractJSONString(obj, "term")
	r.ExactSynonyms = extractJSONStringArray(obj, "exact_synonyms")
	r.RelatedTerms = extractJSONStringArray(obj, "related_terms")

	return r
}

// extractJSONString extracts a simple string value for a given key from a
// JSON object fragment.
func extractJSONString(obj, key string) string {
	search := fmt.Sprintf(`"%s"`, key)
	idx := strings.Index(obj, search)
	if idx < 0 {
		return ""
	}
	rest := obj[idx+len(search):]
	// Find the next ":"
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = rest[colon+1:]
	// Find the string value between quotes.
	qStart := strings.Index(rest, `"`)
	if qStart < 0 {
		return ""
	}
	qEnd := strings.Index(rest[qStart+1:], `"`)
	if qEnd < 0 {
		return ""
	}
	return rest[qStart+1 : qStart+1+qEnd]
}

// extractJSONStringArray extracts a JSON string array for a given key.
func extractJSONStringArray(obj, key string) []string {
	search := fmt.Sprintf(`"%s"`, key)
	idx := strings.Index(obj, search)
	if idx < 0 {
		return nil
	}
	rest := obj[idx+len(search):]
	// Find the ":"
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return nil
	}
	rest = rest[colon+1:]
	// Find the "["
	bracket := strings.Index(rest, "[")
	if bracket < 0 {
		return nil
	}
	rest = rest[bracket+1:]
	// Find the closing "]"
	closeBracket := strings.Index(rest, "]")
	if closeBracket < 0 {
		return nil
	}
	arrStr := rest[:closeBracket]

	var vals []string
	// Split by "," and extract each quoted string.
	parts := strings.Split(arrStr, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && p[0] == '"' {
			// Find closing quote.
			end := strings.Index(p[1:], `"`)
			if end > 0 {
				vals = append(vals, p[1:1+end])
			}
		}
	}
	return vals
}

// FormatDictAsYAML converts DictGenerationResult entries to a YAML string
// in the same format as dictionaries/*.yaml, ready for human review and
// direct inclusion.
func FormatDictAsYAML(entries []DictGenerationResult) string {
	if len(entries) == 0 {
		return "# No domain terms extracted.\n"
	}

	var b strings.Builder
	b.WriteString("# Auto-generated domain dictionary — review before use.\n")
	b.WriteString("# Format: canonical_term → {exact_synonyms, related_terms}\n")
	b.WriteString("# Generated from knowledge base document analysis.\n\n")

	for _, e := range entries {
		b.WriteString(fmt.Sprintf("%s:\n", e.Term))
		b.WriteString("  exact_synonyms:\n")
		for _, s := range e.ExactSynonyms {
			b.WriteString(fmt.Sprintf("    - %s\n", s))
		}
		b.WriteString("  related_terms:\n")
		for _, r := range e.RelatedTerms {
			b.WriteString(fmt.Sprintf("    - %s\n", r))
		}
		b.WriteString("\n")
	}
	return b.String()
}
