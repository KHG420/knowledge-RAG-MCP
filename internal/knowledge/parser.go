package knowledge

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"knowledge-mcp/internal/logging"
	"github.com/tsawler/tabula"
)

// minerUEnabled controls whether MinerU (magic-pdf CLI) is tried for document
// parsing. It is set by SetMinerUEnabled (called from main during init).
var minerUEnabled bool

// parserLogger is the package-level logger for document parsing operations.
// Set by SetParserLogger (called from Store.SetLogger during init).
var parserLogger = logging.NewNopLogger()

// SetParserLogger configures the logger used by ParseFile and related parser
// functions for reporting MinerU success/failure and fallback events.
func SetParserLogger(l *logging.Logger) {
	parserLogger = l
}

// SetMinerUEnabled enables or disables MinerU-based document parsing.
// MinerU provides high-quality PDF→Markdown extraction with layout retention,
// table recognition, and formula (LaTeX) support.
func SetMinerUEnabled(enabled bool) {
	minerUEnabled = enabled
}

// minerUAvailable reports whether MinerU's magic-pdf CLI is both enabled by
// config AND installed on the system PATH.
func minerUAvailable() bool {
	if !minerUEnabled {
		return false
	}
	_, err := exec.LookPath("magic-pdf")
	return err == nil
}

// parseWithMinerU extracts text from a document using MinerU (magic-pdf CLI).
// It creates a temporary directory, runs magic-pdf to convert the document to
// Markdown, reads the output, and cleans up.
func parseWithMinerU(path string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "mineru-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("magic-pdf", "-p", path, "-o", tmpDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("magic-pdf failed: %w\nstderr: %s", err, strings.TrimSpace(stderr.String()))
	}

	// Find the first .md file magic-pdf produced under tmpDir.
	var mdContent string
	walkErr := filepath.Walk(tmpDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if !info.IsDir() && strings.HasSuffix(p, ".md") {
			data, readErr := os.ReadFile(p)
			if readErr == nil {
				mdContent = string(data)
			}
			return io.EOF // stop after the first .md
		}
		return nil
	})
	if walkErr != nil && walkErr != io.EOF {
		return "", fmt.Errorf("walk mineru output: %w", walkErr)
	}
	if mdContent == "" {
		return "", fmt.Errorf("magic-pdf produced no markdown output")
	}
	return mdContent, nil
}

// ParseFile extracts plain text from a document file.
//
// Supported formats:
//   - MD, TXT — read directly
//   - PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX — via MinerU (when available)
//     with tabula as fallback
//
// Scanned-image PDFs with no embedded text return a descriptive error hinting
// that OCR requires a Tesseract installation and the "ocr" build tag.
func ParseFile(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))

	// Plain-text formats: read directly — they are already text.
	switch ext {
	case ".md", ".txt":
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s file: %w", ext, err)
		}
		return string(data), nil
	}

	// Try MinerU first (when the magic-pdf CLI is installed and enabled).
	if minerUAvailable() {
		parserLogger.Infof("MinerU: parsing %s", filepath.Base(path))
		text, err := parseWithMinerU(path)
		if err == nil {
			parserLogger.Infof("MinerU: success for %s (%d chars)", filepath.Base(path), len(text))
			return strings.TrimSpace(text), nil
		}
		// MinerU failed — fall through to tabula below.
		parserLogger.Warnf("MinerU: failed for %s, falling back to tabula: %v", filepath.Base(path), err)
	} else {
		parserLogger.Debugf("MinerU: not available for %s (enabled=%v)", filepath.Base(path), minerUEnabled)
	}

	// Fall back to tabula for all other supported formats.
	text, warnings, err := tabula.Open(path).Text()
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}

	text = strings.TrimSpace(text)

	// Scanned-image PDF: tabula succeeded but produced no text (all content is
	// image-based). Give the user a clear next step.
	if text == "" && len(warnings) > 0 {
		return "", fmt.Errorf(
			"document appears to be a scanned image with no embedded text — " +
				"OCR requires Tesseract installed and Reasonix built with -tags ocr",
		)
	}

	// Non-fatal warnings are ignored; extraction succeeded.
	_ = warnings
	return text, nil
}
