# knowledge-mcp — User Guide

[中文](GUIDE_zh.md) | [← Back to README](README.md)

> Detailed configuration, operations, and troubleshooting for knowledge-mcp.

---

## Table of Contents

- [Configuration](#configuration)
- [Running Modes](#running-modes)
- [Web Management UI](#web-management-ui)
- [Running as a Daemon / Service](#running-as-a-daemon--service)
- [MCP Client Integration](#mcp-client-integration)
- [Environment Variables](#environment-variables)
- [MCP Tools](#mcp-tools)
- [KB Routing](#kb-routing)
- [Search Pipeline](#search-pipeline)
- [Storage Layout](#storage-layout)
- [Caching](#caching)
- [Logging & Debugging](#logging--debugging)
- [Domain Dictionaries](#domain-dictionaries)
- [LLM Query Rewriting](#llm-query-rewriting)
- [Database Schema (MySQL backend)](#database-schema-mysql-backend)
- [Model Deployment](#model-deployment)
- [Maintenance](#maintenance)
- [FAQ](#faq)

---

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
| `doc_parser_endpoint` | `DOC_PARSER_ENDPOINT` | — | External document parsing HTTP API URL. Leave empty to use local tabula |
| `doc_parser_api_key` | `DOC_PARSER_API_KEY` | — | Bearer token for the document parsing API (optional) |
| `doc_parser_timeout` | `DOC_PARSER_TIMEOUT` | `600s` | HTTP request timeout for document parsing |
| `manage_port` | `MANAGE_PORT` | `8085` | Web management UI port |
| `serve_port` | `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | MCP HTTP server listen port (SSE + Streamable HTTP) |
| `serve_base_url` | `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | MCP server base URL (for reverse proxy) |
| `log_file` | `KNOWLEDGE_MCP_LOG_FILE` | `<exe-dir>/knowledge-mcp.log` | Log file path |
| `log_level` | `KNOWLEDGE_MCP_LOG_LEVEL` | `info` | Log level: `debug` or `info` |
| `mysql_dsn` | `MYSQL_DSN` | — | MySQL DSN. When set, enables MySQL backend |
| `mysql_user` | `MYSQL_USER` | `root` | MySQL user |
| `mysql_password` | `MYSQL_PASSWORD` | — | MySQL password |
| `mysql_host` | `MYSQL_HOST` | `127.0.0.1` | MySQL host |
| `mysql_port` | `MYSQL_PORT` | `3306` | MySQL port |
| `mysql_database` | `MYSQL_DATABASE` | `knowledge_rag` | MySQL database name |
| `mysql_socket_path` | `MYSQL_SOCKET_PATH` | — | MySQL Unix socket path |
| `redis_enabled` | `REDIS_ENABLED` | `false` | Enable Redis query result cache |
| `redis_addr` | `REDIS_ADDR` | `127.0.0.1:6379` | Redis server address |
| `redis_password` | `REDIS_PASSWORD` | — | Redis password (optional) |
| `redis_db` | `REDIS_DB` | `0` | Redis database number |
| `mineru_enabled` | `MINERU_ENABLED` | `true` | Enable external document parser (MinerU) |
| `deepseek_api_key` | `DEEPSEEK_API_KEY` | — | DeepSeek API key. LLM rewriting disabled when empty |
| `deepseek_endpoint` | `DEEPSEEK_ENDPOINT` | `https://api.deepseek.com/chat/completions` | DeepSeek API endpoint |
| `deepseek_model` | `DEEPSEEK_MODEL` | `deepseek-v4-flash` | DeepSeek model name |
| `redis_pool_size` | `REDIS_POOL_SIZE` | `10` | Redis connection pool size |
| `cache_query_ttl` | `CACHE_QUERY_TTL` | `300` | Query result cache TTL in seconds (Redis) |
| `cache_chunk_ttl` | `CACHE_CHUNK_TTL` | `0` | Chunk text cache TTL in seconds (built-in) |
| `cache_meta_ttl` | `CACHE_META_TTL` | `0` | Document metadata cache TTL in seconds (built-in) |
| `cache_index_ttl` | `CACHE_INDEX_TTL` | `0` | Chunk index cache TTL in seconds (built-in) |
| `cache_kblist_ttl` | `CACHE_KBLIST_TTL` | `60` | KB list cache TTL in seconds (built-in) |
| `upload_max_size_mb` | `UPLOAD_MAX_SIZE_MB` | `100` | Maximum upload file size in MB |

---

## Running Modes

knowledge-mcp supports three running modes:

### stdio mode (recommended for MCP clients)

Communicate via stdin/stdout using the MCP protocol. No HTTP server, no web UI.
Ideal for Reasonix, Claude Desktop, and other stdio-based MCP hosts:

```bash
knowledge-mcp stdio
```

> **Note**: In stdio mode all configuration must come from environment variables or a TOML file.

### HTTP mode (default)

MCP server with web management UI. Supports both **Streamable HTTP** (`/mcp`, modern)
and **SSE** (`/sse`, legacy) transports on the same port:

```bash
knowledge-mcp serve
```

- MCP Streamable HTTP endpoint: `http://localhost:8086/mcp`
- MCP SSE endpoint (legacy): `http://localhost:8086/sse`
- Web management UI: `http://localhost:8085`

### HTTP MCP-only

MCP server without management UI:

```bash
knowledge-mcp serve --mcp
```

### Management commands

```bash
knowledge-mcp manage    # Web management UI only (no MCP server), port 8085
knowledge-mcp version   # Print version info
knowledge-mcp dict      # Domain dictionary management (mine / gen)
```

---

## Web Management UI

A management web interface is **built in** — it starts automatically alongside the MCP server
in `serve` mode (not in `serve --mcp` mode). Open [http://localhost:8085](http://localhost:8085)
(default port) to upload, browse, search, delete documents, and manage multiple knowledge bases.

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
| System config | `/config` | View and modify chunking/search params, tool descriptions at runtime |
| Batch delete | `/kb/{name}` | Select multiple documents for batch deletion (tombstone soft-delete)

---

## Running as a Daemon / Service

### Linux (systemd)

```bash
sudo cp scripts/knowledge-mcp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now knowledge-mcp
```

Configure environment variables in `/etc/knowledge-mcp/env`:

```bash
sudo mkdir -p /etc/knowledge-mcp
cat <<EOF | sudo tee /etc/knowledge-mcp/env
KNOWLEDGE_MCP_DATA_DIR=/var/lib/knowledge-mcp
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings
EOF
```

### macOS (launchd)

```bash
cp scripts/com.knowledge-mcp.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.knowledge-mcp.plist
```

### tmux / screen / nohup

```bash
tmux new-session -d -s kmcp './knowledge-mcp serve'
# or
nohup ./knowledge-mcp serve > /tmp/kmcp.log 2>&1 &
```

---

## MCP Client Integration

### stdio mode (recommended)

For clients like **Reasonix**, **Claude Desktop**, and **Cline**, use `.mcp.json`:

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

### HTTP mode

For clients that connect over HTTP, use **Streamable HTTP**:

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

---

## Environment Variables

### Required

| Variable | Default | Description |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_DATA_DIR` | `~/knowledge_base/` | Knowledge base storage directory |
| `KNOWLEDGE_MCP_DEFAULT_KB` | — | Default KB name |

### Management

| Variable | Default | Description |
|----------|---------|-------------|
| `MANAGE_PORT` | `8085` | Web management UI port |

### MCP HTTP Server

| Variable | Default | Description |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | MCP HTTP server listen port |
| `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | MCP server base URL (reverse proxy) |

### Logging

| Variable | Default | Description |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_LOG_FILE` | `<exe-dir>/knowledge-mcp.log` | Log file path |
| `KNOWLEDGE_MCP_LOG_LEVEL` | `info` | Log level: `debug` / `info` |

### Search

| Variable | Default | Description |
|----------|---------|-------------|
| `SEARCH_MODE` | `hybrid` | Default search mode: `bm25` / `hybrid` |
| `RERANK_ENABLED` | `true` | Enable Cross-Encoder reranking |
| `RRF_K` | `60` | RRF fusion parameter k |
| `BM25_K1` | `1.2` | BM25 k1 parameter |
| `BM25_B` | `0.75` | BM25 b parameter |
| `ABSTRACT_BOOST` | `1.5` | Boost multiplier for abstract hits |
| `RERANK_BATCH_SIZE` | `20` | Reranker batch size |

### Chunking

| Variable | Default | Description |
|----------|---------|-------------|
| `CHUNK_MIN_CHARS` | `200` | Merge threshold for short paragraphs |
| `CHUNK_MAX_CHARS` | `2000` | Split threshold for long paragraphs |
| `CHUNK_OVERLAP_CHARS` | `200` | Overlap between chunks |
| `CHUNK_SEMANTIC_THRESHOLD` | `0.75` | Cosine threshold for merging adjacent chunks |
| `UPLOAD_MAX_SIZE_MB` | `100` | Maximum upload file size in MB |

### GPU Scheduler

| Variable | Default | Description |
|----------|---------|-------------|
| `GPU_SCHEDULER_ENABLED` | `false` | Enable GPU scheduler |
| `GPU_SCHEDULER_TIMEOUT` | `30s` | Sleep/wake HTTP request timeout |
| `GPU_SCHEDULER_WAKE_DELAY` | `3s` | Delay after wake for GPU model load |

---

## MCP Tools

### `knowledge_research` — Semantic search

| Parameter | Required | Description |
|-----------|----------|-------------|
| `question` | **yes** | User's original natural language question |
| `limit` | no (default 5) | Maximum results (1–20) |
| `kbName` | no | KB to search. When omitted, searches all KBs |
| `searchMode` | no | Override search mode (`bm25` / `hybrid`) |

### `knowledge_read` — Read document chunk

| Parameter | Required | Description |
|-----------|----------|-------------|
| `docSlug` | **yes** | Document slug identifier |
| `chunkID` | no | Specific chunk ID. If omitted, returns document overview |
| `context` | no (default 0) | Number of surrounding chunks for context |
| `sectionID` | no | Read a specific section chunk |
| `kbName` | no | KB name |

### `knowledge_list` — List documents

| Parameter | Required | Description |
|-----------|----------|-------------|
| `limit` | no (default 20) | Max documents to list |
| `kbName` | no | KB name |
| `tag` | no | Filter by tag |

### `knowledge_list_kbs` — List knowledge bases

No required parameters. Returns all KBs with their descriptions.

### `knowledge_upload` — Upload document

| Parameter | Required | Description |
|-----------|----------|-------------|
| `filePath` | conditional | Absolute path to a single file. Mutually exclusive with `directory` |
| `directory` | conditional | Directory path for batch upload. Mutually exclusive with `filePath` |
| `kbName` | no | Target KB name |
| `tags` | no | Comma-separated tags |

### `knowledge_remove` — Remove document

| Parameter | Required | Description |
|-----------|----------|-------------|
| `docSlug` | **yes** | Document slug to remove |
| `kbName` | no | KB name |
| `ttlSeconds` | no (default 604800, 7 days) | Tombstone TTL in seconds |

---

## KB Routing

When `default_kb` is not set or a query targets all KBs, the intelligent KB router selects
the most relevant knowledge base(s) using four-dimension weighted scoring:

| Dimension | Weight | Description |
|-----------|--------|-------------|
| Keyword match | 0.35 | Overlap between query tokens and KB name/description |
| Embedding similarity | 0.35 | Cosine similarity between query embedding and KB description embedding |
| Description quality | 0.15 | Length and information density of KB description |
| Domain constraints | 0.15 | Explicit domain mapping rules defined in the KB description |

Top-1 to Top-3 KBs are selected based on score gaps.

---

## Search Pipeline

```
Query
  ├─ Query triage: Simple → dictionary / Medium → multi-synonym / Complex → LLM
  ├─ Query rewriting & expansion
  │
  ├─ Phase 1: Broad Recall ───────────────────
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

---

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

---

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

At `info` log level you can see HIT/MISS/SET for each cache type.

Cache entries are automatically invalidated on document re-upload or deletion.

### Redis cache (optional)

Install Redis and set `redis_enabled = true`. Redis only caches **query-level search results**
(HybridSearch output), keyed by normalized query hash + KB + filter hash.

---

## Logging & Debugging

Set `log_level = "debug"` for detailed output including tokenization, per-term IDF, per-candidate
BM25 scores, RRF fusion details, and HNSW search steps. At `info` level you still get: search
request summaries, cache HIT/MISS, KB routing decisions, upload progress, and all warnings/errors.

```bash
# Follow logs
tail -f ~/.knowledge-mcp/knowledge-mcp.log

# Filter by module
tail -f ~/.knowledge-mcp/knowledge-mcp.log | grep "\[search\]"
tail -f ~/.knowledge-mcp/knowledge-mcp.log | grep "HIT\|MISS"
```

---

## Domain Dictionaries

Place YAML files in `dictionaries/` for query expansion with domain-specific synonyms.
Example `dictionaries/ship_motion.yaml`:

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

Dictionaries are loaded at startup. A query like "ship resistance CFD" automatically expands
to include "drag", "computational fluid dynamics", etc. in both BM25 and vector recall.

### Dictionary management tools

```bash
# Mine synonym candidates from accumulated search logs
knowledge-mcp dict mine

# Generate domain dictionary from KB chunks via LLM (requires DEEPSEEK_API_KEY)
knowledge-mcp dict gen
```

---

## LLM Query Rewriting

When `DEEPSEEK_API_KEY` is configured, complex queries (about 5% of all queries) are automatically
rewritten by DeepSeek LLM to generate 2–4 expanded variants for improved recall. Simple and medium
queries use the domain dictionary and synonym rewriter instead — avoiding unnecessary LLM costs.

**Three-layer fallback for resilience:**

1. **LLM layer** — DeepSeek API returns rewritten variants on success
2. **Fallback layer** — LLM failure falls back to `SynonymRewriter` with built-in synonym table + dictionary files
3. **Bottom layer** — even with an empty synonym table, the original query is returned unchanged; search never breaks

**Security:**

- API key injected **only via TOML config file or environment variable**; never hard-coded
- API responses in error logs are sanitised via `sanitiseForLog()` — any `sk-*` format key is replaced with `sk-***`
- API key never appears in logs, error messages, or the management UI

---

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

All tables use InnoDB + utf8mb4. No foreign keys — deletion is handled at the application layer.

---

## Model Deployment

See [docs/deployment-models.md](docs/deployment-models.md) / [中文版](docs/deployment-models_zh.md) for detailed model deployment instructions.

Quick reference:

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

---

## Maintenance

### Cleaning orphaned vector index entries

```bash
# Dry-run first
go run ./cmd/cleanup-vector/ --dry-run

# Clean a specific KB
go run ./cmd/cleanup-vector/ --kb ship-hydrodynamics --dry-run

# Apply
go run ./cmd/cleanup-vector/
```

### Rebuilding the vector index

Delete `VECTOR.gob` under the KB directory. The index rebuilds from `chunks_index` data on the next hybrid search request.

```bash
rm ~/knowledge_base/<kb-name>/VECTOR.gob
# Restart — index rebuilds on next hybrid query
```

---

## FAQ

**Q: "chunk xxx not found in document xxx" error?**

The search index references chunks that don't exist. Fix: run `go run ./cmd/cleanup-vector/` or delete `VECTOR.gob`.

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

1. Back up source files from `data_dir`
2. Configure MySQL connection and restart
3. Re-import documents via the web UI or `knowledge_upload`

**Q: Can I restore a soft-deleted document?**

Tombstoned documents can be restored before their TTL expires by removing the entry from `.tombstones.gob`.

**Q: How do I back up MySQL storage?**

```bash
mysqldump -u knowledge -p knowledge_rag > backup.sql
```

Also back up `data_dir/<kb-name>/VECTOR.gob` files.
