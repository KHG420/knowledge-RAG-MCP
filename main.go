package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
)

func main() {
	dataDir := os.Getenv("KNOWLEDGE_MCP_DATA_DIR")
	defaultKB := os.Getenv("KNOWLEDGE_MCP_DEFAULT_KB")
	store := knowledge.NewStore(".")
	if dataDir != "" {
		store.WithDataDir(dataDir)
	}
	if defaultKB != "" {
		store = store.WithKB(defaultKB)
		log.Printf("[knowledge-mcp] default KB: %s", defaultKB)
	}
	if err := store.EnsureDir(); err != nil {
		log.Fatalf("failed to init data dir: %v", err)
	}

	// --- Optional: vector embedder (OpenAI-compatible API, e.g. Ollama) ---
	if baseURL := os.Getenv("EMBED_API_BASE_URL"); baseURL != "" {
		opts := []knowledge.OpenAIEmbedderOption{knowledge.WithBaseURL(baseURL)}
		if key := os.Getenv("EMBED_API_KEY"); key != "" {
			opts = append(opts, knowledge.WithAPIKey(key))
		}
		model := os.Getenv("EMBED_MODEL")
		if model == "" {
			model = "text-embedding-ada-002"
		}
		opts = append(opts, knowledge.WithModel(model))
		if dimStr := os.Getenv("EMBED_DIM"); dimStr != "" {
			if dim, err := strconv.Atoi(dimStr); err == nil && dim > 0 {
				opts = append(opts, knowledge.WithDim(dim))
			}
		}
		store.SetEmbedder(knowledge.NewOpenAIEmbedder(opts...))
		log.Printf("[knowledge-mcp] embedder: %s (model=%s)", baseURL, model)
	}

	// --- Optional: cross-encoder reranker (Infinity/Cohere-compatible API) ---
	if baseURL := os.Getenv("RERANK_API_BASE_URL"); baseURL != "" {
		opts := []knowledge.InfinityRerankerOption{knowledge.WithRerankBaseURL(baseURL)}
		if key := os.Getenv("RERANK_API_KEY"); key != "" {
			opts = append(opts, knowledge.WithRerankAPIKey(key))
		}
		if model := os.Getenv("RERANK_MODEL"); model != "" {
			opts = append(opts, knowledge.WithRerankModel(model))
		}
		store.SetReranker(knowledge.NewInfinityReranker(opts...))
		log.Printf("[knowledge-mcp] reranker: %s", baseURL)
	}

	// --- Optional: rerank candidate limit (default 100) ---
	if s := os.Getenv("RERANK_CANDIDATE_LIMIT"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			store.SetRerankCandidateLimit(n)
			log.Printf("[knowledge-mcp] rerank candidate limit: %d", n)
		}
	}

	// --- Web management UI (default: http://localhost:8084) ---
	managePort := os.Getenv("MANAGE_PORT")
	if managePort == "" {
		managePort = "8084"
	}
	go func() {
		kbInfo := defaultKB
		if kbInfo == "" {
			kbInfo = "(none)"
		}
		log.Printf("[knowledge-mcp] management UI → http://localhost:%s (default KB: %s)", managePort, kbInfo)
		if err := store.StartManageServer(managePort); err != nil {
			log.Printf("[knowledge-mcp] management UI error: %v", err)
		}
	}()

	s := server.NewMCPServer(
		"knowledge-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	registerSearch(s, store)
	registerRead(s, store)
	registerList(s, store)
	registerUpload(s, store)
	registerRemove(s, store)

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// --- Tool registration ---

func registerSearch(s *server.MCPServer, store *knowledge.Store) {
	tool := mcp.NewTool("knowledge_search",
		mcp.WithDescription(`BM25/hybrid keyword search across all documents in the knowledge base.

BEFORE CALLING: you MUST rewrite the user's question into a space-separated string of distinctive keywords and phrases. Do NOT pass the raw question verbatim. Fix typos, resolve pronouns from conversation context, add synonyms and related terms (Chinese + English where applicable).

Examples of required rewriting:
  User: "how to chunk documents?"
    → search_keywords: "chunking text splitting segmentation document chunk longChunk shortChunk overlap"
  User: (after discussing chunking) "它的参数有哪些？"
    → search_keywords: "分块 chunking 参数 longChunk shortChunk overlapChars fragmentThreshold"
  User: "embeding vs retrieval"
    → search_keywords: "embedding vector retrieval search dense sparse BM25 hybrid"`),
		mcp.WithString("search_keywords",
			mcp.Required(),
			mcp.Description("REWRITTEN keyword string (space-separated terms) — NOT the user's raw question. Fix typos, expand context, add synonyms. Use distinctive keywords the documents are likely to contain."),
		),
		mcp.WithString("original_question",
			mcp.Description("The user's original question verbatim, for logging purposes."),
		),
		mcp.WithString("query",
			mcp.Description("DEPRECATED: use search_keywords instead. Fallback for backward compatibility."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum results to return. Default 8, max 20."),
		),
		mcp.WithString("mode",
			mcp.Description("Search mode: 'bm25' (default, keyword) or 'hybrid' (BM25 + embedding). Requires embedder for hybrid."),
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
			mcp.Description("Optional knowledge base name. When set, search only within that KB. When omitted, search all KBs."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Prefer search_keywords; fall back to deprecated query param.
		searchKW := getString(req, "search_keywords")
		if searchKW == "" {
			searchKW = getString(req, "query")
		}
		if searchKW == "" {
			return mcp.NewToolResultError("search_keywords is required — rewrite the user's question into distinctive keywords before calling"), nil
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
		searchStore := store
		if kbName != "" {
			searchStore = store.WithKB(kbName)
		}

		var hits []knowledge.SearchHit
		var err error
		switch strings.ToLower(getString(req, "mode")) {
		case "hybrid":
			if kbName != "" {
				hits, err = searchStore.HybridSearch(searchKW, limit, filter)
			} else {
				// HybridSearchAll not implemented; fallback to SearchAll
				hits, err = searchStore.SearchAll(searchKW, limit, filter)
			}
		default:
			if kbName != "" {
				hits, err = searchStore.Search(searchKW, limit, filter)
			} else {
				hits, err = searchStore.SearchAll(searchKW, limit, filter)
			}
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
		}
		if len(hits) == 0 {
			return mcp.NewToolResultText("No matching chunks found."), nil
		}
		data, _ := json.MarshalIndent(hits, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
	})
}

func registerRead(s *server.MCPServer, store *knowledge.Store) {
	tool := mcp.NewTool("knowledge_read",
		mcp.WithDescription(`Read a specific chunk from a document in the knowledge base.

If search results show multiple hits from the same section (SectionHint field is non-empty), consider reading with level=section to get the full section context instead of just the individual chunk.`),
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
			mcp.Description("Optional knowledge base name. Required when the document slug is not unique across KBs."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docSlug := getString(req, "docSlug")
		chunkID := getString(req, "chunkID")
		if docSlug == "" || chunkID == "" {
			return mcp.NewToolResultError("docSlug and chunkID are required"), nil
		}

		kbName := getString(req, "kbName")

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
			return mcp.NewToolResultText(text), nil
		}

		text, err := tryReadChunk(store, kbName, docSlug, chunkID, ctxCount)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(text), nil
	})
}

func readSection(store *knowledge.Store, docSlug, chunkID string) (string, error) {
	index, err := store.ReadChunksIndex(docSlug)
	if err != nil || index == nil {
		return "", fmt.Errorf("no index found for document %q", docSlug)
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
	for _, kb := range kbs {
		text, err := store.WithKB(kb).ReadChunkContext(docSlug, chunkID, ctxCount)
		if err == nil {
			return text, nil
		}
	}
	return "", fmt.Errorf("document %q not found in any KB", docSlug)
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
	for _, kb := range kbs {
		text, err := readSection(store.WithKB(kb), docSlug, chunkID)
		if err == nil {
			return text, nil
		}
	}
	return "", fmt.Errorf("document %q not found in any KB", docSlug)
}

func registerList(s *server.MCPServer, store *knowledge.Store) {
	tool := mcp.NewTool("knowledge_list",
		mcp.WithDescription(`List all uploaded documents in the knowledge base.`),
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

		// Notify the user if there are more docs than shown.
		var msg string
		if len(full) > 10 {
			msg = fmt.Sprintf("Showing %d of %d documents. Full list saved to snapshot file.\n\n", len(display), len(full))
		}

		data, _ := json.MarshalIndent(display, "", "  ")
		return mcp.NewToolResultText(msg + string(data)), nil
	})
}

func registerUpload(s *server.MCPServer, store *knowledge.Store) {
	tool := mcp.NewTool("knowledge_upload",
		mcp.WithDescription(`Upload a document file or batch-upload a directory into the knowledge base. Supports PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX, MD, TXT.`),
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

		if directory != "" {
			if filePath != "" {
				return mcp.NewToolResultError("filePath and directory are mutually exclusive"), nil
			}
			summary, err := uploadStore.UploadDirectory(directory, recursive, tags...)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("batch upload failed: %v", err)), nil
			}
			return mcp.NewToolResultText(summary), nil
		}

		if filePath == "" {
			return mcp.NewToolResultError("filePath or directory is required for upload"), nil
		}
		meta, err := uploadStore.UploadDocument(filePath, tags...)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("upload failed: %v", err)), nil
		}
		return mcp.NewToolResultText(
			fmt.Sprintf("Document uploaded: %s (%d chunks, %d chars)",
				meta.OriginalName, meta.ChunkCount, meta.TotalChars),
		), nil
	})
}

func registerRemove(s *server.MCPServer, store *knowledge.Store) {
	tool := mcp.NewTool("knowledge_remove",
		mcp.WithDescription(`Remove a document and all its chunks from the knowledge base.`),
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

		kbName := getString(req, "kbName")
		if kbName != "" {
			if err := store.WithKB(kbName).RemoveDocument(docSlug); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("remove failed: %v", err)), nil
			}
		} else {
			// Try to remove from all KBs
			kbs, err := store.ListKBs()
			if err != nil {
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
		return mcp.NewToolResultText(fmt.Sprintf("Document %q removed.", docSlug)), nil
	})
}

// --- helpers ---

func getString(req mcp.CallToolRequest, key string) string {
	v, _ := req.Params.Arguments[key].(string)
	return v
}

func getBool(req mcp.CallToolRequest, key string) bool {
	v, _ := req.Params.Arguments[key].(bool)
	return v
}

// parseTags splits a comma-separated tag string, trims whitespace,
// and filters out empty strings.
func parseTags(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseTime parses an ISO 8601 date string, supporting both date-only
// ("2006-01-02") and full RFC 3339 ("2006-01-02T15:04:05Z07:00") formats.
// Returns the zero time on empty input or parse failure.
func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t
	}
	return time.Time{}
}
