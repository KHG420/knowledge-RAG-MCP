package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// FileSearchLogger is a SearchLogger implementation that appends JSONL entries
// to a file. Each search event is written as a single JSON line appended to the
// configured path. Write errors are silently discarded to avoid blocking search.
type FileSearchLogger struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// NewFileSearchLogger creates a FileSearchLogger that writes to
// ~/knowledge_base/.searchlog.jsonl.
func NewFileSearchLogger(workspaceRoot string) *FileSearchLogger {
	homeDir, _ := os.UserHomeDir()
	return &FileSearchLogger{
		path: filepath.Join(homeDir, "knowledge_base", ".searchlog.jsonl"),
	}
}

// NewFileSearchLoggerFromStore creates a FileSearchLogger whose log file is
// placed under the Store's knowledge directory (respecting WithDataDir).
func NewFileSearchLoggerFromStore(s *Store) *FileSearchLogger {
	return &FileSearchLogger{
		path: filepath.Join(s.knowledgeDir(), ".searchlog.jsonl"),
	}
}

// LogSearch serialises the entry as a JSON line and appends it to the log file.
// Errors are silently discarded to keep search latency unaffected.
func (l *FileSearchLogger) LogSearch(entry SearchLogEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.f == nil {
		dir := filepath.Dir(l.path)
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return
		}
		f, openErr := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if openErr != nil {
			return
		}
		l.f = f
	}
	l.f.Write(data) //nolint:errcheck
}

// Close releases the underlying file handle. Safe to call multiple times.
func (l *FileSearchLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		err := l.f.Close()
		l.f = nil
		return err
	}
	return nil
}
