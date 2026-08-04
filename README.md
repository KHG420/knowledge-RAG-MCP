# knowledge-mcp

[中文](README_zh.md)

> ⚡ **No need to build a knowledge base from scratch — just connect MCP, and your agent gets an intelligent knowledge base instantly.**
>
> Drop in documents → auto chunk & index → BM25 + vector hybrid search + cross-encoder rerank → plug & play, zero ops.

MCP (Model Context Protocol) server that provides a local, file-based knowledge base with BM25 keyword search, hybrid (BM25 + vector) retrieval, and optional two-stage Cross-Encoder reranking.

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
- **KB descriptions** — assign a brief description when creating a KB; view all KBs and their descriptions via `knowledge_list_kbs` tool
- **MySQL/MariaDB backend** — optional database storage backend replacing the default filesystem, configurable via DSN, env vars, or TOML
- **Intelligent KB routing** — auto-route queries to the most relevant knowledge base(s) using four-dimension weighted scoring (keyword + embedding + description + domain constraints), with Top-K KB selection
- **Domain dictionary support** — load YAML-based domain synonym dictionaries for query expansion (e.g. ship motion terminology)
- **Redis cache** — optional exact-match query result cache with configurable TTL, keyed by normalized query hash + KB version; incremental chunk/meta/index caching for fast reads
- **Soft delete (tombstone)** — document removal uses a TTL tombstone pattern: documents are hidden from search immediately while physical cleanup follows on expiry
- **Incremental indexing & versioning** — re-uploading a document increments its version; only changed chunks are re-indexed, preserving search consistency
- **Evidence quality signals** — each result carries source_confidence, answer_relevance, and completeness metadata for the calling agent to assess reliability

## Installation

```bash
go build -o knowledge-mcp .
```

## Configuration

knowledge-mcp can be configured via three methods (in priority order):

1. **TOML config file** — `knowledge-mcp.toml` in the same directory as the executable, or `~/.knowledge-mcp/config.toml`
2. **Environment variables** — fallback when no TOML file exists
3. **Hard-coded defaults** — sensible defaults for all fields

### Web configuration

All configuration can be managed at runtime through the Web management UI at `/config`.
Open [http://localhost:8085/config](http://localhost:8085/config) in your browser to view and modify settings.
Most changes take effect immediately (hot-reload); a few (ports, data dir, MySQL) require a restart.

### Config file

| Key | Env var | Default | Description |
|-----|---------|---------|-------------|
| `data_dir` | `KNOWLEDGE_MCP_DATA_DIR` | `~/knowledge_base/` | Knowledge base storage directory |
| `default_kb` | `KNOWLEDGE_MCP_DEFAULT_KB` | — | Default KB name |
| `embed_endpoint` | `EMBED_API_ENDPOINT` | — | OpenAI-compatible embedding API endpoint |
| `embed_model` | `EMBED_MODEL` | `bge-m3` | Embedding model name |
| `embed_dim` | `EMBED_DIM` | auto-detect | Vector dimension |
| `embed_api_key` | `EMBED_API_KEY` | — | API key (not needed for Ollama) |
| `rerank_endpoint` | `RERANK_API_ENDPOINT` | — | Infinity/Cohere-compatible reranker API endpoint |
| `rerank_model` | `RERANK_MODEL` | `gte-multilingual-reranker-base` | Cross-Encoder model name |
| `rerank_api_key` | `RERANK_API_KEY` | — | API key (not needed for self-hosted) |
| `rerank_timeout` | `RERANK_TIMEOUT` | `30s` | Reranker HTTP request timeout |
| `rerank_candidate_limit` | `RERANK_CANDIDATE_LIMIT` | `100` | How many BM25/RRF candidates to feed the reranker |
| `gpu_scheduler_enabled` | `GPU_SCHEDULER_ENABLED` | `false` | Enable GPU scheduler for model sleep/wake |
| `gpu_scheduler_timeout` | `GPU_SCHEDULER_TIMEOUT` | `30s` | Sleep/wake HTTP request timeout |
| `gpu_scheduler_wake_delay` | `GPU_SCHEDULER_WAKE_DELAY` | `3s` | Delay after wake for model to load into GPU |
| `doc_parser_endpoint` | `DOC_PARSER_ENDPOINT` | — | External document parsing HTTP API URL. Leave empty to skip external parsing and use local tabula directly |
| `doc_parser_api_key` | `DOC_PARSER_API_KEY` | — | Bearer token for the document parsing API (optional) |
| `doc_parser_timeout` | `DOC_PARSER_TIMEOUT` | `600s` | HTTP request timeout for document parsing |
| `manage_port` | `MANAGE_PORT` | `8085` | Web management UI port |
| `serve_port` | `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | MCP HTTP server listen port (SSE + Streamable HTTP) |
| `serve_base_url` | `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | MCP server base URL (for reverse proxy) |
| `log_file` | `KNOWLEDGE_MCP_LOG_FILE` | `<exe-dir>/knowledge-mcp.log` | Log file path |
| `log_level` | `KNOWLEDGE_MCP_LOG_LEVEL` | `info` | Log level: `debug` or `info` |
| `mysql_dsn` | `MYSQL_DSN` | — | MySQL DSN, e.g. `user:pass@tcp(host:3306)/db?parseTime=true`. When set, enables MySQL backend |
| `mysql_user` | `MYSQL_USER` | `root` | MySQL user (used when DSN not set) |
| `mysql_password` | `MYSQL_PASSWORD` | — | MySQL password |
| `mysql_host` | `MYSQL_HOST` | `127.0.0.1` | MySQL host |
| `mysql_port` | `MYSQL_PORT` | `3306` | MySQL port |
| `mysql_database` | `MYSQL_DATABASE` | `knowledge_rag` | MySQL database name |
| `mysql_socket_path` | `MYSQL_SOCKET_PATH` | — | MySQL Unix socket path (takes precedence over host:port) |
| `redis_enabled` | `REDIS_ENABLED` | `false` | Enable Redis query result cache |
| `redis_addr` | `REDIS_ADDR` | `127.0.0.1:6379` | Redis server address |
| `redis_password` | `REDIS_PASSWORD` | — | Redis password (optional) |
| `redis_db` | `REDIS_DB` | `0` | Redis database number |
| `mineru_enabled` | `MINERU_ENABLED` | `true` | Enable external document parser (MinerU) |
| `deepseek_api_key` | `DEEPSEEK_API_KEY` | — | DeepSeek API key. LLM rewriting is disabled when empty |
| `deepseek_endpoint` | `DEEPSEEK_ENDPOINT` | `https://api.deepseek.com/chat/completions` | DeepSeek API endpoint |
| `deepseek_model` | `DEEPSEEK_MODEL` | `deepseek-v4-flash` | DeepSeek model name |
| `redis_pool_size` | `REDIS_POOL_SIZE` | `10` | Redis connection pool size |
| `cache_query_ttl` | `CACHE_QUERY_TTL` | `300` | Query result cache TTL in seconds (Redis) |
| `cache_chunk_ttl` | `CACHE_CHUNK_TTL` | `0` | Chunk text cache TTL in seconds (built-in) |
| `cache_meta_ttl` | `CACHE_META_TTL` | `0` | Document metadata cache TTL in seconds (built-in) |
| `cache_index_ttl` | `CACHE_INDEX_TTL` | `0` | Chunk index cache TTL in seconds (built-in) |
| `cache_kblist_ttl` | `CACHE_KBLIST_TTL` | `60` | KB list cache TTL in seconds (built-in) |
| `upload_max_size_mb` | `UPLOAD_MAX_SIZE_MB` | `100` | Maximum upload file size in MB |

## Quick Start

### Running modes

knowledge-mcp supports three running modes:

- **stdio mode (recommended for MCP clients)** — communicate via stdin/stdout using the
  MCP protocol. No HTTP server, no web UI. Ideal for Reasonix, Claude Desktop, and
  other stdio-based MCP hosts:
  ```bash
  knowledge-mcp stdio
  ```
- **HTTP mode (default)** — MCP server with web management UI. Supports both
  **Streamable HTTP** (`/mcp`, modern) and **SSE** (`/sse`, legacy) transports on the same port:
  ```bash
  knowledge-mcp serve
  ```
- **HTTP MCP-only** — MCP server without management UI:
  ```bash
  knowledge-mcp serve --mcp
  ```

### Minimal (BM25 only, zero dependencies)

```bash
export KNOWLEDGE_MCP_DATA_DIR=./kb-data
knowledge-mcp serve
```

### Full stack (BM25 + embeddings + reranker)

Refer to [docs/deployment-models.md](docs/deployment-models.md) / [中文版](docs/deployment-models_zh.md) for detailed model deployment instructions.

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

### MySQL/MariaDB backend (alternative to file storage)

knowledge-mcp supports MySQL or MariaDB as an alternative storage backend.
All document data (metadata, chunks, search indices) is stored in database tables,
making it easier to integrate with existing infrastructure.

```bash
# Connect via DSN
MYSQL_DSN="user:password@tcp(127.0.0.1:3306)/knowledge_rag?parseTime=true" \
  knowledge-mcp serve

# Connect via Unix socket
MYSQL_SOCKET_PATH=/var/run/mysqld/mysqld.sock \
  MYSQL_DATABASE=knowledge_rag \
  knowledge-mcp serve

# Via TOML config:
#   mysql_host = "127.0.0.1"
#   mysql_port = "3306"
#   mysql_user = "root"
#   mysql_database = "knowledge_rag"
```

On first startup, the required tables are created automatically. Switching backend types requires re-importing existing data.

## Web Management UI

A management web interface is **built in** — it starts automatically alongside the MCP server in `serve` mode (not in `serve --mcp` mode).
Open [http://localhost:8085](http://localhost:8085) (default port) in your browser to upload,
browse, search, and delete documents, and manage multiple knowledge bases.

Override the port with the `MANAGE_PORT` environment variable:

```bash
MANAGE_PORT=8080 knowledge-mcp serve
```

The UI shares the same data directory as the MCP server, so documents uploaded via the
web UI are immediately searchable through `knowledge_research`.

### UI Features

| Feature | Path | Description |
|---------|------|-------------|
| KB management | `/` | Create/delete KBs, view KB list and descriptions |
| Document upload | `/kb/{name}` | Drag-and-drop or file selection, batch upload with optional tags |
| Document list | `/kb/{name}` | Browse, search, filter by tags/type/time |
| Document detail | `/kb/{name}/{slug}` | View metadata, chunk list, raw text |
| Search console | `/kb/{name}/search` | Test retrieval, switch BM25/hybrid modes, view scores |
| System config | `/config` | View and modify chunking params, search params, tool descriptions at runtime |
| Batch delete | `/kb/{name}` | Select multiple documents for batch deletion (supports tombstone soft-delete)

## Running as a Daemon / Service

The `serve` command runs as a foreground process. For production use, run it as a
system service to survive reboots and crashes.

### Linux (systemd)

Copy the service template and reload systemd:

```bash
sudo cp scripts/knowledge-mcp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now knowledge-mcp
```

Configure environment variables (embeddings, reranker, etc.) in `/etc/knowledge-mcp/env`:

```bash
sudo mkdir -p /etc/knowledge-mcp
cat <<EOF | sudo tee /etc/knowledge-mcp/env
KNOWLEDGE_MCP_DATA_DIR=/var/lib/knowledge-mcp
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings
EOF
```

### macOS (launchd)

Copy the plist to your LaunchAgents directory and load it:

```bash
cp scripts/com.knowledge-mcp.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.knowledge-mcp.plist
```

Edit `~/Library/LaunchAgents/com.knowledge-mcp.plist` to set the correct binary path
and environment variables before loading.

### MCP client integration (stdio)

For MCP clients such as **Reasonix**, **Claude Desktop**, and **Cline**, the
recommended approach is to use the **stdio** mode via a `.mcp.json` file in your
project root. The client automatically starts and manages the process lifecycle:

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

No launchd setup is needed — the MCP client handles everything.

### MCP client integration (HTTP)

For clients that connect over HTTP (or when you need a shared, long-running server),
use `serve` mode and configure the client to connect to the **Streamable HTTP** endpoint:

```json
{
  "mcpServers": {
    "knowledge-mcp": {
      "type": "http",
      "url": "http://localhost:8086/mcp"
    }
  }
}
```

Legacy SSE clients can use `http://localhost:8086/sse` instead.

### Other options

- **tmux / screen**: run `knowledge-mcp serve --mcp` inside a persistent session.
- **nohup**: `nohup knowledge-mcp serve --mcp > /tmp/kmcp.log 2>&1 &`

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

### MCP HTTP Server (SSE + Streamable HTTP)

| Variable | Default | Description |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | MCP HTTP server listen port |
| `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | MCP server base URL (for reverse proxy scenarios) |

### Embedding (hybrid search)

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBED_API_ENDPOINT` | — | Full OpenAI-compatible embedding API endpoint |
| `EMBED_MODEL` | `bge-m3` | Model name |
| `EMBED_API_KEY` | — | API key (not needed for Ollama) |
| `EMBED_DIM` | auto-detect | Vector dimension |

### Reranker (two-stage retrieval)

| Variable | Default | Description |
|----------|---------|-------------|
| `RERANK_API_ENDPOINT` | `http://localhost:7997/rerank` | Full Infinity/Cohere-compatible reranker API endpoint |
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

### Document Parser

When configured, all non-plain-text formats (PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX) are sent to the external HTTP API first for parsing. If the API is unavailable, the system automatically falls back to the local tabula library without interrupting the upload flow.

| Variable | Default | Description |
|----------|---------|-------------|
| `DOC_PARSER_ENDPOINT` | — | External document parsing API URL |
| `DOC_PARSER_API_KEY` | — | Bearer token (optional) |
| `DOC_PARSER_TIMEOUT` | `600s` | HTTP request timeout |

### MySQL Backend

When the MySQL backend is enabled, all knowledge base data is stored in database tables instead of the filesystem. Set `MYSQL_DSN` or any `MYSQL_*` variable to enable.

| Variable | Default | Description |
|----------|---------|-------------|
| `MYSQL_DSN` | — | MySQL DSN, e.g. `user:pass@tcp(host:3306)/db?parseTime=true`. When set, enables the backend |
| `MYSQL_USER` | `root` | Username (used when DSN not set) |
| `MYSQL_PASSWORD` | — | Password |
| `MYSQL_HOST` | `127.0.0.1` | Host address |
| `MYSQL_PORT` | `3306` | Port |
| `MYSQL_DATABASE` | `knowledge_rag` | Database name |
| `MYSQL_SOCKET_PATH` | — | Unix socket path (takes precedence over host:port) |

### Redis Cache

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ENABLED` | `false` | Set to `true` or `1` to enable |
| `REDIS_ADDR` | `127.0.0.1:6379` | Redis server address |
| `REDIS_PASSWORD` | — | Redis password (optional) |
| `REDIS_DB` | `0` | Redis database number |
| `REDIS_PREFIX` | `kmcp:` | Key namespace prefix |

## MCP Tools

### `knowledge_research`

Semantic research search across all documents. The system auto-handles:
KB routing, query analysis, keyword/semantic expansion, retrieval strategy
selection (BM25 / vector / hybrid), reranking, and evidence confidence scoring.

**For simple factual questions, pass the user's original question directly.**
**For complex research tasks, you may decompose into multiple focused queries.**
The agent focuses on research planning and answer synthesis; the retrieval
system handles finding reliable evidence.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `question` | **yes** | User's original natural language question. Pass it verbatim — the internal query analyzer handles Chinese/English expansion and domain terminology automatically |
| `search_keywords` | no | **Deprecated** — use `question` instead. Accepted for backward compatibility only |
| `kbName` | no | Optional. Leave empty — the system auto-routes to the correct 1–3 KBs. Only pass a kbName if you have a specific reason to force a particular KB |
| `limit` | no | Max results (default 8, max 20) |
| `mode` | no | **Deprecated** — the system auto-selects the best strategy. `bm25` or `hybrid`, accepted for backward compatibility only |
| `sourceType` | no | Filter by file extension: `pdf`, `md`, `txt`, etc. |
| `section` | no | Filter chunks whose section heading contains this substring |
| `tags` | no | Comma-separated tags. Only documents matching at least one tag |
| `addedAfter` | no | ISO 8601 date. Only docs added at or after this time |
| `addedBefore` | no | ISO 8601 date. Only docs added at or before this time |
| `coarse` | no | Enable coarse-to-fine 2-phase search: first score sections, then only search within top-3 sections |

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

### `knowledge_list`

List documents in a knowledge base, with optional KB scope filtering.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `kbName` | no | Knowledge base name. When set, list only documents in that KB. When omitted, list all KBs |

### `knowledge_upload`

Upload single documents or batch-import entire directories into a knowledge base.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `filePath` | conditional | Path to a single document file. Mutually exclusive with `directory` |
| `directory` | conditional | Directory path for batch upload. Mutually exclusive with `filePath` |
| `recursive` | no | When `true`, recursively walk subdirectories (for batch upload) |
| `tags` | no | Comma-separated tags assigned to the uploaded document(s) |
| `kbName` | conditional | KB name. Required when no default KB is configured |

### `knowledge_remove`

Remove a document from a knowledge base by its slug.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `docSlug` | **yes** | Document slug (from list/search results) |
| `kbName` | no | KB name. When omitted, the document is removed from all KBs |

## KB Routing

When `knowledge_research` is called without a specific `kbName`, the KB Router scores every knowledge base against the query using four weighted dimensions:

| Dimension | Weight | Description |
|-----------|--------|-------------|
| keyword | 0.35 | Term overlap between query and KB name/description |
| embedding | 0.35 | Cosine similarity of query vector vs KB description vector |
| desc | 0.15 | Description substring match bonus |
| constraint | 0.15 | Domain-specific routing constraints |

Top-K selection: if the score gap between #1 and #2 is > 0.25, only the top KB is used. Otherwise, up to 3 KBs are selected for joint retrieval.

## Search Pipeline

```
user query (question)
  │
  ├─ Query triage (rule-based, zero-cost)
  │   ├─ 70% Simple  → domain dictionary exact_synonyms → BM25 weight boost
  │   ├─ 25% Medium  → SynonymRewriter multi-variant expansion
  │   └─  5% Complex → LLMQueryRewriter semantic rewrite (DeepSeek, optional)
  │
  ├─ BM25 / vector decoupled recall
  │   ├─ BM25 path: exact_synonyms expansion (no related_terms)
  │   └─ Vector path: original query + related_terms (semantic enrichment)
  │
  ├─ KB routing: 4-dimension weighted scoring → Top-1~3 KBs
  │
  ├─ Tokenization: CJK bigram-aware tokenizer
  │
  ├─ Phase 1: Fast Recall ──────────────────────────
  │   ├─ Inverted index fast path
  │   │     candidate collection → BM25 scoring
  │   │
  │   ├─ Vector ANN recall (HNSW, independent parallel)
  │   │     query vectorisation → HNSW search → merge with BM25 candidates
  │   │
  │   └─ RRF fusion (k=60, adaptive query-type weights)
  │         paper-type queries: BM25 weight ↑ / conceptual queries: vector weight ↑
  │        → top-N candidates (default N=100)
  │
  ├─ Phase 2: Precision Re-rank ────────────────  [if reranker configured]
  │     Cross-Encoder scores each (query, chunk) pair
  │    → re-sort by relevance score
  │
  └─ Post-processing
       → cap to limit → snippet generation → deduplicate
       → evidence quality scoring → return
```

**Graceful degradation**:

| Scenario | Behaviour |
|----------|-----------|
| No embedding endpoint configured | Falls back to pure BM25 keyword search |
| No reranker configured | Skips Phase 2, returns RRF/BM25 scores directly |
| Reranker timeout/failure | Falls back to vector cosine similarity scores from Phase 1 |
| Neither configured | Pure BM25, zero external dependencies |

## Storage Layout

```
<data-dir>/
├── <kb-name>/
│   ├── INDEX.md
│   ├── INVERTED.gob        # Global inverted index for accelerated candidate lookup
│   ├── kb.json             # KB description (set at creation time)
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
    setup.go                — Interactive configuration wizard ("knowledge-mcp setup")
    i18n.go                 — Internationalization strings for the setup wizard
  logging/
    logger.go               — Structured file logger (DEBUG/INFO/WARN/ERROR, module-scoped)
  cache/
    cache.go                — Cache interface + NoopCache fallback
    redis.go                — Redis-backed cache implementation
    keys.go                 — Cache key naming conventions (query, chunk, meta, index, KB list)
  knowledge/
    interfaces.go           — Core interfaces: Searcher, ChunkStore, Ingester, KBAdmin, DictService, ManageService, CacheClient
    store.go                — Store Facade (~160 methods), delegates to sub-package engines
    storage.go              — StorageBackend interface (storage backend abstraction)
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
      engine.go             —   UploadDocument, UploadDirectory, CopySource + full parse→chunk→embed→persist pipeline
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

## Caching

knowledge-mcp has two caching layers:

### Built-in cache (always on, no Redis needed)

In-memory map-based cache at the Store layer, with per-type TTL:

| Type | Content | Default TTL | Key format |
|------|---------|-------------|------------|
| chunk | Chunk text | 0 (no expiry) | `kmcp:kb:{kb}:chunk:{slug}:{id}` |
| meta | Document metadata | 0 (no expiry) | `kmcp:kb:{kb}:meta:{slug}` |
| index | CHUNKS.toml index | 0 (no expiry) | `kmcp:kb:{kb}:idx:{slug}` |
| kblist | KB list | 60s | `kmcp:kblist` |

At `info` log level you can see HIT/MISS/SET for each cache type:
```
[INFO] [cache] chunk HIT  key=kmcp:kb:ship:chunk:doc:033 slug="doc" chunk=033 size=1847
[INFO] [cache] chunk MISS key=kmcp:kb:ship:chunk:doc:081 slug="doc" chunk=081
[INFO] [cache] chunk SET  key=kmcp:kb:ship:chunk:doc:081 slug="doc" chunk=081 ttl=0s size=2103
```

Cache entries are automatically invalidated on document re-upload or deletion.

### Redis cache (optional)

Install Redis and set `redis_enabled = true`. Redis only caches **query-level search results** (HybridSearch output), keyed by normalized query hash + KB + filter hash.

## Logging & Debugging

Set `log_level = "debug"` for detailed output including tokenization, per-term IDF, per-candidate BM25 scores, RRF fusion details, and HNSW search steps. At `info` level you still get: search request summaries, cache HIT/MISS, KB routing decisions, upload progress, and all warnings/errors.

```bash
# Follow logs
tail -f ~/.knowledge-mcp/knowledge-mcp.log

# Filter by module
tail -f ~/.knowledge-mcp/knowledge-mcp.log | grep "\[search\]"
tail -f ~/.knowledge-mcp/knowledge-mcp.log | grep "HIT\|MISS"
```

## Domain Dictionaries

Place YAML files in `dictionaries/` for query expansion with domain-specific synonyms. Example `dictionaries/ship_motion.yaml`:

```yaml
resistance:
  - drag
  - 阻力
  - friction resistance

CFD:
  - computational fluid dynamics
  - 计算流体力学
  - numerical simulation
```

Dictionaries are loaded at startup. A query like "ship resistance CFD" automatically expands to include "drag", "computational fluid dynamics", etc. in both BM25 and vector recall.

### Dictionary management tools

Two CLI subcommands help manage domain dictionaries:

```bash
# Mine synonym candidates from accumulated search logs
knowledge-mcp dict mine

# Generate domain dictionary from KB chunks via LLM (requires DEEPSEEK_API_KEY)
knowledge-mcp dict gen
```

The `dict mine` tool reads `.searchlog.jsonl`, discovers candidate synonym pairs via query-document co-occurrence analysis, and outputs a table for manual review. The `dict gen` tool scans the current knowledge base and calls LLM to batch-extract domain terms, synonyms and related terms into `dictionaries/<kb>_generated.yaml`.

## LLM Query Rewriting

When `DEEPSEEK_API_KEY` is configured, complex queries (about 5% of all queries) are automatically rewritten by DeepSeek LLM to generate 2-4 expanded variants for improved recall. Simple and medium queries use the domain dictionary and synonym rewriter instead — avoiding unnecessary LLM costs.

**Three-layer fallback for resilience:**

1. **LLM layer** — DeepSeek API returns rewritten variants on success
2. **Fallback layer** — LLM failure (network error/timeout/empty response) falls back to `SynonymRewriter` with built-in synonym table + dictionary files
3. **Bottom layer** — even with an empty synonym table, the original query is returned unchanged; search never breaks

**Security:**

- API key injected **only via TOML config file or environment variable**; never hard-coded
- API responses in error logs are sanitised via `sanitiseForLog()` — any `sk-*` format key is replaced with `sk-***`
- API key never appears in logs, error messages, or the management UI

**Log example:**
```
[INFO] [startup] query rewriter: LLM (deepseek model=deepseek-v4-flash) + synonym fallback (17 terms)
[DEBUG] [deepseek] deepseek: request model=deepseek-v4-flash promptLen=42 bodyLen=237
[DEBUG] [deepseek] deepseek: OK model=deepseek-v4-flash elapsed=856ms promptLen=42 responseLen=128
[WARN] [deepseek] deepseek: non-200 model=deepseek-v4-flash status=401 elapsed=123ms body={"error":"Invalid API key: sk-***"}
```

## Database Schema (MySQL backend)

The MySQL backend auto-creates the following tables in the configured database:

| Table | Primary Key | Purpose |
|-------|-------------|---------|
| `knowledge_bases` | `name` | KB metadata (name, description) |
| `documents` | `(kb_name, slug)` | Document metadata (filename, type, tags, title, authors, abstract, raw text) |
| `chunks` | `(kb_name, doc_slug, chunk_id)` | Fine-grained chunk text |
| `section_chunks` | `(kb_name, doc_slug, section_id)` | Coarse section-level chunk text |
| `chunks_index` | `(kb_name, doc_slug)` | Per-document search index (JSON) |
| `inverted_index` | `(kb_name, term, doc_slug, chunk_id)` | Global inverted index (term → doc+chunk → TF) |
| `manifests` | `(kb_name, slug)` | Versioned chunk manifests |
| `task_records` | `(kb_name, slug)` | Upload task state |
| `list_snapshots` | `kb_name` | Document list snapshot cache |

All tables use InnoDB + utf8mb4. No foreign keys — deletion is handled at the application layer for compatibility.

## Maintenance

### Cleaning orphaned vector index entries

When chunks are re-uploaded or deleted, stale entries may remain in the HNSW vector index. Use the cleanup tool:

```bash
# Dry-run first
go run ./cmd/cleanup-vector/ --dry-run

# Clean a specific KB
go run ./cmd/cleanup-vector/ --kb ship-hydrodynamics --dry-run

# Apply
go run ./cmd/cleanup-vector/
```

### Rebuilding the vector index

Delete `VECTOR.gob` under the KB directory. The index will be rebuilt from `chunks_index` data on the next hybrid search request.

```bash
rm ~/knowledge_base/<kb-name>/VECTOR.gob
# Restart — index rebuilds on next hybrid query
```

## FAQ

**Q: "chunk xxx not found in document xxx" error?**

The search index references chunks that don't exist in the `chunks` table. This happens when the vector index retains old sequential-format IDs after a re-upload. Fix: run `go run ./cmd/cleanup-vector/` or delete `VECTOR.gob`.

**Q: Uploaded a PDF but search returns nothing?**

1. Check the upload succeeded in logs: `[upload]`
2. Verify `total_chars > 0` in the document's `meta.json` or `documents` table
3. Make sure `kbName` matches in your search call
4. Check search logs: `[search] hybrid: vector recall returned X hits`

**Q: Vector search returns zero results?**

1. Verify the embedding endpoint is reachable: `curl http://localhost:11434/v1/embeddings -d '{"model":"bge-m3","input":"test"}'`
2. Check `embed_dim` matches your model
3. Check if the vector index is empty in search logs

**Q: How to switch from file backend to MySQL?**

The backends use different storage formats. You need to re-import all documents:
1. Back up source files from `data_dir`
2. Configure MySQL connection and restart
3. Re-import documents via the web UI or `knowledge_upload`

**Q: Can I restore a soft-deleted document?**

Tombstoned documents can be restored before their TTL expires by removing the entry from `.tombstones.gob`. After TTL expiry the physical data is permanently deleted.

**Q: How do I back up MySQL storage?**

```bash
mysqldump -u knowledge -p knowledge_rag > backup.sql
```

Also back up `data_dir/<kb-name>/VECTOR.gob` files. Restore both the SQL dump and the gob files.

## License

MIT
