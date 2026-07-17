# knowledge-mcp

[中文](README_zh.md)

> ⚡ **No need to build a knowledge base from scratch — just connect MCP, and your agent gets an intelligent knowledge base instantly.**
>
> Drop in documents → auto chunk & index → BM25 + vector hybrid search + cross-encoder rerank → plug & play, zero ops.

MCP (Model Context Protocol) server that provides a local, file-based knowledge base with BM25 keyword search, hybrid (BM25 + vector) retrieval, and optional two-stage Cross-Encoder reranking.

## Features

- **Document ingestion** — PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX, MD, TXT
- **BM25 search** — Unicode-aware, CJK bigram-aware tokenizer with query rewriting
- **Hybrid search** — BM25 + dense embedding fusion via Reciprocal Rank Fusion (RRF) with adaptive query-type weighting
- **Two-stage reranking** — optional Cross-Encoder (Infinity/Cohere-compatible) to re-rank the top-K recalls for improved precision
- **Paragraph-level chunking** — semantic-boundary splitting, overlap, hierarchical fine + coarse sections, section-role classification
- **Parent-child retrieval** — read a chunk's full parent section for richer context
- **Paper metadata extraction** — title, authors, abstract, section-role detection for academic papers
- **Multi-knowledge-base** — organize documents into isolated KBs; cross-KB search and listing; create/delete KBs via management UI
- **KB descriptions** — assign a brief description when creating a KB; view all KBs and their descriptions via `knowledge_list_kbs` tool

## Installation

```bash
go install ./...
```

## Quick Start

### Minimal (BM25 only, zero dependencies)

```bash
export KNOWLEDGE_MCP_DATA_DIR=./kb-data
knowledge-mcp
```

### Full stack (BM25 + embeddings + reranker)

Refer to [docs/deployment-models.md](docs/deployment-models.md) for detailed model deployment instructions.

```bash
# Embedding service (Ollama + BGE-M3)
ollama pull bge-m3

# Reranker service (Infinity + gte-multilingual-reranker-base)
pip install infinity-emb[all]
infinity_emb v2 --model-id Alibaba-NLP/gte-multilingual-reranker-base --port 7997

# knowledge-mcp
EMBED_API_BASE_URL=http://localhost:11434/v1 \
EMBED_MODEL=bge-m3 \
RERANK_API_BASE_URL=http://localhost:7997 \
RERANK_CANDIDATE_LIMIT=100 \
KNOWLEDGE_MCP_DATA_DIR=./kb-data \
  knowledge-mcp
```

## Web Management UI

A management web interface is **built in** — it starts automatically alongside the MCP server.
Open [http://localhost:8085](http://localhost:8085) (default port) in your browser to upload,
browse, search, and delete documents, and manage multiple knowledge bases.

Override the port with the `MANAGE_PORT` environment variable:

```bash
MANAGE_PORT=8080 knowledge-mcp
```

The UI shares the same data directory as the MCP server, so documents uploaded via the
web UI are immediately searchable through `knowledge_search`.

## Environment Variables

### Required

| Variable | Default | Description |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_DATA_DIR` | `~/knowledge_base/` | Knowledge base storage directory |
| `KNOWLEDGE_MCP_DEFAULT_KB` | — | Default KB name. When set, tools use this KB unless `kbName` is specified. When not set, tools search across all KBs. |

### Management

| Variable | Default | Description |
|----------|---------|-------------|
| `MANAGE_PORT` | `8085` | Web management UI port |

### Embedding (hybrid search)

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBED_API_BASE_URL` | — | OpenAI-compatible `/v1/embeddings` endpoint |
| `EMBED_MODEL` | `bge-m3` | Model name |
| `EMBED_API_KEY` | — | API key (not needed for Ollama) |
| `EMBED_DIM` | auto-detect | Vector dimension |

### Reranker (two-stage retrieval)

| Variable | Default | Description |
|----------|---------|-------------|
| `RERANK_API_BASE_URL` | `http://localhost:7997` | Infinity/Cohere-compatible `/rerank` endpoint |
| `RERANK_MODEL` | `gte-multilingual-reranker-base` | Cross-Encoder model name |
| `RERANK_API_KEY` | — | API key (not needed for self-hosted) |
| `RERANK_CANDIDATE_LIMIT` | `100` | How many BM25/RRF candidates to feed the reranker |
| `RERANK_TIMEOUT` | `30s` | Reranker HTTP request timeout |
| `RERANK_BATCH_SIZE` | `20` | Documents per reranker batch request |

### Logging

| Variable | Default | Description |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_LOG_FILE` | `<exe-dir>/knowledge-mcp.log` | Log file path |
| `KNOWLEDGE_MCP_LOG_LEVEL` | `info` | Log level: `debug` or `info` |

### Search behavior

| Variable | Default | Description |
|----------|---------|-------------|
| `QUERY_REWRITE_SYNONYMS` | — | Custom synonym pairs, format: `term:syn,term:syn` |

### GPU Scheduler

GPU scheduler coordinates sleep/wake of embedding and reranker models sharing a single GPU.
When enabled, it automatically switches models during upload (needs embedding) and
search (needs reranker), so both models can work even when neither fits in GPU memory alone.
Each model has its own sleep/wake API endpoints since they may use different protocols.

| Variable | Default | Description |
|----------|---------|-------------|
| `GPU_SCHEDULER_ENABLED` | `false` | Set to `true` or `1` to enable |
| `GPU_SCHEDULER_EMBEDDING_SLEEP_URL` | — | Embedding model sleep API URL |
| `GPU_SCHEDULER_EMBEDDING_WAKE_URL` | — | Embedding model wake API URL |
| `GPU_SCHEDULER_EMBEDDING_SLEEP_BODY` | — | Optional JSON body for embedding sleep request |
| `GPU_SCHEDULER_RERANKER_SLEEP_URL` | `http://localhost:11435/sleep` | Reranker model sleep API URL |
| `GPU_SCHEDULER_RERANKER_WAKE_URL` | `http://localhost:11435/wake_up` | Reranker model wake API URL |
| `GPU_SCHEDULER_RERANKER_SLEEP_BODY` | `{"level":2}` | JSON body for reranker sleep request |
| `GPU_SCHEDULER_TIMEOUT` | `30s` | HTTP timeout for sleep/wake requests |
| `GPU_SCHEDULER_WAKE_DELAY` | `3s` | Delay after wake to wait for model to load into GPU |

## MCP Tools

### `knowledge_search`

Search across all documents. Supports BM25 (default) and hybrid modes.
When a reranker is configured, results go through two-stage retrieval:
BM25/RRF recall → Cross-Encoder re-rank → final top-K.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `search_keywords` | **yes** | Rewritten keyword string (space-separated). Do NOT pass the user's raw question — fix typos, expand context, add synonyms first |
| `original_question` | no | User's original question verbatim (for logging) |
| `query` | no | **Deprecated** — use `search_keywords` |
| `kbName` | no | KB name. When set, search only that KB; when omitted, search all KBs |
| `limit` | no | Max results (default 8, max 20) |
| `mode` | no | `bm25` or `hybrid` (auto-picks hybrid if embedder available) |
| `sourceType` | no | Filter by file extension: `pdf`, `md`, `txt`, etc. |
| `section` | no | Filter chunks whose section heading contains this substring |
| `tags` | no | Comma-separated tags. Only documents matching at least one tag |
| `addedAfter` | no | ISO 8601 date. Only docs added at or after this time |
| `addedBefore` | no | ISO 8601 date. Only docs added at or before this time |
| `coarse` | no | Enable coarse-to-fine 2-phase search |

### `knowledge_read`

Read a specific chunk or its full parent section.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `docSlug` | **yes** | Document slug (from search/list results) |
| `chunkID` | **yes** | Chunk identifier, e.g. `005` |
| `kbName` | no | KB name. When omitted, the document is looked up across all KBs |
| `context` | no | Adjacent chunks to include before/after (default 0, max 5) |
| `level` | no | `chunk` (default) or `section` — reads the full parent section |

### `knowledge_list_kbs`

List all knowledge bases with their descriptions.

| Parameter | Required | Description |
|-----------|----------|-------------|
| _(none)_ | — | Returns count of KBs and each KB's name + description |

## Search Pipeline

```
query → query rewriting (synonyms) → tokenization
  → Phase 1: Fast Recall ─────────────────────
  │   BM25 keyword scoring
  │   + optional dense embedding cosine similarity
  │   → RRF fusion (adaptive query-type weights)
  │   → top-N candidates (default N=100)
  → Phase 2: Precision Re-rank ────────────────  [if reranker configured]
  │   Cross-Encoder scores each (query, chunk) pair
  │   → re-sort by relevance score
  → cap to limit → snippet generation → deduplicate → return
```

**Graceful degradation**: Without an embedder, hybrid falls back to pure BM25.
Without a reranker, the pipeline skips Phase 2 and returns RRF/BM25 results directly.
When the cross-encoder reranker is unavailable or fails, it falls back to vector
cosine similarity scores from Phase 1.

## Storage Layout

```
<data-dir>/
├── <kb-name>/
│   ├── INDEX.md
│   ├── kb.json            # KB description (set at creation time)
│   ├── LIST_SNAPSHOT.json
│   ├── .searchlog.jsonl
│   └── <document-slug>/
│       ├── meta.json          # OriginalName, SourceType, AddedAt, Title, Authors, Abstract
│       ├── CHUNKS.toml        # Per-chunk: terms, vector, section, offset, sectionRole
│       ├── source.<ext>       # Original file copy
│       └── chunks/
│           ├── 000.md         # Fine-grained chunks
│           ├── 001.md
│           └── sections/
│               ├── S00.md     # Coarse section-level chunks
│               └── S01.md
├── <another-kb>/
│   └── ...
└── (legacy flat documents live at the root level)
```

## Architecture

```
main.go                  — MCP server setup, tool registration, env parsing
internal/
  knowledge/
    store.go             — Store struct, data dir management
    search.go            — Search, HybridSearch, SearchDocuments, coarseToFine, rerankTop
    chunker.go           — ChunkText, ChunkTextHierarchical, semantic merge
    doc.go               — DocumentMeta, ChunkWithMeta, SearchFilter, SearchHit
    embed.go             — Embedder interface, OpenAIEmbedder, Reranker interface
    rerank.go            — InfinityReranker (Cohere/Infinity-compatible)
    gpu_scheduler.go     — GPU scheduler, coordinates embedding/reranker model sleep/wake
    rewrite.go           — QueryRewriter interface, SynonymRewriter
    rewrite_llm.go       — LLMQueryRewriter (optional LLM-based expansion)
    manage.go            — Web management UI server, KB CRUD, upload/delete handlers
    upload.go            — UploadDocument, UploadDirectory
    upload_doc.go        — Format-specific parsers (PDF, DOCX, etc.)
    parser.go            — Document parser dispatch
    inverted.go          — Inverted index for accelerated candidate lookup (G7)
    list.go              — ListPreview, ReadChunk, ReadChunkContext
    remove.go            — RemoveDocument
    searchlog.go         — FileSearchLogger
    meta_extract.go      — Paper metadata extraction
    store_index.go       — CHUNKS.toml read/write
  retrieval/
    bm25.go              — Tokenizer (CJK bigram-aware), BM25Score, MakeSnippet
docs/
  deployment-models.md   — Embedding & reranker model deployment guide
  roadmap.md             — RAG optimization roadmap
```

## License

MIT
