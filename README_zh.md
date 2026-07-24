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
- **多知识库** — 将文档组织到独立的知识库中；跨知识库搜索和列表；通过管理页面创建/删除知识库
- **KB 描述** — 创建知识库时可填写简要描述；通过 `knowledge_list_kbs` 工具查看所有 KB 及其描述

## 安装

```bash
go build -o knowledge-mcp .
```

## 配置

knowledge-mcp 支持三种配置方式（优先级从高到低）：

1. **TOML 配置文件** — 可执行文件同目录下的 `knowledge-mcp.toml`，或 `~/.knowledge-mcp/config.toml`
2. **环境变量** — 无 TOML 文件时的回退方案
3. **内置默认值** — 所有字段均有合理的默认值

### 配置向导

运行交互式配置向导可生成 `knowledge-mcp.toml` 文件：

```bash
knowledge-mcp setup
```

向导会探测各端点连通性并写入有效的配置文件。

### 配置项

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `data_dir` | `KNOWLEDGE_MCP_DATA_DIR` | `~/knowledge_base/` | 知识库存储目录 |
| `default_kb` | `KNOWLEDGE_MCP_DEFAULT_KB` | — | 默认知识库名称 |
| `embed_endpoint` | `EMBED_API_ENDPOINT` | — | OpenAI 兼容的 Embedding API 端点 |
| `embed_model` | `EMBED_MODEL` | `bge-m3` | 嵌入模型名称 |
| `embed_dim` | `EMBED_DIM` | 自动检测 | 向量维度 |
| `embed_api_key` | `EMBED_API_KEY` | — | API 密钥（Ollama 无需） |
| `rerank_endpoint` | `RERANK_API_ENDPOINT` | — | Infinity/Cohere 兼容的 Reranker API 端点 |
| `rerank_model` | `RERANK_MODEL` | `gte-multilingual-reranker-base` | 交叉编码器模型名称 |
| `rerank_api_key` | `RERANK_API_KEY` | — | API 密钥（自部署无需） |
| `rerank_timeout` | `RERANK_TIMEOUT` | `30s` | 重排序 HTTP 请求超时 |
| `rerank_candidate_limit` | `RERANK_CANDIDATE_LIMIT` | `100` | 送入重排序的 BM25/RRF 候选数量 |
| `gpu_scheduler_enabled` | `GPU_SCHEDULER_ENABLED` | `false` | 启用 GPU 调度器 |
| `gpu_scheduler_timeout` | `GPU_SCHEDULER_TIMEOUT` | `30s` | sleep/wake HTTP 请求超时 |
| `gpu_scheduler_wake_delay` | `GPU_SCHEDULER_WAKE_DELAY` | `3s` | 唤醒后等待模型加载到 GPU 的延迟 |
| `doc_parser_endpoint` | `DOC_PARSER_ENDPOINT` | — | 外部文档解析 HTTP API 地址。留空则跳过外部解析，直接用本地 tabula |
| `doc_parser_api_key` | `DOC_PARSER_API_KEY` | — | 文档解析 API 的 Bearer token（可选） |
| `doc_parser_timeout` | `DOC_PARSER_TIMEOUT` | `120s` | 文档解析 HTTP 请求超时 |
| `manage_port` | `MANAGE_PORT` | `8085` | Web 管理页面端口 |
| `serve_port` | `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | SSE 服务器监听端口 |
| `serve_base_url` | `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | SSE 服务器基础 URL（反向代理场景） |
| `log_file` | `KNOWLEDGE_MCP_LOG_FILE` | `<exe-dir>/knowledge-mcp.log` | 日志文件路径 |
| `log_level` | `KNOWLEDGE_MCP_LOG_LEVEL` | `info` | 日志级别：`debug` 或 `info` |

## 快速开始

### 运行模式

knowledge-mcp 支持四种运行模式：

- **stdio 模式（推荐 MCP 客户端使用）** — 通过 stdin/stdout 走 MCP 协议通信。
  无 HTTP 服务器，无 Web 管理页面。适合 Reasonix、Claude Desktop 等
  基于 stdio 的 MCP 客户端：
  ```bash
  knowledge-mcp stdio
  ```
- **HTTP SSE 模式（默认）** — 长期运行的 MCP 服务器；包含 Web 管理页面：
  ```bash
  knowledge-mcp serve
  ```
- **仅 MCP SSE** — HTTP SSE 不含管理页面：
  ```bash
  knowledge-mcp serve --mcp
  ```
- **配置向导** — 交互式配置：
  ```bash
  knowledge-mcp setup
  ```

### 最小配置（仅 BM25，零外部依赖）

```bash
export KNOWLEDGE_MCP_DATA_DIR=./kb-data
knowledge-mcp serve
```

### 完整配置（BM25 + 向量嵌入 + 重排序）

详见 [docs/deployment-models.md](docs/deployment-models.md) / [中文版](docs/deployment-models_zh.md) 了解详细模型部署说明。

```bash
# 嵌入服务 (Ollama + BGE-M3)
ollama pull bge-m3

# 重排序服务 (Infinity + gte-multilingual-reranker-base)
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

## Web 管理页面

管理页面已**内嵌**在 MCP server 中 —— 在 `serve` 模式下自动启动（`serve --mcp` 模式不启动）。
打开浏览器访问 [http://localhost:8085](http://localhost:8085)（默认端口）即可上传、
浏览、搜索和删除文档，以及管理多个知识库。

可通过 `MANAGE_PORT` 环境变量修改端口：

```bash
MANAGE_PORT=8080 knowledge-mcp serve
```

管理页面与 MCP server 共享同一数据目录，通过网页上传的文档立即可通过 `knowledge_search` 搜索。

## 作为守护进程 / 服务运行

`serve` 命令本身以前台进程方式运行。生产环境中建议通过系统服务管理，以实现开机自启和崩溃自动恢复。

### Linux (systemd)

复制服务模板并重新加载 systemd：

```bash
sudo cp scripts/knowledge-mcp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now knowledge-mcp
```

在 `/etc/knowledge-mcp/env` 中配置环境变量（嵌入、重排序等）：

```bash
sudo mkdir -p /etc/knowledge-mcp
cat <<EOF | sudo tee /etc/knowledge-mcp/env
KNOWLEDGE_MCP_DATA_DIR=/var/lib/knowledge-mcp
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings
EOF
```

### macOS (launchd)

将 plist 复制到 LaunchAgents 目录并加载：

```bash
cp scripts/com.knowledge-mcp.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.knowledge-mcp.plist
```

加载前请编辑 `~/Library/LaunchAgents/com.knowledge-mcp.plist`，设置正确的二进制路径和环境变量。

### MCP 客户端集成（stdio）

对于 **Reasonix**、**Claude Desktop**、**Cline** 等 MCP 客户端，推荐使用 **stdio** 模式，
通过在项目根目录配置 `.mcp.json` 文件即可。MCP 客户端会自动管理进程生命周期：

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

无需配置 launchd，MCP 客户端会处理一切。

### 其他方式

- **tmux / screen**：在持久会话中运行 `knowledge-mcp serve --mcp`。
- **nohup**：`nohup knowledge-mcp serve --mcp > /tmp/kmcp.log 2>&1 &`

## 环境变量

### 必填

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_DATA_DIR` | `~/knowledge_base/` | 知识库存储目录 |
| `KNOWLEDGE_MCP_DEFAULT_KB` | — | 默认知识库名称。设置后工具默认使用该 KB（除非指定 `kbName`）；未设置时搜索所有 KB。 |

### 管理页面

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `MANAGE_PORT` | `8085` | Web 管理页面端口 |

### SSE 服务器

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | SSE 服务器监听端口 |
| `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | SSE 服务器基础 URL（反向代理场景） |

### 嵌入（混合搜索）

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `EMBED_API_ENDPOINT` | — | 完整的 OpenAI 兼容的 Embedding API 端点 |
| `EMBED_MODEL` | `bge-m3` | 模型名称 |
| `EMBED_API_KEY` | — | API 密钥（Ollama 无需） |
| `EMBED_DIM` | 自动检测 | 向量维度 |

### 重排序（两阶段检索）

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `RERANK_API_ENDPOINT` | `http://localhost:7997/rerank` | 完整的 Infinity/Cohere 兼容的 Reranker API 端点 |
| `RERANK_MODEL` | `gte-multilingual-reranker-base` | 交叉编码器模型名称 |
| `RERANK_API_KEY` | — | API 密钥（自部署无需） |
| `RERANK_CANDIDATE_LIMIT` | `100` | 送入重排序的 BM25/RRF 候选数量 |
| `RERANK_TIMEOUT` | `30s` | 重排序 HTTP 请求超时 |
| `RERANK_BATCH_SIZE` | `20` | 每批送入重排序的文档数 |

### 日志

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `KNOWLEDGE_MCP_LOG_FILE` | `<exe-dir>/knowledge-mcp.log` | 日志文件路径 |
| `KNOWLEDGE_MCP_LOG_LEVEL` | `info` | 日志级别：`debug` 或 `info` |

### 搜索行为

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `QUERY_REWRITE_SYNONYMS` | — | 自定义同义词对，格式：`词:同义词,词:同义词` |

### GPU 调度器

GPU 调度器协调嵌入和重排序模型在单 GPU 上的休眠/唤醒。
启用后，在上传文档（需要嵌入）和搜索（需要重排序）时自动切换模型，
使得两个模型即使单独均无法放入 GPU 显存也能正常工作。
两个模型的休眠/唤醒 API 各自独立配置，因为它们使用不同的协议和端口。

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `GPU_SCHEDULER_ENABLED` | `false` | 设为 `true` 或 `1` 开启 |
| `GPU_SCHEDULER_EMBEDDING_SLEEP_URL` | — | 嵌入模型休眠 API 地址 |
| `GPU_SCHEDULER_EMBEDDING_WAKE_URL` | — | 嵌入模型唤醒 API 地址 |
| `GPU_SCHEDULER_EMBEDDING_SLEEP_BODY` | — | 嵌入模型休眠请求的可选 JSON body |
| `GPU_SCHEDULER_RERANKER_SLEEP_URL` | `http://localhost:11435/sleep` | 重排序模型休眠 API 地址 |
| `GPU_SCHEDULER_RERANKER_WAKE_URL` | `http://localhost:11435/wake_up` | 重排序模型唤醒 API 地址 |
| `GPU_SCHEDULER_RERANKER_SLEEP_BODY` | `{"level":2}` | 重排序模型休眠请求的 JSON body |
| `GPU_SCHEDULER_TIMEOUT` | `30s` | sleep/wake HTTP 请求超时 |
| `GPU_SCHEDULER_WAKE_DELAY` | `3s` | 唤醒后等待模型加载到 GPU 的延迟 |

### 文档解析

配置后，所有非纯文本格式（PDF、DOCX、ODT、EPUB、HTML、XLSX、PPTX）优先走外部 HTTP API 解析；
API 不可用时自动回退到本地 tabula 库，不会中断上传流程。

| 变量 | 默认值 | 说明 |
|----------|---------|-------------|
| `DOC_PARSER_ENDPOINT` | — | 外部文档解析 API 地址 |
| `DOC_PARSER_API_KEY` | — | Bearer token（可选） |
| `DOC_PARSER_TIMEOUT` | `120s` | HTTP 请求超时 |

## MCP 工具

### `knowledge_search`

跨文档全文搜索。支持 BM25（默认）和混合模式。
配置重排序后，结果经过两阶段检索：BM25/RRF 召回 → 交叉编码器精排 → 最终 top-K。

| 参数 | 必填 | 说明 |
|-----------|----------|-------------|
| `search_keywords` | **是** | 重写后的关键词（空格分隔）。不要直接传用户原始问题——先修正拼写、扩展上下文、添加同义词 |
| `original_question` | 否 | 用户原始问题原文（用于日志记录） |
| `query` | 否 | **已废弃** — 请使用 `search_keywords` |
| `kbName` | 否 | 知识库名称。设置后只搜索该 KB；不传则搜索全部 KB |
| `limit` | 否 | 最大结果数（默认 8，上限 20） |
| `mode` | 否 | `bm25` 或 `hybrid`（有嵌入服务时自动选择 hybrid） |
| `sourceType` | 否 | 按文件类型过滤：`pdf`、`md`、`txt` 等 |
| `section` | 否 | 按章节标题过滤（子串匹配） |
| `tags` | 否 | 逗号分隔的标签。仅返回匹配至少一个标签的文档 |
| `addedAfter` | 否 | ISO 8601 日期。仅返回此时间之后添加的文档 |
| `addedBefore` | 否 | ISO 8601 日期。仅返回此时间之前添加的文档 |
| `coarse` | 否 | 启用粗粒度到细粒度的两阶段搜索 |

### `knowledge_read`

读取指定分块或其完整父章节。

| 参数 | 必填 | 说明 |
|-----------|----------|-------------|
| `docSlug` | **是** | 文档标识符（来自搜索/列表结果） |
| `chunkID` | **是** | 分块标识符，如 `005` |
| `kbName` | 否 | 知识库名称。不传则遍历所有 KB 查找文档 |
| `context` | 否 | 包含前后相邻分块数（默认 0，上限 5） |
| `level` | 否 | `chunk`（默认）或 `section`——读取完整父章节 |

### `knowledge_list_kbs`

列出所有知识库及其描述。

| 参数 | 必填 | 说明 |
|-----------|----------|-------------|
| _(无)_ | — | 返回 KB 数量及每个 KB 的名称 + 描述 |

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
当交叉编码器重排序不可用或失败时，自动回退到阶段一的向量余弦相似度排序。

## 存储布局

```
<data-dir>/
├── <kb-name>/
│   ├── INDEX.md
│   ├── INVERTED.gob        # 全局倒排索引，加速候选查找
│   ├── kb.json             # KB 描述（创建时填写）
│   ├── LIST_SNAPSHOT.json
│   ├── .searchlog.jsonl
│   └── <document-slug>/
│       ├── meta.json          # 原始文件名、来源类型、添加时间、标题、作者、摘要
│       ├── CHUNKS.toml        # 每块：词项、向量、章节、偏移、章节角色
│       ├── source.<ext>       # 原始文件副本
│       └── chunks/
│           ├── 000.md         # 细粒度分块
│           ├── 001.md
│           └── sections/
│               ├── S00.md     # 粗粒度章节级分块
│               └── S01.md
├── <another-kb>/
│   └── ...
└── (旧版扁平文档位于根目录)
```

## 架构

```
main.go                  — CLI 入口点、子命令 (stdio / serve / setup)、工具注册
internal/
  config/
    config.go            — TOML 配置加载、环境变量回退、默认值
  setup/
    setup.go             — 交互式配置向导 ("knowledge-mcp setup")
    probe.go             — 端点连通性探测
  logging/
    logger.go            — 结构化文件日志 (DEBUG/INFO/WARN/ERROR，模块化)
  knowledge/
    store.go             — Store 结构体、数据目录管理、CHUNKS.toml I/O、KB CRUD
    search.go            — Search、HybridSearch、SearchDocuments、coarseToFine、rerankTop
    chunker.go           — ChunkText、ChunkTextHierarchical、语义合并
    doc.go               — DocumentMeta、ChunkWithMeta、SearchFilter、SearchHit、ChunksIndex
    embed.go             — Embedder 接口、OpenAIEmbedder
    rerank.go            — InfinityReranker（兼容 Cohere/Infinity）、Reranker 接口
    gpu_scheduler.go     — GPU 调度器，协调嵌入/重排序模型的休眠与唤醒
    rewrite.go           — QueryRewriter 接口、SynonymRewriter
    rewrite_llm.go       — LLMQueryRewriter（可选的 LLM 查询扩展）
    manage.go            — Web 管理页面服务、知识库 CRUD、上传/删除/搜索处理器
    upload.go            — UploadDocument、UploadDirectory
    parser.go            — 文档解析调度 — 外部 HTTP API + tabula 回退 (PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX, MD, TXT)
    inverted.go          — 全局倒排索引 (INVERTED.gob)，加速候选查找
    list.go              — ListPreview、ReadChunk、ReadChunkContext
    remove.go            — RemoveDocument
    searchlog.go         — FileSearchLogger (.searchlog.jsonl)
    meta_extract.go      — 论文元数据提取（标题、作者、摘要、章节角色）
  retrieval/
    bm25.go              — 分词器（CJK 双字感知）、BM25Score、MakeSnippet
scripts/
  eval.go                — 检索评估脚本 (NDCG@5, MRR, Recall@10)
service-manager.sh       — Ollama + Infinity 依赖的服务管理脚本
docs/
  deployment-models.md   — 嵌入与重排序模型部署指南
  deployment-models_zh.md
  roadmap.md             — RAG 优化路线图
  roadmap_zh.md
```

## 许可证

MIT
