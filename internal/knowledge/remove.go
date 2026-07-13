package knowledge

import (
	"fmt"
	"os"
	"strings"
)

// RemoveDocument deletes a document's directory from the knowledge base and
// removes its entry from INDEX.md. It is a no-op (no error) if the document
// does not exist.
func (s *Store) RemoveDocument(slug string) error {
	// Read current metadata for the INDEX.md update message.
	meta, metaErr := s.ReadMeta(slug)

	// Remove the directory.
	if err := s.removeDir(slug); err != nil {
		return err
	}

	// Remove the line from INDEX.md (best-effort).
	if metaErr == nil {
		s.removeFromIndex(slug, meta)
	}
	return nil
}

// removeDir deletes the document directory.
func (s *Store) removeDir(slug string) error {
	// Delegates to the lower-level implementation. The original implementation
	// in store.go was inlined; we keep the logic here.
	dir := s.DocDir(slug)
	if err := osRemoveAll(dir); err != nil {
		return fmt.Errorf("remove document %q: %w", slug, err)
	}
	return nil
}

// removeFromIndex rewrites INDEX.md without the entry for the given slug.
func (s *Store) removeFromIndex(slug string, meta DocumentMeta) {
	existing, err := s.ReadIndex()
	if err != nil || existing == "" {
		return
	}
	// The line format is: - [name](slug/meta.json) ...
	marker := "(" + slug + "/meta.json)"
	lines := strings.Split(existing, "\n")
	var kept []string
	for _, line := range lines {
		if strings.Contains(line, marker) {
			continue
		}
		kept = append(kept, line)
	}
	_ = s.WriteIndex(strings.Join(kept, "\n"))
}

// osRemoveAll is a variable so tests can replace it. It wraps os.RemoveAll.
var osRemoveAll = os.RemoveAll
