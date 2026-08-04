package knowledge

import (
	"sync"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/logging"
)

// ManageServer holds the web management layer state and configuration.
// HTTP handlers (manage.go + manage_enhanced.go) use this as a shared state
// container. When manage/ subpackage migration is complete, this type will
// become an interface backed by manage.Server.
type ManageServer struct {
	config     *config.Config
	configPath string
	settings   *storeSettings

	gpuScheduler *GPUScheduler
	kbRouter     *KBRouter
	taskManager  *UploadTaskManager

	logger *logging.Logger
	mu     *sync.Mutex
}

// NewManageServer creates a ManageServer.
func NewManageServer(
	cfg *config.Config, cfgPath string,
	settings *storeSettings,
	gpuSched *GPUScheduler,
	router *KBRouter,
	taskMgr *UploadTaskManager,
	mu *sync.Mutex,
	logger *logging.Logger,
) *ManageServer {
	return &ManageServer{
		config:       cfg,
		configPath:   cfgPath,
		settings:     settings,
		gpuScheduler: gpuSched,
		kbRouter:     router,
		taskManager:  taskMgr,
		logger:       logger,
		mu:           mu,
	}
}
