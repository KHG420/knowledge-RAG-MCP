package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ManagePort == "" {
		t.Error("expected non-empty ManagePort")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for missing file")
	}
}

func TestLoad_ValidTOML(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	content := `
data_dir = "/tmp/kb-test"
manage_port = "9090"
log_level = "debug"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DataDir != "/tmp/kb-test" {
		t.Errorf("expected DataDir=/tmp/kb-test, got %q", cfg.DataDir)
	}
	if cfg.ManagePort != "9090" {
		t.Errorf("expected ManagePort=9090, got %q", cfg.ManagePort)
	}
}

func TestLoad_InvalidTOML(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("not valid toml {{{"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestSave_Roundtrip(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	cfg := &Config{
		DataDir:    "/tmp/kb-roundtrip",
		ManagePort: "8080",
		LogLevel:   "info",
	}
	if err := Save(cfgPath, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	loaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.DataDir != cfg.DataDir {
		t.Errorf("DataDir mismatch: got %q, want %q", loaded.DataDir, cfg.DataDir)
	}
	if loaded.ManagePort != cfg.ManagePort {
		t.Errorf("ManagePort mismatch: got %q, want %q", loaded.ManagePort, cfg.ManagePort)
	}
}

func TestLoadWithEnvFallback(t *testing.T) {
	// MANAGE_PORT env var overrides default ManagePort
	os.Setenv("MANAGE_PORT", "9999")
	defer os.Unsetenv("MANAGE_PORT")

	cfg := LoadWithEnvFallback("/nonexistent/path/config.toml")
	if cfg.ManagePort != "9999" {
		t.Errorf("expected ManagePort from env=9999, got %q", cfg.ManagePort)
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "subdir", "nested", "config.toml")
	cfg := DefaultConfig()
	cfg.DataDir = "/tmp/kb-save-test"
	if err := Save(cfgPath, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}
