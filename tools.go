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

func registerSearch(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_research",
		mcp.WithDescription(store.ToolSearchDesc()),
		mcp.WithString("question",
			mcp.Required(),
			mcp.Description("The research question or search query. For simple factual questions, pass the user's original question directly. For complex research tasks, you may decompose into focused queries targeting different aspects. Use natural language or keyword strings as appropriate."),
		),
		mcp.WithString("search_keywords",
			mcp.Description("DEPRECATED: use 'question' instead. Space-separated keyword string. Only for backward compatibility with older Agent versions."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum results to return. Default 8, max 20."),
		),
		mcp.WithString("mode",
			mcp.Description("DEPRECATED: the system auto-selects the best retrieval strategy. Accepted for backward compatibility only."),
			mcp.Enum("bm25", "hybrid"),
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
		// question takes priority; fall back to search_keywords → query (deprecated).
		searchKW := getString(req, "question")
		if searchKW == "" {
			searchKW = getString(req, "search_keywords")
		}
		if searchKW == "" {
			searchKW = getString(req, "query") // truly ancient fallback
		}
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

		filter := knowledge.SearchFilter{
			SourceType:  getString(req, "sourceType"),
			Section:     getString(req, "section"),
			Tags:        parseTags(getString(req, "tags")),
			AddedAfter:  parseTime(getString(req, "addedAfter")),
			AddedBefore: parseTime(getString(req, "addedBefore")),
			Coarse:      getBool(req, "coarse"),
		}

		kbName := getString(req, "kbName")
		if kbName != "" && strings.Contains(kbName, "..") {
			return mcp.NewToolResultError(fmt.Sprintf("invalid kbName %q: must not contain '..'", kbName)), nil
		}

		// v4: When kbName is empty, use KB Router to select best 1–3 KBs.
		var routedKBs []string
		if kbName == "" {
			routedKBs = store.RouteKBs(searchKW)
		}

		var hits []knowledge.SearchHit
		var err error

		// v4: Default to hybrid unless explicitly set to "bm25" for backward compat.
		useMode := strings.ToLower(getString(req, "mode"))
		isHybrid := useMode != "bm25" // default: hybrid

		if kbName != "" {
			searchStore := store.WithKB(kbName)
			if isHybrid {
				hits, err = searchStore.HybridSearch(searchKW, limit, filter)
			} else {
				hits, err = searchStore.Search(searchKW, limit, filter)
			}
		} else if len(routedKBs) > 0 {
			routedMode := "bm25"
			if isHybrid {
				routedMode = "hybrid"
			}
			hits, err = searchMultiKB(store, searchKW, limit, filter, routedMode, routedKBs)
		} else {
			if isHybrid {
				hits, err = store.SearchAll(searchKW, limit, filter)
			} else {
				hits, err = store.SearchAll(searchKW, limit, filter)
			}
		}

		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
		}
		tlog := logger.WithModule("tool")
		tlog.Debugf("knowledge_research: query=%q limit=%d kb=%q routed=%v mode=%q hybrid=%v hits=%d", searchKW, limit, kbName, routedKBs, useMode, isHybrid, len(hits))
		if len(hits) == 0 {
			return mcp.NewToolResultText("No matching chunks found."), nil
		}
		data, _ := json.MarshalIndent(hits, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	})
}

func registerRead(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_read",
		mcp.WithDescription(store.ToolReadDesc()),
		mcp.WithString("docSlug",
			mcp.Required(),
			mcp.Description("Document slug (from list/search results)."),
		),
		mcp.WithString("chunkID",
			mcp.Required(),
			mcp.Description("Chunk identifier (e.g. '005'). From search results."),
		),
		mcp.WithNumber("context",
			mcp.Description("Number of adjacent chunks to include before and after. Default 0, max 5."),
		),
		mcp.WithString("level",
			mcp.Description("Read granularity: 'chunk' (default) or 'section'."),
			mcp.Enum("chunk", "section"),
		),
		mcp.WithString("kbName",
			mcp.Description(store.ToolReadKbNameDesc()),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docSlug := getString(req, "docSlug")
		chunkID := getString(req, "chunkID")
		if docSlug == "" || chunkID == "" {
			return mcp.NewToolResultError("docSlug and chunkID are required"), nil
		}

		// Path-traversal guard: reject ".." in user-supplied path components.
		for _, v := range []string{docSlug, chunkID} {
			if strings.Contains(v, "..") {
				return mcp.NewToolResultError(fmt.Sprintf("invalid parameter %q: must not contain '..'", v)), nil
			}
		}

		kbName := getString(req, "kbName")
		if kbName != "" && strings.Contains(kbName, "..") {
			return mcp.NewToolResultError(fmt.Sprintf("invalid kbName %q: must not contain '..'", kbName)), nil
		}

		ctxCount := 0
		if v, ok := req.Params.Arguments["context"].(float64); ok {
			ctxCount = int(v)
			if ctxCount < 0 {
				ctxCount = 0
			}
			if ctxCount > 5 {
				ctxCount = 5
			}
		}

		if strings.ToLower(getString(req, "level")) == "section" {
			text, err := tryReadSection(store, kbName, docSlug, chunkID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			logger.WithModule("tool").Debugf("knowledge_read: section slug=%q chunk=%q kb=%q ok", docSlug, chunkID, kbName)
			evidence, _ := buildEvidenceJSON(store, kbName, docSlug, chunkID, text)
			return mcp.NewToolResultText(evidence), nil
		}

		text, err := tryReadChunk(store, kbName, docSlug, chunkID, ctxCount)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		logger.WithModule("tool").Debugf("knowledge_read: chunk slug=%q chunk=%q ctx=%d kb=%q textlen=%d", docSlug, chunkID, ctxCount, kbName, len(text))
		evidence, _ := buildEvidenceJSON(store, kbName, docSlug, chunkID, text)
		return mcp.NewToolResultText(evidence), nil
	})
}

func readSection(store *knowledge.Store, docSlug, chunkID string) (string, error) {
	index, err := store.ReadChunksIndex(docSlug)
	if err != nil {
		// Index corrupt — try plain chunk read as fallback.
		if text, chunkErr := store.ReadChunk(docSlug, chunkID); chunkErr == nil {
			return text, nil
		}
		return "", fmt.Errorf("index corrupted for document %q and chunk read also failed: %w", docSlug, err)
	}
	if index == nil {
		// ChunksIndex missing — try plain chunk read as fallback.
		if text, chunkErr := store.ReadChunk(docSlug, chunkID); chunkErr == nil {
			return text, nil
		}
		return "", fmt.Errorf(
			"chunks-index and chunk both missing for document %q chunk %q — "+
				"the document may have been deleted or is not fully indexed; "+
				"try re-searching to get fresh results",
			docSlug, chunkID,
		)
	}
	for _, entry := range index.Chunks {
		if entry.ID == chunkID && entry.SectionChunkID != "" {
			return store.ReadSectionChunk(docSlug, entry.SectionChunkID)
		}
	}
	return store.ReadChunk(docSlug, chunkID)
}

// tryReadChunk reads a chunk from a specific KB, or searches across all KBs
// when kbName is empty.
func tryReadChunk(store *knowledge.Store, kbName, docSlug, chunkID string, ctxCount int) (string, error) {
	if kbName != "" {
		return store.WithKB(kbName).ReadChunkContext(docSlug, chunkID, ctxCount)
	}
	kbs, err := store.ListKBs()
	if err != nil {
		return "", err
	}
	var lastErr error
	for _, kb := range kbs {
		text, err := store.WithKB(kb).ReadChunkContext(docSlug, chunkID, ctxCount)
		if err == nil {
			return text, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", fmt.Errorf("chunk %q not found in document %q across %d KBs: %w — the document may have been deleted or re-indexed; try re-searching", chunkID, docSlug, len(kbs), lastErr)
	}
	return "", fmt.Errorf("document %q not found in any of %d KBs — try re-searching to get current results", docSlug, len(kbs))
}

// tryReadSection reads a section chunk from a specific KB, or searches across
// all KBs when kbName is empty.
func tryReadSection(store *knowledge.Store, kbName, docSlug, chunkID string) (string, error) {
	if kbName != "" {
		return readSection(store.WithKB(kbName), docSlug, chunkID)
	}
	kbs, err := store.ListKBs()
	if err != nil {
		return "", err
	}
	var lastErr error
	for _, kb := range kbs {
		text, err := readSection(store.WithKB(kb), docSlug, chunkID)
		if err == nil {
			return text, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", fmt.Errorf("section read failed for document %q across %d KBs: %w — try reading at chunk level or re-searching", docSlug, len(kbs), lastErr)
	}
	return "", fmt.Errorf("document %q not found in any of %d KBs — try re-searching to get current results", docSlug, len(kbs))
}

// buildEvidenceJSON constructs an EvidenceChunk JSON string that wraps the
// content with full source attribution so the LLM never loses context.
func buildEvidenceJSON(store *knowledge.Store, kbName, docSlug, chunkID, text string) (string, error) {
	// Resolve the correct store view.
	s := store
	if kbName != "" {
		s = store.WithKB(kbName)
	} else {
		// Try to find the document across all KBs to get metadata.
		kbs, err := store.ListKBs()
		if err == nil {
			for _, kb := range kbs {
				if _, metaErr := store.WithKB(kb).ReadMeta(docSlug); metaErr == nil {
					s = store.WithKB(kb)
					break
				}
			}
		}
	}

	meta, metaErr := s.ReadMeta(docSlug)
	if metaErr != nil {
		// Degrade gracefully: return content without metadata.
		feats := knowledge.ExtractEvidenceFeatures(text)
		data, _ := json.MarshalIndent(knowledge.EvidenceChunk{
			Document:   knowledge.DocumentInfo{ID: docSlug},
			Location:   knowledge.LocationInfo{ChunkID: chunkID},
			Content:    text,
			CitationID: fmt.Sprintf("%s_%s", docSlug, chunkID),
			Evidence: knowledge.EvidenceMeta{
				SourceConfidence: "exact_section",
				AnswerRelevance:  knowledge.ClassifyAnswerRelevance(feats),
				Completeness:     knowledge.ClassifyCompleteness(feats),
			},
		}, "", "  ")
		return string(data), nil
	}

	// Try to get section/offset/page from the chunks index.
	sec := ""
	off := 0
	ps := 0
	pe := 0
	if index, idxErr := s.ReadChunksIndex(docSlug); idxErr == nil && index != nil {
		for _, entry := range index.Chunks {
			if entry.ID == chunkID {
				sec = entry.Section
				off = entry.Offset
				ps = entry.PageStart
				pe = entry.PageEnd
				break
			}
		}
	}

	evidence := knowledge.EvidenceChunk{
		Document: knowledge.DocumentInfo{
			ID:           meta.Slug,
			Title:        meta.Title,
			OriginalName: meta.OriginalName,
			Type:         meta.SourceType,
		},
		Location: knowledge.LocationInfo{
			ChunkID:   chunkID,
			Section:   sec,
			Offset:    off,
			PageStart: ps,
			PageEnd:   pe,
		},
		Content:    text,
		CitationID: fmt.Sprintf("%s_%s", docSlug, chunkID),
	}

	// v4: Feature-based evidence quality signals (pure rules, 0 extra cost).
	feats := knowledge.ExtractEvidenceFeatures(text)
	evidence.Evidence = knowledge.EvidenceMeta{
		SourceConfidence: "exact_section", // read path always targets an exact section/chunk
		AnswerRelevance:  knowledge.ClassifyAnswerRelevance(feats),
		Completeness:     knowledge.ClassifyCompleteness(feats),
	}

	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// searchMultiKB searches across multiple routed KBs and merges results,
// picking top-N per KB then re-ranking globally by score (v4 multi-KB).
func searchMultiKB(store *knowledge.Store, query string, limit int, filter knowledge.SearchFilter, mode string, kbNames []string) ([]knowledge.SearchHit, error) {
	perKB := limit
	if len(kbNames) > 1 {
		// Distribute limit across KBs; ensure at least 3 per KB.
		perKB = limit / len(kbNames)
		if perKB < 3 {
			perKB = 3
		}
	}

	var allHits []knowledge.SearchHit
	for _, kb := range kbNames {
		kbStore := store.WithKB(kb)
		var hits []knowledge.SearchHit
		var err error
		switch mode {
		case "hybrid":
			hits, err = kbStore.HybridSearch(query, perKB, filter)
		default:
			hits, err = kbStore.Search(query, perKB, filter)
		}
		if err != nil {
			// Log and continue — one KB failure shouldn't block others.
			continue
		}
		allHits = append(allHits, hits...)
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

func registerListKBs(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_list_kbs",
		mcp.WithDescription(store.ToolListKBsDesc()),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kbs, err := store.ListKBsInfo()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list KBs failed: %v", err)), nil
		}
		type kbEntry struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		entries := make([]kbEntry, len(kbs))
		for i, kb := range kbs {
			desc := kb.Description
			if desc == "" {
				desc = "(no description)"
			}
			entries[i] = kbEntry{Name: kb.Name, Description: desc}
		}
		result := map[string]any{
			"count":          len(entries),
			"knowledgeBases": entries,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		logger.WithModule("tool").Debugf("knowledge_list_kbs: count=%d", len(entries))
		return mcp.NewToolResultText(string(data)), nil
	})
}

func registerList(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_list",
		mcp.WithDescription(store.ToolListDesc()),
		mcp.WithString("kbName",
			mcp.Description("Optional knowledge base name. When set, list only documents in that KB. When omitted, list all KBs."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		kbName := getString(req, "kbName")
		var display, full []knowledge.DocumentMeta
		var err error
		if kbName != "" {
			display, full, err = store.WithKB(kbName).ListPreview(10)
		} else {
			display, full, err = store.ListPreviewAll(10)
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(display) == 0 {
			return mcp.NewToolResultText("Knowledge base is empty."), nil
		}
		logger.WithModule("tool").Debugf("knowledge_list: kb=%q total=%d displayed=%d", kbName, len(full), len(display))

		// Notify the user if there are more docs than shown.
		var msg string
		if len(full) > 10 {
			msg = fmt.Sprintf("Showing %d of %d documents. Full list saved to snapshot file.\n\n", len(display), len(full))
		}

		data, _ := json.MarshalIndent(display, "", "  ")
		return mcp.NewToolResultText(msg + string(data)), nil
	})
}

func registerUpload(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_upload",
		mcp.WithDescription(store.ToolUploadDesc()),
		mcp.WithString("filePath",
			mcp.Description("Path to a single document file. Mutually exclusive with 'directory'."),
		),
		mcp.WithString("directory",
			mcp.Description("Directory path for batch upload. Mutually exclusive with 'filePath'."),
		),
		mcp.WithBoolean("recursive",
			mcp.Description("When true, recursively walk subdirectories (for batch upload)."),
		),
		mcp.WithString("tags",
			mcp.Description("Comma-separated tags to assign to the uploaded document(s)."),
		),
		mcp.WithString("kbName",
			mcp.Description("Knowledge base name. Required when no default KB is configured via KNOWLEDGE_MCP_DEFAULT_KB."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath := getString(req, "filePath")
		directory := getString(req, "directory")
		tags := parseTags(getString(req, "tags"))
		recursive := false
		if v, ok := req.Params.Arguments["recursive"].(bool); ok {
			recursive = v
		}

		kbName := getString(req, "kbName")
		if kbName == "" {
			kbs, listErr := store.ListKBs()
			if listErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("list KBs failed: %v", listErr)), nil
			}
			if len(kbs) == 0 {
				return mcp.NewToolResultError("No knowledge base exists. Create one first via the management UI."), nil
			}
			return mcp.NewToolResultError("kbName is required when no default KB is configured. Specify which knowledge base to upload to."), nil
		}
		uploadStore := store.WithKB(kbName)
		tlog := logger.WithModule("tool")

		if directory != "" {
			if filePath != "" {
				return mcp.NewToolResultError("filePath and directory are mutually exclusive"), nil
			}
			if strings.Contains(directory, "..") {
				return mcp.NewToolResultError("invalid directory path"), nil
			}
			tlog.Debugf("knowledge_upload: directory=%q recursive=%v kb=%q", directory, recursive, kbName)
			summary, err := uploadStore.UploadDirectory(directory, recursive, tags...)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("batch upload failed: %v", err)), nil
			}
			return mcp.NewToolResultText(summary), nil
		}

		if filePath == "" {
			return mcp.NewToolResultError("filePath or directory is required for upload"), nil
		}
		if strings.Contains(filePath, "..") {
			return mcp.NewToolResultError("invalid file path"), nil
		}
		meta, err := uploadStore.UploadDocument(filePath, tags...)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("upload failed: %v", err)), nil
		}
		tlog.Debugf("knowledge_upload: file=%q slug=%q chunks=%d chars=%d", filePath, meta.Slug, meta.ChunkCount, meta.TotalChars)
		return mcp.NewToolResultText(
			fmt.Sprintf("Document uploaded: %s (%d chunks, %d chars)",
				meta.OriginalName, meta.ChunkCount, meta.TotalChars),
		), nil
	})
}

func registerRemove(s *server.MCPServer, store *knowledge.Store, logger *logging.Logger) {
	tool := mcp.NewTool("knowledge_remove",
		mcp.WithDescription(store.ToolRemoveDesc()),
		mcp.WithString("docSlug",
			mcp.Required(),
			mcp.Description("Document slug to remove (from list results)."),
		),
		mcp.WithString("kbName",
			mcp.Description("Optional knowledge base name. When set, remove from that KB. When omitted, remove from all KBs."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docSlug := getString(req, "docSlug")
		if docSlug == "" {
			return mcp.NewToolResultError("docSlug is required for remove"), nil
		}

		if strings.Contains(docSlug, "..") {
			return mcp.NewToolResultError("invalid docSlug"), nil
		}

		kbName := getString(req, "kbName")
		tlog := logger.WithModule("tool")
		tlog.Debugf("knowledge_remove: slug=%q kb=%q", docSlug, kbName)
		if kbName != "" {
			if err := store.WithKB(kbName).RemoveDocument(docSlug); err != nil {
				tlog.Errorf("knowledge_remove: slug=%q kb=%q failed: %v", docSlug, kbName, err)
				return mcp.NewToolResultError(fmt.Sprintf("remove failed: %v", err)), nil
			}
		} else {
			// Try to remove from all KBs
			kbs, err := store.ListKBs()
			if err != nil {
				tlog.Errorf("knowledge_remove: list KBs failed: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("list KBs failed: %v", err)), nil
			}
			removed := false
			for _, kb := range kbs {
				if err := store.WithKB(kb).RemoveDocument(docSlug); err == nil {
					removed = true
					break
				}
			}
			if !removed {
				return mcp.NewToolResultError(fmt.Sprintf("document %q not found in any KB", docSlug)), nil
			}
		}
		tlog.Debugf("knowledge_remove: slug=%q done", docSlug)
		return mcp.NewToolResultText(fmt.Sprintf("Document %q removed.", docSlug)), nil
	})
}

// --- helpers ---

