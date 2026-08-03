# 部署状态

> 最后更新: 2026-08-03

## 服务状态

| 服务 | 端口 | 模型 | 状态 | 管理方式 |
|------|------|------|------|---------|
| Ollama (Embedding) | 11434 | `bge-m3` (1024d) | ✅ 运行中 | systemd / 手动 |
| knowledge-mcp | stdio | embedding + reranker | ✅ 可用 | systemd / 手动 |

---

## 最近变更 (2026-08-03)

### 性能优化
- **KB Router desc embedding 缓存**：KB description 向量不再每次 Route 重新计算，首次后缓存
- **MemCache**：无 Redis 时自动启用 LRU 内存缓存（cap=20000），支持 chunk/meta/index/query 缓存
- **日志降级**：搜索内部阶段日志从 INFO 降为 DEBUG，减少生产噪音
- **MySQL 连接池**：新增 `ConnMaxIdleTime(2min)`，`/health` 端点返回 MySQL 连接状态

### 代码质量
- **vet 零警告**：Store 中 RWMutex 值复制问题已修复
- **main.go 拆分**：单文件 1328 行 → 8 文件按职责分离（main/init/serve/stdio/manage/tools/helpers/dict）
- **清理 8 个 debug_test 遗留文件**
- **VectorID/ParseVectorID**：向量 ID 格式抽象，消除硬编码 "/"
- **并发安全**：全局 chunk 参数改为 atomic.Value，支持运行时热更新

### Bug 修复
- **KB Router CJK 分词**：`tokenizeForRoute` 从简单 whitespace split 升级为 CJK unigram+bigram 分词，中文查询路由不再失效

### 测试
- 新增 `cache/memory_test.go`（16 个测试）：LRU/TTL/并发/值隔离
- 新增 `knowledge/kb_router_test.go`（4 个测试）：CJK 分词边界条件
- 新增 `knowledge/chunker_race_test.go`（2 个测试）：chunkParams 并发安全
- 新增 `vector_index_test.go` 中 VectorID/ParseVectorID 测试
- 全量 `go test ./... -race` 通过，零警告

---

## 项目路径

```
/home/aq/knowledge-RAG-MCP/    # 项目根目录
/home/aq/.local/bin/           # 编译后的二进制 (knowledge-mcp, ollama)
/home/aq/.knowledge-mcp/       # 日志目录
```

---

## 启动命令

### Ollama

```bash
ollama serve
# 或 systemd: systemctl --user start ollama
```

### knowledge-mcp（完整模式）

```bash
cd /home/aq/knowledge-RAG-MCP
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings \
EMBED_MODEL=bge-m3 \
EMBED_DIM=1024 \
  knowledge-mcp serve
```

或通过 TOML 配置文件启动：

```bash
knowledge-mcp serve --config /home/aq/knowledge-RAG-MCP/knowledge-mcp.toml
```

### MCP 客户端集成 (stdio 模式)

```bash
knowledge-mcp stdio
```

---

## 配置管理

所有配置项可通过 Web 管理界面 `http://localhost:8085/config` 进行查看和修改，大部分配置支持热更新（无需重启）。

---

## 验证命令

### Embedding 服务

```bash
curl -s http://localhost:11434/api/tags
```

### knowledge-mcp

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}' | \
EMBED_API_ENDPOINT=http://localhost:11434/v1/embeddings EMBED_MODEL=bge-m3 EMBED_DIM=1024 \
knowledge-mcp stdio 2>/dev/null
```

---

## 已知问题

- **Reranker 未常驻运行**：`gte-multilingual-reranker-base` 模型需通过 Infinity 部署在端口 7997，当前未配置自动启动
- 如需启用 Reranker，参考 `docs/deployment-models_zh.md` 部署 Infinity 服务
