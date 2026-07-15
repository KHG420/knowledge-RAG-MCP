# 部署状态

> 最后更新: 2026-07-15 15:51

## 服务状态

| 服务 | 端口 | 模型 | 状态 | 管理方式 |
|------|------|------|------|---------|
| Ollama (Embedding) | 11434 | `bge-m3` (1024d, 1.1GB) | ✅ 运行中 | nohup (PID 42262) |
| Infinity Reranker | 7997 | `Alibaba-NLP/gte-multilingual-reranker-base` | ✅ 运行中 | nohup (PID 42290) |
| knowledge-mcp | stdio | embedding + reranker | ✅ 可用 | 手动启动 |

---

## 持久化存储目录

所有服务数据从 `/tmp` 迁移到了持久目录：

```
/Users/aq/knowledge-mcp/
├── .ollama_persist/           # Ollama 持久数据
│   ├── Ollama.app/            # Ollama 二进制
│   ├── home/.ollama/models/   # 模型数据 (bge-m3 ~1.1GB)
│   ├── logs/                  # 日志
│   ├── com.ollama.service.plist           # launchd plist
│   └── com.infinity-reranker.service.plist # launchd plist
├── .infinity_cache/           # HuggingFace 缓存 + Reranker 日志
└── service-manager.sh         # 服务管理脚本
```

## 服务管理

### 使用管理脚本

```bash
cd /Users/aq/knowledge-mcp

# 查看状态
./service-manager.sh status

# 启动全部服务
./service-manager.sh all start

# 停止全部服务
./service-manager.sh all stop

# 重启特定服务
./service-manager.sh ollama restart
./service-manager.sh reranker restart
```

### 安装为 launchd 服务（推荐）⚠️

当前 shell 环境有沙盒限制，需在**真实终端**中执行以下命令：

```bash
cd /Users/aq/knowledge-mcp

# 安装 plist 到 LaunchAgents
cp .ollama_persist/com.ollama.service.plist ~/Library/LaunchAgents/
cp .ollama_persist/com.infinity-reranker.service.plist ~/Library/LaunchAgents/

# 加载服务
launchctl load ~/Library/LaunchAgents/com.ollama.service.plist
launchctl load ~/Library/LaunchAgents/com.infinity-reranker.service.plist

# 查看状态
launchctl list | grep -E 'ollama|infinity'
```

安装后，两个服务会在用户登录时自动启动，进程崩溃后自动重启。

### 管理命令备忘

```bash
# 查看 launchd 服务状态
launchctl list | grep -E 'ollama|infinity'

# 重启服务
launchctl unload ~/Library/LaunchAgents/com.ollama.service.plist
launchctl load   ~/Library/LaunchAgents/com.ollama.service.plist

# 查看日志
tail -f ~/Library/LaunchAgents/com.ollama.service.plist
tail -f /Users/aq/knowledge-mcp/.infinity_cache/infinity.err
```

## 启动命令（直接 nohup）

### Ollama
```bash
cd /Users/aq/knowledge-mcp
export OLLAMA_HOME="/Users/aq/knowledge-mcp/.ollama_persist/home/.ollama"
export HOME="/Users/aq/knowledge-mcp/.ollama_persist/home"
nohup .ollama_persist/Ollama.app/Contents/Resources/ollama serve \
  > .ollama_persist/logs/ollama.log \
  2> .ollama_persist/logs/ollama.err &
```

### Reranker
```bash
cd /Users/aq/knowledge-mcp
export HF_HOME="/Users/aq/knowledge-mcp/.infinity_cache"
nohup .venv/bin/infinity_emb v2 \
  --model-id Alibaba-NLP/gte-multilingual-reranker-base \
  --port 7997 \
  > .infinity_cache/infinity.log \
  2> .infinity_cache/infinity.err &
```

### knowledge-mcp（完整模式）
```bash
cd /Users/aq/knowledge-mcp
EMBED_API_BASE_URL=http://localhost:11434/v1 \
EMBED_MODEL=bge-m3 \
EMBED_DIM=1024 \
RERANK_API_BASE_URL=http://localhost:7997 \
RERANK_CANDIDATE_LIMIT=100 \
DO_NOT_TRACK=1 \
  ./knowledge-mcp
```

## 验证命令

### Embedding 服务
```bash
curl -s http://localhost:11434/api/tags
```

### Reranker 服务
```bash
curl -X POST http://localhost:7997/rerank \
  -H 'Content-Type: application/json' \
  -d '{"query":"分块参数","documents":["长段落阈值2000字符","短段落阈值200字符"],"top_n":2}'
```

### knowledge-mcp
```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}' | \
EMBED_API_BASE_URL=http://localhost:11434/v1 EMBED_MODEL=bge-m3 EMBED_DIM=1024 \
RERANK_API_BASE_URL=http://localhost:7997 RERANK_CANDIDATE_LIMIT=100 \
./knowledge-mcp 2>/dev/null
```

## 已知问题

- **transformers 降级**: 从 4.57.6 降级到 4.48.3，解决 `optimum.bettertransformer` 在 transformers ≥4.49 时硬编码报错的问题
- **urllib3 警告**: LibreSSL 2.8.3 兼容性警告（非阻塞，可忽略）
- **colpali-engine 兼容**: colpali-engine 0.3.13 期望 transformers ≥4.53.1（该功能未使用，不影响）
- **launchd 安装**: 当前 shell 沙盒限制无法直接安装到 `~/Library/LaunchAgents/`，需在真实终端手动执行 `cp` + `launchctl load`
