# Code Review Fix Plan

> 审查日期：2025-07-16
> 审查范围：`knowledge-RAG-MCP` 全量代码（排除 vendor、.go-bin、横摇论文）
> 审查重点：安全漏洞、代码质量、性能、架构设计

---

## 一、安全修复（Security）

### P0 — 立即修复

#### 1. 文件路径穿越保护不完整

**影响文件**：
- `tools_upload.go:62,76` — `knowledge_upload` 工具
- `tools_read.go:48,54` — `knowledge_read` 工具
- `tools_search.go:74` — `knowledge_research` 工具
- `tools_remove.go:33` — `knowledge_remove` 工具

**现状**：仅使用 `strings.Contains(path, "..")` 检测，绝对路径（如 `/etc/passwd`）可绕过。

**方案**：
- 在 `helpers.go` 中新增 `isPathSafe(p string) bool` 函数，使用 `filepath.Clean()` + 绝对路径检测 + `..` 检测
- 将所有工具的 `strings.Contains(x, "..")` 替换为 `!isPathSafe(x)`

**估时**：0.5h

---

#### 2. HTTPDocParser 缺少 Context 传递

**影响文件**：`internal/knowledge/parser.go:150`

**现状**：`http.NewRequest(http.MethodPost, ...)` 未使用 context，上层取消时请求不会中止。

**方案**：
- 修改 `defaultSendFile` 方法签名，增加 `ctx context.Context` 参数
- 将 `http.NewRequest` 改为 `http.NewRequestWithContext(ctx, ...)`
- `Parse` 方法调用时传入 `context.Background()` 或从上层传递

**估时**：0.5h

---

#### 3. MCP HTTP Server 缺少超时配置

**影响文件**：`serve.go:118-121`

**现状**：

```go
httpServer := &http.Server{
    Addr:    ":" + servePort,
    Handler: mux,
}
```

未设置 `ReadTimeout` / `WriteTimeout` / `IdleTimeout` / `ReadHeaderTimeout`，存在 Slowloris 攻击风险。

**方案**：

```go
httpServer := &http.Server{
    Addr:              ":" + servePort,
    Handler:           mux,
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       60 * time.Second,
    WriteTimeout:      120 * time.Second,
    IdleTimeout:       120 * time.Second,
}
```

**估时**：0.25h

---

### P1 — 尽快修复

#### 4. API Token 通过 URL Query Parameter 泄露

**影响文件**：`internal/knowledge/middleware.go:48-49`

**现状**：支持 `?token=xxx` query 参数方式传递 API Token，会出现在服务器日志、Referer header 中。

**方案**：移除 query-parameter 认证，仅保留 `Authorization: Bearer <token>` header 方式。或至少添加弃用警告日志。

**估时**：0.25h

---

#### 5. LLM Rewrite 使用 Background Context

**影响文件**：`internal/knowledge/rewrite_llm.go:87`

**现状**：`context.Background()` 硬编码，用户断开后 LLM 调用仍继续，浪费 API 费用。

**方案**：将 `Rewrite` 方法签名改为 `Rewrite(ctx context.Context, query string) []string`，传递至 `Complete`。调用方（search engine）已有 context，需要一路透传。

**估时**：0.5h

---

#### 6. RateLimitMiddleware 是空实现

**影响文件**：`internal/knowledge/middleware.go:77-98`

**现状**：函数签名完整但内部 `_ = counter` 无实际逻辑。

**方案**：两个选择：
- **方案 A**：实现真正的 token-bucket 限流（per-IP），使用 `golang.org/x/time/rate`
- **方案 B**：删除该函数，注释说明"限流应通过反向代理（nginx/caddy）处理"

推荐方案 B，简单且不引入新依赖。

**估时**：0.25h

---

### P2 — 择机修复

#### 7. API 密钥存在性泄露

**影响文件**：`internal/knowledge/config_api.go:118-123`

**现象**：密钥已配置时返回 `"***"`，未配置时返回 `""`，暴露了密钥是否已配置的信息。

**方案**：统一返回 `"***"`（无论是否配置）。

**估时**：0.25h

---

#### 8. GPU Scheduler 端点 SSRF 风险

**影响文件**：`internal/knowledge/gpu_scheduler.go`

**现象**：sleep/wake URL 完全由配置控制，可能被用于 SSRF。

**方案**：在 `GPUScheduler` 初始化时校验所有 URL 的 host 为 `localhost` 或 `127.0.0.1`，否则返回错误。同时可以使用自定义 `http.Transport` 限制 `DialContext` 仅允许 loopback。

**估时**：1h

---

## 二、可靠性修复（Reliability）

### P0

#### 9. serve.go 的 context cancel 未传递给 HTTP Server

**影响文件**：`serve.go:124`

**现状**：

```go
_, cancel := context.WithCancel(context.Background())
defer cancel()
```

`cancel` 只在 signal goroutine 中被调用，但没有通过 `httpServer.BaseContext` 注册。

**方案**：

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
httpServer.BaseContext = func(_ net.Listener) context.Context { return ctx }
```

**估时**：0.25h

---

### P1

#### 10. ManageServer 启动失败静默

**影响文件**：`serve.go:93-98`

**现状**：goroutine 中启动失败仅打日志，MCP 服务继续运行但管理功能不可用。

**方案**：增加重试机制（如 3 次，间隔 1s），或通过 channel 通知主 goroutine 降级启动；在 `--mcp` 模式下保持现有行为。

**估时**：0.5h

---

#### 11. searchMultiKB 静默吞掉全部失败

**影响文件**：`tools_search.go:122-128`

**现状**：每个 KB 搜索失败只 `continue`，全部失败时返回空结果而非错误，用户无法区分"无匹配"和"全部失败"。

**方案**：收集每个 KB 的错误，全部失败时返回最后一个错误（含 KB 名称）。

**估时**：0.25h

---

#### 12. section chunks 写入失败只静默忽略

**影响文件**：`internal/knowledge/upload.go:149-153`

**现状**：`_ = err` 完全忽略，无日志。

**方案**：至少打 WARN 级别日志，便于排查问题。

**估时**：0.25h

---

## 三、性能优化（Performance）

### P2

#### 13. 搜索收集阶段 N+1 查询

**影响文件**：`internal/knowledge/search_collect.go:18-137`

**现状**：`collectEntries` 逐文档调用 `ReadMeta` + `ReadChunksIndex`，每个文档产生两次数据库查询。

**方案**：
- 在 `MySQLBackend` 新增 `ReadAllMetas(kbName string) ([]DocumentMeta, error)` 批量方法
- 或在搜索 engine 初始化时将全量 chunks-index 加载到内存（已有 `VectorIndexState` 缓存概念）

**估时**：2h

---

#### 14. 倒排索引全量重建

**影响文件**：`internal/knowledge/mysql_backend.go:587-614`

**现状**：`WriteInvertedIndex` 先 `DELETE` 全部再逐条 `INSERT`。

**方案**：改用 `INSERT ... ON DUPLICATE KEY UPDATE` 实现增量更新。`updateInvertedIndex` (inverted.go:36) 已有按文档增删逻辑，可改写为增量 SQL。

**估时**：1.5h

---

#### 15. 搜索日志同步写入

**影响文件**：`internal/knowledge/searchlog.go:53`

**现状**：每次搜索都 open-write-close。

**方案**：使用带缓冲的 writer 或异步 channel + 定期 flush。可引入一个 `searchLogWriter` 结构体持有 `bufio.Writer` 并定期 `Flush`。

**估时**：1h

---

## 四、架构 / 清理（Architecture & Cleanup）

### P2

#### 16. 合并管理 Handler 重复代码

**背景**：`manage.go` 和 `manage/handlers_*.go` 存在大量重复的 HTTP handler。

**方案**：
- 确认所有调用路径都已迁移到 `manage.Start()`
- 将 `manage.go` 中的 handler 标记为 `// Deprecated` 或直接删除（需先确认无引用）
- 最终删除 `manage.go:28` 的 `StartManageServer` 方法

**估时**：1h

---

#### 17. GPU Scheduler sleepCooldown 未使用

**影响文件**：`internal/knowledge/gpu_scheduler.go:65`

**现状**：定义了 `sleepCooldown time.Duration` 字段但代码中从未 `time.Sleep`。

**方案**：在 `doSleep` → `sendRequest` 成功返回后添加 `time.Sleep(s.sleepCooldown)`，确保 GPU 释放内存。同时检查 `defaultGPUSchedulerCooldown` 常量。

**估时**：0.5h

---

#### 18. RateLimitMiddleware 移除或实现

参见 #6，同上处理。

---

## 五、执行顺序建议

```
第1轮（安全优先，预计 1.5h）：
  ├── #1  路径穿越保护加强       (0.5h)
  ├── #2  HTTPDocParser context  (0.5h)
  ├── #3  HTTP Server 超时配置   (0.25h)
  └── #9  context 传递修复       (0.25h)

第2轮（安全+可靠性，预计 1.75h）：
  ├── #4  API Token query 移除   (0.25h)
  ├── #5  LLM ctx 传递           (0.5h)
  ├── #6  空限流清理             (0.25h)
  ├── #10 Manage 启动失败处理     (0.5h)
  └── #11 searchMultiKB 错误传播  (0.25h)

第3轮（完善，预计 3.25h）：
  ├── #7  密钥存在性统一遮罩     (0.25h)
  ├── #8  GPU SSRF 防护          (1h)
  ├── #12 section 写入日志       (0.25h)
  ├── #17 sleepCooldown 实现     (0.5h)
  └── #16 管理 Handler 合并      (1h)

第4轮（性能，预计 4.5h）：
  ├── #13 N+1 批量查询           (2h)
  ├── #14 倒排索引增量更新       (1.5h)
  └── #15 搜索日志缓冲写入       (1h)
```

**总计预估**：约 **11 小时**（4 轮迭代）

---

## 附：审查中发现的亮点

以下实践值得保留和推广：

1. **API 密钥日志安全**：`deepseek_completer.go` 的 `sanitiseForLog` 函数在做 API 调用报错时会先脱敏再写日志
2. **认证时间恒定比较**：`middleware.go:62` 使用 `subtle.ConstantTimeCompare` 而非 `==`，防止时序攻击
3. **Facade 模式解耦**：Store → 6 个子包的 facade 设计（Searcher / ChunkStore / Ingester / KBAdmin / DictService / ManageService）架构清晰
4. **Content-addressable Chunk ID**：`chunk_id.go` 使用 SHA256 生成确定性 ID，支持增量更新和去重
5. **多传输协议支持**：stdio / SSE / Streamable HTTP 三种 MCP 传输模式
