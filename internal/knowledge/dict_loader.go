package knowledge

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// dictEntry holds the parsed content of one term entry from a dictionary YAML.
type dictEntry struct {
	Term          string
	ExactSynonyms []string
	RelatedTerms  []string
}

// LoadDictionaries walks dictionaries/ and parses all .yaml files into a flat
// list of dictEntry. Only the specific YAML structure used by ship_motion.yaml
// is supported — no external YAML library needed.
func LoadDictionaries(dir string) ([]dictEntry, error) {
	var entries []dictEntry

	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	// Also try .yml extension.
	ymlFiles, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
	files = append(files, ymlFiles...)

	for _, path := range files {
		parsed, err := parseDictYAML(path)
		if err != nil {
			return entries, err
		}
		entries = append(entries, parsed...)
	}
	return entries, nil
}

// parseDictYAML is a minimal YAML parser that handles only the subset used by
// the dictionary files:
//
//	term_name:
//	  exact_synonyms:
//	    - value1
//	    - value2
//	  related_terms:
//	    - value1
func parseDictYAML(path string) ([]dictEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []dictEntry
	var cur *dictEntry
	var inList string // "exact" or "related"

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Skip comments and blank lines.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Detect top-level key (no leading whitespace, ends with ":").
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.HasSuffix(trimmed, ":") {
			if cur != nil {
				entries = append(entries, *cur)
			}
			name := strings.TrimSuffix(trimmed, ":")
			// Remove optional quotes.
			name = strings.Trim(name, `"`)
			cur = &dictEntry{Term: name}
			inList = ""
			continue
		}

		if cur == nil {
			continue
		}

		// Detect sub-key like "exact_synonyms:" or "related_terms:".
		if strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "-") {
			key := strings.TrimSuffix(trimmed, ":")
			switch key {
			case "exact_synonyms":
				inList = "exact"
			case "related_terms":
				inList = "related"
			default:
				inList = ""
			}
			continue
		}

		// List items: "- value".
		if strings.HasPrefix(trimmed, "- ") && inList != "" {
			val := strings.TrimPrefix(trimmed, "- ")
			val = strings.Trim(val, `"'`)
			switch inList {
			case "exact":
				cur.ExactSynonyms = append(cur.ExactSynonyms, val)
			case "related":
				cur.RelatedTerms = append(cur.RelatedTerms, val)
			}
		}
	}
	if cur != nil {
		entries = append(entries, *cur)
	}
	return entries, scanner.Err()
}

// DictToSynonyms converts dictionary entries into the map[term][]synonym format
// expected by SynonymRewriter. Both exact_synonyms and the reverse mapping
// (synonym → canonical term) are included so queries containing either form
// get expanded.
func DictToSynonyms(entries []dictEntry) map[string][]string {
	m := make(map[string][]string)
	for _, e := range entries {
		lowerTerm := strings.ToLower(e.Term)
		for _, syn := range e.ExactSynonyms {
			lowerSyn := strings.ToLower(syn)
			// canonical → synonym
			m[lowerTerm] = append(m[lowerTerm], lowerSyn)
			// synonym → canonical (bidirectional)
			m[lowerSyn] = append(m[lowerSyn], lowerTerm)
		}
	}
	return m
}

// DictToRelatedTerms returns all related_terms across all entries as a flat
// list, for appending as extra context to the rewritten query.
func DictToRelatedTerms(entries []dictEntry) []string {
	seen := make(map[string]bool)
	var terms []string
	for _, e := range entries {
		for _, t := range e.RelatedTerms {
			lower := strings.ToLower(t)
			if !seen[lower] {
				seen[lower] = true
				terms = append(terms, lower)
			}
		}
	}
	return terms
}
