// Package manage implements the web management HTTP API extracted from
// Store (REFACTOR_PLAN Phase 3.4). It owns the config, settings, GPU
// scheduler, KB router, and upload task manager references used by the
// management handlers.
package manage

import (
	"sync"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// Server holds the web management layer state and configuration.
type Server struct {
	// ── Core service (Store facade implementing ManageService) ──
	svc knowledge.ManageService // back-reference for handlers (avoids circular dep)

	// ── Runtime configuration ──
	config     *config.Config
	configPath string

	// ── Storage backend (needed for health, export/import, raw chunk access) ──
	backend knowledge.StorageBackend

	// ── Core services (needed for probing, hot-reload, vector index) ──
	embedder    knowledge.Embedder
	reranker    knowledge.Reranker
	vectorIndex interface{} // *knowledge.HNSWIndex (HNSWIndex doesn't satisfy VectorIndex due to Remove signature)

	// ── KB identity (needed by handlers that reference current KB) ──
	kbName  string
	dataDir string

	// ── GPU scheduler (shared reference) ──
	gpuScheduler *knowledge.GPUScheduler

	// ── KB router (shared reference) ──
	kbRouter *knowledge.KBRouter

	// ── Upload task manager (shared reference) ──
	taskManager *knowledge.UploadTaskManager

	// ── Infrastructure ──
	logger *logging.Logger
	mu     *sync.Mutex
}

// New creates a Server with the given configuration.
func New(
	svc knowledge.ManageService,
	cfg *config.Config, cfgPath string,
	backend knowledge.StorageBackend,
	embedder knowledge.Embedder,
	reranker knowledge.Reranker,
	vectorIndex interface{},
	kbName, dataDir string,
	gpuSched *knowledge.GPUScheduler,
	router *knowledge.KBRouter,
	taskMgr *knowledge.UploadTaskManager,
	mu *sync.Mutex,
	logger *logging.Logger,
) *Server {
	return &Server{
		svc:          svc,
		config:       cfg,
		configPath:   cfgPath,
		backend:      backend,
		embedder:     embedder,
		reranker:     reranker,
		vectorIndex:  vectorIndex,
		kbName:       kbName,
		dataDir:      dataDir,
		gpuScheduler: gpuSched,
		kbRouter:     router,
		taskManager:  taskMgr,
		logger:       logger,
		mu:           mu,
	}
}

// ── Accessors ────────────────────────────────────────────────────────────────

func (s *Server) Service() knowledge.ManageService              { return s.svc }
func (s *Server) Config() *config.Config                        { return s.config }
func (s *Server) SetConfig(cfg *config.Config)                  { s.config = cfg }
func (s *Server) ConfigPath() string                            { return s.configPath }
func (s *Server) SetConfigPath(p string)                        { s.configPath = p }
func (s *Server) Backend() knowledge.StorageBackend             { return s.backend }
func (s *Server) Embedder() knowledge.Embedder                  { return s.embedder }
func (s *Server) SetEmbedder(e knowledge.Embedder)              { s.embedder = e }
func (s *Server) Reranker() knowledge.Reranker                  { return s.reranker }
func (s *Server) SetReranker(r knowledge.Reranker)              { s.reranker = r }
func (s *Server) VectorIndex() interface{}                { return s.vectorIndex }
func (s *Server) SetVectorIndex(vi interface{})           { s.vectorIndex = vi }
func (s *Server) KBName() string                                { return s.kbName }
func (s *Server) SetKBName(n string)                            { s.kbName = n }
func (s *Server) DataDir() string                               { return s.dataDir }
func (s *Server) SetDataDir(d string)                           { s.dataDir = d }
func (s *Server) GPUScheduler() *knowledge.GPUScheduler         { return s.gpuScheduler }
func (s *Server) SetGPUScheduler(gs *knowledge.GPUScheduler)    { s.gpuScheduler = gs }
func (s *Server) KBRouter() *knowledge.KBRouter                 { return s.kbRouter }
func (s *Server) SetKBRouter(r *knowledge.KBRouter)             { s.kbRouter = r }
func (s *Server) TaskManager() *knowledge.UploadTaskManager      { return s.taskManager }
func (s *Server) SetTaskManager(tm *knowledge.UploadTaskManager) { s.taskManager = tm }
func (s *Server) Logger() *logging.Logger                       { return s.logger }
func (s *Server) SetLogger(l *logging.Logger)                   { s.logger = l }
func (s *Server) Mutex() *sync.Mutex                            { return s.mu }
