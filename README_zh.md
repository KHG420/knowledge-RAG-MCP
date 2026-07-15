# knowledge-mcp

[English](README.md)

> ⚡ **无需自己费心搭建知识库 — 只需连接 MCP，你的 Agent 即刻拥有智能知识库。**
>
> 拖入文档 → 自动分块索引 → BM25 + 向量混合检索 + 交叉编码器精排 → 即插即用，零运维。

基于 MCP (Model Context Protocol) 协议的本地文件知识库服务，提供 BM25 关键词搜索、混合检索（BM25 + 向量）以及可选的两阶段交叉编码器重排序。

## 特性

- **文档导入** — 支持 PDF、DOCX、ODT、EPUB、HTML、XLSX、PPTX、MD、TXT
- **BM25 搜索** — Unicode 感知、CJK 双字分词，支持查询重写
- **混合搜索** — BM25 + 稠密向量融合，采用 RRF 算法，自适应查询类型权重
- **两阶段重排序** — 可选的交叉编码器（兼容 Infinity/Cohere API）对 top-K 候选进行精排
- **段落级分块** — 语义边界切分、重叠、层级化细粒度 + 粗粒度章节分块、章节角色分类
- **父子检索** — 可读取分块所属的完整父章节，获取更丰富的上下文
- **论文元数据提取** — 自动提取标题、作者、摘要，识别章节角色

## 安装

```bash
go install ./...
```

## 快速开始

### 最小配置（仅 BM25，零外部依赖）

```bash
export KNOWLEDGE_MCP_DATA_DIR=./kb-data
knowledge-mcp
```

### 完整配置（BM25 + 向量嵌入 + 重排序）

详见 [docs/deployment-models.md](docs/deployment-models.md) 了解详细模型部署说明。

```bash
# 嵌入服务 (Ollama + BGE-M3)
ollama pull bge-m3

# 重排序服务 (Infinity + gte-multilingual-reranker-base)
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

## Web 管理页面

启动独立 Web 界面，通过浏览器上传、浏览和删除文档：

```bash
MANAGE_PORT=8080 KNOWLEDGE_MCP_DATA_DIR=./kb-data go run ./cmd/manager/
```

打开 [http://localhost:8080](http://localhost:8080) 即可使用。

管理页面与 MCP server 共享同一数据目录，通过网页上传的文档立即可通过 `knowledge_search` 搜索。

## 环境变量

### 必填

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_DATA_DIR` | `.reasonix/knowledge/` | 知识库存储目录 |

### 嵌入（混合搜索）

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `EMBED_API_BASE_URL` | — | 兼容 OpenAI 的 `/v1/embeddings` 端点 |
| `EMBED_MODEL` | `text-embedding-ada-002` | 模型名称 |
| `EMBED_API_KEY` | — | API 密钥（Ollama 无需） |
| `EMBED_DIM` | 自动检测 | 向量维度 |

### 重排序（两阶段检索）

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `RERANK_API_BASE_URL` | `http://localhost:7997` | 兼容 Infinity/Cohere 的 `/rerank` 端点 |
| `RERANK_MODEL` | `gte-multilingual-reranker-base` | 交叉编码器模型名称 |
| `RERANK_API_KEY` | — | API 密钥（自部署无需） |
| `RERANK_CANDIDATE_LIMIT` | `100` | 送入重排序的 BM25/RRF 候选数量 |

### 搜索行为

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `QUERY_REWRITE_SYNONYMS` | — | 自定义同义词对，格式：`词:同义词,词:同义词` |

## MCP 工具

### `knowledge_search`

跨文档全文搜索。支持 BM25（默认）和混合模式。
配置重排序后，结果经过两阶段检索：BM25/RRF 召回 → 交叉编码器精排 → 最终 top-K。

| 参数 | 必填 | 说明 |
|-----------|----------|-------------|
| `search_keywords` | **是** | 重写后的关键词（空格分隔）。不要直接传用户原始问题——先修正拼写、扩展上下文、添加同义词 |
| `original_question` | 否 | 用户原始问题原文（用于日志记录） |
| `query` | 否 | **已废弃** — 请使用 `search_keywords` |
| `limit` | 否 | 最大结果数（默认 8，上限 20） |
| `mode` | 否 | `bm25` 或 `hybrid`（有嵌入服务时自动选择 hybrid） |
| `sourceType` | 否 | 按文件类型过滤：`pdf`、`md`、`txt` 等 |
| `section` | 否 | 按章节标题过滤（子串匹配） |

### `knowledge_read`

读取指定分块或其完整父章节。

| 参数 | 必填 | 说明 |
|-----------|----------|-------------|
| `docSlug` | **是** | 文档标识符（来自搜索/列表结果） |
| `chunkID` | **是** | 分块标识符，如 `005` |
| `context` | 否 | 包含前后相邻分块数（默认 0，上限 5） |
| `level` | 否 | `chunk`（默认）或 `section`——读取完整父章节 |

### `knowledge_list`

列出所有已上传文档及其元数据。

### `knowledge_upload`

上传单个文件或批量导入目录。

| 参数 | 必填 | 说明 |
|-----------|----------|-------------|
| `filePath` | * | 单个文件路径 |
| `directory` | * | 批量导入的目录路径 |
| `recursive` | 否 | 是否递归子目录（批量导入时） |

\* `filePath` 和 `directory` 二选一。

### `knowledge_remove`

删除指定文档及其所有分块。

| 参数 | 必填 | 说明 |
|-----------|----------|-------------|
| `docSlug` | **是** | 要删除的文档标识符（来自列表结果） |

## 搜索流程

```
查询 → 查询重写（同义词）→ 分词
  → 阶段一：快速召回 ─────────────────────
  │   BM25 关键词打分
  │   + 可选的稠密向量余弦相似度
  │   → RRF 融合（自适应查询类型权重）
  │   → top-N 候选（默认 N=100）
  → 阶段二：精准重排 ────────────────────  [如配置了重排序]
  │   交叉编码器对每对 (查询, 分块) 打分
  │   → 按相关性重新排序
  → 截断到 limit → 生成摘录 → 去重 → 返回
```

**优雅降级**：无嵌入服务时，混合模式自动回退为纯 BM25。无重排序时，跳过阶段二直接返回 RRF/BM25 结果。

## 存储布局

```
<data-dir>/
├── INDEX.md
├── .searchlog.jsonl
└── <document-slug>/
    ├── meta.json          # 原始文件名、来源类型、添加时间、标题、作者、摘要
    ├── CHUNKS.toml        # 每块：词项、向量、章节、偏移、章节角色
    ├── source.<ext>       # 原始文件副本
    └── chunks/
        ├── 000.md         # 细粒度分块
        ├── 001.md
        └── sections/
            ├── S00.md     # 粗粒度章节级分块
            └── S01.md
```

## 架构

```
main.go                  — MCP 服务启动、工具注册、环境变量解析
internal/
  knowledge/
    store.go             — Store 结构体、数据目录管理
    search.go            — Search、HybridSearch、SearchDocuments、coarseToFine、rerankTop
    chunker.go           — ChunkText、ChunkTextHierarchical、语义合并
    doc.go               — DocumentMeta、ChunkWithMeta、SearchFilter、SearchHit
    embed.go             — Embedder 接口、OpenAIEmbedder、Reranker 接口
    rerank.go            — InfinityReranker（兼容 Cohere/Infinity）
    rewrite.go           — QueryRewriter 接口、SynonymRewriter
    rewrite_llm.go       — LLMQueryRewriter（可选的 LLM 查询扩展）
    upload.go            — UploadDocument、UploadDirectory
    upload_doc.go        — 格式特定解析器（PDF、DOCX 等）
    parser.go            — 文档解析调度
    inverted.go          — 倒排索引加速候选查找 (G7)
    list.go              — ListPreview、ReadChunk、ReadChunkContext
    remove.go            — RemoveDocument
    searchlog.go         — FileSearchLogger
    meta_extract.go      — 论文元数据提取
    store_index.go       — CHUNKS.toml 读写
  retrieval/
    bm25.go              — 分词器（CJK 双字感知）、BM25Score、MakeSnippet
docs/
  deployment-models.md   — 嵌入与重排序模型部署指南
  roadmap.md             — RAG 优化路线图
```

## 许可证

MIT
