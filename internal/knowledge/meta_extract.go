package knowledge

import (
	"strings"
	"unicode"
)

// paperIndicators are section headings whose presence suggests the text is an
// academic paper. At least 2 must match for looksLikePaper to return true.
var paperIndicators = []string{
	"abstract",
	"introduction",
	"related work",
	"methodology",
	"experiments",
	"conclusion",
	"references",
	"discussion",
	"results",
}

// looksLikePaper heuristically determines whether the given plain text looks
// like an academic paper by scanning the first ~2000 characters for at least 2
// standard paper section headings.
func looksLikePaper(text string) bool {
	head := text
	if len(head) > 2000 {
		head = head[:2000]
	}
	lower := strings.ToLower(head)
	hits := 0
	for _, ind := range paperIndicators {
		if strings.Contains(lower, ind) {
			hits++
			if hits >= 2 {
				return true
			}
		}
	}
	return false
}

// ExtractPaperMeta attempts to extract the title, authors, and abstract from
// the beginning of an academic paper. It uses heuristic rules:
//
//   - Title: the first non-empty line that is a heading (starting with #) or a
//     short standalone line (< 120 chars) at the very top of the document.
//   - Authors: consecutive lines between the title and the abstract heading that
//     contain author-like patterns (commas, "@" emails, "and").
//   - Abstract: text between a heading containing "abstract" and the next
//     heading, or between "abstract" keyword (on its own line) and the next
//     section heading.
//
// When the text does not appear to be a paper, all return values are empty.
func ExtractPaperMeta(text string) (title string, authors []string, abstract string) {
	if !looksLikePaper(text) {
		return "", nil, ""
	}

	normalised := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalised, "\n")

	// Phase 1: locate key landmarks.
	titleLine, abstractStart, abstractEnd := locateLandmarks(lines)

	// Phase 2: extract title.
	if titleLine >= 0 && titleLine < len(lines) {
		title = cleanTitle(lines[titleLine])
	}

	// Phase 3: extract authors (lines between title and abstract).
	if titleLine >= 0 && abstractStart > 0 {
		authorLines := lines[titleLine+1 : abstractStart]
		authors = extractAuthors(authorLines)
	}

	// Phase 4: extract abstract text.
	if abstractStart >= 0 && abstractEnd > abstractStart {
		abstractLines := lines[abstractStart+1 : abstractEnd]
		abstract = cleanAbstract(abstractLines)
	}

	return title, authors, abstract
}

// locateLandmarks scans document lines to find the title line, abstract start
// line, and abstract end line (0-based). Returns -1 for unfound landmarks.
func locateLandmarks(lines []string) (titleLine, abstractStart, abstractEnd int) {
	titleLine = -1
	abstractStart = -1
	abstractEnd = -1

	// First pass: find the abstract heading.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if isHeading(trimmed) && strings.Contains(lower, "abstract") {
			abstractStart = i
			break
		}
	}

	// If no abstract heading found, search for "abstract" as a standalone word.
	if abstractStart < 0 {
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			lower := strings.ToLower(trimmed)
			if lower == "abstract" || lower == "abstract." {
				abstractStart = i
				break
			}
		}
	}

	// Find the end of the abstract: the next heading after abstractStart.
	if abstractStart >= 0 {
		for i := abstractStart + 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if isHeading(trimmed) {
				abstractEnd = i
				break
			}
		}
		if abstractEnd < 0 {
			// No further heading; abstract goes to end of scanned lines.
			abstractEnd = len(lines)
			if abstractEnd > 100 {
				abstractEnd = 100 // cap to first ~100 lines
			}
		}
	}

	// Find the title: search from line 0, skipping blank/empty lines.
	// Stop at abstractStart (or the first heading before it).
	abstractOrEnd := abstractStart
	if abstractOrEnd < 0 {
		abstractOrEnd = 20 // scan up to 20 lines if no abstract
	}

	for i := 0; i < abstractOrEnd && i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		// A heading or a short line likely represents the title.
		if isHeading(trimmed) {
			titleLine = i
			break
		}
		// Short initial lines (no punctuation as sentence) → likely title.
		if len(trimmed) < 120 && !strings.ContainsAny(trimmed, ".?!") {
			// Skip lines that look like author content.
			if !looksLikeAuthorLine(trimmed) {
				titleLine = i
				break
			}
		}
	}

	return titleLine, abstractStart, abstractEnd
}

// cleanTitle removes markdown heading markers and trims whitespace.
func cleanTitle(line string) string {
	trimmed := strings.TrimSpace(line)
	// Strip leading # characters and whitespace.
	for strings.HasPrefix(trimmed, "#") {
		trimmed = strings.TrimSpace(trimmed[1:])
	}
	return trimmed
}

// extractAuthors scans lines for author-like patterns and returns the names
// found. Heuristic: lines with commas separating names, "@" emails, or
// " and " between names that don't look like full sentences.
func extractAuthors(lines []string) []string {
	var authors []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip lines that look like section headings.
		if isHeading(trimmed) {
			break
		}
		if looksLikeAuthorLine(trimmed) {
			// Split on commas to extract individual names.
			parts := strings.Split(trimmed, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				// Remove leading numbers or labels (e.g. "1 ", "2.").
				p = stripLeadingNumber(p)
				if p != "" && len(p) > 3 {
					// Remove trailing email/affiliation in parens.
					if idx := strings.Index(p, "("); idx > 0 {
						p = strings.TrimSpace(p[:idx])
					}
					p = strings.TrimSpace(p)
					if p != "" {
						authors = append(authors, p)
					}
				}
			}
		}
	}
	return authors
}

// looksLikeAuthorLine heuristically checks if a line contains author-like
// content: commas separating names, "@" emails, or " and " between names,
// while being reasonably short (< 300 chars).
func looksLikeAuthorLine(line string) bool {
	lower := strings.ToLower(line)
	// Too long → likely paragraph text.
	if len(line) > 300 {
		return false
	}
	// Contains email → clearly author line.
	if strings.Contains(line, "@") {
		return true
	}
	// Contains comma-separated names and no sentence-ending punctuation.
	if strings.Contains(line, ",") && !strings.ContainsAny(line, ".?!") {
		return true
	}
	// Contains " and " in the middle (e.g. "John Doe and Jane Smith").
	if strings.Contains(lower, " and ") {
		// Make sure it's not a sentence fragment like "theory and practice".
		idx := strings.Index(lower, " and ")
		if idx > 0 && idx < len(lower)-6 {
			return true
		}
	}
	return false
}

// stripLeadingNumber removes leading number patterns like "1 ", "2.", "12 ".
func stripLeadingNumber(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if r == ' ' || r == '.' {
			if i > 0 {
				// Check if the prefix before the separator is a number.
				prefix := s[:i]
				isNum := true
				for _, pr := range prefix {
					if !unicode.IsDigit(pr) {
						isNum = false
						break
					}
				}
				if isNum {
					return strings.TrimSpace(s[i+1:])
				}
			}
			break
		}
		if !unicode.IsDigit(r) && r != '.' {
			break
		}
	}
	return s
}

// cleanAbstract joins abstract lines and trims whitespace.
func cleanAbstract(lines []string) string {
	var b strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			continue
		}
		if isHeading(trimmed) {
			break
		}
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n\n") {
			b.WriteString(" ")
		}
		b.WriteString(trimmed)
	}
	return strings.TrimSpace(b.String())
}
