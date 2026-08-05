// Package manage — HTTP router and server startup.
package manage

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"knowledge-mcp/internal/knowledge"
)

//go:embed ui/index.html
var manageUI embed.FS

// BuildMux builds and returns the HTTP handler (mux + middleware) for the
// management API. It does NOT start any background goroutines or listeners.
// Tests can use this to exercise the exact same handler stack as production.
func BuildMux(srv *Server) http.Handler {
	mux := http.NewServeMux()

	// ── Health ──
	mux.HandleFunc("GET /health", srv.handleHealth)
	mux.HandleFunc("GET /api/health", srv.handleHealth)

	// ── UI ──
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := manageUI.ReadFile("ui/index.html")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data) //nolint:errcheck
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

	// Metrics middleware: count every API request.
	next := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		IncrementRequestCounter()
		next.ServeHTTP(w, r)
	})

	if cfg := srv.Config(); cfg != nil && cfg.APIToken != "" {
		handler = knowledge.AuthMiddleware(cfg.APIToken)(handler)
	}

	return handler
}

// Start starts the HTTP management server on the given port.
func Start(srv *Server, port string) error {
	handler := BuildMux(srv)

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
