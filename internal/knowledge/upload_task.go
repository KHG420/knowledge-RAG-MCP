package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"knowledge-mcp/internal/logging"
)

// UploadTask represents a single file upload processing task.
// The task is created immediately when the file is received and runs
// asynchronously in a goroutine, allowing the client to subscribe to
// progress via SSE or poll for status.
type UploadTask struct {
	ID        string    `json:"id"`
	FileName  string    `json:"fileName"`
	KBName    string    `json:"kbName"`
	Status    string    `json:"status"` // pending | processing | done | error
	Slug      string    `json:"slug,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`

	mu     sync.RWMutex
	mgr    *UploadTaskManager // for disk persistence
	tmpDir string             // temporary directory holding the uploaded file
	events []ProgressEvent    // full event log, for SSE replay on reconnect
	logger *logging.Logger
	doneCh chan struct{} // closed when Status reaches done or error
}

// RecordEvent appends a progress event to the task's event log.
// It is safe for concurrent calls from the processing goroutine and
// SSE subscriber readers.
func (t *UploadTask) RecordEvent(ev ProgressEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, ev)
}

// Events returns a copy of all recorded events.
// Safe for concurrent read access.
func (t *UploadTask) Events() []ProgressEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	cp := make([]ProgressEvent, len(t.events))
	copy(cp, t.events)
	return cp
}

// Snapshot returns a consistent, thread-safe copy of the task's mutable
// fields (Status, Slug, Error) for readers that run concurrently with
// MarkDone / MarkError / processing goroutines.
func (t *UploadTask) Snapshot() (status, slug, errMsg string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status, t.Slug, t.Error
}

// MarkDone transitions the task to done status with the given slug
// and cleans up the temporary directory.
func (t *UploadTask) MarkDone(slug string) {
	t.mu.Lock()
	t.Status = "done"
	t.Slug = slug
	t.cleanupLocked()
	close(t.doneCh)
	t.mu.Unlock()
	if t.mgr != nil {
		t.mgr.saveTask(t)
	}
}

// MarkError transitions the task to error status with the given error
// and cleans up the temporary directory.
func (t *UploadTask) MarkError(err error) {
	t.mu.Lock()
	t.Status = "error"
	t.Error = err.Error()
	t.cleanupLocked()
	close(t.doneCh)
	t.mu.Unlock()
	if t.mgr != nil {
		t.mgr.saveTask(t)
	}
}

// Done returns a channel that is closed when the task reaches a terminal state.
func (t *UploadTask) Done() <-chan struct{} {
	return t.doneCh
}

// cleanupLocked removes the temporary directory holding the uploaded file.
// Must be called with t.mu write lock held.
func (t *UploadTask) cleanupLocked() {
	if t.tmpDir != "" {
		if err := os.RemoveAll(t.tmpDir); err != nil {
			t.logger.Warnf("Task %s: cleanup tmp dir %s: %v", t.ID, t.tmpDir, err)
		}
		t.tmpDir = ""
	}
}

// ---------------------------------------------------------------------------
// TaskManager
// ---------------------------------------------------------------------------

// UploadTaskManager manages all active and recent upload tasks.
// Tasks are persisted to disk under a tasks/ subdirectory so they survive
// service restarts.
type UploadTaskManager struct {
	mu     sync.RWMutex
	tasks  map[string]*UploadTask
	dir    string // directory for task persistence (tasks/*.json)
	logger *logging.Logger
}

// NewUploadTaskManager creates a new task manager with disk persistence
// in the given directory. Pass an empty string to disable persistence.
func NewUploadTaskManager(dir string, logger *logging.Logger) *UploadTaskManager {
	m := &UploadTaskManager{
		tasks:  make(map[string]*UploadTask),
		dir:    dir,
		logger: logger,
	}
	// Load persisted tasks from disk.
	for _, t := range m.loadTasks() {
		m.tasks[t.ID] = t
	}
	if len(m.tasks) > 0 {
		logger.Infof("TaskManager: restored %d tasks from %s", len(m.tasks), dir)
	}
	return m
}

// taskDir returns the full path to the tasks directory.
func (m *UploadTaskManager) taskDir() string {
	return m.dir
}

// saveTask persists a single task to disk as JSON.
// Thread-safe: serializes only the exported JSON fields.
func (m *UploadTaskManager) saveTask(task *UploadTask) {
	if m.dir == "" {
		return // persistence disabled
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		m.logger.Warnf("TaskManager: create tasks dir %s: %v", m.dir, err)
		return
	}
	data, err := json.Marshal(task)
	if err != nil {
		m.logger.Warnf("TaskManager: marshal task %s: %v", task.ID, err)
		return
	}
	path := filepath.Join(m.dir, task.ID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		m.logger.Warnf("TaskManager: write task %s: %v", task.ID, err)
	}
}

// loadTasks reads all persisted task files from disk. Terminal tasks
// (done/error) are restored as-is. Non-terminal tasks (pending/processing)
// cannot be resumed: the goroutine that was processing them no longer exists,
// and the temporary upload directory does not survive a restart. Instead of
// silently deleting user-visible history, they are transitioned to error with
// explicit re-upload guidance, their Done() channel is closed, and the
// transition is persisted so the result is stable across repeated reloads.
func (m *UploadTaskManager) loadTasks() []*UploadTask {
	if m.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		m.logger.Warnf("TaskManager: read tasks dir %s: %v", m.dir, err)
		return nil
	}
	var tasks []*UploadTask
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(m.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			m.logger.Warnf("TaskManager: read %s: %v", path, err)
			continue
		}
		var t UploadTask
		if err := json.Unmarshal(data, &t); err != nil {
			m.logger.Warnf("TaskManager: unmarshal %s: %v", path, err)
			continue
		}
		// Re-initialize runtime-only fields.
		t.mu = sync.RWMutex{}
		t.mgr = m
		t.doneCh = make(chan struct{})
		t.events = nil
		t.logger = m.logger
		t.tmpDir = "" // temp dirs do not survive restart

		// Non-terminal tasks: the processing goroutine is gone and the task
		// cannot be resumed. Preserve the record as a terminal error so the
		// user can see what was interrupted and re-upload the file.
		if t.Status != "done" && t.Status != "error" {
			previous := t.Status
			t.Status = "error"
			t.Error = fmt.Sprintf(
				"upload interrupted by a service restart (previous status: %q); "+
					"the uploaded file was not completed and cannot be resumed — re-upload it to retry",
				previous,
			)
			m.logger.Warnf("TaskManager: marked interrupted task %s (%q) as error", t.ID, t.FileName)
			close(t.doneCh)
			tasks = append(tasks, &t)
			m.saveTask(&t) // persist the terminal transition
			continue
		}

		// Terminal tasks get a closed doneCh so Done() never blocks.
		close(t.doneCh)

		tasks = append(tasks, &t)
	}
	return tasks
}

// deleteTaskFile removes the persisted task file from disk.
func (m *UploadTaskManager) deleteTaskFile(id string) {
	if m.dir == "" {
		return
	}
	path := filepath.Join(m.dir, id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		m.logger.Warnf("TaskManager: remove task file %s: %v", path, err)
	}
}

// Create registers a new upload task. The task ID is a random unique string.
func (m *UploadTaskManager) Create(fileName, kbName, tmpDir string) *UploadTask {
	task := &UploadTask{
		ID:        generateTaskID(),
		FileName:  fileName,
		KBName:    kbName,
		Status:    "pending",
		CreatedAt: time.Now(),
		mgr:       m,
		tmpDir:    tmpDir,
		logger:    m.logger,
		doneCh:    make(chan struct{}),
	}
	m.mu.Lock()
	m.tasks[task.ID] = task
	m.mu.Unlock()
	m.saveTask(task)
	m.logger.Infof("TaskManager: created task %s for %q kb=%q", task.ID, fileName, kbName)
	return task
}

// Get returns a task by ID, or nil if not found.
func (m *UploadTaskManager) Get(id string) *UploadTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tasks[id]
}

// Delete removes a task from the manager by ID.
func (m *UploadTaskManager) Delete(id string) {
	m.mu.Lock()
	delete(m.tasks, id)
	m.mu.Unlock()
	m.deleteTaskFile(id)
}

// Cleanup removes tasks that are in a terminal state (done/error) and
// older than maxAge. Called periodically from a background goroutine.
func (m *UploadTaskManager) Cleanup(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.tasks {
		t.mu.RLock()
		terminal := t.Status == "done" || t.Status == "error"
		old := t.CreatedAt.Before(cutoff)
		t.mu.RUnlock()
		if terminal && old {
			delete(m.tasks, id)
			m.deleteTaskFile(id)
			m.logger.Debugf("TaskManager: cleaned up task %s", id)
		}
	}
}

// ListActive returns all tasks that are not in a terminal state.
func (m *UploadTaskManager) ListActive() []*UploadTask {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var active []*UploadTask
	for _, t := range m.tasks {
		t.mu.RLock()
		terminal := t.Status == "done" || t.Status == "error"
		t.mu.RUnlock()
		if !terminal {
			active = append(active, t)
		}
	}
	return active
}

// generateTaskID creates a short unique task identifier.
func generateTaskID() string {
	// Use timestamp + nanosecond prefix for uniqueness without crypto rand.
	now := time.Now()
	return fmt.Sprintf("task-%d-%d", now.UnixMilli(), now.Nanosecond()%100000)
}
