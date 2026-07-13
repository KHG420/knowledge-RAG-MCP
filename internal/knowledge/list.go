package knowledge

// List returns metadata for all documents in the knowledge base.
// It is an alias for ListDocuments.
func (s *Store) List() ([]DocumentMeta, error) {
	return s.ListDocuments()
}

// ListPreview returns up to n documents for display, using the snapshot cache.
// When the knowledge base hasn't changed, it reads from the cached snapshot
// file instead of re-scanning all meta.json files. The full list is always
// persisted to the snapshot file when changes are detected.
func (s *Store) ListPreview(n int) (display []DocumentMeta, full []DocumentMeta, err error) {
	full, err = s.ListWithSnapshot()
	if err != nil {
		return nil, nil, err
	}
	if len(full) > n {
		display = full[:n]
	} else {
		display = full
	}
	return display, full, nil
}
