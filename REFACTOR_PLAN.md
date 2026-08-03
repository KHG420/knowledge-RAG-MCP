# knowledge-RAG-MCP 重构计划

> 生成日期：2026-08-03 | 状态：待审阅

---

## 一、项目现状诊断

### 1.1 规模概览

| 指标 | 数值 |
|------|------|
| 根目录 Go 文件 | 8 个（main, init, serve, stdio, tools, dict, helpers, manage_run） |
| `internal/` 目录 | 4 个子包（knowledge, cache, config, logging, retrieval, setup） |
| `internal/knowledge/` 文件数 | 55 个 `.go` 文件（全部平放） |
| `knowledge.Store` 方法数 | ≥200 个，分布在 13+ 文件中 |
| `knowledge.Store` 字段数 | 26 个 |
| 核心未测试文件 | `store.go`(55KB), `search.go`(60KB) |
| 测试文件数 | 14 个 `_test.go` |

### 1.2 依赖拓扑

```
main.go
  ├── init.go ── 270行的巨型初始化，硬编码连接所有组件
  ├── tools.go ── MCP 工具注册 (knowledge_research/read/list/list_kbs/upload/remove)
  ├── serve.go ── HTTP SSE + Streamable HTTP 双传输
  ├── stdio.go ── stdio MCP 模式
  ├── manage_run.go ── Web 管理界面
  └── dict.go ── 字典管理子命令

internal/
  ├── knowledge/   (核心：Store God Object + 50+ 文件)
  │     ├── 依赖 → internal/retrieval/ (BM25分词)
  │     ├── 依赖 → internal/cache/     (Redis/Memory缓存)
  │     └── 依赖 → internal/config/    (配置)
  ├── retrieval/   (BM25 分词与索引，单向被 knowledge 依赖)
  ├── cache/       (Redis + Memory 缓存)
  ├── config/      (TOML 配置加载)
  ├── logging/     (结构化日志)
  └── setup/       (交互式安装向导)
```

### 1.3 已识别问题清单

| # | 问题 | 严重度 | 影响 |
|---|------|--------|------|
| 1 | `Store` God Object：200+ 方法、26 字段、10+ 职责域 | 🔴 严重 | 任何改动牵一发动全身；无法独立测试；新人理解成本极高 |
| 2 | `store.go`(55KB) + `search.go`(60KB) 零单元测试 | 🔴 严重 | 核心检索逻辑无安全网，重构风险极大 |
| 3 | `init.go` 的 `initStoreAndLogger()` 270 行顺序初始化 | 🟠 高 | 组件创建顺序隐含耦合；无法替换/模拟单个组件 |
| 4 | 死代码链：`SnapshotPath → ListWithSnapshot → ListPreview` 等 7 个导出方法无人调用 | 🟠 高 | 增加维护负担；误导新开发者 |
| 5 | `tools.go` 600+ 行混合了 6 个工具的注册逻辑 | 🟡 中 | 单文件过大，查找不便 |
| 6 | `manage.go`(42KB) 38 个 HTTP handler 挂在 Store 上 | 🟡 中 | HTTP 层与业务逻辑未分离 |
| 7 | `internal/knowledge/` 55 文件平放无分层 | 🟡 中 | IDE 导航困难；新增功能不知道该放哪 |
| 8 | MCP 工具中 `search_keywords` / `mode` 参数已标记 DEPRECATED 但仍暴露 | 🟢 低 | 对 Agent 造成混淆 |

---

## 二、重构策略

### 核心原则

1. **门面模式 (Facade)**：保留 `Store` 作为统一入口，内部委托给子模块。外部调用者（MCP tools）不受影响。
2. **接口先行**：定义核心接口，再迁移实现。
3. **测试驱动重构**：先为高风险区域补测试，再动手改。
4. **每步可回滚**：一阶段一个 PR，每个 PR 独立可上线。

### Store 拆分架构（目标态）

```
                    ┌───────────────────────────────┐
                    │        knowledge.Store         │  ← Facade，仅保留协调逻辑
                    │  (Embedder/Reranker/Cache/...) │     约 200 行
                    └───────┬───────────────────────┘
                            │ 委托
        ┌───────┬───────────┼───────────┬───────────┬──────────┐
        ▼       ▼           ▼           ▼           ▼          ▼
   ChunkStore  SearchEngine  IngestService  ManageServer  DictService
   (I/O+CRUD)  (BM25+向量    (解析+分块    (HTTP API     (字典管理
               +混合+重排)    +上传)       38 handlers)  同义词+LLM)
```

| 子模块 | 职责 | 从哪个文件拆出 |
|--------|------|---------------|
| `ChunkStore` | chunk 读写、索引、manifest、tombstone、storage | `store.go`, `store_incremental.go`, `tombstone.go`, `manifest.go`, `storage.go` |
| `SearchEngine` | BM25、向量检索、混合检索、重排序、多 KB 合并、缓存查询 | `search.go`, `rerank.go`, `inverted.go`, `vector_index.go` |
| `IngestService` | 文档解析、分块、上传、上传任务管理 | `parser.go`, `chunker.go`, `upload.go`, `upload_task.go`, `doc.go` |
| `ManageServer` | Web 管理界面 38 个 HTTP handler | `manage.go`, `manage_enhanced.go` |
| `DictService` | 字典加载/生成/挖掘、查询改写 | `dict_loader.go`, `dict_generator.go`, `synonym_miner.go`, `rewrite.go`, `rewrite_llm.go` |
| `KBManager` | KB CRUD、路由、配置热加载 | `config_api.go`, `kb_router.go`, `store_settings.go` |

### 接口定义（草案）

```go
// 核心检索接口
type Searcher interface {
    Search(ctx context.Context, query string, limit int, filter SearchFilter) ([]SearchHit, error)
    HybridSearch(ctx context.Context, query string, limit int, filter SearchFilter) ([]SearchHit, error)
}

// 分块存储接口
type ChunkReader interface {
    ReadChunk(docSlug, chunkID string) (string, error)
    ReadChunkContext(docSlug, chunkID string, ctxCount int) (string, error)
    ReadChunksIndex(docSlug string) (*ChunksIndex, error)
}

type ChunkWriter interface {
    WriteChunk(docSlug string, chunk Chunk) error
    WriteChunksIndex(docSlug string, index *ChunksIndex) error
    RemoveDocument(docSlug string) error
}

// 文档摄取接口
type Ingester interface {
    IngestDocument(filePath string, tags ...string) (*DocumentMeta, error)
    IngestDirectory(dirPath string, recursive bool, tags ...string) (string, error)
}

// KB 管理接口
type KBManager interface {
    ListKBs() ([]string, error)
    CreateKB(name string) error
    DeleteKB(name string) error
}
```

---

## 三、分阶段实施计划

### 阶段一：安全清理 🔵 低风险（预计 1-2 天）

**目标**：消除已知垃圾，降低后续重构的阅读负担。

| 步骤 | 内容 | 涉及文件 | 风险 |
|------|------|---------|------|
| 1.1 | 删除死代码：`SnapshotPath`, `WriteListSnapshot`, `ReadListSnapshot`, `ListWithSnapshot`, `ListPreview`, `ListPreviewAll`, `List` 共 7 个方法 | `store.go`, `list.go` | ✅ 零风险（确认无调用者） |
| 1.2 | `tools.go` 按工具拆分：`tools_search.go`, `tools_read.go`, `tools_upload.go`, `tools_remove.go`, `tools_list.go`, `tools_helpers.go` | `tools.go` → 6 个文件 | ✅ 纯重组 |
| 1.3 | 移除 MCP 工具中已标记 DEPRECATED 的 `search_keywords` 和 `mode` 参数 | `tools.go`, `tool_descs.go` | ⚠️ 与 Agent 兼容性有关，需在 CHANGELOG 标注 |
| 1.4 | 清理 `helpers.go` 中重复的 `runDictMine` 注释（与 `dict.go` 重复） | `helpers.go` | ✅ 零风险 |

**阶段一完成后**：项目骨架更清晰，文件职责一目了然。

---

### 阶段二：补充测试 🟢 中低风险（预计 3-5 天）

**目标**：为核心路径建安全网，让后续重构有底气。

| 步骤 | 内容 | 测试文件 | 覆盖场景 |
|------|------|---------|---------|
| 2.1 | `SearchEngine` 集成测试 | `search_test.go` | HybridSearch、SearchAll、searchMultiKB、Coarse 模式、缓存命中/未命中、空结果 |
| 2.2 | `ChunkStore` CRUD 测试 | `store_test.go` | WriteChunk → ReadChunk 往返、ChunksIndex 读写、边界条件（空文档/超大chunk） |
| 2.3 | `Parser` 边界测试 | `parser_test.go` | 空文件、PDF边界、DOCX复杂表格、超大文件超时 |
| 2.4 | `Chunker` 增强测试 | `chunker_test.go` | 多语言文本、边界 split、section heading 检测 |
| 2.5 | `ManageServer` handler 测试 | `manage_test.go` (补充) | 关键 API 的 HTTP 状态码、JSON 响应结构 |
| 2.6 | Race condition 检查 | 运行 `go test -race ./...` | 确认无 data race |

**阶段二完成后**：核心检索和存储路径测试覆盖率 > 60%。

---

### 阶段三：Store 拆分 🟠 中高风险（预计 5-8 天）

**目标**：按门面模式拆分 God Object，接口抽象在先。

#### 3.1 创建接口 (`internal/knowledge/interfaces.go`)

```go
package knowledge

type Searcher interface { ... }
type ChunkStore interface { ... }
type Ingester interface { ... }
// ... 等
```

#### 3.2 提取 SearchEngine

- 新建 `internal/knowledge/search/` 子包
- 从 `search.go`、`rerank.go`、`inverted.go` 提取到 `search/engine.go`
- `Store` 中搜索相关字段迁移到 `SearchEngine`
- `Store.SearchAll()` → 委托给 `engine.SearchAll()`
- 同时迁移 BM25 逻辑从 `internal/retrieval/bm25.go` → `internal/knowledge/search/bm25.go`

#### 3.3 提取 ChunkStore

- 新建 `internal/knowledge/chunkstore/` 子包
- 从 `store.go` 提取 CRUD 操作
- `Store.ReadChunk()` → 委托给 `chunkStore.ReadChunk()`

#### 3.4 提取 ManageServer

- 新建 `internal/knowledge/manage/` 子包
- `manage.go` + `manage_enhanced.go` → `manage/server.go` + `manage/handlers_*.go`
- `Store.StartManageServer()` → `manage.Start(store)`

#### 3.5 提取 DictService & IngestService

- 同理拆出 `internal/knowledge/dict/` 和 `internal/knowledge/ingest/`

#### 3.6 Store Facade 收口

最终的 `Store` 变为：

```go
type Store struct {
    chunk    *ChunkStore
    search   *SearchEngine
    ingest   *IngestService
    manage   *ManageServer
    dict     *DictService
    kb       *KBManager

    // 横切关注点
    embedder Embedder
    reranker Reranker
    cache    CacheLayer
    scheduler *GPUScheduler
    logger   *logging.Logger
    // ... 仅保留跨组件协调字段
}

func (s *Store) SearchAll(query string, limit int, filter SearchFilter) ([]SearchHit, error) {
    return s.search.SearchAll(query, limit, filter)
}
```

**阶段三完成后**：`Store` 从 200+ 方法降到约 50 个委托方法 + 协调逻辑。

---

### 阶段四：目录分层优化 🟢 低风险（预计 1-2 天）

**目标**：物理目录反映逻辑分层。

```
internal/knowledge/
  ├── interfaces.go          # 所有核心接口定义
  ├── store.go               # Store Facade (仅协调)
  ├── store_settings.go      # Store 设置方法
  ├── store_incremental.go   # 增量更新协调
  │
  ├── chunkstore/            # 分块存储
  │   ├── chunkstore.go
  │   ├── manifest.go
  │   ├── tombstone.go
  │   └── storage.go
  │
  ├── search/                # 检索引擎
  │   ├── engine.go
  │   ├── bm25.go            # (从 retrieval/ 迁入)
  │   ├── vector.go
  │   ├── hybrid.go
  │   ├── rerank.go
  │   └── multi_kb.go
  │
  ├── ingest/                # 文档摄取
  │   ├── parser.go
  │   ├── chunker.go
  │   ├── upload.go
  │   └── upload_task.go
  │
  ├── manage/                # Web 管理
  │   ├── server.go
  │   └── handlers_*.go
  │
  ├── dict/                  # 字典管理
  │   ├── loader.go
  │   ├── generator.go
  │   ├── miner.go
  │   └── rewrite.go
  │
  ├── embed/                 # 嵌入相关
  │   ├── embedder.go
  │   ├── vector_index.go
  │   └── kb_router.go
  │
  └── infra/                 # 基础设施
      ├── mysql_backend.go
      ├── gpu_scheduler.go
      ├── cache_bridge.go
      └── state_machine.go
```

---

## 四、风险评估与缓解

| 风险 | 概率 | 缓解措施 |
|------|------|---------|
| 拆分后循环依赖 | 低 | 接口定义在父包，子包实现接口，不相互引用 |
| MCP 协议兼容性 | 中 | 移除 `search_keywords` 前确认主流 Agent 已迁移到 `question` |
| 测试覆盖不足导致回归 | 中 | 阶段二先行，每步重构前后运行全量测试 |
| MySQL backend 行为变化 | 低 | `mysql_backend.go` 几乎不变，只是迁移到 infra/ 目录 |
| 性能退化 | 低 | 门面模式只增加一层函数调用开销（编译器通常内联） |

---

## 五、不改动的部分

以下保持不变：

- ✅ **MCP 工具名称**：`knowledge_research`, `knowledge_read` 等不变，Agent 兼容
- ✅ **配置文件格式**：`knowledge-mcp.toml` 结构不变
- ✅ **MySQL 表结构**：数据库 schema 不变
- ✅ **HTTP API 路径**：管理界面 API 路径不变
- ✅ **日志格式**：结构化日志格式不变
- ✅ **命令行接口**：`knowledge-mcp serve/stdio/manage/dict` 子命令不变

---

## 六、建议执行顺序总览

```
阶段一 (1-2天)          阶段二 (3-5天)           阶段三 (5-8天)          阶段四 (1-2天)
  安全清理                 补充测试               Store 拆分              目录分层
  ┌──────────┐           ┌──────────┐           ┌──────────┐           ┌──────────┐
  │ 删死代码  │           │ search测试│           │ 接口定义  │           │ 目录迁移  │
  │ 拆tools  │           │ store测试 │           │ 提取Search│          │ import调整│
  │ 清过期API │           │ parser测试│           │ 提取Chunk │          │ 全量测试  │
  │ 清重复注释│           │ chunker测试│          │ 提取Manage│          │ 清理旧文件│
  └──────────┘           │ manage测试 │          │ 提取Dict  │          └──────────┘
                         │ race检测   │          │ 提取Ingest│
                         └──────────┘           │ Store收口 │
                                                └──────────┘
```

---

## 七、待确认事项

- [ ] 阶段三的拆分子包数量是否过多？（当前方案 7 个子包）
- [ ] `internal/retrieval/` 是否整体迁入 `internal/knowledge/search/`？（它目前只被 knowledge 引用）
- [ ] 是否需要同时引入 `wire`（Google 依赖注入工具）替代 `init.go` 手工初始化？
- [ ] MCP `search_keywords` 参数移除时机：发布新 major 版本时移除还是直接移除？
