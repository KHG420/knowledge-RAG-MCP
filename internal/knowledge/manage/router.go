// Package manage — HTTP router and server startup.
package manage

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"knowledge-mcp/internal/knowledge"
)

const manageUIHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Knowledge RAG MCP — 管理</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 900px; margin: 2rem auto; padding: 0 1rem; }
  h1 { color: #333; } a { color: #06c; }
  .section { margin: 2rem 0; padding: 1rem; background: #f5f5f5; border-radius: 8px; }
  code { background: #e0e0e0; padding: 2px 6px; border-radius: 3px; }
</style>
</head>
<body>
<h1>🧠 Knowledge RAG MCP</h1>
<div class="section">
  <h2>API 端点</h2>
  <ul>
    <li><a href="/api/health">GET /api/health</a> — 健康检查</li>
    <li><a href="/api/documents">GET /api/documents</a> — 文档列表</li>
    <li><a href="/api/search?q=test">GET /api/search</a> — 文档搜索</li>
    <li><a href="/api/knowledge-bases">GET /api/knowledge-bases</a> — 知识库列表</li>
    <li><a href="/api/config">GET /api/config</a> — 运行时配置</li>
    <li><a href="/api/metrics">GET /api/metrics</a> — 指标监控</li>
    <li><a href="/api/logs">GET /api/logs</a> — 日志查看</li>
    <li><a href="/api/gpu-scheduler">GET /api/gpu-scheduler</a> — GPU 调度器</li>
    <li><a href="/api/system-info">GET /api/system-info</a> — 系统信息</li>
    <li><a href="/api/models">GET /api/models</a> — 模型信息</li>
  </ul>
</div>
</body>
</html>`

// Start starts the HTTP management server on the given port.
func Start(srv *Server, port string) error {
	mux := http.NewServeMux()

	// ── Health ──
	mux.HandleFunc("GET /health", srv.handleHealth)
	mux.HandleFunc("GET /api/health", srv.handleHealth)

	// ── UI ──
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(manageUIHTML)) //nolint:errcheck
	})

	// ── Documents ──
	mux.HandleFunc("GET /api/documents", srv.handleManageList)
	mux.HandleFunc("POST /api/upload", srv.handleManageUpload)
	mux.HandleFunc("DELETE /api/documents/{slug}", srv.handleManageDelete)
	mux.HandleFunc("POST /api/documents/batch-delete", srv.handleBatchDelete)
	mux.HandleFunc("GET /api/documents/{slug}", srv.handleManageDocDetail)
	mux.HandleFunc("GET /api/documents/{slug}/chunks", srv.handleDocChunks)
	mux.HandleFunc("GET /api/documents/{slug}/download", srv.handleDocDownload)
	mux.HandleFunc("PUT /api/documents/{slug}", srv.handleDocReplace)
	mux.HandleFunc("PATCH /api/documents/{slug}/tags", srv.handleDocTagsUpdate)

	// ── Search ──
	mux.HandleFunc("GET /api/search", srv.handleManageSearch)
	mux.HandleFunc("POST /api/search-console", srv.handleSearchConsole)

	// ── Config / Tools ──
	mux.HandleFunc("GET /api/config", srv.handleConfigGet)
	mux.HandleFunc("PUT /api/config", srv.handleConfigPut)
	mux.HandleFunc("GET /api/tool-descriptions", srv.handleToolDescriptionsGet)
	mux.HandleFunc("PUT /api/tool-descriptions", srv.handleToolDescriptionsPut)
	mux.HandleFunc("POST /api/restart", srv.handleRestart)

	// ── KB management ──
	mux.HandleFunc("GET /api/knowledge-bases", func(w http.ResponseWriter, r *http.Request) {
		svc := srv.Service()
		kbs, err := svc.ListKBsInfo()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list KBs failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"knowledgeBases": kbs,
			"currentKB":      svc.KBName(),
		})
	})
	mux.HandleFunc("POST /api/knowledge-bases", func(w http.ResponseWriter, r *http.Request) {
		svc := srv.Service()
		log := srv.Logger().WithModule("manage")
		type kbCreateReq struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		var body kbCreateReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		if err := svc.ValidateComponent(body.Name); err != nil {
			writeError(w, http.StatusBadRequest, "invalid name: "+err.Error())
			return
		}
		if err := svc.CreateKB(body.Name, body.Description); err != nil {
			log.Errorf("CreateKB(%q) failed: %v", body.Name, err)
			writeError(w, http.StatusInternalServerError, "create KB failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "created", "name": body.Name})
	})
	mux.HandleFunc("DELETE /api/knowledge-bases/{name}", func(w http.ResponseWriter, r *http.Request) {
		svc := srv.Service()
		log := srv.Logger().WithModule("manage")
		name := r.PathValue("name")
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		if err := svc.ValidateComponent(name); err != nil {
			writeError(w, http.StatusBadRequest, "invalid name: "+err.Error())
			return
		}
		if err := svc.DeleteKB(name); err != nil {
			log.Errorf("DeleteKB(%q) failed: %v", name, err)
			writeError(w, http.StatusInternalServerError, "delete KB failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "deleted", "name": name})
	})

	// ── KB export/import ──
	mux.HandleFunc("GET /api/knowledge-bases/{name}/export", srv.handleKBExport)
	mux.HandleFunc("POST /api/knowledge-bases/import", srv.handleKBImport)

	// ── Models ──
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		svc := srv.Service()
		writeJSON(w, http.StatusOK, map[string]any{
			"embedder":             svc.EmbedderInfo(),
			"reranker":             svc.RerankerInfo(),
			"rerankCandidateLimit": svc.RerankCandidateLimit(),
			"docParser":            knowledge.DocParserInfo(),
		})
	})
	mux.HandleFunc("POST /api/models/probe", srv.handleModelProbe)

	// ── Tasks ──
	mux.HandleFunc("GET /api/tasks/{id}", srv.handleTaskStatus)
	mux.HandleFunc("GET /api/tasks/{id}/events", srv.handleTaskEvents)

	// ── Tombstones ──
	mux.HandleFunc("GET /api/tombstones", srv.handleTombstoneList)
	mux.HandleFunc("DELETE /api/tombstones/{slug}", srv.handleTombstoneRestore)
	mux.HandleFunc("POST /api/tombstones/clean", srv.handleTombstoneClean)

	// ── Reconcile ──
	mux.HandleFunc("POST /api/reconcile", srv.handleReconcile)

	// ── Manifest ──
	mux.HandleFunc("GET /api/documents/{slug}/manifest", srv.handleManifestView)

	// ── Vector ──
	mux.HandleFunc("GET /api/vector-stats", srv.handleVectorStats)
	mux.HandleFunc("GET /api/vector-index", srv.handleVectorIndexInfo)
	mux.HandleFunc("POST /api/rebuild-vectors", srv.handleRebuildVectors)

	// ── GPU scheduler ──
	mux.HandleFunc("GET /api/gpu-scheduler", srv.handleGPUSchedulerStatus)

	// ── Logs & metrics ──
	mux.HandleFunc("GET /api/logs", srv.handleLogs)
	mux.HandleFunc("GET /api/metrics", srv.handleMetrics)

	// ── System info ──
	mux.HandleFunc("GET /api/system-info", srv.handleSystemInfo)

	// ── Middleware ──
	handler := knowledge.CORSMiddleware(mux)
	if cfg := srv.Config(); cfg != nil && cfg.APIToken != "" {
		handler = knowledge.AuthMiddleware(cfg.APIToken)(handler)
	}

	// ── Background goroutines ──
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if tm := srv.TaskManager(); tm != nil {
				tm.Cleanup(30 * time.Minute)
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cleaned, err := srv.Service().CleanExpiredTombstones()
			if err != nil {
				srv.Logger().Warnf("background tombstone cleanup failed: %v", err)
			} else if cleaned > 0 {
				srv.Logger().Infof("background tombstone cleanup: %d expired removed", cleaned)
			}
		}
	}()

	// ── Listen (dual-stack fallback: tcp → tcp4 → tcp6) ──
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		ln, err = net.Listen("tcp4", ":"+port)
	}
	if err != nil {
		ln, err = net.Listen("tcp6", ":"+port)
	}
	if err != nil {
		return fmt.Errorf("listen on :%s: %w", port, err)
	}

	srv.Logger().Infof("management server listening on http://localhost:%s", port)
	return server.Serve(ln)
}
