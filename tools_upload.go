package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

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
