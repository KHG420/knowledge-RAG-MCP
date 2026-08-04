package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

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
