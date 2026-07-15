# RAG 优化路线图

> 2026-07-13 审计 knowledge-mcp 项目后制定。基于 9 项 RAG 高级用法的逐条评估，
> 按价值/阻力比排序，标记为"已有 / 计划中 / 后续 / 跳过"。

## 一、已有 ✓（无需改动）

### 智能分块 (`internal/knowledge/chunker.go`)
- 按 `\n\n` 段落边界切分 → 短块合并(<200字符) → 长块按句子边界再切(>2000字符) → 碎片合并(<60字符)
- 块间重叠 ~200 字符（句子对齐）
- Markdown 标题追踪 + section 角色分类 (abstract/introduction/methodology 等)
- Embedding 余弦相似度合并相邻同义块 (阈值 0.75)
- Fine chunk + Coarse section chunk 两层结构

### 父子块索引 (`internal/knowledge/doc.go:58, store.go:870-930`)
- `ChunkWithMeta.SectionID` → `CHUNKS.toml.SectionChunkID` → `chunks/sections/S00.md`
- `knowledge_read` 支持 `level: "section"` 读取子块所属的完整 section
- `context` 参数可读取相邻 chunk 窗口 (前后各 0-5 个)

### 分层检索 (`internal/knowledge/search.go:666-768`)
- `coarseToFineFilter`: BM25 评分 section-level chunks → 选 top-3 → 只在匹配 section 内做精细检索
- 启用方式: `SearchFilter.Coarse = true`

---

## 二、计划中 → 下一步实施

### 1. 元数据过滤增强

**现状**: `SearchFilter` 仅有 `SourceType` / `Section` / `DocSlug` / `Coarse` 四个字段。
`meta.json` 无自定义标签、无文档日期过滤。

**计划**:

- [x] `SearchFilter` 新增字段:
  - `Tags []string` — 标签白名单（文档必须有至少一个匹配标签）
  - `AddedAfter` / `AddedBefore` — 上传时间范围 (`meta.json` 已有 `AddedAt`)
- [x] `meta.json` 结构新增 `Tags []string`
- [x] `knowledge_upload` 工具新增可选 `tags` 参数 (逗号分隔)
- [x] `knowledge_search` 工具新增可选 `tags` / `addedAfter` / `addedBefore` 参数
- [x] `collectEntries` / `collectEntriesFromCandidates` 增加标签和时间过滤逻辑

### 2. Coarse 模式暴露到 MCP 工具

**现状**: `SearchFilter.Coarse` 只在 Go 代码层可用，MCP 调用方无法触发。

**计划**:

- [x] `knowledge_search` 工具新增 `coarse` boolean 参数
- [x] 当 `coarse=true` 时，handler 设置 `filter.Coarse = true`

### 3. 父块命中自动提示

**现状**: 搜索结果中同一 section 下的多个 chunk 分别返回，调用方不知道它们属于同一个 section。

**计划**:

- [x] `SearchHit` 新增 `SectionHint` 字段: 当同一 section 被 ≥2 个 chunk 命中时，
  返回 `"Multiple hits in section 'XXX'. Consider reading with level=section for full context."`
- [x] 实现逻辑: 在 `Search` / `HybridSearch` 的 Phase 9 后统计 sectionChunkID 频率，
  给命中 ≥2 次的 section 的 chunk 添加 section hint
- [x] 或者: 让 `knowledge_read` 的描述明确建议"如果搜索结果来自同一 section，使用 level=section"

---

## 三、后续考虑 → 有了前置条件再做

### 4. 评估体系

**现状**: `SearchLogger` 接口已存在，输出 `.searchlog.jsonl`（Query / HitCount / HitIDs / TopScores / Filter / Timestamp）。
`SearchLogEntry` 已新增 `JudgedHits` 字段用于人工标注。评测脚本 `scripts/eval.go` 已完成，
可读取 `.searchlog.jsonl` + 人工标注 JSON 并计算 NDCG@5 / MRR / Recall@10。

**前置条件**: 需要先积累人工标注的 query→relevant_chunk 映射。

**已完成**:

- [x] 编写评测脚本 `scripts/eval.go`：读取 `.searchlog.jsonl` + 人工标注 JSON → 计算 NDCG@5 / MRR / Recall@10
- [x] `SearchLogEntry` 新增 `JudgedHits []string`（人工标注的 relevant chunk IDs）字段
- [x] 提供一个交互式标注 CLI（可选）

---

## 四、跳过 ✗

| 建议 | 跳过理由 |
|------|---------|
| **上下文压缩** | 需额外部署压缩模型；调用方 agent 可自行摘要。增加延迟和运维成本，收益不大 |
| **GraphRAG** | 需引入图索引 + 实体提取 + 关系推理，是对当前 BM25/向量架构的根本性改造，复杂度爆炸 |
| **Agentic RAG** | MCP 调用者（Claude/GPT）本身就是 agent，已有路由/反思/重试能力。在 MCP server 内重建一层 agent 是重复建设 |
| **提示词工程** | `knowledge_search` 描述已有详细查询重写规则；调用方 system prompt 不在本项目控制范围内 |
