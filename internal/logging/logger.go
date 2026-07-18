package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level represents a log severity level.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

var levelNames = map[Level]string{
	DEBUG: "DEBUG",
	INFO:  "INFO",
	WARN:  "WARN",
	ERROR: "ERROR",
}

// ParseLevel converts a string to a Level.
// Accepts "debug" (case-insensitive). Everything else defaults to INFO.
func ParseLevel(s string) Level {
	if s == "debug" || s == "DEBUG" {
		return DEBUG
	}
	return INFO
}

// Logger is a thread-safe, level-aware logger that writes to a file.
type Logger struct {
	mu     sync.Mutex
	out    io.WriteCloser
	level  Level
	module string
}

// NewLogger creates a Logger writing to the given log file path.
// The parent directory is created if it does not exist.
// level controls the minimum level to output.
func NewLogger(logPath string, level Level) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return &Logger{
		out:    f,
		level:  level,
		module: "",
	}, nil
}

// NewNopLogger returns a Logger that discards all output.
// Useful for tests or when logging is disabled.
func NewNopLogger() *Logger {
	return &Logger{
		out:   nopCloser{},
		level: ERROR + 1, // nothing passes
	}
}

// SetLevel changes the minimum log level.
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// WithModule returns a copy of the logger scoped to a module name.
// The returned logger shares the same underlying file and level.
func (l *Logger) WithModule(module string) *Logger {
	return &Logger{
		out:    l.out,
		level:  l.level,
		module: module,
	}
}

// log writes a formatted log entry if the level is enabled.
func (l *Logger) log(level Level, format string, args ...interface{}) {
	if level < l.level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	now := time.Now().Format("2006-01-02 15:04:05")
	levelStr := levelNames[level]
	var line string
	if l.module != "" {
		line = fmt.Sprintf("[%s] [%s] [%s] %s\n", now, levelStr, l.module, msg)
	} else {
		line = fmt.Sprintf("[%s] [%s] %s\n", now, levelStr, msg)
	}
	l.mu.Lock()
	_, _ = l.out.Write([]byte(line))
	l.mu.Unlock()
}

// Debugf logs at DEBUG level.
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.log(DEBUG, format, args...)
}

// Infof logs at INFO level.
func (l *Logger) Infof(format string, args ...interface{}) {
	l.log(INFO, format, args...)
}

// Warnf logs at WARN level.
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.log(WARN, format, args...)
}

// Errorf logs at ERROR level.
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.log(ERROR, format, args...)
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	return l.out.Close()
}

// Write implements io.Writer for compatibility with the standard log package.
// Always writes at INFO level.
func (l *Logger) Write(p []byte) (n int, err error) {
	l.log(INFO, "%s", string(p))
	return len(p), nil
}

// nopCloser is a writer that discards everything.
type nopCloser struct{}

func (nopCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopCloser) Close() error               { return nil }

// Ensure standard library log is imported (used by Write's %s formatting).
var _ = log.New(nopCloser{}, "", 0)
