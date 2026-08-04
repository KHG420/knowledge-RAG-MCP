# knowledge-mcp

[中文](README_zh.md) | [📖 User Guide](GUIDE.md)

> ⚡ **No need to build a knowledge base from scratch — just connect MCP, and your agent gets an intelligent knowledge base instantly.**
>
> Drop in documents → auto chunk & index → BM25 + vector hybrid search + cross-encoder rerank → plug & play, zero ops.

MCP (Model Context Protocol) server that provides a local, file-based knowledge base with BM25 keyword search, hybrid (BM25 + vector) retrieval, and optional two-stage Cross-Encoder reranking.

---

## Table of Contents

- [Features](#features)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Architecture](#architecture)
- [License](#license)

> 📖 For detailed configuration, MCP tools, search pipeline, caching, deployment, and troubleshooting, see the **[User Guide](GUIDE.md)**.

---

## Features

- **Document ingestion** — PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX, MD, TXT
- **Query triage** — zero-cost rule-based router: 70% simple queries use dictionary expansion, 25% medium queries use multi-synonym variants, 5% complex queries trigger LLM rewriting
- **BM25 search** — Unicode-aware, CJK bigram-aware tokenizer with query rewriting, synonym expansion, and optional LLM query expansion
- **LLM query rewriting** — optional DeepSeek LLM-driven query expansion generating 2–4 rewritten variants; only triggered for complex queries, with graceful fallback
- **BM25/vector decoupling** — exact synonyms go to BM25 only, related terms go to vector side only, preventing cross-contamination
- **Hybrid search** — BM25 + dense embedding fusion via Reciprocal Rank Fusion (RRF) with adaptive query-type weighting
- **Two-stage reranking** — optional Cross-Encoder (Infinity/Cohere-compatible) to re-rank the top-K recalls for improved precision
- **Paragraph-level chunking** — semantic-boundary splitting, overlap, hierarchical fine + coarse sections, section-role classification
- **Page-aware chunking** — PDF chunks carry `page_start` / `page_end` metadata, surfaced in search results and chunk reads
- **Parent-child retrieval** — read a chunk's full parent section for richer context
- **Paper metadata extraction** — title, authors, abstract, section-role detection for academic papers
- **Multi-knowledge-base** — organize documents into isolated KBs; cross-KB search and listing; create/delete KBs via management UI
- **Intelligent KB routing** — auto-route queries to the most relevant knowledge base(s) using four-dimension weighted scoring
- **Domain dictionary support** — load YAML-based domain synonym dictionaries for query expansion
- **MySQL/MariaDB backend** — optional database storage backend replacing the default filesystem
- **Redis cache** — optional exact-match query result cache; built-in LRU memory cache always on
- **Soft delete (tombstone)** — document removal uses a TTL tombstone pattern
- **Incremental indexing & versioning** — re-uploading a document only re-indexes changed chunks
- **Evidence quality signals** — each result carries source_confidence, answer_relevance, and completeness metadata

---

## Installation

```bash
go build -o knowledge-mcp .
```

The resulting `knowledge-mcp` binary is self-contained and ready to run.

---

## Quick Start

### Minimal (BM25 only, zero dependencies)

```bash
export KNOWLEDGE_MCP_DATA_DIR=./kb-data
knowledge-mcp serve
```

Web management UI auto-starts at `http://localhost:8085`.

### Full stack (BM25 + embeddings + reranker)

See [docs/deployment-models.md](docs/deployment-models.md) / [中文版](docs/deployment-models_zh.md) for detailed model deployment instructions.

```bash
# Embedding service (Ollama + BGE-M3)
ollama pull bge-m3

# Reranker service (Infinity + gte-multilingual-reranker-base)
pip install infinity-emb[all]
infinity_emb v2 --model-id Alibaba-NLP/gte-multilingual-reranker-base --port 7997

# knowledge-mcp
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings \
EMBED_MODEL=bge-m3 \
RERANK_API_ENDPOINT=http://localhost:7997/rerank \
RERANK_CANDIDATE_LIMIT=100 \
KNOWLEDGE_MCP_DATA_DIR=./kb-data \
  knowledge-mcp serve
```

### MySQL/MariaDB backend

```bash
# Connect via DSN
MYSQL_DSN="user:password@tcp(127.0.0.1:3306)/knowledge_rag?parseTime=true" \
  knowledge-mcp serve
```

On first startup, the required tables are created automatically.

### MCP client integration (stdio)

For **Reasonix**, **Claude Desktop**, **Cline**, etc., use `.mcp.json` in your project root:

```json
{
  "mcpServers": {
    "knowledge-mcp": {
      "command": "/path/to/knowledge-mcp",
      "args": ["stdio"]
    }
  }
}
```

---

## Architecture

The project follows a **Facade pattern**: `Store` is the unified entry point, delegating to 6 sub-packages
(search, chunkstore, kb, dict, ingest, manage) that each implement a well-defined interface from `interfaces.go`.

```
main.go                     — CLI entry point, subcommands (stdio / serve / manage / dict), tool registration
init.go                     — Dependency injection: assembles all sub-package engines into the Store facade
tools.go / tools_*.go       — MCP tool registration (knowledge_research/read/list/list_kbs/upload/remove)
serve.go / stdio.go          — HTTP SSE / Streamable HTTP / stdio MCP transports
dict.go                     — Dictionary management subcommands (mine / gen)
manage_run.go               — Web management UI launcher
internal/
  config/
    config.go               — TOML config loading, env-var fallback, defaults
  setup/
    setup.go                — Interactive configuration wizard
    i18n.go                 — Internationalization strings
  logging/
    logger.go               — Structured file logger (DEBUG/INFO/WARN/ERROR, module-scoped)
  cache/
    cache.go                — Cache interface + NoopCache fallback
    redis.go                — Redis-backed cache implementation
    keys.go                 — Cache key naming conventions
  knowledge/
    interfaces.go           — Core interfaces: Searcher, ChunkStore, Ingester, KBAdmin, DictService, ManageService, CacheClient
    store.go                — Store Facade (~160 methods), delegates to sub-package engines
    storage.go              — StorageBackend interface
    mysql_backend.go        — MySQL/MariaDB storage backend
    ── Sub-packages (engine implementations) ──
    search/                 — Retrieval engine (implements Searcher)
      engine.go             —   6 core search methods + 2 inverted-index write ops + query caching
      query.go              —   Query rewriting, complexity analysis, RRF weight tuning
      collect.go            —   Candidate collection, inverted-index fast path
      rerank.go             —   Coarse-to-fine filtering, cross-encoder rerank, cache
      helpers.go            —   Dedup, sort, cosine similarity utilities
      types.go              —   Internal type definitions
      retrieval/            —   BM25 tokenizer (CJK bigram-aware), BM25Score, MakeSnippet
    chunkstore/             — Chunk I/O engine (implements ChunkStore)
      engine.go             —   26 CRUD methods + 3-tier cache (chunk/meta/index)
    kb/                     — KB admin engine (implements KBAdmin)
      engine.go             —   7 methods: List/Create/Delete KBs + routing + cache
    dict/                   — Dictionary engine (implements DictService)
      engine.go             —   LoadDictionaries, GenerateDictionary, RunDictMine, RunDictGen
    ingest/                 — Document ingestion engine (implements Ingester)
      engine.go             —   UploadDocument, UploadDirectory, CopySource + full pipeline
    manage/                 — Web management engine (uses ManageService interface)
      server.go             —   Server struct (14 fields + DI constructor)
      helpers.go            —   Shared utilities (JSON responses, file upload, SSE, model probing)
      handlers_core.go      —   17 core handlers (doc CRUD, search, tombstone, vector, reconciliation)
      handlers_enhanced.go  —   13 enhanced handlers (health, GPU, logs, metrics, import/export)
      handlers_config.go    —   5 config handlers + hot-reload logic
      router.go             —   Route registration, Start(), middleware, background goroutines
    ── Legacy files (kept for ManageService interface & nil-engine fallback) ──
    embed.go, rerank.go, vector_index.go, gpu_scheduler.go, kb_router.go,
    rewrite.go, rewrite_llm.go, query_triage.go, dict_loader.go, dict_generator.go,
    synonym_miner.go, chunker.go, doc.go, parser.go, upload.go, upload_task.go,
    inverted.go, remove.go, tombstone.go, version.go, store_incremental.go,
    manifest.go, reconcile.go, state_machine.go, chunk_id.go, config_api.go,
    store_settings.go, middleware.go, tool_descs.go, searchlog.go, meta_extract.go
cmd/
  cleanup-vector/           — HNSW vector index orphan entry cleanup tool
dictionaries/               — Domain dictionary YAML files
scripts/
  eval.go                   — Retrieval evaluation script (NDCG@5, MRR, Recall@10)
service-manager.sh          — Service management script for Ollama + Infinity dependencies
docs/
  deployment-models.md      — Embedding & reranker model deployment guide
  deployment-models_zh.md
  roadmap.md                — RAG optimization roadmap
  roadmap_zh.md
```

---

## License

MIT
