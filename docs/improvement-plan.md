# 知识库 RAG/MCP 改进计划 v4（v3.1 最终版）

> 经三轮架构评审，从"RAG 调参方案"演进为"面向科研知识库的 Retrieval Engine"。
> 核心转变：单点决策 → 概率排序 + 不确定性处理。
> 本版 = v3 + 6条GPT反馈中筛选出的 2条P0 + 2条P1，剔除 2条过度设计。

---

## 一、架构演进

| 版本 | 核心特征 | 主要问题 |
|------|----------|----------|
| v1 | Agent 选 mode/kbName/关键词 | 把 RAG 核心能力交给 LLM |
| v2 | MCP 接管检索，统一 knowledge_search(question) | KB Router 太弱、Evidence 单维度 |
| v3 | 四维 Router、exact/related 拆分、Evidence 三维、缓存、评测集 | KB Router 单选、complexity 依赖 LLM |
| **v4** | **Router Top-K、纯规则 complexity、轻量 evidence feature、完整评测体系** | — |

---

## 二、目标架构

```
Agent(LLM) → knowledge_search(question)  唯一接口
                  ↓
   ┌──────────────────────────────────────────────┐
   │  MCP Retrieval Engine（内部闭环，不依赖 LLM）   │
   │                                              │
   │  Cache Check（exact + kb_version）             │
   │   hit → 直接返回                               │
   │   miss ↓                                     │
   │                                              │
   │  Query Analyzer（纯规则，0 额外开销）            │
   │   ├─ QueryFeatures（termCount, hasCompare...） │
   │   └─ RetrievalBudget → bm25N, vecBeam, rerankN │
   │                                              │
   │  KB Router（Top-K + confidence）  ← v4 核心修正 │
   │   输出 KBRouteResult{Candidates[], Selected[]}  │
   │   gap > 0.25 → 单 KB                          │
   │   gap ≤ 0.25 → 多 KB 联合检索                   │
   │                                              │
   │  Query Expansion（双通道）                       │
   │   ├─ exact_synonyms → BM25                     │
   │   └─ related_terms  → Vector only              │
   │                                              │
   │  Hybrid Retrieval（动态参数）                    │
   │  Rerank（按 complexity 分档）                    │
   │                                              │
   │  Evidence Analyzer（feature-based）             │
   │   source_confidence + answer_relevance         │
   │   + completeness（轻量规则，不判断 equation）     │
   │                                              │
   │  Cache Save → SearchResult                    │
   └──────────────────────────────────────────────┘
                  ↓
     SearchResult { hits[], evidence[], route, stats }
```

---

## 三、v4 四项修正（v3→v4 变更清单）

### 修正 1（P0）：KB Router 不返回单 KB，改为 Top-K + 置信度

**v3 问题**：`Route() string` 单一返回。跨域问题（如"舭龙骨为什么降低横摇但增加阻力"）天然需要横摇论文 + 阻力论文，单选会丢信息。

**v4 方案**：

```go
// kb_router.go

type KBCandidate struct {
    Name  string  `json:"name"`
    Score float64 `json:"score"`
}

type KBRouteResult struct {
    Candidates []KBCandidate `json:"candidates"` // 全部 KB 的排序结果
    Selected   []string      `json:"selected"`   // 实际用于检索的 KB（1~3 个）
}

func (r *KBRouter) Route(ctx context.Context, query string, candidates []string) *KBRouteResult {
    // ... 四维评分计算（keyword 0.35 + embedding 0.35 + desc 0.15 + constraint 0.15）

    // 按总分降序排列
    sort.Slice(scored, func(i, j int) bool { return scored[i].total > scored[j].total })

    result := &KBRouteResult{}
    for _, s := range scored {
        result.Candidates = append(result.Candidates, KBCandidate{Name: s.name, Score: s.total})
    }

    // 决策逻辑：gap 大 = 单选，gap 小 = 多选
    if len(scored) == 0 {
        return result
    }
    if len(scored) == 1 {
        result.Selected = []string{scored[0].name}
        return result
    }

    top1, top2 := scored[0].total, scored[1].total
    if top1-top2 > 0.25 {
        result.Selected = []string{scored[0].name}
    } else {
        // 取 top-3（或实际可用数）
        n := min(3, len(scored))
        for i := 0; i < n; i++ {
            result.Selected = append(result.Selected, scored[i].name)
        }
    }

    return result
}
```

**检索时使用**：对 `Selected` 中每个 KB 分别检索，取 top-5 合并后统一 rerank。

---

### 修正 2（P0）：complexity 去掉 LLM-first，改为纯规则

**v3 问题**：LLM 优先判定 → 失败降级规则。一次搜索 = 额外 LLM 调用 + embedding + BM25 + rerank。违背"MCP 内部不依赖 LLM"的设计原则。

**v4 方案**：complexity 本质是"需要多少检索预算"，不是语义理解。纯规则，0 额外开销。

```go
// search.go

type QueryFeatures struct {
    TermCount       int
    HasComparison   bool
    HasMethod       bool
    HasMultiConcept bool
}

func analyzeQuery(query string) QueryFeatures {
    lower := strings.ToLower(query)
    terms := strings.Fields(query)

    return QueryFeatures{
        TermCount:       len(terms),
        HasComparison:   strings.Contains(lower, "比较") || strings.Contains(lower, "对比") ||
                         strings.Contains(lower, "差异") || strings.Contains(lower, "vs"),
        HasMethod:       strings.Contains(lower, "方法") || strings.Contains(lower, "method") ||
                         strings.Contains(lower, "模型") || strings.Contains(lower, "计算"),
        HasMultiConcept: strings.Contains(lower, "和") || strings.Contains(lower, "与") ||
                         strings.Contains(lower, "and") || strings.Contains(lower, "+"),
    }
}

func retrievalBudget(qf QueryFeatures) (bm25N, vecBeam, rerankN int, label string) {
    switch {
    case qf.TermCount > 12 || (qf.TermCount > 6 && qf.HasComparison):
        return 80, 200, 100, "complex"
    case qf.TermCount > 6 || qf.HasMethod || qf.HasMultiConcept:
        return 60, 120, 60, "medium"
    default:
        return 40, 80, 40, "simple"
    }
}
```

各阶段数量（不变）：

| 复杂度 | BM25 top-N | Vector beam | Rerank N | 返回 |
|--------|-----------|-------------|----------|------|
| simple | 40 | 80 | 40 | 8 |
| medium | 60 | 120 | 60 | 8 |
| complex | 80 | 200 | 100 | 10 |

---

### 修正 3（P1）：Evidence completeness 改为轻量 feature-based

**v3 问题**：`hasDefinition && (hasCondition || hasEquation)` 太脆。中文论文里"是"和"="不一定稳定。

**v4 方案**：不判断"有没有公式"，判断"包含什么类型的证据内容"。

```go
// doc.go

type EvidenceMeta struct {
    SourceConfidence string `json:"source_confidence"` // exact_section | related_section | semantic_match
    AnswerRelevance  string `json:"answer_relevance"`  // high | medium | low
    Completeness     string `json:"completeness"`      // complete | partial | context_only
}

// 轻量特征提取（纯文本规则，0 额外开销）
type evidenceFeatures struct {
    hasDefinition   bool // "是"、"定义"、"指"、"refers to"
    hasMechanism    bool // "因为"、"由于"、"导致"、"because"
    hasQuantitative bool // 数字出现 ≥3 次 或 含单位符号（m/s, deg, N·m）
    hasConclusion   bool // "因此"、"结果表明"、"thus"
}

func extractEvidenceFeatures(text string) evidenceFeatures {
    lower := strings.ToLower(text)
    digitCount := 0
    for _, r := range text {
        if r >= '0' && r <= '9' {
            digitCount++
        }
    }
    return evidenceFeatures{
        hasDefinition: strings.Contains(lower, "是") ||
            strings.Contains(lower, "定义") ||
            strings.Contains(lower, "指") ||
            strings.Contains(lower, "refers to") ||
            strings.Contains(lower, "defined as"),
        hasMechanism: strings.Contains(lower, "因为") ||
            strings.Contains(lower, "由于") ||
            strings.Contains(lower, "导致") ||
            strings.Contains(lower, "because") ||
            strings.Contains(lower, "机理"),
        hasQuantitative: digitCount >= 3 ||
            strings.Contains(lower, "m/s") ||
            strings.Contains(lower, "deg") ||
            strings.Contains(lower, "n·m") ||
            strings.Contains(lower, "rad/s"),
        hasConclusion: strings.Contains(lower, "因此") ||
            strings.Contains(lower, "结果表明") ||
            strings.Contains(lower, "thus") ||
            strings.Contains(lower, "therefore"),
    }
}

func classifyCompleteness(feats evidenceFeatures) string {
    if feats.hasDefinition && (feats.hasMechanism || feats.hasQuantitative) {
        return "complete"
    }
    if feats.hasDefinition || feats.hasMechanism {
        return "partial"
    }
    return "context_only"
}
```

**比 v3 更泛化**：不依赖公式检测（`$$`/`\frac`），检测数字密度和单位符号即可判断量化程度。

---

### 修正 4（P1）：Cache 分层——先 exact，semantic 延后

**v3 已包含 exact cache**（normalized_query + kb_version，TTL 300s）。

**v4 追加**：不强制 Week 3 交付 semantic cache。semantic cache 实现复杂度是 exact 的 5 倍（需维护 embedding index、阈值调参、防污染），作为 Phase 2 后续迭代。

当前只交付 exact cache：

```go
// search.go — cache 层已就绪，不追加新逻辑
func (s *Store) cacheKey(question string) string {
    normalized := strings.ToLower(strings.TrimSpace(question))
    return fmt.Sprintf("as:%s:%s", s.kbVersion, normalized)
}
```

---

## 四、v4 明确不采纳的设计（及原因）

| 建议 | 来源 | 不采纳原因 |
|------|------|-----------|
| **negative_terms** | GPT 反馈 | BM25 中做减法极易误杀相关文档。"横摇阻尼"和"结构阻尼"在论文里可能同时出现。一旦配置错误，召回直接崩。替代方案：在 rerank 阶段软降权即可 |
| **ambiguity-driven rerank** | GPT 反馈 | ambiguity 的计算本身要做一次检索（先检索再决定 rerank 数 = 循环依赖）。固定按 complexity 分档已够用 |
| **LLM classify complexity** | 已被 v4 P0 修正2 替代 | 搜索链路不应依赖 LLM，规则足够且 0 额外开销 |
| **semantic cache Week 3 强制交付** | 已被 v4 P1 修正4 降级 | 实现复杂度过高，先上 exact，semantic 作为后续迭代 |

---

## 五、实施阶段（修正版）

```
Week 1 — 建立基准
├── Day 1: Evaluation Dataset + 跑 baseline 记录指标
├── Day 2: Embedding 稳定性修复（重试 + 推荐换 bge-m3）
└── Day 3: Rerank 动态参数（纯规则版，无 LLM）

Week 2 — 核心架构
├── Day 4: 统一 knowledge_search(question)
├── Day 5: KB Router Top-K（不返回单 KB）  ← v4 核心修正
└── Day 6: Query Expansion（exact/related 拆分，不加 negative）

Week 3 — 质量提升
├── Day 7: Evidence 三维度（feature-based completeness）  ← v4 调整
├── Day 8: Chunk Metadata（规则 cheap + 异步小模型 topic）
├── Day 9: Exact Cache
└── Day 10: Debug Dashboard / 收尾

后续迭代（不强制 Week 3）
├── Semantic Cache（embedding 相似度匹配）
└── Rerank 软降权（替代 negative_terms）
```

---

## 六、产出物

| 文件 | 状态 | 说明 |
|------|------|------|
| `docs/improvement-plan.md` | ✅ v4 | 本文件 |
| `evaluation/questions.json` | ✅ | 10题评测集（简单2/中等6/复杂2） |
| `evaluation/eval_test.go` | ✅ | 7个自动化测试（go test 全 PASS） |
| `dictionaries/ship_motion.yaml` | ✅ | 17术语 × exact_synonyms + related_terms |

---

## 七、v3 → v4 修正对照

| 模块 | v3 | v4 | 原因 |
|------|-----|-----|------|
| KB Router | `Route() string` 单选 | `Route() *KBRouteResult` Top-K | 跨域问题天然需要多 KB |
| complexity | LLM 优先判定 | 纯规则 QueryFeatures | 搜索链路不应依赖 LLM |
| Evidence completeness | `hasEquation` 硬判断 | `evidenceFeatures` 轻量规则 | 中文论文公式检测不稳定 |
| Cache | exact | exact（semantic 延后） | semantic 实现复杂度 5x |
| negative_terms | — | ❌ 不采纳 | BM25 减法误杀风险太高 |
| ambiguity rerank | — | ❌ 不采纳 | 循环依赖，固定分档已够用 |

---

> 最后更新：2026-07-31
> 版本：v4（v3.1 最终版）— 2条P0 + 2条P1，剔除2条过度设计
