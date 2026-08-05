// Package kb manages knowledge-base lifecycle and routing. Extracted from
// Store (REFACTOR_PLAN Phase 3 — KBAdmin).
//
// Engine implements knowledge.KBAdmin.
package kb

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"knowledge-mcp/internal/cache"
	"knowledge-mcp/internal/knowledge"
	"knowledge-mcp/internal/logging"
)

// Engine manages knowledge-base CRUD and intelligent routing.
type Engine struct {
	// ── Storage ──
	backend knowledge.StorageBackend

	// ── KB routing ──
	kbRouter *knowledge.KBRouter

	// ── Cache ──
	cacheClient     knowledge.CacheClient
	kbListCacheTTL  time.Duration

	// ── Infrastructure ──
	logger *logging.Logger
	mu     *sync.Mutex
}

// New creates a KB Admin Engine.
func New(
	backend knowledge.StorageBackend,
	mu *sync.Mutex,
	logger *logging.Logger,
) *Engine {
	return &Engine{
		backend: backend,
		logger:  logger,
		mu:      mu,
	}
}

// ── Accessors ────────────────────────────────────────────────────────────────

func (e *Engine) SetKBRouter(r *knowledge.KBRouter)   { e.kbRouter = r }
func (e *Engine) KBRouter() *knowledge.KBRouter        { return e.kbRouter }
func (e *Engine) SetCacheClient(c knowledge.CacheClient) { e.cacheClient = c }
func (e *Engine) SetKBListCacheTTL(d time.Duration)    { e.kbListCacheTTL = d }
func (e *Engine) Logger() *logging.Logger               { return e.logger }
func (e *Engine) SetLogger(l *logging.Logger)           { e.logger = l }
func (e *Engine) Mutex() *sync.Mutex                    { return e.mu }

// ── Helpers ──────────────────────────────────────────────────────────────────

func (e *Engine) cacheEnabled() bool { return e.cacheClient != nil && !cache.IsNoop(e.cacheClient) }

// NOTE: All cache operations (Get/Set/Delete/DeletePattern) in this file
// intentionally use context.Background() because cache reads and writes
// are best-effort and insensitive to request cancellation — a cancelled
// request should not prevent cache invalidation or population.

// InvalidateKBList removes the cached KB list (used after create/delete KB).
func (e *Engine) InvalidateKBList() {
	if !e.cacheEnabled() {
		return
	}
	log := e.logger.WithModule("cache")
	if err := e.cacheClient.Delete(context.Background(), cache.KBListKey()); err != nil {
		log.Warnf("invalidate kblist FAILED: err=%v", err)
	} else {
		log.Debugf("invalidate kblist: deleted")
	}
}

// ── KBAdmin interface implementation ─────────────────────────────────────────

// ListKBs returns knowledge base names from the backend, with Redis cache.
func (e *Engine) ListKBs() ([]string, error) {
	// Try cache first.
	if e.cacheEnabled() {
		key := cache.KBListKey()
		if raw, err := e.cacheClient.Get(context.Background(), key); err == nil && raw != nil {
			var names []string
			if json.Unmarshal(raw, &names) == nil {
				e.logger.WithModule("cache").Debugf("kblist HIT: %d KBs", len(names))
				return names, nil
			}
		}
		e.logger.WithModule("cache").Debugf("kblist MISS")
	}

	kbs, err := e.backend.ListKBs()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(kbs))
	for i, kb := range kbs {
		names[i] = kb.Name
	}

	// Store in cache.
	if e.cacheEnabled() {
		key := cache.KBListKey()
		if raw, jerr := json.Marshal(names); jerr == nil {
			if setErr := e.cacheClient.Set(context.Background(), key, raw, e.kbListCacheTTL); setErr != nil {
				e.logger.WithModule("cache").Warnf("kblist SET failed: err=%v", setErr)
			} else {
				e.logger.WithModule("cache").Debugf("kblist SET: %d KBs ttl=%v", len(names), e.kbListCacheTTL)
			}
		}
	}

	return names, nil
}

// ListKBsInfo delegates to the backend and returns full KBInfo records.
func (e *Engine) ListKBsInfo() ([]knowledge.KBInfo, error) {
	return e.backend.ListKBs()
}

// CreateKB delegates to the backend and invalidates the KB list cache.
func (e *Engine) CreateKB(name, description string) error {
	e.logger.Infof("KB %q: creating", name)
	if err := e.backend.CreateKB(name, description); err != nil {
		e.logger.Errorf("KB %q: create failed: %v", name, err)
		return err
	}
	e.InvalidateKBList()
	// Also invalidate any query cache entries for this KB.
	if e.cacheEnabled() {
		qpattern := cache.DocQueryInvalidatePattern(name)
		if qn, qerr := e.cacheClient.DeletePattern(context.Background(), qpattern); qerr != nil {
			e.logger.WithModule("cache").Warnf("invalidate query cache for deleted KB %q: %v", name, qerr)
		} else if qn > 0 {
			e.logger.WithModule("cache").Infof("invalidate query cache for deleted KB %q: deleted %d keys", name, qn)
		}
	}
	e.logger.Infof("KB %q: created (description=%q)", name, description)
	return nil
}

// DeleteKB delegates to the backend and invalidates the KB list cache.
func (e *Engine) DeleteKB(name string) error {
	e.logger.Infof("KB %q: deleting", name)
	if err := e.backend.DeleteKB(name); err != nil {
		e.logger.Errorf("KB %q: delete failed: %v", name, err)
		return err
	}
	e.InvalidateKBList()
	// Also invalidate any query cache entries for this KB.
	if e.cacheEnabled() {
		qpattern := cache.DocQueryInvalidatePattern(name)
		if qn, qerr := e.cacheClient.DeletePattern(context.Background(), qpattern); qerr != nil {
			e.logger.WithModule("cache").Warnf("invalidate query cache for deleted KB %q: %v", name, qerr)
		} else if qn > 0 {
			e.logger.WithModule("cache").Infof("invalidate query cache for deleted KB %q: deleted %d keys", name, qn)
		}
	}
	e.logger.Infof("KB %q: deleted", name)
	return nil
}

// RouteKBs uses the configured KB router to select the best 1–3 KBs for the
// query. Returns nil when no router is configured (caller should fall back to
// cross-KB search).
func (e *Engine) RouteKBs(ctx context.Context, query string) []string {
	if e.kbRouter == nil {
		return nil
	}
	result := e.kbRouter.Route(ctx, query, nil)
	if result == nil {
		return nil
	}
	return result.Selected
}

// SyncKBRouterDescs refreshes the KB router's cached name/description list
// from the backend. Call after CreateKB or DeleteKB.
func (e *Engine) SyncKBRouterDescs() {
	if e.kbRouter == nil {
		return
	}
	kbs, err := e.backend.ListKBs()
	if err != nil {
		e.logger.Warnf("kb router: failed to sync KB descriptions: %v", err)
		return
	}
	descs := make([]knowledge.KBDesc, len(kbs))
	for i, kb := range kbs {
		descs[i] = knowledge.KBDesc{Name: kb.Name, Desc: kb.Description}
	}
	e.kbRouter.SetKBDescs(descs)
}

// Ensure knowledge.KBAdmin interface is satisfied.
var _ knowledge.KBAdmin = (*Engine)(nil)
