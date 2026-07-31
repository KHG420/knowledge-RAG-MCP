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

### Setup wizard

Run the interactive configuration wizard to generate a `knowledge-mcp.toml` file:

```bash
knowledge-mcp setup
```

The wizard probes endpoint connectivity and writes a valid config file.

### Config keys

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
| `serve_port` | `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | SSE server listen port |
| `serve_base_url` | `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | SSE server base URL (for reverse proxy) |
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
| `redis_prefix` | `REDIS_PREFIX` | `kmcp:` | Redis key namespace prefix |

## Quick Start

### Running modes

knowledge-mcp supports four running modes:

- **stdio mode (recommended for MCP clients)** — communicate via stdin/stdout using the
  MCP protocol. No HTTP server, no web UI. Ideal for Reasonix, Claude Desktop, and
  other stdio-based MCP hosts:
  ```bash
  knowledge-mcp stdio
  ```
- **HTTP SSE mode (default)** — includes web management UI:
  ```bash
  knowledge-mcp serve
  ```
- **SSE MCP-only** — HTTP SSE without management UI:
  ```bash
  knowledge-mcp serve --mcp
  ```
- **Setup wizard** — interactive configuration:
  ```bash
  knowledge-mcp setup
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
web UI are immediately searchable through `knowledge_search`.

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

### SSE Server

| Variable | Default | Description |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | SSE server listen port |
| `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | SSE server base URL (for reverse proxy scenarios) |

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

When `knowledge_search` is called without a specific `kbName`, the KB Router scores every knowledge base against the query using four weighted dimensions:

| Dimension | Weight | Description |
|-----------|--------|-------------|
| keyword | 0.35 | Term overlap between query and KB name/description |
| embedding | 0.35 | Cosine similarity of query vector vs KB description vector |
| desc | 0.15 | Description substring match bonus |
| constraint | 0.15 | Domain-specific routing constraints |

Top-K selection: if the score gap between #1 and #2 is > 0.25, only the top KB is used. Otherwise, up to 3 KBs are selected for joint retrieval.

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

```
main.go                  — CLI entry point, subcommands (stdio / serve / setup), tool registration
internal/
  config/
    config.go            — TOML config loading, env-var fallback, defaults
  setup/
    setup.go             — Interactive configuration wizard ("knowledge-mcp setup")
    i18n.go              — Internationalization strings for the setup wizard
  logging/
    logger.go            — Structured file logger (DEBUG/INFO/WARN/ERROR, module-scoped)
  cache/
    cache.go             — Cache interface + NoopCache fallback
    redis.go             — Redis-backed cache implementation
    keys.go              — Cache key naming conventions (query, chunk, meta, index, KB list)
  knowledge/
    store.go             — Store struct, data dir management, CHUNKS.toml I/O, KB CRUD
    storage.go           — StorageBackend interface (storage backend abstraction)
    mysql_backend.go     — MySQL/MariaDB storage backend
    search.go            — Search, HybridSearch, SearchDocuments, SearchAll, coarseToFine, rerankTop
    chunker.go           — ChunkText, ChunkTextHierarchical, semantic merge, page-aware chunking
    doc.go               — DocumentMeta, ChunkWithMeta, SearchFilter, SearchHit, EvidenceMeta, ChunksIndex
    embed.go             — Embedder interface, OpenAIEmbedder (OpenAI & Ollama native format)
    rerank.go            — InfinityReranker (Cohere/Infinity-compatible), Reranker interface
    vector_index.go      — HNSW vector index (M=48, efConstruction=400) for ANN search
    gpu_scheduler.go     — GPU scheduler, coordinates embedding/reranker model sleep/wake
    kb_router.go         — Multi-KB intelligent router (keyword + embedding + desc + constraint scoring)
    rewrite.go           — QueryRewriter interface, SynonymRewriter (built-in + YAML dictionaries)
    rewrite_llm.go       — LLMQueryRewriter (optional LLM-based query expansion)
    dict_loader.go       — Domain dictionary loader from YAML files
    manage.go            — Web management UI server, KB CRUD, upload/delete/search handlers
    manage_enhanced.go   — Extended management features (search console, config, tool descriptions)
    upload.go            — UploadDocument, UploadDirectory
    upload_task.go       — Persistent upload task tracking
    parser.go            — Document parser dispatch — external HTTP API + tabula fallback (PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX, MD, TXT)
    inverted.go          — Global inverted index (INVERTED.gob) for accelerated candidate lookup
    list.go              — ListPreview, ReadChunk, ReadChunkContext
    remove.go            — RemoveDocument
    tomstone.go          — Soft-delete tombstone manager with TTL-based cleanup
    version.go           — Document/index version tracking for incremental updates
    store_incremental.go — Incremental re-indexing on document re-upload
    manifest.go          — Chunk manifest tracking for index integrity
    reconcile.go         — Index consistency verification and repair
    state_machine.go     — Document lifecycle state machine
    chunk_id.go          — Chunk ID generation and management
    config_api.go        — Runtime configuration read/write API
    store_settings.go    — Store runtime settings management
    middleware.go         — HTTP middleware (CORS, logging, recovery)
    tool_descs.go         — Default MCP tool descriptions (customisable via Web UI)
    searchlog.go          — FileSearchLogger (.searchlog.jsonl)
    meta_extract.go       — Paper metadata extraction (title, authors, abstract, section roles)
  retrieval/
    bm25.go              — Tokenizer (CJK bigram-aware), BM25Score, MakeSnippet
scripts/
  eval.go                — Retrieval evaluation script (NDCG@5, MRR, Recall@10)
service-manager.sh       — Service management script for Ollama + Infinity dependencies
docs/
  deployment-models.md   — Embedding & reranker model deployment guide
  deployment-models_zh.md
  roadmap.md             — RAG optimization roadmap
  roadmap_zh.md
```

## License

MIT
