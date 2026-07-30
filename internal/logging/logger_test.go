package logging

import (
	"os"
	"strings"
	"testing"
)

func TestParseLevel_Debug(t *testing.T) {
	if ParseLevel("debug") != DEBUG {
		t.Error("expected DEBUG")
	}
}

func TestParseLevel_Info(t *testing.T) {
	if ParseLevel("info") != INFO {
		t.Error("expected INFO")
	}
}

func TestParseLevel_Warn(t *testing.T) {
	if ParseLevel("warn") != INFO {
		t.Error("expected INFO (warn maps to INFO)")
	}
}

func TestParseLevel_CaseInsensitive(t *testing.T) {
	if ParseLevel("DEBUG") != DEBUG {
		t.Error("expected DEBUG for uppercase")
	}
}

func TestParseLevel_Default(t *testing.T) {
	if ParseLevel("unknown") != INFO {
		t.Error("expected INFO for unknown level")
	}
	if ParseLevel("") != INFO {
		t.Error("expected INFO for empty string")
	}
}

func TestNewLogger_InvalidPath(t *testing.T) {
	_, err := NewLogger("/nonexistent/path/should/fail.log", DEBUG)
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestLogger_WithModule(t *testing.T) {
	tmp := t.TempDir()
	logPath := tmp + "/test.log"
	l, err := NewLogger(logPath, DEBUG)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	child := l.WithModule("child")
	child.Infof("test message")
	// Verify the child has the right module name
	if child.module != "child" {
		t.Errorf("expected module 'child', got %q", child.module)
	}
	// Verify the file has content
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "test message") {
		t.Errorf("expected 'test message' in log, got %q", string(data))
	}
}

func TestNopLogger_NoPanic(t *testing.T) {
	l := NewNopLogger()
	// Should not panic
	l.Debugf("test")
	l.Infof("test")
	l.Warnf("test")
	l.Errorf("test")
	l.WithModule("x").Debugf("test")
}

func TestLogger_LevelFiltering(t *testing.T) {
	tmp := t.TempDir()
	logPath := tmp + "/filter.log"
	l, err := NewLogger(logPath, WARN)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	l.Debugf("debug msg")
	l.Infof("info msg")
	l.Warnf("warn msg")
	data, _ := os.ReadFile(logPath)
	s := string(data)
	if strings.Contains(s, "debug") {
		t.Errorf("expected debug to be filtered out")
	}
	if strings.Contains(s, "info") {
		t.Errorf("expected info to be filtered out")
	}
	if !strings.Contains(s, "warn") {
		t.Errorf("expected warn in output")
	}
}

func TestSetLevel(t *testing.T) {
	tmp := t.TempDir()
	logPath := tmp + "/setlevel.log"
	l, err := NewLogger(logPath, INFO)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	l.Debugf("should be filtered")
	l.SetLevel(DEBUG)
	l.Debugf("should appear")
	data, _ := os.ReadFile(logPath)
	s := string(data)
	if strings.Contains(s, "should be filtered") {
		t.Errorf("debug message before SetLevel should be filtered")
	}
	if !strings.Contains(s, "should appear") {
		t.Errorf("debug message after SetLevel should appear")
	}
}

func TestWrite_ImplementsWriter(t *testing.T) {
	tmp := t.TempDir()
	logPath := tmp + "/write.log"
	l, err := NewLogger(logPath, INFO)
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	// Logger implements io.Writer
	n, err := l.Write([]byte("direct write\n"))
	if err != nil {
		t.Errorf("Write failed: %v", err)
	}
	if n <= 0 {
		t.Error("expected bytes written")
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "direct write") {
		t.Errorf("expected 'direct write' in log, got %q", string(data))
	}
}
