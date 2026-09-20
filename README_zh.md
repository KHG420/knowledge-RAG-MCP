# knowledge-mcp

[English](README.md) | [📖 使用指南](GUIDE_zh.md)

> ⚡ **无需自己费心搭建知识库 — 只需连接 MCP，你的 Agent 即刻拥有智能知识库。**
>
> 拖入文档 → 自动分块索引 → BM25 关键词检索（可选混合向量检索与交叉编码器精排）→ 需要一个已存在的 MySQL/MariaDB 数据库。

基于 MCP (Model Context Protocol) 协议的知识库服务，使用 MySQL/MariaDB 作为存储后端，提供 BM25 关键词搜索、可选的混合检索（BM25 + 向量）以及可选的两阶段交叉编码器重排序。嵌入与重排序模型均为可选，服务不会假定它们一定可用。

---

## 目录

- [特性](#特性)
- [安装](#安装)
- [快速开始](#快速开始)
- [架构总览](#架构总览)

> 📖 详细配置、MCP 工具、检索管线、缓存、运维部署、故障排查等请见 **[使用指南](GUIDE_zh.md)**。

---

## 特性

- **文档导入** — 支持 PDF、DOCX、ODT、EPUB、HTML、XLSX、PPTX、MD、TXT
- **查询智能分流** — 零成本纯规则分流器：70% 简单查询走词典扩展、25% 中等查询走同义词多变体、5% 复杂查询才调 LLM
- **BM25 搜索** — Unicode 感知、CJK 双字分词，支持查询重写、同义词扩展、LLM 查询改写
- **LLM 查询改写** — 可选 DeepSeek LLM 驱动，自动生成 2-4 个改写变体提升召回率；仅对复杂查询触发，LLM 不可用时优雅降级
- **BM25/向量解耦** — 精确同义词仅送 BM25，关联术语仅送向量侧，互不污染
- **混合搜索** — BM25 + 稠密向量融合，采用 RRF 算法（k=60），自适应查询类型权重
- **两阶段重排序** — 可选的交叉编码器（兼容 Infinity/Cohere API）对 top-K 候选精排
- **段落级分块** — 语义边界切分、重叠、层级化 fine+coarse 分块、章节角色分类
- **父子检索** — 可读取分块所属的完整父章节，获取更丰富的上下文
- **论文元数据提取** — 自动提取标题、作者、摘要，识别章节角色
- **多知识库** — 将文档组织到独立的知识库中；跨知识库搜索和列表
- **智能知识库路由** — 四维度加权评分自动路由查询到最相关的知识库
- **领域词典支持** — 加载 YAML 格式的领域同义词词典进行查询扩展
- **MySQL/MariaDB 后端** — 必需的存储后端，用于存储文档、分块与知识库元数据
- **软删除（Tombstone）** — 文档删除采用 TTL 墓碑模式，删除后立即从搜索中隐藏
- **增量索引与版本管理** — 重新上传文档时仅对变更的分块重新建索引
- **证据溯源** — 读取结果附带 source_confidence 以及 document/location/citation_id；answer_relevance 与 completeness 固定返回 `unknown`，由 Agent 自行判断，而不是信任启发式猜测
- **页码感知** — PDF 分块标记页码，搜索结果中透传
- **健康检查** — `/health` 端点返回服务状态 + MySQL 连接健康
- **并发安全** — 分块参数支持运行时热更新（atomic.Value），零锁开销

---

## 安装

环境要求：Go 1.24+，以及一个已存在的 MySQL/MariaDB 数据库。

```bash
go build -o knowledge-mcp .
```

依赖已 vendor 到 `vendor/` 目录，默认构建无需联网。运行时 **必须** 提供
MySQL/MariaDB —— 不存在仅文件系统或零外部依赖的运行模式。

---

## 快速开始

这是唯一受支持的上手路径。可直接复制执行的验证脚本见
[docs/onboarding.md](docs/onboarding.md)。

### 1. 构建

```bash
go build -o knowledge-mcp .
```

### 2. 准备 MySQL 并配置

在已有的 MySQL/MariaDB 中创建数据库和用户。表会在首次启动时自动创建，
但数据库本身不会被自动创建。

然后运行交互式向导，它会把 `knowledge-mcp.toml` 写到可执行文件旁边。
向导不会连接 MySQL，也不会修改任何服务：

```bash
./knowledge-mcp setup
```

也可以直接手写配置文件，最小示例：

```toml
mysql_dsn = "user:password@tcp(127.0.0.1:3306)/knowledge_rag?parseTime=true"
```

配置优先级：

1. 可执行文件旁的 `knowledge-mcp.toml`（最高优先级）
2. 环境变量（仅当 TOML 文件不存在时生效）
3. 内置默认值

### 3. 启动服务

```bash
./knowledge-mcp serve          # 管理界面 (:8085) + MCP HTTP (:8086)
./knowledge-mcp serve --mcp    # 仅启动 MCP HTTP
./knowledge-mcp manage         # 仅启动管理界面（配合 stdio 使用）
./knowledge-mcp stdio          # stdio MCP（Reasonix / Claude Desktop / Cline）
```

### 4. 创建知识库并上传文档

打开 `http://localhost:8085`，创建知识库并上传文档。
创建知识库与上传文档在管理界面完成；暴露给 Agent 的 MCP 工具只负责检索与读取。

### 5. 连接 MCP 客户端

- **HTTP（Streamable）**：`http://localhost:8086/mcp`
- **HTTP（旧版 SSE）**：8086 端口的 `/sse` + `/message`
- **stdio**：在项目根目录配置 `.mcp.json`：

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

如果在 TOML 中设置了 `api_token`，则 MCP HTTP 端点（`/mcp`、`/sse`、
`/message`）与管理 API 都需要 `Authorization: Bearer <token>`；
stdio 不使用该 token。未设置 token 时不要将 HTTP 端口暴露到外部网络。

### 已注册的 MCP 工具

只注册三个工具：

| 工具 | 用途 |
| --- | --- |
| `knowledge_research` | 检索排序后的证据段落 |
| `knowledge_read` | 读取单个分块/章节及其溯源信息 |
| `knowledge_list_kbs` | 列出知识库 |

`knowledge_research` 返回 JSON 信封：`results`（始终为数组）、`searched_kbs`、
`failed_kbs`、`warnings` 和 `coverage`（`complete` 或 `partial`，描述的是检索本身，
而不是结果是否回答了问题）。`complete` 表示服务端所选并实际尝试的每个知识库都
检索成功，但不代表所有可用知识库都已检索，因此 `results` 为空不等于"完全不存在"。
这取代了旧版的裸 JSON 数组/纯文本行为。

### 可选：向量嵌入与重排序

BM25 检索无需额外服务即可使用。混合检索与交叉编码器重排序为可选项，
只有在 `knowledge-mcp.toml` 中配置了相应端点时才会启用（若无 TOML 文件，
则使用对应环境变量）。详见
[docs/deployment-models_zh.md](docs/deployment-models_zh.md)。

```bash
# 示例：嵌入服务使用 Ollama，重排序服务使用 Infinity
ollama pull bge-m3
pip install infinity-emb[all]
infinity_emb v2 --model-id Alibaba-NLP/gte-multilingual-reranker-base --port 7997
```

```toml
embed_endpoint = "http://localhost:11434/v1/embeddings"
embed_model = "bge-m3"
rerank_endpoint = "http://localhost:7997/rerank"
rerank_candidate_limit = 100
```

---

## 架构总览

项目采用 **Facade（门面）模式**：`Store` 作为统一入口，将请求委托给 6 个子包
（search、chunkstore、kb、dict、ingest、manage），每个子包实现 `interfaces.go` 中定义的对应接口。

```
main.go                     — CLI 入口点、子命令 (serve / stdio / manage / setup / dict)、工具注册
init.go                     — 依赖注入：将所有子包引擎装配到 Store 门面
tools_*.go                  — MCP 工具注册（仅注册 knowledge_research、knowledge_read、knowledge_list_kbs）
serve.go / stdio.go          — HTTP SSE / Streamable HTTP / stdio MCP 传输
dict.go                     — 字典管理子命令 (mine / gen)
manage_run.go               — Web 管理界面启动器
internal/
  config/
    config.go               — TOML 配置加载、环境变量回退、默认值
  setup/
    setup.go                — 交互式配置向导
    i18n.go                 — 配置向导国际化文本
  logging/
    logger.go               — 结构化文件日志 (DEBUG/INFO/WARN/ERROR，按模块)
  cache/
    cache.go                — Cache 接口 + NoopCache 空实现
    redis.go                — Redis 缓存实现
    keys.go                 — 缓存键命名规范
  knowledge/
    interfaces.go           — 核心接口：Searcher、ChunkStore、Ingester、KBAdmin、DictService、ManageService、CacheClient
    store.go                — Store 门面（约 160 方法），委托给子包引擎
    storage.go              — StorageBackend 接口（存储后端抽象层）
    mysql_backend.go        — MySQL/MariaDB 存储后端
    ── 子包（引擎实现） ──
    search/                 — 检索引擎（实现 Searcher）
      engine.go             —   6 核心搜索方法 + 2 倒排索引写操作 + 查询缓存
      query.go              —   查询改写、复杂度分析、RRF 权重调优
      collect.go            —   候选收集、倒排索引快速路径
      rerank.go             —   粗到细过滤、交叉编码器重排序、缓存
      helpers.go            —   去重、排序、余弦相似度工具
      types.go              —   内部类型定义
      retrieval/            —   BM25 分词器（CJK 双字感知）、BM25Score、MakeSnippet
    chunkstore/             — 分块 I/O 引擎（实现 ChunkStore）
      engine.go             —   26 个 CRUD 方法 + 三级缓存 (chunk/meta/index)
    kb/                     — KB 管理引擎（实现 KBAdmin）
      engine.go             —   7 方法：List/Create/Delete KB + 路由 + 缓存
    dict/                   — 字典引擎（实现 DictService）
      engine.go             —   LoadDictionaries、GenerateDictionary、RunDictMine、RunDictGen
    ingest/                 — 文档摄取引擎（实现 Ingester）
      engine.go             —   UploadDocument、UploadDirectory、CopySource + 完整管线
    manage/                 — Web 管理引擎（使用 ManageService 接口）
      server.go             —   Server 结构体（14 字段 + DI 构造）
      helpers.go            —   共享工具（JSON 响应、文件上传、SSE、模型探测）
      handlers_core.go      —   17 个核心处理器（文档 CRUD、搜索、墓碑、向量、对账）
      handlers_enhanced.go  —   13 个增强处理器（健康、GPU、日志、指标、导入导出）
      handlers_config.go    —   5 个配置处理器 + hot-reload 逻辑
      router.go             —   路由注册、Start()、中间件、后台协程
    ── 遗留文件（为 ManageService 接口和 nil-engine 回退保留） ──
    embed.go、rerank.go、vector_index.go、gpu_scheduler.go、kb_router.go、
    rewrite.go、rewrite_llm.go、query_triage.go、dict_loader.go、dict_generator.go、
    synonym_miner.go、chunker.go、doc.go、parser.go、upload.go、upload_task.go、
    inverted.go、remove.go、tombstone.go、version.go、store_incremental.go、
    manifest.go、reconcile.go、state_machine.go、chunk_id.go、config_api.go、
    store_settings.go、middleware.go、tool_descs.go、searchlog.go、meta_extract.go
cmd/
  cleanup-vector/           — 向量索引清理工具
scripts/
  eval.go                   — 检索评估脚本 (NDCG@5、MRR、Recall@10)
dictionaries/               — 领域词典文件
docs/
  deployment-models.md      — 嵌入与重排序模型部署指南
  deployment-models_zh.md
  roadmap.md                — RAG 优化路线图
  roadmap_zh.md
```
