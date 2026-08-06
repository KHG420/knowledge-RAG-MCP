// E2E Mock Server — starts an HTTP management server with mock data for
// Playwright end-to-end tests. Run with:
//
//	go run ./cmd/e2e-mock [--port=8085]
//
// The server uses an in-memory mock backend and is preloaded with test
// documents covering multiple file types so the UI has data to display.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/knowledge/manage"
	"knowledge-mcp/internal/logging"
)

func main() {
	port := flag.String("port", "8085", "HTTP listen port")
	flag.Parse()

	tmp, err := os.MkdirTemp("", "e2e-mock-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	// ── Create store with mock backend ──────────────────────────────────
	backend := knowledge.NewMockBackend()
	store := knowledge.NewStoreWithBackend(backend)
	store.SetDataDir(tmp)
	log := logging.NewNopLogger()
	store.SetLogger(log)

	// ── Set up mock embedder + reranker ─────────────────────────────────
	store.SetEmbedder(knowledge.NewMockEmbedder(256))
	store.SetReranker(&knowledge.MockReranker{})

	// ── Create default KB and upload test documents ─────────────────────
	if err := store.CreateKB("test-kb", "E2E test knowledge base"); err != nil {
		fmt.Fprintf(os.Stderr, "CreateKB: %v\n", err)
		os.Exit(1)
	}
	store = store.WithKB("test-kb")

	// Create test documents of various types
	createTestDocs(tmp, store, log)

	// ── Configuration ───────────────────────────────────────────────────
	cfg := config.DefaultConfig()
	cfg.DataDir = tmp
	cfg.LogLevel = "info"
	cfgPath := filepath.Join(tmp, "knowledge-mcp.toml")
	store.SetConfig(cfg, cfgPath)

	// ── Manage server ───────────────────────────────────────────────────
	mu := store.Mutex()
	srv := manage.New(
		store, cfg, cfgPath,
		backend,
		store.Embedder(), store.Reranker(), store.VectorIndexRaw(),
		store.KBName(), store.DataDir(),
		store.GPUScheduler(), store.KBRouter(), store.TaskManager(),
		mu, log,
	)

	handler := manage.BuildMux(srv)

	// ── Start HTTP server ───────────────────────────────────────────────
	addr := ":" + *port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ln, err = net.Listen("tcp4", addr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen on %s: %v\n", addr, err)
		os.Exit(1)
	}

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	// Print ready signal for Playwright to detect
	fmt.Fprintf(os.Stderr, "e2e-mock server ready on http://localhost:%s\n", *port)
	fmt.Printf("http://localhost:%s\n", *port)

	// Shutdown on SIGTERM/SIGINT
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(ctx) //nolint:errcheck
	}()

	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func createTestDocs(tmp string, store *knowledge.Store, log *logging.Logger) {
	docs := []struct {
		name    string
		ext     string
		content string
	}{
		{
			name: "introduction",
			ext:  ".md",
			content: `# Introduction

This is a test markdown document for E2E testing.

## Overview

The knowledge-mcp server provides document indexing and search capabilities.

## Features

- BM25 keyword search
- Vector semantic search
- Hybrid search with RRF fusion
- Cross-encoder reranking
`,
		},
		{
			name: "api-reference",
			ext:  ".md",
			content: `# API Reference

## knowledge_research

Search across knowledge bases with hybrid search.

Parameters:
- query (string): The search query
- kbName (string): Knowledge base name
- topK (int): Number of results to return

## knowledge_read

Read specific chunks from a document.

## knowledge_upload

Upload documents to the knowledge base.
`,
		},
		{
			name: "ship-roll-study",
			ext:  ".md",
			content: `# 船舶横摇运动研究综述

## 摘要

船舶横摇是船舶在波浪中航行时最常见和最重要的运动形式之一。

## 1. 引言

横摇运动对船舶的安全性、舒适性和操作性都有重要影响。准确的横摇预测对于船舶设计和航行安全至关重要。

## 2. 横摇运动方程

船舶横摇运动可用以下非线性方程描述：

(I + ΔI)φ̈ + B(φ, φ̇) + C(φ) = M(t)

其中 I 为惯性矩，ΔI 为附加惯性矩，B 为阻尼项，C 为恢复力矩，M(t) 为波浪激励力矩。

## 3. 阻尼建模

横摇阻尼的准确建模是横摇预测中的关键难点。常用的阻尼模型包括：
- 线性阻尼模型
- 二次阻尼模型
- 线性+立方阻尼模型
`,
		},
		{
			name: "data-report",
			ext:  ".md",
			content: `# 2024年度数据报告

## 总体概览

本年度共处理文档 15,432 份，总块数 87,216，平均每文档 5.65 块。

## 文档类型分布

| 类型 | 数量 | 占比 |
|------|------|------|
| PDF  | 8,201 | 53.1% |
| MD   | 4,321 | 28.0% |
| DOCX | 1,892 | 12.3% |
| EPUB | 618   | 4.0% |
| 其他 | 400   | 2.6% |

## 结论

PDF 仍是最主要的文档格式，占比超过一半。
`,
		},
		{
			name: "readme",
			ext:  ".md",
			content: `# knowledge-mcp

A MCP (Model Context Protocol) knowledge server with hybrid search.

## Quick Start

` + "```bash" + `
go build -o knowledge-mcp .
./knowledge-mcp serve --port 8086
` + "```" + `

## Configuration

Edit ` + "`knowledge-mcp.toml`" + ` to configure embeddings, reranker, and storage.
`,
		},
	}

	for _, d := range docs {
		src := filepath.Join(tmp, d.name+d.ext)
		if err := os.WriteFile(src, []byte(d.content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "write doc %s: %v\n", d.name, err)
			continue
		}
		meta, err := store.UploadDocument(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "upload doc %s: %v\n", d.name, err)
			continue
		}
		_ = meta
	}

	// Create a second KB for multi-KB testing
	if err := store.CreateKB("second-kb", "Second test knowledge base"); err != nil {
		fmt.Fprintf(os.Stderr, "CreateKB second-kb: %v\n", err)
	} else {
		saved := store.WithKB("test-kb")
		store2 := store.WithKB("second-kb")
		src := filepath.Join(tmp, "second-doc.md")
		if err := os.WriteFile(src, []byte("# Second KB Document\n\nThis document belongs to the second knowledge base.\n\nIt has a few paragraphs for testing purposes."), 0644); err == nil {
			if _, err := store2.UploadDocument(src); err != nil {
				fmt.Fprintf(os.Stderr, "upload to second-kb: %v\n", err)
			}
		}
		// Switch back to test-kb
		*saved = *store.WithKB("test-kb")
	}
}
