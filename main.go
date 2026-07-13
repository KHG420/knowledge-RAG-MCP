package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
)

func main() {
	dataDir := os.Getenv("KNOWLEDGE_MCP_DATA_DIR")
	store := knowledge.NewStore(".")
	if dataDir != "" {
		store.WithDataDir(dataDir)
	}
	if err := store.EnsureDir(); err != nil {
		log.Fatalf("failed to init data dir: %v", err)
	}

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
		mcp.WithDescription(`BM25/hybrid search across all documents in the knowledge base. Use distinctive keywords.`),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query. Use distinctive keywords."),
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
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, _ := req.Params.Arguments["query"].(string)
		if query == "" {
			return mcp.NewToolResultError("query is required for search"), nil
		}

		limit := 8
		if v, ok := req.Params.Arguments["limit"].(float64); ok && v > 0 {
			limit = int(v)
		}
		if limit > 20 {
			limit = 20
		}

		filter := knowledge.SearchFilter{
			SourceType: getString(req, "sourceType"),
			Section:    getString(req, "section"),
		}

		var hits []knowledge.SearchHit
		var err error
		switch strings.ToLower(getString(req, "mode")) {
		case "hybrid":
			hits, err = store.HybridSearch(query, limit, filter)
		default:
			hits, err = store.Search(query, limit, filter)
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
		mcp.WithDescription(`Read a specific chunk from a document in the knowledge base.`),
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
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docSlug := getString(req, "docSlug")
		chunkID := getString(req, "chunkID")
		if docSlug == "" || chunkID == "" {
			return mcp.NewToolResultError("docSlug and chunkID are required"), nil
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
			text, err := readSection(store, docSlug, chunkID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(text), nil
		}

		text, err := store.ReadChunkContext(docSlug, chunkID, ctxCount)
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

func registerList(s *server.MCPServer, store *knowledge.Store) {
	tool := mcp.NewTool("knowledge_list",
		mcp.WithDescription(`List all uploaded documents in the knowledge base.`),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docs, err := store.List()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(docs) == 0 {
			return mcp.NewToolResultText("Knowledge base is empty."), nil
		}
		data, _ := json.MarshalIndent(docs, "", "  ")
		return mcp.NewToolResultText(string(data)), nil
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
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath := getString(req, "filePath")
		directory := getString(req, "directory")
		recursive := false
		if v, ok := req.Params.Arguments["recursive"].(bool); ok {
			recursive = v
		}

		if directory != "" {
			if filePath != "" {
				return mcp.NewToolResultError("filePath and directory are mutually exclusive"), nil
			}
			summary, err := store.UploadDirectory(directory, recursive)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("batch upload failed: %v", err)), nil
			}
			return mcp.NewToolResultText(summary), nil
		}

		if filePath == "" {
			return mcp.NewToolResultError("filePath or directory is required for upload"), nil
		}
		meta, err := store.UploadDocument(filePath)
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
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docSlug := getString(req, "docSlug")
		if docSlug == "" {
			return mcp.NewToolResultError("docSlug is required for remove"), nil
		}
		if err := store.RemoveDocument(docSlug); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("remove failed: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Document %q removed.", docSlug)), nil
	})
}

// --- helpers ---

func getString(req mcp.CallToolRequest, key string) string {
	v, _ := req.Params.Arguments[key].(string)
	return v
}
