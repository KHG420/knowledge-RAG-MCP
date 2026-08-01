# knowledge-mcp

[English](README.md)

> ⚡ **无需自己费心搭建知识库 — 只需连接 MCP，你的 Agent 即刻拥有智能知识库。**
>
> 拖入文档 → 自动分块索引 → BM25 + 向量混合检索 + 交叉编码器精排 → 即插即用，零运维。

基于 MCP (Model Context Protocol) 协议的本地知识库服务，提供 BM25 关键词搜索、混合检索（BM25 + 向量）以及可选的两阶段交叉编码器重排序。

---

## 目录

- [特性](#特性)
- [安装](#安装)
- [快速开始](#快速开始)
- [运行模式](#运行模式)
- [配置详解](#配置详解)
- [MCP 客户端集成](#mcp-客户端集成)
- [Web 管理界面](#web-管理界面)
- [MCP 工具](#mcp-工具)
- [检索管线](#检索管线)
- [存储后端](#存储后端)
- [缓存机制](#缓存机制)
- [模型部署](#模型部署)
- [领域词典](#领域词典)
- [LLM 查询改写](#llm-查询改写)
- [日志与调试](#日志与调试)
- [运维部署](#运维部署)
- [数据库表结构](#数据库表结构)
- [维护工具](#维护工具)
- [架构总览](#架构总览)
- [常见问题](#常见问题)

---

## 特性

- **文档导入** — 支持 PDF、DOCX、ODT、EPUB、HTML、XLSX、PPTX、MD、TXT
- **BM25 搜索** — Unicode 感知、CJK 双字分词，支持查询重写、同义词扩展、LLM 查询改写
- **LLM 查询改写** — 可选 DeepSeek LLM 驱动，自动生成 2-4 个改写变体提升召回率；LLM 不可用时优雅降级到同义词重写
- **混合搜索** — BM25 + 稠密向量融合，采用 RRF 算法（k=60），自适应查询类型权重
- **两阶段重排序** — 可选的交叉编码器（兼容 Infinity/Cohere API）对 top-K 候选精排
- **段落级分块** — 语义边界切分（默认 200-2000 字符）、~200 字符重叠、层级化 fine+coarse 分块、章节角色分类（摘要/引言/方法/实验/结论）
- **父子检索** — 可读取分块所属的完整父章节，获取更丰富的上下文
- **论文元数据提取** — 自动提取标题、作者、摘要，识别章节角色
- **多知识库** — 将文档组织到独立的知识库中；跨知识库搜索和列表；通过管理页面或 MCP 工具创建/删除知识库
- **智能知识库路由** — 使用四维度加权评分（关键词 0.35 + 嵌入 0.35 + 描述 0.15 + 领域约束 0.15）自动路由查询到最相关的 1-3 个知识库
- **领域词典支持** — 加载 YAML 格式的领域同义词词典进行查询扩展
- **MySQL/MariaDB 后端** — 可选的数据库存储后端，替代默认的文件系统存储；所有文档/分块/索引/清单数据存储在关系表中
- **Redis 缓存** — 可选的查询结果缓存 + 内置分块/元数据/索引增量缓存
- **软删除（Tombstone）** — 文档删除采用 TTL 墓碑模式（默认 7 天），删除后立即从搜索中隐藏，物理清理在过期后执行
- **增量索引与版本管理** — 重新上传文档时仅对变更的分块重新建索引；基于内容寻址的分块 ID 实现幂等更新
- **证据质量信号** — 每个结果附带 source_confidence、answer_relevance 和 completeness 元数据
- **页码感知** — PDF 分块标记页码，搜索结果和 chunk 读取中透传 `page_start` / `page_end`

---

## 安装

```bash
cd knowledge-RAG-MCP
go build -o knowledge-mcp .
```

生成的二进制文件 `knowledge-mcp` 即可独立运行。

---

## 快速开始

### 最小配置（仅 BM25，零外部依赖）

```bash
export KNOWLEDGE_MCP_DATA_DIR=./kb-data
./knowledge-mcp serve
```

Web 管理界面自动在 `http://localhost:8085` 启动，可直接上传文档并搜索。

### 完整配置（BM25 + 向量嵌入 + 重排序）

详见 [模型部署](#模型部署) 章节了解各模型服务的安装方法。

```bash
# 嵌入服务 (Ollama + BGE-M3 / Qwen3-Embedding)
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
  ./knowledge-mcp serve
```

---

## 运行模式

knowledge-mcp 支持四种运行模式：

### stdio 模式（推荐 MCP 客户端使用）

通过 stdin/stdout 走 MCP 协议通信。无 HTTP 服务器，无 Web 管理页面。适合 Reasonix、Claude Desktop 等基于 stdio 的 MCP 客户端：

```bash
./knowledge-mcp stdio
```

> **注意**：stdio 模式下所有配置只能通过环境变量或 TOML 文件传入。

### HTTP SSE 模式（默认）

长期运行的 MCP 服务器 + Web 管理界面同时启动：

```bash
./knowledge-mcp serve
```

- MCP SSE 端点：`http://localhost:8086/sse`
- Web 管理界面：`http://localhost:8085`

### 仅 MCP SSE

HTTP SSE 不含管理界面（适用于已有独立管理后台的场景）：

```bash
./knowledge-mcp serve --mcp
```

### 配置向导

交互式配置，探测端点连通性并生成 `knowledge-mcp.toml`：

```bash
./knowledge-mcp setup
```

### 管理命令

```bash
./knowledge-mcp manage    # 仅启动 Web 管理界面（不含 MCP server），端口 8085
./knowledge-mcp version   # 打印版本信息
```

---

## 配置详解

knowledge-mcp 支持三种配置方式（优先级从高到低）：

1. **TOML 配置文件** — 可执行文件同目录下的 `knowledge-mcp.toml`
2. **环境变量** — 无 TOML 文件时的回退方案
3. **内置默认值** — 所有字段均有合理的默认值

### 最小 TOML 示例

```toml
data_dir = "~/knowledge_base/"
default_kb = "my-kb"
log_level = "debug"
```

### 完整 TOML 示例（含 MySQL + 嵌入 + 重排序）

```toml
data_dir = "~/knowledge_base/"
default_kb = ""

# Embedding（向量检索）
embed_endpoint = "http://127.0.0.1:11434/api/embed"
embed_model = "qwen3-embedding:q4_k_m"
embed_dim = 2560
embed_api_key = ""

# Reranker（Cross-Encoder 精排）
rerank_endpoint = "http://127.0.0.1:11435/rerank"
rerank_model = "Qwen3-Reranker-0.6B"
rerank_api_key = ""
rerank_timeout = "300s"
rerank_candidate_limit = 100

# GPU 调度器（单 GPU 共享嵌入和重排序模型）
gpu_scheduler_enabled = false
gpu_scheduler_timeout = "30s"

# 文档解析（可选的外部 HTTP API）
mineru_enabled = true
doc_parser_endpoint = "http://127.0.0.1:8000/pdf_to_md"
doc_parser_api_key = ""
doc_parser_timeout = "600s"

# 服务端口
manage_port = "8085"
serve_port = "8086"
serve_base_url = ""

# 日志
log_file = "/home/aq/.knowledge-mcp/knowledge-mcp.log"
log_level = "info"

# MySQL 后端（必须配置数据库中对应的库和用户权限）
mysql_dsn = ""
mysql_user = "knowledge"
mysql_password = "your-password"
mysql_host = "127.0.0.1"
mysql_port = "3306"
mysql_database = "knowledge_rag"
mysql_socket_path = ""

# Redis 缓存（可选）
redis_enabled = false
redis_addr = "127.0.0.1:6379"
redis_password = ""
redis_db = 0
redis_prefix = "kmcp:"
redis_pool_size = 10

# 缓存 TTL（秒，0 表示永不过期）
cache_query_ttl = 300
cache_chunk_ttl = 0
cache_meta_ttl = 0
cache_index_ttl = 0
cache_kblist_ttl = 60
```

### 全部配置项

#### 存储

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `data_dir` | `KNOWLEDGE_MCP_DATA_DIR` | `~/knowledge_base/` | 知识库存储目录（文件后端时使用；MySQL 后端时存储 VECTOR.gob 等文件） |
| `default_kb` | `KNOWLEDGE_MCP_DEFAULT_KB` | — | 默认知识库名称。设置后 MCP 工具默认使用该 KB（除非指定 `kbName`） |

#### 嵌入（向量检索）

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `embed_endpoint` | `EMBED_API_ENDPOINT` | — | OpenAI 兼容或 Ollama 原生的 Embedding API 端点 |
| `embed_model` | `EMBED_MODEL` | `bge-m3` | 嵌入模型名称 |
| `embed_dim` | `EMBED_DIM` | 自动检测 | 向量维度 |
| `embed_api_key` | `EMBED_API_KEY` | — | API 密钥（Ollama 无需） |

#### 重排序（两阶段检索）

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `rerank_endpoint` | `RERANK_API_ENDPOINT` | — | Infinity/Cohere 兼容的 Reranker API 端点 |
| `rerank_model` | `RERANK_MODEL` | `gte-multilingual-reranker-base` | 交叉编码器模型名称 |
| `rerank_api_key` | `RERANK_API_KEY` | — | API 密钥（自部署无需） |
| `rerank_timeout` | `RERANK_TIMEOUT` | `30s` | 重排序 HTTP 请求超时 |
| `rerank_candidate_limit` | `RERANK_CANDIDATE_LIMIT` | `100` | 送入重排序的 BM25/RRF 候选数量 |

#### LLM 查询改写（DeepSeek）

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `deepseek_api_key` | `DEEPSEEK_API_KEY` | — | DeepSeek API 密钥。**不设置则不启用 LLM 改写** |
| `deepseek_endpoint` | `DEEPSEEK_ENDPOINT` | `https://api.deepseek.com/chat/completions` | DeepSeek API 端点 |
| `deepseek_model` | `DEEPSEEK_MODEL` | `deepseek-flash` | 模型名称（推荐 `deepseek-flash`） |

#### 服务端口

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `manage_port` | `MANAGE_PORT` | `8085` | Web 管理页面端口 |
| `serve_port` | `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | SSE 服务器监听端口 |
| `serve_base_url` | `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | SSE 服务器基础 URL（反向代理场景，如 `https://example.com/mcp`） |

#### 日志

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `log_file` | `KNOWLEDGE_MCP_LOG_FILE` | `<exe-dir>/knowledge-mcp.log` | 日志文件路径 |
| `log_level` | `KNOWLEDGE_MCP_LOG_LEVEL` | `info` | 日志级别：`debug` / `info`；`debug` 会输出 Tokenization、BM25 打分、RRF 融合等详细过程 |

#### 搜索行为（运行时热更新）

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `search_mode` | `SEARCH_MODE` | `hybrid` | 默认搜索模式：`bm25` / `hybrid` |
| `rerank_enabled` | `RERANK_ENABLED` | `true` | 是否启用重排序 |
| `rrf_k` | `RRF_K` | `60` | RRF 融合参数 |
| `bm25_k1` | `BM25_K1` | `1.2` | BM25 k1 参数 |
| `bm25_b` | `BM25_B` | `0.75` | BM25 b 参数 |
| `abstract_boost` | `ABSTRACT_BOOST` | `1.5` | 论文摘要命中时的分数奖励 |

#### 分块参数（运行时热更新）

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `chunk_min_chars` | `CHUNK_MIN_CHARS` | `200` | 短段落合并阈值（低于此值合并到前一块） |
| `chunk_max_chars` | `CHUNK_MAX_CHARS` | `2000` | 长段落拆分阈值（高于此值按句子边界再分） |
| `chunk_overlap_chars` | `CHUNK_OVERLAP_CHARS` | `200` | 块间重叠字符数（句子边界对齐） |
| `chunk_semantic_threshold` | `CHUNK_SEMANTIC_THRESHOLD` | `0.75` | 语义相邻块合并的余弦相似度阈值 |

#### 文档解析

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `doc_parser_endpoint` | `DOC_PARSER_ENDPOINT` | — | 外部文档解析 HTTP API 地址（如 MinerU）。留空则使用本地 tabula 库 |
| `doc_parser_api_key` | `DOC_PARSER_API_KEY` | — | Bearer token（可选） |
| `doc_parser_timeout` | `DOC_PARSER_TIMEOUT` | `600s` | HTTP 请求超时 |
| `mineru_enabled` | `MINERU_ENABLED` | `true` | 是否启用外部解析器 |

#### GPU 调度器

GPU 调度器协调嵌入和重排序模型共享单 GPU。启用后，在上传文档和搜索时自动切换模型。

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `gpu_scheduler_enabled` | `GPU_SCHEDULER_ENABLED` | `false` | 设为 `true` 启用 |
| `gpu_scheduler_embedding_sleep_url` | `GPU_SCHEDULER_EMBEDDING_SLEEP_URL` | — | 嵌入模型休眠 API 地址 |
| `gpu_scheduler_reranker_sleep_url` | `GPU_SCHEDULER_RERANKER_SLEEP_URL` | — | 重排序模型休眠 API 地址 |
| `gpu_scheduler_timeout` | `GPU_SCHEDULER_TIMEOUT` | `30s` | sleep/wake HTTP 请求超时 |

#### MySQL 后端

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `mysql_dsn` | `MYSQL_DSN` | — | MySQL DSN，如 `user:pass@tcp(host:3306)/db?parseTime=true`。设置后启用 MySQL 后端 |
| `mysql_user` | `MYSQL_USER` | `root` | 用户名（DSN 未设置时使用） |
| `mysql_password` | `MYSQL_PASSWORD` | — | 密码 |
| `mysql_host` | `MYSQL_HOST` | `127.0.0.1` | 主机地址 |
| `mysql_port` | `MYSQL_PORT` | `3306` | 端口 |
| `mysql_database` | `MYSQL_DATABASE` | `knowledge_rag` | 数据库名 |
| `mysql_socket_path` | `MYSQL_SOCKET_PATH` | — | Unix Socket 路径（设置后优先于 host:port） |

#### Redis 缓存

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `redis_enabled` | `REDIS_ENABLED` | `false` | 设为 `true` 启用 Redis |
| `redis_addr` | `REDIS_ADDR` | `127.0.0.1:6379` | Redis 服务器地址 |
| `redis_password` | `REDIS_PASSWORD` | — | Redis 密码（可选） |
| `redis_db` | `REDIS_DB` | `0` | Redis 数据库编号 |
| `redis_prefix` | `REDIS_PREFIX` | `kmcp:` | 键命名空间前缀 |
| `redis_pool_size` | `REDIS_POOL_SIZE` | `10` | 连接池大小 |

#### 缓存 TTL

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `cache_query_ttl` | `CACHE_QUERY_TTL` | `300` | 查询结果缓存 TTL（秒） |
| `cache_chunk_ttl` | `CACHE_CHUNK_TTL` | `0` | 分块文本缓存 TTL（0=永不过期） |
| `cache_meta_ttl` | `CACHE_META_TTL` | `0` | 文档元数据缓存 TTL |
| `cache_index_ttl` | `CACHE_INDEX_TTL` | `0` | 分块索引缓存 TTL |
| `cache_kblist_ttl` | `CACHE_KBLIST_TTL` | `60` | KB 列表缓存 TTL |

---

## MCP 客户端集成

### Reasonix / Claude Desktop / Cline（推荐）

在项目根目录创建 `.mcp.json`：

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

MCP 客户端会自动启动和管理进程生命周期。如需传递环境变量：

```json
{
  "mcpServers": {
    "knowledge-mcp": {
      "command": "/path/to/knowledge-mcp",
      "args": ["stdio"],
      "env": {
        "KNOWLEDGE_MCP_DATA_DIR": "/home/user/knowledge_base",
        "EMBED_API_ENDPOINT": "http://localhost:11434/v1/embeddings",
        "EMBED_MODEL": "bge-m3"
      }
    }
  }
}
```

### HTTP SSE 模式

如果你的 MCP 客户端不支持 stdio（或需要远程访问），使用 `serve` 模式：

```bash
./knowledge-mcp serve
```

然后在 MCP 客户端配置 HTTP SSE 连接：`http://host:8086/sse`

---

## Web 管理界面

管理页面**内嵌**在服务中，`serve` 模式下自动启动。打开 `http://localhost:8085` 即可使用。

### 主要功能

| 功能 | 路径 | 说明 |
|------|------|------|
| 知识库管理 | `/` | 创建/删除知识库，查看 KB 列表和描述 |
| 文档上传 | `/kb/{name}` | 拖拽或选择文件上传，支持批量；上传时可选打标签 |
| 文档列表 | `/kb/{name}` | 浏览文档列表、搜索、按标签/类型/时间过滤 |
| 文档详情 | `/kb/{name}/{slug}` | 查看元数据、分块列表、原始文本 |
| 搜索控制台 | `/kb/{name}/search` | 测试检索效果，切换 BM25/混合模式 |
| 系统配置 | `/config` | 运行时查看和修改分块参数、搜索参数、工具描述 |
| 批量删除 | `/kb/{name}` | 勾选多个文档批量删除（支持软删除墓碑模式） |

### 标签管理

上传时可以给文档打标签（逗号分隔），之后可按标签过滤搜索：

```bash
# MCP 工具上传时打标签
knowledge_upload filePath="/path/to/paper.pdf" tags="CFD,ship,resistance" kbName="ship-hydrodynamics"
```

在 Web 界面中也可以对已有文档添加或修改标签。

### 搜索控制台

Web 界面的搜索控制台可以实时测试检索效果：

- 输入查询词，选择搜索模式（`bm25` / `hybrid`）
- 可选择文件类型、章节、标签、时间范围过滤
- 启用 `coarse` 模式进行粗到细两阶段检索
- 结果展示 BM25 分数、向量相似度、RRF 融合分数和最终排序分数

---

## MCP 工具

### `knowledge_research` — 语义研究检索

跨文档语义研究检索。系统自动处理：知识库路由、查询分析（中英文/领域术语扩展）、检索策略选择（BM25 / 向量 / 混合）、重排序和证据置信度评分。

**简单事实查询**：直接传入用户的原始问题。
**复杂研究任务**：可拆解为多个聚焦的子查询并行检索。

| 参数 | 必填 | 默认 | 说明 |
|------|------|------|------|
| `question` | **是** | — | 用户的原始自然语言问题。直接传入原文——内部查询分析器会自动处理中英文扩展和领域术语 |
| `kbName` | 否 | — | 知识库名称。留空时系统自动路由到最佳的 1-3 个 KB |
| `limit` | 否 | `8` | 最大结果数（上限 20） |
| `sourceType` | 否 | — | 按文件类型过滤：`pdf`、`md`、`txt` 等 |
| `section` | 否 | — | 按章节标题过滤（子串匹配），如 `"Introduction"` |
| `tags` | 否 | — | 逗号分隔的标签。仅返回匹配至少一个标签的文档 |
| `addedAfter` | 否 | — | ISO 8601 日期。仅返回此时间之后添加的文档，如 `"2025-01-01T00:00:00Z"` |
| `addedBefore` | 否 | — | ISO 8601 日期。仅返回此时间之前添加的文档 |
| `coarse` | 否 | `false` | 启用粗到细两阶段搜索：先对章节打分，再仅在 top-3 章节内精细化搜索 |

**已废弃参数**（仅为兼容旧版保留，新调用不要使用）：
- `search_keywords` → 请用 `question` 替代
- `mode` → 系统自动选择最佳策略

### `knowledge_read` — 读取文档内容

读取指定分块或其完整父章节。

| 参数 | 必填 | 默认 | 说明 |
|------|------|------|------|
| `docSlug` | **是** | — | 文档标识符（来自搜索/列表结果） |
| `chunkID` | **是** | — | 分块标识符，如 `"005"` 或内容寻址 ID（如 `"C3f2a8b1c0d1"`） |
| `kbName` | 否 | — | 知识库名称。不传则遍历所有 KB 查找文档 |
| `context` | 否 | `0` | 包含前后相邻分块数（上限 5）。设为 2 即读取 `[003, 004, 005, 006, 007]` |
| `level` | 否 | `chunk` | `chunk`（默认）或 `section`——读取完整父章节 |

**使用示例**：

```
# 读取单个分块
knowledge_read docSlug="paper-2025-abc" chunkID="005" kbName="my-kb"

# 读取分块及其前后各 2 个相邻块
knowledge_read docSlug="paper-2025-abc" chunkID="005" context=2

# 读取该分块所属的完整章节（论文中的整节）
knowledge_read docSlug="paper-2025-abc" chunkID="005" level="section"
```

### `knowledge_list_kbs` — 列出知识库

列出所有知识库及其描述。无参数。

返回示例：
```json
{
  "count": 3,
  "knowledge_bases": [
    {"name": "ship-hydrodynamics", "description": "船舶流体力学论文"},
    {"name": "machine-learning", "description": "机器学习基础文献"}
  ]
}
```

### `knowledge_list` — 列出文档

列出知识库中的文档。

| 参数 | 必填 | 默认 | 说明 |
|------|------|------|------|
| `kbName` | 否 | — | 知识库名称。设置后仅列出该 KB 的文档；不传则列出所有 KB |

### `knowledge_upload` — 上传文档

上传单个文档或批量导入整个目录。

| 参数 | 必填 | 默认 | 说明 |
|------|------|------|------|
| `filePath` | 条件 | — | 单个文档文件的绝对路径。与 `directory` 互斥 |
| `directory` | 条件 | — | 批量上传的目录绝对路径。与 `filePath` 互斥 |
| `recursive` | 否 | `false` | 设为 `true` 时递归遍历子目录（批量上传时有效） |
| `tags` | 否 | — | 逗号分隔的标签，赋予上传的文档 |
| `kbName` | 条件 | — | 知识库名称。未配置默认 KB 时必填 |

**使用示例**：

```
# 上传单个文件
knowledge_upload filePath="/data/papers/CFD_paper.pdf" tags="CFD,turbulence" kbName="ship-hydrodynamics"

# 批量导入目录（递归）
knowledge_upload directory="/data/papers/2025/" recursive=true kbName="ship-hydrodynamics"
```

### `knowledge_remove` — 删除文档

按 slug 从知识库中删除文档。

| 参数 | 必填 | 默认 | 说明 |
|------|------|------|------|
| `docSlug` | **是** | — | 文档 slug（来自列表/搜索结果） |
| `kbName` | 否 | — | 知识库名称。不传则从所有 KB 中删除 |

### 搜索结果格式

`knowledge_research` 和 `knowledge_read` 返回结构化证据，包含完整的来源追踪信息：

```json
{
  "score": 12.45,
  "document": {
    "id": "1706-03762v7-20250101-120000",
    "title": "Attention Is All You Need",
    "original_name": "1706.03762v7.pdf",
    "type": "pdf"
  },
  "location": {
    "chunk_id": "003",
    "section": "## Attention Mechanism",
    "offset": 4521,
    "page_start": 5,
    "page_end": 6
  },
  "content": {
    "snippet": "An attention function can be described as mapping...",
    "section_role": "body"
  },
  "citation_id": "1706-03762v7-20250101-120000_003",
  "source_confidence": 0.85,
  "answer_relevance": 0.72,
  "completeness": 0.60
}
```

- **`citation_id`**：`{slug}_{chunkID}`，整个知识库中稳定唯一，可用于前端引用标注和审计追踪
- **`page_start` / `page_end`**：PDF 页码（1-based），未注入时省略

---

## 检索管线

```
用户查询 (question)
  │
  ├─ 查询分析：中英文检测、领域词典同义词扩展、LLM 查询改写（可选）
  │
  ├─ KB 路由：四维度加权评分，选出 Top-1~3 知识库
  │
  ├─ 分词：CJK 双字感知 tokenizer（中文按 bigram、英文按空格）
  │
  ├─ 阶段一：快速召回 ──────────────────────────
  │   ├─ 倒排索引快速路径（G7）
  │   │    候选收集 → BM25 打分
  │   │
  │   ├─ 向量 ANN 召回（HNSW，独立并行）
  │   │    查询向量化 → HNSW 搜索 → 与 BM25 候选合并
  │   │
  │   └─ RRF 融合（k=60，自适应查询类型权重）
  │        论文类查询：BM25 权重↑ / 概念查询：向量权重↑
  │        → top-N 候选（默认 N=100）
  │
  ├─ 阶段二：精准重排 [可选，需配置 reranker]
  │    交叉编码器逐对 (query, chunk) 深度语义打分
  │    → 按新分数重排序
  │
  └─ 后处理
       → 截断到 limit
       → 片段摘录生成
       → 去重合并
       → 证据质量评分
       → 返回结果
```

**优雅降级行为**：

| 场景 | 行为 |
|------|------|
| 未配置 embedding 端点 | 退化为纯 BM25 关键词检索 |
| 未配置 reranker 端点 | 跳过阶段二，RRF/BM25 分数直接返回 |
| Reranker 调用超时/失败 | 自动回退到向量余弦相似度排序 |
| 两者都未配置 | 纯 BM25，零外部依赖 |

**RRF 自适应权重**：系统根据查询特征自动调节 BM25 (α) 和向量 (1-α) 权重比例：
- 包含缩写、代码、符号的查询 → BM25 权重较高（关键词精确匹配更重要）
- 自然语言问题、概念性查询 → 向量权重较高（语义理解更重要）

---

## 存储后端

knowledge-mcp 支持两种存储后端，通过是否配置 MySQL 相关参数自动切换。

### 文件系统后端（默认）

数据存储在 `data_dir` 指定的目录中，按 KB → 文档 slug 两级目录组织：

```
<data-dir>/
├── <kb-name>/
│   ├── INDEX.md             # KB 级文档索引（Markdown 格式）
│   ├── INVERTED.gob         # 全局倒排索引（加速候选查找）
│   ├── VECTOR.gob           # HNSW 向量索引（ANN 近似最近邻搜索）
│   ├── kb.json              # KB 元数据（名称、描述）
│   ├── LIST_SNAPSHOT.json   # 文档列表快照缓存
│   ├── .searchlog.jsonl     # 搜索日志
│   ├── .tombstones.gob      # 软删除墓碑记录
│   └── <document-slug>/
│       ├── meta.json        # 文档元数据（文件名、类型、标签、标题、作者、摘要）
│       ├── raw_text.md      # 解析后的原始纯文本
│       ├── CHUNKS.toml      # 分块索引（每块的词项、向量、章节、偏移、页码）
│       ├── MANIFEST.json    # 分块清单（版本化、内容寻址 ID）
│       ├── source.<ext>     # 原始文件副本
│       └── chunks/
│           ├── 000.md       # 细粒度分块
│           ├── 001.md
│           └── sections/
│               ├── S00.md   # 粗粒度章节级分块
│               └── S01.md
```

### MySQL 后端

配置 `mysql_dsn` 或 `mysql_host` 后自动启用。所有数据存储在关系表中，同时 `VECTOR.gob` 和 `.tombstones.gob` 等仍存储在 `data_dir` 下（因为这些是二进制索引，不适合放数据库）。

首次启动时自动建表（约 8 张表），无需手动初始化。

**优势**：
- 数据持久性更好，支持备份恢复
- 适合与其他系统共享数据
- 查询统计、审计等更方便

**注意事项**：
- 切换后端后已有数据需要重新导入
- MySQL 用户需要对 `knowledge_rag` 库有完整 CRUD 权限
- 建议设置 `innodb_buffer_pool_size` 足够大以缓存索引数据

---

## 缓存机制

knowledge-mcp 有两层缓存：

### 内置缓存（默认开启，无需 Redis）

内置于 Store 层，通过内存 map 缓存：

| 缓存类型 | 缓存内容 | 默认 TTL | 缓存键前缀 |
|----------|---------|---------|-----------|
| chunk | 分块文本内容 | 0（永不过期） | `kmcp:kb:{kb}:chunk:{slug}:{id}` |
| meta | 文档元数据 | 0（永不过期） | `kmcp:kb:{kb}:meta:{slug}` |
| index | CHUNKS.toml 索引 | 0（永不过期） | `kmcp:kb:{kb}:idx:{slug}` |
| kblist | KB 列表 | 60s | `kmcp:kblist` |

缓存日志位于 `[cache]` 模块，`info` 级别即可看到 HIT/MISS/SET：
```
[INFO] [cache] chunk HIT  key=kmcp:kb:ship-hydrodynamics:chunk:doc-xxx:033 slug="doc-xxx" chunk=033 kb="ship-hydrodynamics" size=1847
[INFO] [cache] chunk MISS key=kmcp:kb:ship-hydrodynamics:chunk:doc-xxx:081 slug="doc-xxx" chunk=081 kb="ship-hydrodynamics"
[INFO] [cache] chunk SET  key=kmcp:kb:ship-hydrodynamics:chunk:doc-xxx:081 slug="doc-xxx" chunk=081 kb="ship-hydrodynamics" ttl=0s size=2103
```

文档重新上传或删除时会自动失效对应的缓存条目。

### Redis 缓存（可选）

需安装 Redis 并设置 `redis_enabled = true`。Redis 缓存仅缓存**查询结果**（HybridSearch 的返回），而非分块或元数据。

缓存键基于：`kmcp:query:{kb}:{query_hash}`，其中 `query_hash` 由查询文本 + 搜索模式 + limit + filter 综合计算。

```toml
redis_enabled = true
redis_addr = "127.0.0.1:6379"
redis_prefix = "kmcp:"
cache_query_ttl = 300
```

---

## 模型部署

详细模型部署指南见 [docs/deployment-models_zh.md](docs/deployment-models_zh.md)。以下为快速摘要：

### Embedding 模型

| 方案 | 模型 | 维度 | 部署命令 |
|------|------|------|---------|
| Ollama | `bge-m3` | 1024 | `ollama pull bge-m3` |
| Ollama | `qwen3-embedding:q4_k_m` | 2560 | `ollama pull qwen3-embedding:q4_k_m` |
| Ollama | `nomic-embed-text` | 768 | `ollama pull nomic-embed-text` |

Ollama 默认监听 `http://localhost:11434`，支持两种 API 格式：

```bash
# OpenAI 兼容格式
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings

# Ollama 原生格式
EMBED_API_ENDPOINT=http://localhost:11434/api/embed
```

### Reranker 模型

| 方案 | 模型 | 参数 | 部署命令 |
|------|------|------|---------|
| Infinity | `gte-multilingual-reranker-base` | 306M | `infinity_emb v2 --model-id Alibaba-NLP/gte-multilingual-reranker-base --port 7997` |
| Infinity | `bge-reranker-v2-m3` | 0.6B | `infinity_emb v2 --model-id BAAI/bge-reranker-v2-m3 --port 7997` |
| Ollama + wrapper | `Qwen3-Reranker-0.6B` (GGUF) | 0.6B | 需自行封装 `/rerank` 端点 |

### 硬件要求

| 模型 | CPU 内存 | GPU 显存 | CPU 延迟（100 候选） |
|------|---------|---------|---------------------|
| `bge-m3` (embedding) | ~2 GB | ~3 GB | 0.5-1s |
| `gte-multilingual-reranker-base` | ~1.5 GB | ~2 GB | 1-3s |
| `bge-reranker-v2-m3` | ~2.5 GB | ~4 GB | 2-5s |

---

## 领域词典

领域词典用于查询时的同义词/相关术语扩展，放在 `dictionaries/` 目录下，YAML 格式。

**文件**: `dictionaries/ship_motion.yaml`

```yaml
# 船舶运动领域词典
# 格式：keyword: [synonym1, synonym2, ...]

resistance:
  - drag
  - 阻力
  - 摩擦阻力
  - 兴波阻力

wave_making:
  - 兴波
  - wave-making resistance
  - wave pattern

CFD:
  - computational fluid dynamics
  - 计算流体力学
  - numerical simulation
  - 数值模拟

propeller:
  - 螺旋桨
  - propulsion
  - 推进器

maneuvering:
  - 操纵性
  - maneuverability
  - ship maneuvering
```

查询如 `"船舶阻力 CFD 分析"` 会自动扩展出 `resistance`、`drag`、`computational fluid dynamics` 等相关术语参与 BM25 检索和向量召回。

词典加载时机：服务启动时自动扫描 `dictionaries/` 目录。修改后需重启服务生效。

---

## LLM 查询改写

> 🆕 v4：利用 DeepSeek 大模型进行语义级查询改写，提升召回率。

LLM 查询改写在**领域词典同义词扩展**的基础上进一步利用大语言模型的语义理解能力，将用户的原始查询改写为 2-4 个语义相同但用词不同的变体，帮助 BM25 检索匹配更多相关文档。

### 工作流程

```
用户查询 → LLMQueryRewriter
             ├── DeepSeek LLM（生成改写变体）
             │      成功：原始查询 + 2~4 个改写变体 → 合并送入检索引擎
             │      失败：自动降级到 SynonymRewriter（同义词 + 词典扩展）
             └── 即使 LLM 服务不可用，系统仍正常运行
```

### 配置方法

**方式一：TOML 配置文件** (`knowledge-mcp.toml`)

```toml
# DeepSeek LLM 查询改写（可选）
deepseek_api_key = "sk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
# deepseek_endpoint = "https://api.deepseek.com/chat/completions"  # 可选，默认值
# deepseek_model   = "deepseek-flash"                               # 可选，默认值
```

**方式二：环境变量**

```bash
export DEEPSEEK_API_KEY="sk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
# export DEEPSEEK_ENDPOINT="https://api.deepseek.com/chat/completions"
# export DEEPSEEK_MODEL="deepseek-flash"
```

### 参数说明

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `deepseek_api_key` / `DEEPSEEK_API_KEY` | DeepSeek API 密钥。**不设置则不启用 LLM 改写** | (空) |
| `deepseek_endpoint` / `DEEPSEEK_ENDPOINT` | DeepSeek API 端点 URL | `https://api.deepseek.com/chat/completions` |
| `deepseek_model` / `DEEPSEEK_MODEL` | 模型名称 | `deepseek-flash` |

### 降级策略

LLM 查询改写采用**三层防线**确保服务稳定：

1. **LLM 层** — DeepSeek API 调用成功则使用 LLM 生成的改写变体
2. **Fallback 层** — LLM 调用失败（网络错误/超时/空响应）时自动回退到 `SynonymRewriter`，使用内置同义词表 + 词典文件进行查询扩展
3. **最底层** — 即使同义词表为空，也会返回原始查询，确保搜索永不中断

### 安全保护

- API Key **仅通过 TOML 配置文件或环境变量注入**，代码中不硬编码
- 错误日志中的 API 响应会经过 `sanitiseForLog()` 脱敏处理，任何 `sk-*` 格式的 Key 都会被替换为 `sk-***`
- API Key 不会出现在任何日志、错误消息或管理界面中

### 日志示例

```
[2026-01-01 10:00:00] [INFO] [startup] query rewriter: LLM (deepseek model=deepseek-flash) + synonym fallback (17 terms)
[2026-01-01 10:00:05] [DEBUG] [deepseek] deepseek: request model=deepseek-flash promptLen=42 bodyLen=237
[2026-01-01 10:00:06] [DEBUG] [deepseek] deepseek: OK model=deepseek-flash elapsed=856ms promptLen=42 responseLen=128
[2026-01-01 10:01:00] [WARN] [deepseek] deepseek: non-200 model=deepseek-flash status=401 elapsed=123ms body={"error":"Invalid API key: sk-***"}
```

---

## 日志与调试

### 日志查看

日志输出到 `log_file` 指定的文件，格式：

```
[2026-08-01 14:23:05] [INFO] [search] HybridSearch: query="ship resistance CFD" limit=8
[2026-08-01 14:23:05] [INFO] [cache] chunk HIT  key=kmcp:kb:ship:chunk:doc-01:005 slug="doc-01" chunk=005 size=1847
[2026-08-01 14:23:05] [INFO] [search] hybrid: vector recall returned 150 hits (beam=200)
[2026-08-01 14:23:06] [INFO] [search] hybrid: reranking done results=8
```

```bash
# 实时跟踪日志
tail -f ~/.knowledge-mcp/knowledge-mcp.log

# 只看搜索相关
tail -f ~/.knowledge-mcp/knowledge-mcp.log | grep "\[search\]"

# 只看缓存命中
tail -f ~/.knowledge-mcp/knowledge-mcp.log | grep "HIT\|MISS"

# 只看错误
tail -f ~/.knowledge-mcp/knowledge-mcp.log | grep "\[ERROR\]\|\[WARN\]"
```

### 日志级别

| 级别 | 用途 |
|------|------|
| `info`（推荐） | 生产环境：搜索请求/结果统计、缓存命中、KB 路由、上传进度、错误和警告 |
| `debug` | 开发调试：分词详情、BM25 逐项打分、RRF 逐项融合、倒排索引查询、HNSW 搜索细节 |

设置 `log_level = "debug"` 后会输出大量详细日志，包含：
- 每个词项的 IDF 值
- 每个 candidate 的 BM25 分数
- RRF 融合时的 BM25 和向量排名
- 倒排索引候选召回数量

### 搜索日志

`.searchlog.jsonl` 记录每次搜索的元数据（Query、结果数、top scores、filter 等），可用于离线分析和评估。配合 `scripts/eval.go` 可计算 NDCG@5 / MRR / Recall@10。

---

## 运维部署

### Linux (systemd)

```bash
# 复制服务文件
sudo cp scripts/knowledge-mcp.service /etc/systemd/system/

# 创建环境变量配置
sudo mkdir -p /etc/knowledge-mcp
cat <<EOF | sudo tee /etc/knowledge-mcp/env
KNOWLEDGE_MCP_DATA_DIR=/var/lib/knowledge-mcp
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings
EMBED_MODEL=bge-m3
RERANK_API_ENDPOINT=http://localhost:7997/rerank
MYSQL_HOST=127.0.0.1
MYSQL_DATABASE=knowledge_rag
MYSQL_USER=knowledge
MYSQL_PASSWORD=your-password
LOG_LEVEL=info
EOF

# 启动服务
sudo systemctl daemon-reload
sudo systemctl enable --now knowledge-mcp

# 查看状态
sudo systemctl status knowledge-mcp

# 查看日志
sudo journalctl -u knowledge-mcp -f
```

### macOS (launchd)

```bash
cp scripts/com.knowledge-mcp.plist ~/Library/LaunchAgents/
# 编辑 plist 设置正确的二进制路径和环境变量
launchctl load ~/Library/LaunchAgents/com.knowledge-mcp.plist
```

### tmux / screen

```bash
tmux new-session -d -s kmcp './knowledge-mcp serve'
```

### nohup

```bash
nohup ./knowledge-mcp serve > /tmp/kmcp.log 2>&1 &
```

---

## 数据库表结构

MySQL 后端自动创建以下表（`knowledge_rag` 库）：

| 表名 | 主键 | 用途 |
|------|------|------|
| `knowledge_bases` | `name` | KB 元数据（名称、描述、INDEX.md 内容） |
| `documents` | `(kb_name, slug)` | 文档元数据（文件名、类型、标签、标题、作者、摘要、原始文本等） |
| `chunks` | `(kb_name, doc_slug, chunk_id)` | 细粒度分块文本内容 |
| `section_chunks` | `(kb_name, doc_slug, section_id)` | 粗粒度章节级分块文本 |
| `chunks_index` | `(kb_name, doc_slug)` | 每个文档的 CHUNKS.toml 搜索索引（JSON 字段） |
| `inverted_index` | `(kb_name, term, doc_slug, chunk_id)` | 全局倒排索引（term → doc+chunk → TF） |
| `manifests` | `(kb_name, slug)` | 版本化的分块清单（MANIFEST.json） |
| `task_records` | `(kb_name, slug)` | 上传任务状态（TASK.json） |
| `list_snapshots` | `kb_name` | 文档列表快照缓存 |

所有表使用 InnoDB 引擎，utf8mb4 字符集。没有设置外键约束（兼容非 InnoDB 引擎），删除文档时由应用层显式清理各表。

**常用查询示例**：

```sql
-- 查看 KB 列表
SELECT name, description FROM knowledge_bases;

-- 查看某 KB 的文档列表
SELECT slug, original_name, source_type, chunk_count, total_chars, added_at
FROM documents WHERE kb_name = 'ship-hydrodynamics';

-- 查看某文档的分块数
SELECT doc_slug, COUNT(*) AS chunk_count FROM chunks
WHERE kb_name = 'ship-hydrodynamics' GROUP BY doc_slug;

-- 查看倒排索引中的高频词
SELECT term, SUM(tf) AS total_tf FROM inverted_index
WHERE kb_name = 'ship-hydrodynamics' GROUP BY term ORDER BY total_tf DESC LIMIT 20;
```

---

## 维护工具

### 向量索引清理

当向量索引中出现孤立条目（chunks 表中已不存在的旧顺序 ID）时，使用清理工具：

```bash
# 先 dry-run，看有多少脏数据
go run ./cmd/cleanup-vector/ --dry-run

# 只检查特定 KB
go run ./cmd/cleanup-vector/ --kb ship-hydrodynamics --dry-run

# 确认无误后执行清理
go run ./cmd/cleanup-vector/

# 只清理特定 KB
go run ./cmd/cleanup-vector/ --kb ship-hydrodynamics
```

该工具会：
1. 遍历 VECTOR.gob 中的所有 HNSW 节点，对比 `chunks` 表
2. 移除指向不存在 chunk 的孤立向量条目
3. 修复 `chunks_index` 表中的孤立索引条目

### 重建向量索引

如果向量索引损坏或需要完全重建，最简单的办法是删除 VECTOR.gob 文件，下次搜索时会自动通过 `EnsureVectorIndex` 从 `chunks_index` 表中的向量数据重建：

```bash
# 停止服务
# 删除向量索引文件
rm ~/knowledge_base/<kb-name>/VECTOR.gob
# 重启服务
```

重建过程在第一次混合搜索请求时触发，对大量文档可能需要几分钟。

---

## 架构总览

```
main.go                  — CLI 入口点、子命令 (stdio / serve / setup / manage)、工具注册
internal/
  config/
    config.go            — TOML 配置加载、环境变量回退、默认值
  setup/
    setup.go             — 交互式配置向导
    i18n.go              — 配置向导国际化文本
  logging/
    logger.go            — 结构化文件日志 (DEBUG/INFO/WARN/ERROR，按模块)
  cache/
    cache.go             — Cache 接口 + NoopCache 空实现
    redis.go             — Redis 缓存实现
    keys.go              — 缓存键命名规范
  knowledge/
    store.go             — Store 核心结构体、数据目录管理、CHUNKS.toml I/O、KB CRUD
    storage.go           — StorageBackend 接口（存储后端抽象层）
    mysql_backend.go     — MySQL/MariaDB 存储后端
    search.go            — Search、HybridSearch、SearchVector、coarseToFine、rerankTop
    chunker.go           — ChunkText、ChunkTextHierarchical、语义合并、页码感知分块
    doc.go               — DocumentMeta、ChunkWithMeta、SearchFilter、SearchHit、EvidenceMeta、ChunksIndex
    embed.go             — Embedder 接口、OpenAIEmbedder（兼容 OpenAI + Ollama 原生）
    rerank.go            — InfinityReranker（兼容 Cohere/Infinity）、Reranker 接口
    vector_index.go      — HNSW 向量索引 (M=48, efConstruction=400) 用于 ANN
    gpu_scheduler.go     — GPU 调度器，嵌入/重排序模型休眠唤醒
    kb_router.go         — 多知识库智能路由（关键词+嵌入+描述+约束四维评分）
    rewrite.go           — QueryRewriter 接口、SynonymRewriter（内置+YAML词典）
    rewrite_llm.go       — LLMQueryRewriter（可选 LLM 查询扩展）
    dict_loader.go       — YAML 格式领域词典加载器
    manage.go            — Web 管理页面服务、KB CRUD、上传/删除/搜索处理器
    manage_enhanced.go   — 增强管理功能（搜索控制台、配置、工具描述）
    upload.go            — UploadDocument、UploadDirectory
    upload_task.go       — 持久化上传任务记录
    parser.go            — 文档解析调度 — 外部 HTTP API + tabula 回退
    inverted.go          — 全局倒排索引 (INVERTED.gob)，加速候选查找
    list.go              — ListPreview、ReadChunk、ReadChunkContext
    remove.go            — RemoveDocument
    tombstone.go         — 软删除墓碑管理器（TTL 过期清理）
    version.go           — 文档/索引版本跟踪
    store_incremental.go — 增量重新索引（UploadDocumentAtomic）
    manifest.go          — 分块清单跟踪（MANIFEST.json）
    reconcile.go         — 索引一致性校验与修复
    chunk_id.go          — 内容寻址分块 ID 生成（SHA256，幂等）
    config_api.go        — 运行时配置读写 API
    store_settings.go    — Store 运行时设置管理
    middleware.go         — HTTP 中间件（CORS、日志、异常恢复）
    tool_descs.go        — 默认 MCP 工具描述（可通过 Web UI 自定义）
    searchlog.go         — 搜索日志 (.searchlog.jsonl)
    meta_extract.go      — 论文元数据提取（标题、作者、摘要、章节角色）
  retrieval/
    bm25.go              — 分词器（CJK 双字感知）、BM25Score、MakeSnippet
scripts/
  eval.go                — 检索评估脚本 (NDCG@5、MRR、Recall@10)
cmd/
  cleanup-vector/        — 向量索引清理工具
dictionaries/            — 领域词典文件
docs/
  deployment-models.md   — 嵌入与重排序模型部署指南
  deployment-models_zh.md
  roadmap.md             — RAG 优化路线图
  roadmap_zh.md
```

---

## 常见问题

### Q: 上传 PDF 后搜索不到内容？

1. 检查上传是否成功：查看日志中的 `[upload]` 模块
2. 确认 PDF 是否可解析：查看 `meta.json` 中 `total_chars > 0`
3. 确认搜索参数：确保 `knowledge_research` 的 `kbName` 正确
4. 查看搜索日志：`[search] hybrid: vector recall returned X hits`

### Q: "chunk xxx not found in document xxx" 错误？

这表示搜索索引引用了不存在的分块。原因通常是向量索引中残留了旧格式（顺序 ID）的条目。

解决：运行清理工具 `go run ./cmd/cleanup-vector/`，或删除 VECTOR.gob 让它重建。

### Q: 如何切换文件后端到 MySQL 后端？

MySQL 后端和文件后端的存储格式不同，需要重新导入所有文档：

1. 备份 `data_dir` 下的源文件
2. 配置 MySQL 连接参数并重启服务
3. 通过 Web 界面或 `knowledge_upload` 重新导入文档

### Q: 向量搜索返回空结果？

1. 确认 `embed_endpoint` 配置正确且服务可达：`curl http://localhost:11434/api/embed -d '{"model":"bge-m3","input":"test"}'`
2. 检查 `embed_dim` 是否匹配（如未设置则自动检测）
3. 检查向量索引是否为空：搜索日志中 `vector recall returned 0 hits`

### Q: 如何修改分块参数？

在 Web 管理界面 `/config` 页面修改，或在 TOML 中配置后重启。注意：修改分块参数不影响已有文档，只对新上传的文档生效。

### Q: 软删除的文档如何恢复？

软删除（Tombstone）的文档在 TTL 过期前可以恢复：从 `.tombstones.gob` 中移除对应记录即可。TTL 过期后物理文件已被清理，无法恢复。

### Q: MySQL 后端如何备份？

```bash
mysqldump -u knowledge -p knowledge_rag > backup.sql
```

恢复：`mysql -u knowledge -p knowledge_rag < backup.sql`，同时恢复 `data_dir/<kb-name>/VECTOR.gob` 文件。

### Q: 如何评估检索效果？

1. 开启搜索日志：确保 `log_level = "info"`
2. 积累标注数据：记录 query → relevant_chunk_ids 的映射
3. 运行评估：`go run scripts/eval.go --searchlog .searchlog.jsonl --judgments judgments.json`

详见 [docs/roadmap_zh.md](docs/roadmap_zh.md) 中的评估体系章节。
