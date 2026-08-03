# 部署状态

> 最后更新: 2026-08-01

## 服务状态

| 服务 | 端口 | 模型 | 状态 | 管理方式 |
|------|------|------|------|---------|
| Ollama (Embedding) | 11434 | `bge-m3` (1024d) | ✅ 运行中 | systemd / 手动 |
| knowledge-mcp | stdio | embedding + reranker | ✅ 可用 | systemd / 手动 |

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
