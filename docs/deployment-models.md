# Model Deployment Guide

knowledge-mcp relies on two optional external model services for hybrid retrieval (BM25 + vector) and two-stage reranking.

## Architecture Overview

```
User query
  ↓
knowledge_search (MCP tool)
  ↓
┌─ Phase 1: Fast Recall (BM25 + vector) ────────────┐
│  EMBED_API_ENDPOINT → Embedding model              │
│  Converts query to vector, cosine similarity       │
│  with chunk vectors                                │
│  Combined with BM25 keyword matching, RRF fusion   │
│  → 100 candidates                                  │
└────────────────────────────────────────────────────┘
  ↓ (100 candidates)
┌─ Phase 2: Precision Re-rank (Cross-Encoder) ──────┐
│  RERANK_API_BASE_URL → Reranker model              │
│  Deep semantic scoring for each (query, chunk) pair│
│  Re-sort by new score → truncate to limit (e.g. 8) │
└────────────────────────────────────────────────────┘
  ↓ (top-K results)
Returned to user
```

Without external models, the system degrades to pure BM25 keyword search.

---

## 1. Embedding Model (Vector Search)

### Recommended: Ollama + BGE-M3

[BGE-M3](https://huggingface.co/BAAI/bge-m3) supports bilingual Chinese/English, 1024-dimensional vectors, deployable with a single Ollama command.

```bash
# Install Ollama
curl -fsSL https://ollama.com/install.sh | sh

# Pull BGE-M3 model
ollama pull bge-m3

# Ollama listens on http://localhost:11434 by default
```

Inject environment variables when starting knowledge-mcp:

```bash
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings \
EMBED_MODEL=bge-m3 \
EMBED_DIM=1024 \
  knowledge-mcp
```

### Alternatives

| Solution | Model | Dimension | Command |
|----------|-------|-----------|---------|
| Ollama | `nomic-embed-text` | 768 | `ollama pull nomic-embed-text` |
| Ollama | `mxbai-embed-large` | 1024 | `ollama pull mxbai-embed-large` |
| Remote API | `text-embedding-ada-002` | 1536 | Set `EMBED_API_ENDPOINT` + `EMBED_API_KEY` |

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `EMBED_API_ENDPOINT` | Yes | — | Full embedding API endpoint, e.g. `http://localhost:11434/v1/embeddings` for Ollama |
| `EMBED_MODEL` | No | `text-embedding-ada-002` | Model name |
| `EMBED_API_KEY` | No | — | API key (not needed for Ollama) |
| `EMBED_DIM` | No | auto-detect | Vector dimension |

---

## 2. Reranker Model (Cross-Encoder Re-rank)

### Recommended: Infinity + gte-multilingual-reranker-base

[gte-multilingual-reranker-base](https://huggingface.co/Alibaba-NLP/gte-multilingual-reranker-base) is Alibaba's 306M-parameter Cross-Encoder supporting 70+ languages, CPU-friendly.

[Infinity](https://github.com/michaelfeil/infinity) is a dedicated inference server for embedding/rerank models, deployable with a single command, exposing a Cohere-compatible `/rerank` API.

```bash
# Install Infinity
pip install infinity-emb[all]

# Start reranker service
infinity_emb v2 \
  --model-id Alibaba-NLP/gte-multilingual-reranker-base \
  --port 7997
```

The first startup automatically downloads the model from HuggingFace (~1.2GB). Verify after startup:

```bash
curl -X POST http://localhost:7997/rerank \
  -H 'Content-Type: application/json' \
  -d '{"query":"chunking parameters","documents":["long paragraph threshold 2000 chars","short paragraph threshold 200 chars"],"top_n":2}'
```

Inject environment variables when starting knowledge-mcp:

```bash
RERANK_API_BASE_URL=http://localhost:7997 \
  knowledge-mcp
```

### Alternatives

| Solution | Model | Params | Notes |
|----------|-------|--------|-------|
| Infinity | `BAAI/bge-reranker-v2-m3` | 0.6B | Most downloaded, good quality but 2× slower on CPU |
| Infinity | `Alibaba-NLP/gte-multilingual-reranker-base` | 306M | **Recommended**, best CPU price/performance |
| Ollama | `bge-reranker-v2-m3` (GGUF) | 0.6B | `ollama pull` but requires custom `/rerank` endpoint wrapper |

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `RERANK_API_BASE_URL` | Yes | `http://localhost:7997` | Reranker API endpoint |
| `RERANK_MODEL` | No | `gte-multilingual-reranker-base` | Model name |
| `RERANK_API_KEY` | No | — | API key (not needed for self-hosted) |
| `RERANK_CANDIDATE_LIMIT` | No | `100` | How many Phase 1 candidates to feed the reranker |

### Hardware Requirements

| Model | CPU Memory | GPU VRAM | CPU Latency (100 candidates) |
|-------|------------|----------|------------------------------|
| `gte-multilingual-reranker-base` | ~1.5 GB | ~2 GB | 1-3 sec |
| `bge-reranker-v2-m3` | ~2.5 GB | ~4 GB | 2-5 sec |

---

## 3. Full Startup Example

```bash
# Terminal 1: Embedding service
ollama serve
ollama pull bge-m3

# Terminal 2: Reranker service
infinity_emb v2 \
  --model-id Alibaba-NLP/gte-multilingual-reranker-base \
  --port 7997

# Terminal 3: knowledge-mcp
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings \
EMBED_MODEL=bge-m3 \
EMBED_DIM=1024 \
RERANK_API_BASE_URL=http://localhost:7997 \
RERANK_CANDIDATE_LIMIT=100 \
  knowledge-mcp
```

## 4. Degradation Behavior

| Scenario | Behavior |
|----------|----------|
| `EMBED_API_ENDPOINT` not set | Falls back to pure BM25 keyword search |
| `RERANK_API_BASE_URL` not set | Skips reranking, returns BM25/RRF scores directly |
| Reranker call timeout/failure | Graceful degradation, returns BM25-ranked results |
| Neither configured | Pure BM25, zero external dependencies |

## 5. API Contract

### Embedding API (OpenAI-compatible)

```
POST /v1/embeddings
Content-Type: application/json

{
  "input": ["query or document text"],
  "model": "bge-m3"
}

Response:
{
  "data": [{"index": 0, "embedding": [0.123, -0.456, ...]}],
  "usage": {"total_tokens": [redacted]}
}
```

### Reranker API (Cohere/Infinity-compatible)

```
POST /rerank
Content-Type: application/json

{
  "query": "user search query",
  "documents": ["chunk text 1", "chunk text 2", ...],
  "model": "gte-multilingual-reranker-base"
}

Response:
{
  "results": [
    {"index": 0, "relevance_score": 0.95},
    {"index": 3, "relevance_score": 0.32}
  ]
}
```
