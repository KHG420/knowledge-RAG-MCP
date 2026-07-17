# 模型部署指南

knowledge-mcp 依赖两个可选的外部模型服务来实现混合检索（BM25 + 向量）和两阶段重排序。

## 架构总览

```
用户查询
  ↓
knowledge_search (MCP tool)
  ↓
┌─ Phase 1: 快速召回 (BM25 + 向量) ──────────────────┐
│  EMBED_API_ENDPOINT → Embedding 模型                │
│  将 query 转为向量，与 chunk 向量做余弦相似度        │
│  配合 BM25 关键词匹配，RRF 融合 → 召回 100 候选      │
└────────────────────────────────────────────────────┘
  ↓ (100 candidates)
┌─ Phase 2: 精排 (Cross-Encoder Reranker) ───────────┐
│  RERANK_API_BASE_URL → Reranker 模型                │
│  对每个 (query, chunk) 对做深度语义打分              │
│  按新分数重排 → 截断到 limit (如 8)                  │
└────────────────────────────────────────────────────┘
  ↓ (top-K results)
返回给用户
```

无外部模型时，系统降级为纯 BM25 关键词检索。

---

## 1. Embedding 模型（向量检索）

### 推荐方案：Ollama + BGE-M3

[BGE-M3](https://huggingface.co/BAAI/bge-m3) 支持中英双语，1024 维向量，Ollama 一行命令部署。

```bash
# 安装 Ollama
curl -fsSL https://ollama.com/install.sh | sh

# 拉取 BGE-M3 模型
ollama pull bge-m3

# Ollama 默认监听 http://localhost:11434
```

启动 knowledge-mcp 时注入环境变量：

```bash
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings \
EMBED_MODEL=bge-m3 \
EMBED_DIM=1024 \
  knowledge-mcp
```

### 备选方案

| 方案 | 模型 | 维度 | 命令 |
|------|------|------|------|
| Ollama | `nomic-embed-text` | 768 | `ollama pull nomic-embed-text` |
| Ollama | `mxbai-embed-large` | 1024 | `ollama pull mxbai-embed-large` |
| 远程 API | `text-embedding-ada-002` | 1536 | 设置 `EMBED_API_ENDPOINT` + `EMBED_API_KEY` |

### 环境变量

| 变量 | 必需 | 默认值 | 说明 |
|------|------|--------|------|
| `EMBED_API_ENDPOINT` | 是 | — | 完整的 Embedding API 端点，Ollama 填 `http://localhost:11434/v1/embeddings` |
| `EMBED_MODEL` | 否 | `text-embedding-ada-002` | 模型名称 |
| `EMBED_API_KEY` | 否 | — | API Key（Ollama 不需要） |
| `EMBED_DIM` | 否 | 自动检测 | 向量维度 |

---

## 2. Reranker 模型（Cross-Encoder 精排）

### 推荐方案：Infinity + gte-multilingual-reranker-base

[gte-multilingual-reranker-base](https://huggingface.co/Alibaba-NLP/gte-multilingual-reranker-base) 是阿里巴巴的 306M 参数 Cross-Encoder，支持 70+ 语言，CPU 友好。

[Infinity](https://github.com/michaelfeil/infinity) 是一个专为 embedding/rerank 推理设计的服务器，一行命令部署，暴露 Cohere 兼容的 `/rerank` API。

```bash
# 安装 Infinity
pip install infinity-emb[all]

# 启动 reranker 服务
infinity_emb v2 \
  --model-id Alibaba-NLP/gte-multilingual-reranker-base \
  --port 7997
```

首次启动会自动从 HuggingFace 下载模型（~1.2GB）。启动后验证：

```bash
curl -X POST http://localhost:7997/rerank \
  -H 'Content-Type: application/json' \
  -d '{"query":"分块参数","documents":["长段落阈值2000字符","短段落阈值200字符"],"top_n":2}'
```

启动 knowledge-mcp 时注入环境变量：

```bash
RERANK_API_BASE_URL=http://localhost:7997 \
  knowledge-mcp
```

### 备选方案

| 方案 | 模型 | 参数 | 说明 |
|------|------|------|------|
| Infinity | `BAAI/bge-reranker-v2-m3` | 0.6B | 下载量最高，质量好但 CPU 慢一倍 |
| Infinity | `Alibaba-NLP/gte-multilingual-reranker-base` | 306M | **推荐**，CPU 最佳性价比 |
| Ollama | `bge-reranker-v2-m3` (GGUF) | 0.6B | `ollama pull` 但需自行封装 `/rerank` 端点 |

### 环境变量

| 变量 | 必需 | 默认值 | 说明 |
|------|------|--------|------|
| `RERANK_API_BASE_URL` | 是 | `http://localhost:7997` | Reranker API 地址 |
| `RERANK_MODEL` | 否 | `gte-multilingual-reranker-base` | 模型名称 |
| `RERANK_API_KEY` | 否 | — | API Key（自建服务不需要） |
| `RERANK_CANDIDATE_LIMIT` | 否 | `100` | 第一阶段召回多少候选给 reranker 精排 |

### 硬件要求

| 模型 | CPU 内存 | GPU 显存 | CPU 延迟 (100候选) |
|------|---------|---------|-------------------|
| `gte-multilingual-reranker-base` | ~1.5 GB | ~2 GB | 1-3 秒 |
| `bge-reranker-v2-m3` | ~2.5 GB | ~4 GB | 2-5 秒 |

---

## 3. 完整启动示例

```bash
# 终端 1：Embedding 服务
ollama serve
ollama pull bge-m3

# 终端 2：Reranker 服务
infinity_emb v2 \
  --model-id Alibaba-NLP/gte-multilingual-reranker-base \
  --port 7997

# 终端 3：knowledge-mcp
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings \
EMBED_MODEL=bge-m3 \
EMBED_DIM=1024 \
RERANK_API_BASE_URL=http://localhost:7997 \
RERANK_CANDIDATE_LIMIT=100 \
  knowledge-mcp
```

## 4. 降级行为

| 场景 | 行为 |
|------|------|
| 未配置 `EMBED_API_ENDPOINT` | 退化为纯 BM25 关键词检索 |
| 未配置 `RERANK_API_BASE_URL` | 跳过重排序，BM25/RRF 分数直接返回 |
| Reranker 调用超时/失败 | 优雅降级，返回 BM25 排序结果 |
| 两者都未配置 | 纯 BM25，零外部依赖 |

## 5. API 契约

### Embedding API（OpenAI 兼容）

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
  "usage": {"total_tokens": 5}
}
```

### Reranker API（Cohere/Infinity 兼容）

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
