package knowledge

// List returns metadata for all documents in the knowledge base.
// It is an alias for ListDocuments.
func (s *Store) List() ([]DocumentMeta, error) {
	return s.ListDocuments()
}
