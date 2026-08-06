package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

func registerSearch(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_research",
		mcp.WithDescription(store.ToolSearchDesc()),
		mcp.WithString("question",
			mcp.Required(),
			mcp.Description("The research question or search query. For simple factual questions, pass the user's original question directly. For complex research tasks, you may decompose into focused queries targeting different aspects. Use natural language or keyword strings as appropriate."),
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

		// v4: When kbName is empty, use KB Router to select best 1–3 KBs.
		var routedKBs []string
		if kbName == "" {
			routedKBs = store.RouteKBs(searchKW)
		}

		var hits []knowledge.SearchHit
		var err error

		if kbName != "" {
			searchStore := store.WithKB(kbName)
			hits, err = searchStore.HybridSearch(searchKW, limit, filter)
		} else if len(routedKBs) > 0 {
			hits, err = searchMultiKB(store, searchKW, limit, filter, routedKBs)
		} else {
			hits, err = store.SearchAll(searchKW, limit, filter)
		}

		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
		}
		tlog := logger.WithModule("tool")
		tlog.Debugf("knowledge_research: query=%q limit=%d kb=%q routed=%v hits=%d", searchKW, limit, kbName, routedKBs, len(hits))
		if len(hits) == 0 {
			return mcp.NewToolResultText("No matching chunks found."), nil
		}
		data, _ := json.MarshalIndent(hits, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	})
}

// searchMultiKB searches across multiple routed KBs and merges results,
// picking top-N per KB then re-ranking globally by score (v4 multi-KB).
func searchMultiKB(store *knowledge.Store, query string, limit int, filter knowledge.SearchFilter, kbNames []string) ([]knowledge.SearchHit, error) {
	perKB := limit
	if len(kbNames) > 1 {
		// Distribute limit across KBs; ensure at least 3 per KB.
		perKB = limit / len(kbNames)
		if perKB < 3 {
			perKB = 3
		}
	}

	var allHits []knowledge.SearchHit
	var lastErr error
	for _, kb := range kbNames {
		kbStore := store.WithKB(kb)
		hits, err := kbStore.HybridSearch(query, perKB, filter)
		if err != nil {
			// Log and continue — one KB failure shouldn't block others.
			lastErr = fmt.Errorf("search KB %q: %w", kb, err)
			continue
		}
		allHits = append(allHits, hits...)
	}

	if len(allHits) == 0 && lastErr != nil {
		return nil, lastErr
	}

	// Sort merged results by score descending and truncate to limit.
	sort.Slice(allHits, func(i, j int) bool {
		return allHits[i].Score > allHits[j].Score
	})
	if len(allHits) > limit {
		allHits = allHits[:limit]
	}
	return allHits, nil
}
