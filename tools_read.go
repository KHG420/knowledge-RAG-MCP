package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

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
			if !isPathSafe(v) {
				return mcp.NewToolResultError(fmt.Sprintf("invalid parameter %q", v)), nil
			}
		}

		kbName := getString(req, "kbName")
		if kbName != "" && !isPathSafe(kbName) {
			return mcp.NewToolResultError(fmt.Sprintf("invalid kbName %q", kbName)), nil
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
