package knowledge

import (
	"os"
	"strings"
)

// RemoveDocument deletes a document's directory from the knowledge base and
// removes its entry from INDEX.md. It is a no-op (no error) if the document
// does not exist.
func (s *Store) RemoveDocument(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	log := s.logger.WithModule("remove")
	log.Infof("RemoveDocument slug=%q kb=%q", slug, s.kbName)
	// Read current metadata for the INDEX.md update message.
	meta, metaErr := s.ReadMeta(slug)

	// Remove the directory.
	if err := s.removeDir(slug); err != nil {
		log.Errorf("RemoveDocument %q failed: %v", slug, err)
		return err
	}

	// Remove the line from INDEX.md (best-effort).
	if metaErr == nil {
		s.removeFromIndex(slug, meta)
	}
	log.Infof("RemoveDocument %q done", slug)
	return nil
}

// removeDir delegates document removal to the backend.
func (s *Store) removeDir(slug string) error {
	return s.backend.RemoveDocument(s.kbName, slug)
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
