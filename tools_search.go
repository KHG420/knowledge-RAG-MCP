package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// kbSearchFailure identifies a knowledge base that could not be searched and
// carries a short, safe, actionable message. Raw backend errors are not echoed
// because they may contain connection strings, credentials, or file paths.
type kbSearchFailure struct {
	KB      string `json:"kb"`
	Message string `json:"message"`
}

// researchEnvelope is the response shape of knowledge_research. results is
// always an array (possibly empty) and the metadata lets a caller distinguish
// "no matching chunks" from "some knowledge bases failed".
//
// coverage describes the search operation, not whether the results answer the
// question: "complete" means every attempted KB search succeeded and "partial"
// means at least one KB search failed.
type researchEnvelope struct {
	Results     []knowledge.SearchHit `json:"results"`
	SearchedKBs []string              `json:"searched_kbs"`
	FailedKBs   []kbSearchFailure     `json:"failed_kbs"`
	Warnings    []string              `json:"warnings"`
	Coverage    string                `json:"coverage"`
}

const (
	coverageComplete = "complete"
	coveragePartial  = "partial"
)

// searchOutcome is the request-local result of searching one or more KBs.
// It is deliberately kept local (rather than storing last-error state on the
// Store) so concurrent requests cannot observe each other's failures.
type searchOutcome struct {
	Hits     []knowledge.SearchHit
	Searched []string
	Failed   []kbSearchFailure
}

func registerSearch(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_research",
		mcp.WithDescription(store.ToolSearchDesc()),
		mcp.WithString("question",
			mcp.Required(),
			mcp.Description("The research question or search query. Pass the user's question, or a focused rewrite for complex tasks."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum results to return. Default 8, max 20."),
		),
		mcp.WithString("sourceType",
			mcp.Description("Filter by source type, e.g. 'pdf', 'md', 'txt'."),
		),
		mcp.WithString("section",
			mcp.Description("Filter chunks whose section heading contains this substring."),
		),
		mcp.WithString("tags",
			mcp.Description("Comma-separated tags. Only documents matching at least one tag are returned."),
		),
		mcp.WithString("addedAfter",
			mcp.Description("ISO 8601 date (e.g. '2026-07-01' or '2026-07-01T00:00:00Z'). Only docs added at or after this time."),
		),
		mcp.WithString("addedBefore",
			mcp.Description("ISO 8601 date (e.g. '2026-07-31' or '2026-07-31T23:59:59Z'). Only docs added at or before this time."),
		),
		mcp.WithBoolean("coarse",
			mcp.Description("Enable coarse-to-fine 2-phase search: first score sections, then only search within top-3 sections."),
		),
		mcp.WithString("kbName",
			mcp.Description(store.ToolSearchKbNameDesc()),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		searchKW := getString(req, "question")
		if searchKW == "" {
			return mcp.NewToolResultError("question is required — pass the research question or search query"), nil
		}

		limit := 8
		if v, ok := req.Params.Arguments["limit"].(float64); ok && v > 0 {
			limit = int(v)
		}
		if limit > 20 {
			limit = 20
		}

		addedAfterRaw := getString(req, "addedAfter")
		addedBeforeRaw := getString(req, "addedBefore")

		addedAfter := parseTime(addedAfterRaw)
		if addedAfterRaw != "" && addedAfter.IsZero() {
			return mcp.NewToolResultError(fmt.Sprintf("invalid addedAfter %q — use ISO 8601 format (e.g. '2026-07-01')", addedAfterRaw)), nil
		}
		addedBefore := parseTime(addedBeforeRaw)
		if addedBeforeRaw != "" && addedBefore.IsZero() {
			return mcp.NewToolResultError(fmt.Sprintf("invalid addedBefore %q — use ISO 8601 format (e.g. '2026-07-31')", addedBeforeRaw)), nil
		}

		filter := knowledge.SearchFilter{
			SourceType:  getString(req, "sourceType"),
			Section:     getString(req, "section"),
			Tags:        parseTags(getString(req, "tags")),
			AddedAfter:  addedAfter,
			AddedBefore: addedBefore,
			Coarse:      getBool(req, "coarse"),
		}

		kbName := getString(req, "kbName")
		if kbName != "" && !isPathSafe(kbName) {
			return mcp.NewToolResultError(fmt.Sprintf("invalid kbName %q", kbName)), nil
		}

		outcome := runResearch(store, searchKW, limit, filter, kbName)

		// If every KB we attempted failed, surface an MCP-level error rather
		// than an empty-looking success.
		if len(outcome.Searched) == 0 && len(outcome.Failed) > 0 {
			names := make([]string, len(outcome.Failed))
			for i, f := range outcome.Failed {
				names[i] = f.KB
			}
			return mcp.NewToolResultError(fmt.Sprintf(
				"search failed for all %d knowledge base(s) (%s); they may be unavailable or misconfigured — retry or check server logs",
				len(outcome.Failed), strings.Join(names, ", "))), nil
		}

		warnings := researchWarnings(store, outcome)
		if len(outcome.Searched) == 0 && len(outcome.Failed) == 0 {
			warnings = append(warnings, "no knowledge bases are configured; create a knowledge base and upload documents first")
		}

		coverage := coverageComplete
		if len(outcome.Failed) > 0 {
			coverage = coveragePartial
		}

		results := outcome.Hits
		if results == nil {
			results = []knowledge.SearchHit{}
		}
		envelope := researchEnvelope{
			Results:     results,
			SearchedKBs: append([]string{}, outcome.Searched...),
			FailedKBs:   append([]kbSearchFailure{}, outcome.Failed...),
			Warnings:    warnings,
			Coverage:    coverage,
		}

		logger.WithModule("tool").Debugf(
			"knowledge_research: query=%q limit=%d kb=%q searched=%v failed=%d hits=%d coverage=%s",
			searchKW, limit, kbName, envelope.SearchedKBs, len(envelope.FailedKBs), len(envelope.Results), coverage)

		data, _ := json.MarshalIndent(envelope, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	})
}

// runResearch resolves which KBs to search and returns a request-local outcome.
func runResearch(store *knowledge.Store, query string, limit int, filter knowledge.SearchFilter, kbName string) searchOutcome {
	if kbName != "" {
		hits, err := store.WithKB(kbName).HybridSearch(query, limit, filter)
		if err != nil {
			return searchOutcome{Failed: []kbSearchFailure{{KB: kbName, Message: safeSearchErrorMessage(err)}}}
		}
		for i := range hits {
			hits[i].KBName = kbName
		}
		return searchOutcome{Hits: hits, Searched: []string{kbName}}
	}

	// When kbName is empty, use the KB router to select the best KBs; fall
	// back to searching every KB when no router is configured.
	routedKBs := store.RouteKBs(query)
	if len(routedKBs) > 0 {
		return searchMultiKB(store, query, limit, filter, routedKBs)
	}

	kbs, err := store.ListKBs()
	if err != nil {
		return searchOutcome{Failed: []kbSearchFailure{{
			KB:      "(all knowledge bases)",
			Message: safeSearchErrorMessage(err),
		}}}
	}
	return searchMultiKB(store, query, limit, filter, kbs)
}

// searchMultiKB searches every named KB for up to the final requested limit,
// then merges and truncates to that limit. Requesting the final limit from each
// KB (rather than limit/N) ensures a single strong KB can supply the full
// result set instead of being capped at a small per-KB share.
func searchMultiKB(store *knowledge.Store, query string, limit int, filter knowledge.SearchFilter, kbNames []string) searchOutcome {
	if limit <= 0 {
		limit = 8
	}
	var out searchOutcome
	for _, kb := range kbNames {
		hits, err := store.WithKB(kb).HybridSearch(query, limit, filter)
		if err != nil {
			out.Failed = append(out.Failed, kbSearchFailure{KB: kb, Message: safeSearchErrorMessage(err)})
			continue
		}
		out.Searched = append(out.Searched, kb)
		for i := range hits {
			hits[i].KBName = kb
		}
		out.Hits = append(out.Hits, hits...)
	}

	// Merge globally by score descending and truncate to the final limit.
	sort.SliceStable(out.Hits, func(i, j int) bool {
		return out.Hits[i].Score > out.Hits[j].Score
	})
	if len(out.Hits) > limit {
		out.Hits = out.Hits[:limit]
	}
	return out
}

// safeSearchErrorMessage converts a raw backend error into a short, actionable
// message that does not leak connection strings, credentials, or file paths.
func safeSearchErrorMessage(err error) string {
	if err == nil {
		return "knowledge base search failed"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"), strings.Contains(msg, "no such"):
		return "knowledge base not found; it may have been removed — list knowledge bases and retry"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"), strings.Contains(msg, "context canceled"):
		return "knowledge base search timed out; retry, or narrow the query"
	default:
		return "knowledge base search failed; it may be temporarily unavailable or misconfigured — retry or check server logs"
	}
}

// researchWarnings reports configuration facts and partial coverage. It only
// warns about models that are *not* configured; when a model is configured the
// runtime may still fall back, so no success claim is made about it. It also
// states explicitly that runtime model status is not reported, because the
// engine cannot expose it without an architectural expansion.
func researchWarnings(store *knowledge.Store, out searchOutcome) []string {
	warnings := []string{}
	if store.Embedder() == nil {
		warnings = append(warnings, "semantic embedding is not configured; results use keyword (BM25) retrieval only")
	}
	if store.Reranker() == nil {
		warnings = append(warnings, "cross-encoder reranking is not configured; results use retrieval ranking only")
	}
	warnings = append(warnings, "runtime success or fallback of optional embedding/reranking models is not reported; configuration alone does not prove they were used")
	if len(out.Failed) > 0 && len(out.Searched) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d of %d knowledge base searches failed; returned results are partial",
			len(out.Failed), len(out.Failed)+len(out.Searched)))
	}
	return warnings
}
