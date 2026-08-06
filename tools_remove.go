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

		if !isPathSafe(docSlug) {
			return mcp.NewToolResultError("invalid docSlug"), nil
		}

		kbName := getString(req, "kbName")
		if kbName != "" && !isPathSafe(kbName) {
			return mcp.NewToolResultError(fmt.Sprintf("invalid kbName %q", kbName)), nil
		}
		tlog := logger.WithModule("tool")
		tlog.Debugf("knowledge_remove: slug=%q kb=%q", docSlug, kbName)

		var removedFrom []string
		if kbName != "" {
			if err := store.WithKB(kbName).RemoveDocument(docSlug); err != nil {
				tlog.Errorf("knowledge_remove: slug=%q kb=%q failed: %v", docSlug, kbName, err)
				return mcp.NewToolResultError(fmt.Sprintf("remove failed: %v", err)), nil
			}
			removedFrom = []string{kbName}
		} else {
			// Remove from every KB that contains this document.
			kbs, err := store.ListKBs()
			if err != nil {
				tlog.Errorf("knowledge_remove: list KBs failed: %v", err)
				return mcp.NewToolResultError(fmt.Sprintf("list KBs failed: %v", err)), nil
			}
			var errs []string
			for _, kb := range kbs {
				if err := store.WithKB(kb).RemoveDocument(docSlug); err == nil {
					removedFrom = append(removedFrom, kb)
				} else {
					errs = append(errs, fmt.Sprintf("%s: %v", kb, err))
				}
			}
			if len(removedFrom) == 0 {
				detail := strings.Join(errs, "; ")
				if detail == "" {
					detail = "no KBs available"
				}
				return mcp.NewToolResultError(fmt.Sprintf("document %q not found in any KB: %s", docSlug, detail)), nil
			}
		}

		msg := fmt.Sprintf("Document %q removed.", docSlug)
		if len(removedFrom) > 1 {
			msg = fmt.Sprintf("Document %q removed from %d KBs.", docSlug, len(removedFrom))
		}
		tlog.Debugf("knowledge_remove: slug=%q done (%d KBs)", docSlug, len(removedFrom))
		return mcp.NewToolResultText(msg), nil
	})
}
