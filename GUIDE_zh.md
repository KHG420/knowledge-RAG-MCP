# knowledge-mcp — 使用指南

[English](GUIDE.md) | [← 返回项目介绍](README_zh.md)

> 详细配置、操作与故障排查指南。

---

## 目录

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
- [常见问题](#常见问题)

---

## 运行模式

knowledge-mcp 支持三种运行模式：

### stdio 模式（推荐 MCP 客户端使用）

通过 stdin/stdout 走 MCP 协议通信。无 HTTP 服务器，无 Web 管理页面。适合 Reasonix、Claude Desktop 等基于 stdio 的 MCP 客户端：

```bash
./knowledge-mcp stdio
```

> **注意**：stdio 模式下所有配置只能通过环境变量或 TOML 文件传入。

### HTTP 模式（默认）

长期运行的 MCP 服务器 + Web 管理界面同时启动。同一端口上同时支持
**Streamable HTTP**（`/mcp`，新一代）和 **SSE**（`/sse`，遗留）两种传输：

```bash
./knowledge-mcp serve
```

- MCP Streamable HTTP 端点：`http://localhost:8086/mcp`
- MCP SSE 端点（遗留）：`http://localhost:8086/sse`
- Web 管理界面：`http://localhost:8085`

### 仅 MCP HTTP

HTTP 模式不含管理界面（适用于已有独立管理后台的场景）：

```bash
./knowledge-mcp serve --mcp
```

### 管理命令

```bash
./knowledge-mcp manage    # 仅启动 Web 管理界面（不含 MCP server），端口 8085
./knowledge-mcp version   # 打印版本信息
./knowledge-mcp dict      # 领域词典管理（mine 挖掘同义词 / gen 生成词典）
```

> **注意**：所有配置的填写和更改请在 Web 管理界面 `/config` 页面完成，支持热更新。

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
embed_endpoint = "http://127.0.0.1:11434/v1/embeddings"
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

# MySQL 后端
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
| `data_dir` | `KNOWLEDGE_MCP_DATA_DIR` | `~/knowledge_base/` | 知识库存储目录 |
| `default_kb` | `KNOWLEDGE_MCP_DEFAULT_KB` | — | 默认知识库名称 |

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
| `rerank_batch_size` | `RERANK_BATCH_SIZE` | `20` | 重排序每批次文档数 |

#### LLM 查询改写（DeepSeek）

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `deepseek_api_key` | `DEEPSEEK_API_KEY` | — | DeepSeek API 密钥。**不设置则不启用 LLM 改写** |
| `deepseek_endpoint` | `DEEPSEEK_ENDPOINT` | `https://api.deepseek.com/chat/completions` | DeepSeek API 端点 |
| `deepseek_model` | `DEEPSEEK_MODEL` | `deepseek-v4-flash` | 模型名称（推荐 `deepseek-v4-flash`） |

#### 服务端口

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `manage_port` | `MANAGE_PORT` | `8085` | Web 管理页面端口 |
| `serve_port` | `KNOWLEDGE_MCP_SERVE_PORT` | `8086` | MCP HTTP 服务器监听端口（SSE + Streamable HTTP） |
| `serve_base_url` | `KNOWLEDGE_MCP_SERVE_BASE_URL` | — | MCP 服务器基础 URL（反向代理场景） |

#### 日志

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `log_file` | `KNOWLEDGE_MCP_LOG_FILE` | `<exe-dir>/knowledge-mcp.log` | 日志文件路径 |
| `log_level` | `KNOWLEDGE_MCP_LOG_LEVEL` | `info` | 日志级别：`debug` / `info` |

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
| `chunk_min_chars` | `CHUNK_MIN_CHARS` | `200` | 短段落合并阈值 |
| `chunk_max_chars` | `CHUNK_MAX_CHARS` | `2000` | 长段落拆分阈值 |
| `chunk_overlap_chars` | `CHUNK_OVERLAP_CHARS` | `200` | 块间重叠字符数 |
| `chunk_semantic_threshold` | `CHUNK_SEMANTIC_THRESHOLD` | `0.75` | 语义相邻块合并的余弦相似度阈值 |

#### 文档解析

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `doc_parser_endpoint` | `DOC_PARSER_ENDPOINT` | — | 外部文档解析 HTTP API 地址（如 MinerU） |
| `doc_parser_api_key` | `DOC_PARSER_API_KEY` | — | Bearer token（可选） |
| `doc_parser_timeout` | `DOC_PARSER_TIMEOUT` | `600s` | HTTP 请求超时 |
| `mineru_enabled` | `MINERU_ENABLED` | `true` | 是否启用外部解析器 |

#### GPU 调度器

GPU 调度器协调嵌入和重排序模型共享单 GPU。启用后，在上传文档和搜索时自动切换模型。

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `gpu_scheduler_enabled` | `GPU_SCHEDULER_ENABLED` | `false` | 设为 `true` 启用 |
| `gpu_scheduler_embedding_sleep_url` | `GPU_SCHEDULER_EMBEDDING_SLEEP_URL` | — | 嵌入模型休眠 API 地址 |
| `gpu_scheduler_embedding_wake_url` | `GPU_SCHEDULER_EMBEDDING_WAKE_URL` | — | 嵌入模型唤醒 API 地址 |
| `gpu_scheduler_reranker_sleep_url` | `GPU_SCHEDULER_RERANKER_SLEEP_URL` | — | 重排序模型休眠 API 地址 |
| `gpu_scheduler_reranker_wake_url` | `GPU_SCHEDULER_RERANKER_WAKE_URL` | — | 重排序模型唤醒 API 地址 |
| `gpu_scheduler_timeout` | `GPU_SCHEDULER_TIMEOUT` | `30s` | sleep/wake HTTP 请求超时 |
| `gpu_scheduler_wake_delay` | `GPU_SCHEDULER_WAKE_DELAY` | `3s` | 唤醒后等待模型加载到 GPU 的延迟 |

#### MySQL 后端

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `mysql_dsn` | `MYSQL_DSN` | — | MySQL DSN。设置后启用 MySQL 后端 |
| `mysql_user` | `MYSQL_USER` | `root` | 用户名 |
| `mysql_password` | `MYSQL_PASSWORD` | — | 密码 |
| `mysql_host` | `MYSQL_HOST` | `127.0.0.1` | 主机地址 |
| `mysql_port` | `MYSQL_PORT` | `3306` | 端口 |
| `mysql_database` | `MYSQL_DATABASE` | `knowledge_rag` | 数据库名 |
| `mysql_socket_path` | `MYSQL_SOCKET_PATH` | — | Unix socket 路径 |

#### Redis 缓存

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `redis_enabled` | `REDIS_ENABLED` | `false` | 设为 `true` 启用 Redis |
| `redis_addr` | `REDIS_ADDR` | `127.0.0.1:6379` | Redis 地址 |
| `redis_password` | `REDIS_PASSWORD` | — | Redis 密码（可选） |
| `redis_db` | `REDIS_DB` | `0` | Redis 库编号 |
| `redis_pool_size` | `REDIS_POOL_SIZE` | `10` | 连接池大小 |

#### 缓存 TTL

| 配置键 | 环境变量 | 默认值 | 说明 |
|--------|---------|--------|------|
| `cache_query_ttl` | `CACHE_QUERY_TTL` | `300` | 查询结果缓存 TTL（秒，Redis） |
| `cache_chunk_ttl` | `CACHE_CHUNK_TTL` | `0` | Chunk 文本缓存 TTL（秒，内置） |
| `cache_meta_ttl` | `CACHE_META_TTL` | `0` | 文档元数据缓存 TTL（秒，内置） |
| `cache_index_ttl` | `CACHE_INDEX_TTL` | `0` | 分块索引缓存 TTL（秒，内置） |
| `cache_kblist_ttl` | `CACHE_KBLIST_TTL` | `60` | KB 列表缓存 TTL（秒，内置） |
| `upload_max_size_mb` | `UPLOAD_MAX_SIZE_MB` | `100` | 上传文件大小上限（MB） |

---

## MCP 客户端集成

### stdio 模式（推荐）

适用于 **Reasonix**、**Claude Desktop**、**Cline** 等支持 stdio 的 MCP 客户端。在项目根目录放置 `.mcp.json`：

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

无需 launchd/systemd 设置，MCP 客户端自动管理进程生命周期。

### HTTP 模式

适用于通过 HTTP 连接或需要共享的长期运行服务器的场景，使用 **Streamable HTTP** 端点：

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

遗留 SSE 客户端可使用 `http://localhost:8086/sse`。

---

## Web 管理界面

Web 管理界面**内置于服务中**，在 `serve` 模式下（非 `serve --mcp` 模式）自动随 MCP 服务器一起启动。
浏览器中打开 [http://localhost:8085](http://localhost:8085)（默认端口）即可上传、浏览、搜索和删除文档，管理多个知识库。

通过 `MANAGE_PORT` 环境变量覆盖端口：

```bash
MANAGE_PORT=8080 knowledge-mcp serve
```

管理界面与 MCP 服务器共享同一数据目录，通过 Web 界面上传的文档可通过 MCP 工具 `knowledge_research` 立即搜索。

### 界面功能

| 功能 | 路径 | 说明 |
|------|------|------|
| KB 管理 | `/` | 创建/删除 KB，查看 KB 列表和描述 |
| 文档上传 | `/kb/{name}` | 拖拽或选择文件，批量上传，可选标签 |
| 文档列表 | `/kb/{name}` | 浏览、搜索、按标签/类型/时间过滤 |
| 文档详情 | `/kb/{name}/{slug}` | 查看元数据、分块列表、原始文本 |
| 搜索控制台 | `/kb/{name}/search` | 测试检索，切换 BM25/混合模式，查看分数 |
| 系统配置 | `/config` | 查看和运行时修改分块/搜索参数、工具描述 |
| 批量删除 | `/kb/{name}` | 多选文档批量删除（支持墓碑软删除） |

---

## MCP 工具

### `knowledge_research` — 语义搜索

| 参数 | 必填 | 说明 |
|------|------|------|
| `question` | **是** | 用户的原始自然语言问题 |
| `limit` | 否（默认 5） | 返回结果数（1–20） |
| `kbName` | 否 | 指定 KB 名称。不填则搜索所有 KB |
| `searchMode` | 否 | 覆盖默认搜索模式（`bm25` / `hybrid`） |

**返回结果中每个 `source_uri` 格式**：`<kb-name>/<slug>/<chunk-id>`

Agent 应将 `source_uri` 和 `docSlug`/`chunkID` 原样传递给后续的 `knowledge_read` 调用。

### `knowledge_read` — 读取文档分块

| 参数 | 必填 | 说明 |
|------|------|------|
| `docSlug` | **是** | 文档 slug 标识符 |
| `chunkID` | 否 | 指定分块 ID。不填则返回文档概览 |
| `context` | 否（默认 0） | 返回该分块前后各 N 个分块，提供更完整的上下文 |
| `sectionID` | 否 | 读取指定章节分块 |
| `kbName` | 否 | KB 名称 |

### `knowledge_list` — 列出文档

| 参数 | 必填 | 说明 |
|------|------|------|
| `limit` | 否（默认 20） | 返回文档数上限 |
| `kbName` | 否 | KB 名称 |
| `tag` | 否 | 按标签过滤 |

### `knowledge_list_kbs` — 列出知识库

无需参数。返回所有 KB 及其描述信息。

### `knowledge_upload` — 上传文档

| 参数 | 必填 | 说明 |
|------|------|------|
| `filePath` | 条件 | 单个文件的绝对路径。与 `directory` 互斥 |
| `directory` | 条件 | 目录路径，用于批量上传。与 `filePath` 互斥 |
| `kbName` | 否 | 目标 KB 名称 |
| `tags` | 否 | 逗号分隔的标签 |

### `knowledge_remove` — 删除文档

| 参数 | 必填 | 说明 |
|------|------|------|
| `docSlug` | **是** | 要删除的文档 slug |
| `kbName` | 否 | KB 名称 |
| `ttlSeconds` | 否（默认 604800，7天） | 墓碑 TTL（秒） |

---

## 检索管线

```
查询
  ├─ 查询分流：简单 → 词典 / 中等 → 多同义词 / 复杂 → LLM
  ├─ 查询改写与扩展
  │
  ├─ 阶段一：宽召回 ─────────────────────
  │   ├─ BM25 关键词召回（倒排索引加速）
  │   │     候选收集 → BM25 打分
  │   │
  │   ├─ 向量 ANN 召回（HNSW，独立并行）
  │   │     查询向量化 → HNSW 搜索 → 合并
  │   │
  │   └─ RRF 融合（k=60，自适应查询类型权重）
  │         → top-N 候选（默认 N=100）
  │
  ├─ 阶段二：精排 ────────────────────────  [如配置了 reranker]
  │     交叉编码器对 (查询, 分块) 逐对打分
  │     → 按相关度重新排序
  │
  └─ 后处理
        → 截断至 limit → 片段生成 → 去重
        → 证据质量评分 → 返回
```

**优雅降级**：

| 场景 | 行为 |
|------|------|
| 未配置嵌入端点 | 回退到纯 BM25 关键词搜索 |
| 未配置重排序器 | 跳过精排阶段，直接返回 RRF/BM25 结果 |
| 重排序器超时/失败 | 回退到阶段一的向量余弦相似度评分 |
| 两者均未配置 | 纯 BM25，零外部依赖，开箱即用 |

---

## 存储后端

knowledge-mcp 支持两种存储后端，通过 TOML/环境变量切换：

### 文件系统（默认）

数据存储在 `data_dir` 下，按 KB → 文档结构组织。目录布局见下方存储布局章节。

| 优点 | 注意事项 |
|------|----------|
| 零外部依赖，开箱即用 | 不支持集群部署 |
| 文件可读性强（TOML/YAML/Markdown/JSON） | 多 KB 场景需手动管理磁盘空间 |

### MySQL / MariaDB（可选）

所有文档（元数据、分块、搜索索引）存储到关系型数据库表中。

| 优点 | 注意事项 |
|------|----------|
| 支持并发访问 | 需自行管理 MySQL 实例 |
| 易于集成到现有基础设施 | 首次启动需要建表权限 |
| 大容量场景性能更好 | 向量文件（VECTOR.gob）仍存储于 `data_dir` |

**存储布局**（文件系统后端）：

```
<data-dir>/
├── <kb-name>/
│   ├── INDEX.md
│   ├── INVERTED.gob        # 全局倒排索引，加速候选查找
│   ├── VECTOR.gob          # HNSW 向量索引
│   ├── .tombstones.gob     # 软删除墓碑记录
│   ├── kb.json             # KB 描述信息
│   ├── LIST_SNAPSHOT.json  # 文档列表快照
│   ├── .searchlog.jsonl    # 搜索日志
│   └── <document-slug>/
│       ├── meta.json          # 文档元数据（原始名、类型、标题、作者、摘要等）
│       ├── CHUNKS.toml        # 逐块信息（词项、向量、章节、偏移量、章节角色）
│       ├── source.<ext>       # 原始文件副本
│       └── chunks/
│           ├── 000.md         # 细粒度分块
│           ├── 001.md
│           └── sections/
│               ├── S00.md     # 粗粒度章节块
│               └── S01.md
├── <another-kb>/
│   └── ...
└── （根级存放扁平文档，兼容旧版无 KB 的数据）
```

---

## 缓存机制

knowledge-mcp 内置两层缓存：

### 内置缓存（始终开启，无需 Redis）

基于 Store 层的内存 map 缓存，按类型设置独立 TTL：

| 类型 | 内容 | 默认 TTL | 键格式 |
|------|------|----------|--------|
| chunk | 分块文本 | 0（永不过期） | `kmcp:kb:{kb}:chunk:{slug}:{id}` |
| meta | 文档元数据 | 0（永不过期） | `kmcp:kb:{kb}:meta:{slug}` |
| index | CHUNKS.toml 索引 | 0（永不过期） | `kmcp:kb:{kb}:idx:{slug}` |
| kblist | KB 列表 | 60s | `kmcp:kblist` |

在 `info` 日志级别可以看到各类缓存的 HIT/MISS/SET：

```
[INFO] [cache] chunk HIT  key=kmcp:kb:ship:chunk:doc:033 slug="doc" chunk=033 size=1847
[INFO] [cache] chunk MISS key=kmcp:kb:ship:chunk:doc:081 slug="doc" chunk=081
[INFO] [cache] chunk SET  key=kmcp:kb:ship:chunk:doc:081 slug="doc" chunk=081 ttl=0s size=2103
```

文档重新上传或删除时缓存自动失效。

### Redis 缓存（可选）

安装 Redis 后设置 `redis_enabled = true`。Redis 仅缓存**查询级别的搜索结果**（HybridSearch 输出），以规范化查询 hash + KB + filter hash 为键。

---

## 模型部署

详见 [docs/deployment-models_zh.md](docs/deployment-models_zh.md) / [English](docs/deployment-models.md)。

快速参考：

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
  ./knowledge-mcp serve
```

---

## 领域词典

在 `dictionaries/` 目录放置 YAML 文件实现领域特定同义词的查询扩展。示例 `dictionaries/ship_motion.yaml`：

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

词典在启动时加载。查询 "船舶阻力 CFD" 会自动扩展出 "drag"、"computational fluid dynamics" 等，BM25 和向量召回均生效。

### 领域词典管理

> 🆕 自动从搜索日志发现同义词 + LLM 离线批量生成词典。

**从搜索日志挖掘同义词候选：**

```bash
# 运行一段时间积累日志后，挖掘同义词候选
knowledge-mcp dict mine
```

该工具读取 `.searchlog.jsonl`，通过查询词-文档共现分析自动发现候选同义词对，输出表格供人工审核。审核后手动加入 `dictionaries/*.yaml` 重启生效。

**用 LLM 离线批量生成词典：**

```bash
# 前提：已配置 DEEPSEEK_API_KEY
knowledge-mcp dict gen
```

扫描当前知识库，调用 LLM 批量提取领域术语+同义词+关联词，输出 `dictionaries/<kb>_generated.yaml`。人工审核后合并到现有词典文件即可。

---

## LLM 查询改写

当配置了 `DEEPSEEK_API_KEY` 后，复杂查询（约占总查询的 5%）会自动通过 DeepSeek LLM 改写出 2-4 个扩展变体，提升召回率。简单和中等查询则走领域词典和内置同义词改写器，避免不必要的 LLM 开销。

**三层降级保障：**

1. **LLM 层** — DeepSeek API 返回改写变体（成功时）
2. **降级层** — LLM 故障（网络错误/超时/空响应）时自动回退到内置 `SynonymRewriter`（内置同义词表 + 词典文件）
3. **兜底层** — 即使同义词表为空，也会返回原始查询，确保搜索永不中断

### 安全保护

- API Key **仅通过 TOML 配置文件或环境变量注入**，代码中不硬编码
- 错误日志中的 API 响应会经过 `sanitiseForLog()` 脱敏处理，任何 `sk-*` 格式的 Key 都会被替换为 `sk-***`
- API Key 不会出现在任何日志、错误消息或管理界面中

### 日志示例

```
[INFO] [startup] query rewriter: LLM (deepseek model=deepseek-v4-flash) + synonym fallback (17 terms)
[DEBUG] [deepseek] deepseek: request model=deepseek-v4-flash promptLen=42 bodyLen=237
[DEBUG] [deepseek] deepseek: OK model=deepseek-v4-flash elapsed=856ms promptLen=42 responseLen=128
[WARN] [deepseek] deepseek: non-200 model=deepseek-v4-flash status=401 elapsed=123ms body={"error":"Invalid API key: sk-***"}
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

所有表使用 InnoDB 引擎，utf8mb4 字符集。没有设置外键约束，删除文档时由应用层显式清理各表。

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

当向量索引中出现孤立条目时，使用清理工具：

```bash
# 先 dry-run
go run ./cmd/cleanup-vector/ --dry-run

# 只检查特定 KB
go run ./cmd/cleanup-vector/ --kb ship-hydrodynamics --dry-run

# 执行清理
go run ./cmd/cleanup-vector/

# 只清理特定 KB
go run ./cmd/cleanup-vector/ --kb ship-hydrodynamics
```

该工具会：
1. 遍历 VECTOR.gob 中的所有 HNSW 节点，对比 `chunks` 表
2. 移除指向不存在 chunk 的孤立向量条目
3. 修复 `chunks_index` 表中的孤立索引条目

### 重建向量索引

删除 VECTOR.gob 文件，下次搜索时自动从 `chunks_index` 表中的向量数据重建：

```bash
# 停止服务
# 删除向量索引文件
rm ~/knowledge_base/<kb-name>/VECTOR.gob
# 重启服务
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

1. 确认 `embed_endpoint` 配置正确且服务可达：`curl http://localhost:11434/v1/embeddings -d '{"model":"bge-m3","input":"test"}'`
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
