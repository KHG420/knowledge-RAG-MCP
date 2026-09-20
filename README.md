# knowledge-mcp

[中文](README_zh.md) | [📖 User Guide](GUIDE.md)

> ⚡ **No need to build a knowledge base from scratch — just connect MCP, and your agent gets an intelligent knowledge base instantly.**
>
> Drop in documents → auto chunk & index → BM25 keyword search, with optional hybrid (vector) retrieval and cross-encoder rerank → requires an existing MySQL/MariaDB database.

MCP (Model Context Protocol) server that provides a knowledge base on a MySQL/MariaDB storage backend, with BM25 keyword search, optional hybrid (BM25 + vector) retrieval, and optional two-stage Cross-Encoder reranking. Embedding and reranking models are opt-in; the server never assumes they are available.

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
- **MySQL/MariaDB backend** — required storage backend for documents, chunks, and KB metadata
- **Redis cache** — optional exact-match query result cache; built-in LRU memory cache always on
- **Soft delete (tombstone)** — document removal uses a TTL tombstone pattern
- **Incremental indexing & versioning** — re-uploading a document only re-indexes changed chunks
- **Evidence provenance** — each read result carries source_confidence plus document/location/citation_id; answer_relevance and completeness are reported as `unknown` so the agent judges them instead of trusting a heuristic

---

## Installation

Requirements: Go 1.24+ and an existing MySQL/MariaDB database.

```bash
go build -o knowledge-mcp .
```

Dependencies are vendored under `vendor/`, so the default build works without
network access. MySQL/MariaDB is **required at runtime** — there is no
filesystem-only or zero-external-dependency mode.

---

## Quick Start

This is the single supported onboarding path. For a copy-paste verification
script, see [docs/onboarding.md](docs/onboarding.md).

### 1. Build

```bash
go build -o knowledge-mcp .
```

### 2. Provision MySQL and configure

Create a database (and user) in an existing MySQL/MariaDB instance. The server
creates its tables on first startup; it does not create the database itself.

Then run the interactive wizard, which writes `knowledge-mcp.toml` next to the
executable. The wizard does not connect to MySQL and does not modify any
service:

```bash
./knowledge-mcp setup
```

You can also write the file directly. Minimal example:

```toml
mysql_dsn = "user:password@tcp(127.0.0.1:3306)/knowledge_rag?parseTime=true"
```

Configuration precedence:

1. `knowledge-mcp.toml` next to the executable (highest priority)
2. Environment variables, only when no TOML file exists
3. Built-in defaults

### 3. Start the server

```bash
./knowledge-mcp serve          # management UI (:8085) + MCP HTTP (:8086)
./knowledge-mcp serve --mcp    # MCP HTTP only
./knowledge-mcp manage         # management UI only (use alongside stdio)
./knowledge-mcp stdio          # stdio MCP (Reasonix / Claude Desktop / Cline)
```

### 4. Create a KB and upload documents

Open `http://localhost:8085`, create a knowledge base, and upload documents.
Ingestion and KB creation are done from the management UI; the MCP tools exposed
to agents are read/search only.

### 5. Connect an MCP client

- **HTTP (Streamable)**: `http://localhost:8086/mcp`
- **HTTP (legacy SSE)**: `/sse` + `/message` on port 8086
- **stdio**: configure `.mcp.json` in your project root:

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

If `api_token` is set in the TOML, both the MCP HTTP endpoints (`/mcp`, `/sse`,
`/message`) and the management API require `Authorization: Bearer <token>`.
stdio does not use the token. Do not expose the HTTP port without a token.

### Registered MCP tools

Exactly three tools are registered:

| Tool | Purpose |
| --- | --- |
| `knowledge_research` | Search for ranked evidence passages |
| `knowledge_read` | Read one chunk/section with provenance |
| `knowledge_list_kbs` | List knowledge bases |

`knowledge_research` returns a JSON envelope: `results` (always an array),
`searched_kbs`, `failed_kbs`, `warnings`, and `coverage` (`complete` or
`partial`, describing the search — not whether the results answer the
question). `complete` means every knowledge base the server selected and
attempted was searched successfully; it does not mean all knowledge bases were
searched, so an empty `results` array is not proof that nothing exists. This
replaces the older bare JSON array / plain-string behavior.

### Optional embedding and reranking

BM25 search works with no extra services. Hybrid retrieval and cross-encoder
reranking are opt-in and only activate when their endpoints are configured in
`knowledge-mcp.toml` (or, with no TOML file, the corresponding environment
variables). See [docs/deployment-models.md](docs/deployment-models.md) /
[中文版](docs/deployment-models_zh.md).

```bash
# Example: embedding via Ollama, reranking via Infinity
ollama pull bge-m3
pip install infinity-emb[all]
infinity_emb v2 --model-id Alibaba-NLP/gte-multilingual-reranker-base --port 7997
```

```toml
embed_endpoint = "http://localhost:11434/v1/embeddings"
embed_model = "bge-m3"
rerank_endpoint = "http://localhost:7997/rerank"
rerank_candidate_limit = 100
```

---

## Architecture

The project follows a **Facade pattern**: `Store` is the unified entry point, delegating to 6 sub-packages
(search, chunkstore, kb, dict, ingest, manage) that each implement a well-defined interface from `interfaces.go`.

```
main.go                     — CLI entry point, subcommands (serve / stdio / manage / setup / dict), tool registration
init.go                     — Dependency injection: assembles all sub-package engines into the Store facade
tools_*.go                  — MCP tool registration (only knowledge_research, knowledge_read, knowledge_list_kbs are registered)
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
