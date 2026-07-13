package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tsawler/tabula"
)

// ParseFile extracts plain text from a document file.
//
// Supported formats:
//   - PDF, DOCX, ODT, EPUB, HTML, XLSX, PPTX — via tabula
//   - MD, TXT — read directly
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

	// All other supported formats go through tabula.
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
