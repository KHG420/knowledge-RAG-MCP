package knowledge

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// MySQLBackend implements StorageBackend using a MySQL-compatible database.
// All document data (metadata, chunks, indices) is stored in relational tables.
type MySQLBackend struct {
	db *sql.DB
	// Connection parameters (kept for reconnection if needed)
	dsn string
}

// MySQLBackendConfig holds the parameters for creating a new MySQLBackend.
type MySQLBackendConfig struct {
	// DSN is the MySQL Data Source Name.
	// If empty, it is built from the other fields.
	DSN string

	// Individual connection parameters (used when DSN is empty):
	User     string
	Password string
	Host     string
	Port     string
	Database string

	// SocketPath overrides Host/Port for Unix socket connections.
	SocketPath string
}

// defaultMySQLConfig returns sensible defaults for a local MySQL connection.
func defaultMySQLConfig() MySQLBackendConfig {
	return MySQLBackendConfig{
		User:     "root",
		Password: "",
		Host:     "127.0.0.1",
		Port:     "3306",
		Database: "knowledge_rag",
	}
}

// dsn builds the MySQL DSN from the config.
func (cfg MySQLBackendConfig) dsn() string {
	if cfg.DSN != "" {
		return cfg.DSN
	}
	mcfg := mysql.Config{
		User:                 cfg.User,
		Passwd:               cfg.Password,
		Net:                  "tcp",
		Addr:                 cfg.Host + ":" + cfg.Port,
		DBName:               cfg.Database,
		ParseTime:            true,
		AllowNativePasswords: true,
		MultiStatements:      true,
	}
	if cfg.SocketPath != "" {
		mcfg.Net = "unix"
		mcfg.Addr = cfg.SocketPath
	}
	return mcfg.FormatDSN()
}

// NewMySQLBackend creates a MySQLBackend and connects to the database.
// The connection is tested immediately; if it fails, an error is returned.
func NewMySQLBackend(cfg MySQLBackendConfig) (*MySQLBackend, error) {
	dsn := cfg.dsn()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("mysql open: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("mysql ping: %w", err)
	}
	return &MySQLBackend{db: db, dsn: dsn}, nil
}

// ── Lifecycle ─────────────────────────────────────────────────────────────────

func (mb *MySQLBackend) Init() error {
	schema := `
	CREATE TABLE IF NOT EXISTS knowledge_bases (
		name VARCHAR(255) PRIMARY KEY,
		description TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS documents (
		slug VARCHAR(255) NOT NULL,
		kb_name VARCHAR(255) NOT NULL,
		original_name VARCHAR(512) NOT NULL DEFAULT '',
		source_type VARCHAR(50) NOT NULL DEFAULT '',
		added_at TIMESTAMP NULL,
		chunk_count INT NOT NULL DEFAULT 0,
		total_chars INT NOT NULL DEFAULT 0,
		title VARCHAR(512) NOT NULL DEFAULT '',
		authors TEXT,
		abstract TEXT,
		is_paper BOOLEAN NOT NULL DEFAULT FALSE,
		tags TEXT,
		raw_text LONGTEXT,
		source_data LONGBLOB,
		source_ext VARCHAR(50) NOT NULL DEFAULT '',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		PRIMARY KEY (kb_name, slug)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS chunks (
		kb_name VARCHAR(255) NOT NULL,
		doc_slug VARCHAR(255) NOT NULL,
		chunk_id VARCHAR(10) NOT NULL,
		content LONGTEXT NOT NULL,
		PRIMARY KEY (kb_name, doc_slug, chunk_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS section_chunks (
		kb_name VARCHAR(255) NOT NULL,
		doc_slug VARCHAR(255) NOT NULL,
		section_id VARCHAR(10) NOT NULL,
		content LONGTEXT NOT NULL,
		PRIMARY KEY (kb_name, doc_slug, section_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS chunks_index (
		kb_name VARCHAR(255) NOT NULL,
		doc_slug VARCHAR(255) NOT NULL,
		index_data JSON NOT NULL,
		checksum VARCHAR(64) NOT NULL DEFAULT '',
		PRIMARY KEY (kb_name, doc_slug)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS inverted_index (
		kb_name VARCHAR(255) NOT NULL,
		term VARCHAR(191) NOT NULL,
		doc_slug VARCHAR(255) NOT NULL,
		chunk_id VARCHAR(10) NOT NULL,
		tf INT NOT NULL DEFAULT 0,
		PRIMARY KEY (kb_name, term, doc_slug, chunk_id),
		INDEX idx_term (kb_name, term)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

	CREATE TABLE IF NOT EXISTS list_snapshots (
		kb_name VARCHAR(255) PRIMARY KEY,
		checksum VARCHAR(64) NOT NULL DEFAULT '',
		snapshot_data JSON,
		updated_at TIMESTAMP NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`
	_, err := mb.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("mysql init schema: %w", err)
	}
	return nil
}

func (mb *MySQLBackend) Close() error {
	return mb.db.Close()
}

// ── KB operations ─────────────────────────────────────────────────────────────

func (mb *MySQLBackend) ListKBs() ([]KBInfo, error) {
	rows, err := mb.db.Query("SELECT name, COALESCE(description,'') FROM knowledge_bases ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list KBs: %w", err)
	}
	defer rows.Close()
	var kbs []KBInfo
	for rows.Next() {
		var info KBInfo
		if err := rows.Scan(&info.Name, &info.Description); err != nil {
			return nil, fmt.Errorf("scan KB: %w", err)
		}
		kbs = append(kbs, info)
	}
	return kbs, rows.Err()
}

func (mb *MySQLBackend) CreateKB(name, description string) error {
	_, err := mb.db.Exec(
		"INSERT INTO knowledge_bases (name, description) VALUES (?, ?) ON DUPLICATE KEY UPDATE description=VALUES(description)",
		name, description)
	if err != nil {
		return fmt.Errorf("create KB: %w", err)
	}
	return nil
}

func (mb *MySQLBackend) DeleteKB(name string) error {
	// CASCADE should handle related rows, but we clean up explicitly for safety.
	_, _ = mb.db.Exec("DELETE FROM list_snapshots WHERE kb_name = ?", name)
	_, _ = mb.db.Exec("DELETE FROM inverted_index WHERE kb_name = ?", name)
	_, _ = mb.db.Exec("DELETE FROM chunks_index WHERE kb_name = ?", name)
	_, _ = mb.db.Exec("DELETE FROM section_chunks WHERE kb_name = ?", name)
	_, _ = mb.db.Exec("DELETE FROM chunks WHERE kb_name = ?", name)
	_, _ = mb.db.Exec("DELETE FROM documents WHERE kb_name = ?", name)
	_, err := mb.db.Exec("DELETE FROM knowledge_bases WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("delete KB: %w", err)
	}
	return nil
}

// ── Document metadata ─────────────────────────────────────────────────────────

func (mb *MySQLBackend) ReadMeta(kbName, slug string) (*DocumentMeta, error) {
	var meta DocumentMeta
	var addedAt sql.NullTime
	var authors, abstract, tags sql.NullString
	err := mb.db.QueryRow(
		`SELECT slug, original_name, source_type, added_at, chunk_count, total_chars,
		        COALESCE(title,''), authors, abstract, is_paper, tags
		 FROM documents WHERE kb_name = ? AND slug = ?`,
		kbName, slug,
	).Scan(
		&meta.Slug, &meta.OriginalName, &meta.SourceType,
		&addedAt, &meta.ChunkCount, &meta.TotalChars,
		&meta.Title, &authors, &abstract, &meta.IsPaper, &tags,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("document %q not found", slug)
	}
	if err != nil {
		return nil, fmt.Errorf("read meta %q: %w", slug, err)
	}
	if addedAt.Valid {
		meta.AddedAt = addedAt.Time
	}
	if authors.Valid {
		json.Unmarshal([]byte(authors.String), &meta.Authors)
	}
	if abstract.Valid {
		meta.Abstract = abstract.String
	}
	if tags.Valid {
		json.Unmarshal([]byte(tags.String), &meta.Tags)
	}
	return &meta, nil
}

func (mb *MySQLBackend) WriteMeta(kbName, slug string, meta *DocumentMeta) error {
	authorsJSON := "[]"
	if len(meta.Authors) > 0 {
		if data, err := json.Marshal(meta.Authors); err == nil {
			authorsJSON = string(data)
		}
	}
	tagsJSON := "[]"
	if len(meta.Tags) > 0 {
		if data, err := json.Marshal(meta.Tags); err == nil {
			tagsJSON = string(data)
		}
	}
	_, err := mb.db.Exec(
		`INSERT INTO documents (kb_name, slug, original_name, source_type, added_at, chunk_count, total_chars,
		                       title, authors, abstract, is_paper, tags)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   original_name=VALUES(original_name), source_type=VALUES(source_type),
		   added_at=VALUES(added_at), chunk_count=VALUES(chunk_count),
		   total_chars=VALUES(total_chars), title=VALUES(title),
		   authors=VALUES(authors), abstract=VALUES(abstract),
		   is_paper=VALUES(is_paper), tags=VALUES(tags)`,
		kbName, slug, meta.OriginalName, meta.SourceType,
		meta.AddedAt, meta.ChunkCount, meta.TotalChars,
		meta.Title, authorsJSON, meta.Abstract, meta.IsPaper, tagsJSON,
	)
	if err != nil {
		return fmt.Errorf("write meta %q: %w", slug, err)
	}
	return nil
}

func (mb *MySQLBackend) Exists(kbName, slug string) (bool, error) {
	var count int
	err := mb.db.QueryRow("SELECT COUNT(*) FROM documents WHERE kb_name = ? AND slug = ?", kbName, slug).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check exists %q: %w", slug, err)
	}
	return count > 0, nil
}

func (mb *MySQLBackend) ListDocSlugs(kbName string) ([]string, error) {
	rows, err := mb.db.Query("SELECT slug FROM documents WHERE kb_name = ? ORDER BY slug", kbName)
	if err != nil {
		if isTableNotExists(err) {
			return nil, nil // KB doesn't exist yet
		}
		return nil, fmt.Errorf("list doc slugs: %w", err)
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("scan slug: %w", err)
		}
		slugs = append(slugs, slug)
	}
	return slugs, rows.Err()
}

func (mb *MySQLBackend) RemoveDocument(kbName, slug string) error {
	_, err := mb.db.Exec("DELETE FROM documents WHERE kb_name = ? AND slug = ?", kbName, slug)
	if err != nil {
		return fmt.Errorf("remove document %q: %w", slug, err)
	}
	return nil
}

// ── Chunks ────────────────────────────────────────────────────────────────────

func (mb *MySQLBackend) ReadChunk(kbName, slug, chunkID string) (string, error) {
	var content string
	err := mb.db.QueryRow(
		"SELECT content FROM chunks WHERE kb_name = ? AND doc_slug = ? AND chunk_id = ?",
		kbName, slug, chunkID,
	).Scan(&content)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("chunk %q not found in document %q", chunkID, slug)
	}
	if err != nil {
		return "", fmt.Errorf("read chunk %q/%q: %w", slug, chunkID, err)
	}
	return content, nil
}

func (mb *MySQLBackend) WriteChunk(kbName, slug, chunkID, content string) error {
	_, err := mb.db.Exec(
		"INSERT INTO chunks (kb_name, doc_slug, chunk_id, content) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE content=VALUES(content)",
		kbName, slug, chunkID, content,
	)
	if err != nil {
		return fmt.Errorf("write chunk %q/%q: %w", slug, chunkID, err)
	}
	return nil
}

func (mb *MySQLBackend) ListChunkIDs(kbName, slug string) ([]string, error) {
	rows, err := mb.db.Query(
		"SELECT chunk_id FROM chunks WHERE kb_name = ? AND doc_slug = ? ORDER BY chunk_id",
		kbName, slug,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("document %q not found", slug)
		}
		return nil, fmt.Errorf("list chunk IDs for %q: %w", slug, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan chunk ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (mb *MySQLBackend) DeleteChunks(kbName, slug string) error {
	_, err := mb.db.Exec("DELETE FROM chunks WHERE kb_name = ? AND doc_slug = ?", kbName, slug)
	if err != nil {
		return fmt.Errorf("delete chunks for %q: %w", slug, err)
	}
	return nil
}

// ── Section chunks ────────────────────────────────────────────────────────────

func (mb *MySQLBackend) ReadSectionChunk(kbName, slug, sectionID string) (string, error) {
	var content string
	err := mb.db.QueryRow(
		"SELECT content FROM section_chunks WHERE kb_name = ? AND doc_slug = ? AND section_id = ?",
		kbName, slug, sectionID,
	).Scan(&content)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("section chunk %q not found in document %q", sectionID, slug)
	}
	if err != nil {
		return "", fmt.Errorf("read section chunk %q/%q: %w", slug, sectionID, err)
	}
	return content, nil
}

func (mb *MySQLBackend) WriteSectionChunk(kbName, slug, sectionID, content string) error {
	_, err := mb.db.Exec(
		"INSERT INTO section_chunks (kb_name, doc_slug, section_id, content) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE content=VALUES(content)",
		kbName, slug, sectionID, content,
	)
	if err != nil {
		return fmt.Errorf("write section chunk %q/%q: %w", slug, sectionID, err)
	}
	return nil
}

func (mb *MySQLBackend) ListSectionChunkIDs(kbName, slug string) ([]string, error) {
	rows, err := mb.db.Query(
		"SELECT section_id FROM section_chunks WHERE kb_name = ? AND doc_slug = ? ORDER BY section_id",
		kbName, slug,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("list section chunk IDs for %q: %w", slug, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan section chunk ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (mb *MySQLBackend) DeleteSectionChunks(kbName, slug string) error {
	_, err := mb.db.Exec("DELETE FROM section_chunks WHERE kb_name = ? AND doc_slug = ?", kbName, slug)
	if err != nil {
		return fmt.Errorf("delete section chunks for %q: %w", slug, err)
	}
	return nil
}

// ── Search index ──────────────────────────────────────────────────────────────

func (mb *MySQLBackend) ReadChunksIndex(kbName, slug string) (*ChunksIndex, error) {
	var indexData string
	var checksum string
	err := mb.db.QueryRow(
		"SELECT index_data, checksum FROM chunks_index WHERE kb_name = ? AND doc_slug = ?",
		kbName, slug,
	).Scan(&indexData, &checksum)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read chunks index %q: %w", slug, err)
	}
	var index ChunksIndex
	if err := json.Unmarshal([]byte(indexData), &index); err != nil {
		return nil, fmt.Errorf("unmarshal chunks index %q: %w", slug, err)
	}
	index.Checksum = checksum
	return &index, nil
}

func (mb *MySQLBackend) WriteChunksIndex(kbName, slug string, index *ChunksIndex) error {
	// Remove checksum before storing JSON (it's stored separately).
	cs := index.Checksum
	index.Checksum = ""
	data, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("marshal chunks index %q: %w", slug, err)
	}
	index.Checksum = cs

	_, err = mb.db.Exec(
		"INSERT INTO chunks_index (kb_name, doc_slug, index_data, checksum) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE index_data=VALUES(index_data), checksum=VALUES(checksum)",
		kbName, slug, string(data), cs,
	)
	if err != nil {
		return fmt.Errorf("write chunks index %q: %w", slug, err)
	}
	return nil
}

// ── Inverted index ────────────────────────────────────────────────────────────

func (mb *MySQLBackend) ReadInvertedIndex(kbName string) (*InvertedIndex, error) {
	rows, err := mb.db.Query(
		"SELECT term, doc_slug, chunk_id, tf FROM inverted_index WHERE kb_name = ? ORDER BY term",
		kbName,
	)
	if err != nil {
		if isTableNotExists(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read inverted index: %w", err)
	}
	defer rows.Close()
	idx := NewInvertedIndex()
	for rows.Next() {
		var term, dslg, cid string
		var tf int
		if err := rows.Scan(&term, &dslg, &cid, &tf); err != nil {
			return nil, fmt.Errorf("scan inverted index entry: %w", err)
		}
		idx.Index[term] = append(idx.Index[term], Posting{DocSlug: dslg, ChunkID: cid, TF: tf})
	}
	return idx, rows.Err()
}

func (mb *MySQLBackend) WriteInvertedIndex(kbName string, idx *InvertedIndex) error {
	tx, err := mb.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx for inverted index: %w", err)
	}
	defer tx.Rollback()

	// Delete all entries for this kb.
	if _, err := tx.Exec("DELETE FROM inverted_index WHERE kb_name = ?", kbName); err != nil {
		return fmt.Errorf("clear inverted index: %w", err)
	}

	// Batch insert.
	stmt, err := tx.Prepare("INSERT INTO inverted_index (kb_name, term, doc_slug, chunk_id, tf) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare inverted index insert: %w", err)
	}
	defer stmt.Close()

	for term, postings := range idx.Index {
		for _, p := range postings {
			if _, err := stmt.Exec(kbName, term, p.DocSlug, p.ChunkID, p.TF); err != nil {
				return fmt.Errorf("insert inverted index entry: %w", err)
			}
		}
	}

	return tx.Commit()
}

// ── Raw text & source ─────────────────────────────────────────────────────────

func (mb *MySQLBackend) WriteRawText(kbName, slug, text string) error {
	_, err := mb.db.Exec(
		"UPDATE documents SET raw_text = ? WHERE kb_name = ? AND slug = ?",
		text, kbName, slug,
	)
	if err != nil {
		return fmt.Errorf("write raw text %q: %w", slug, err)
	}
	return nil
}

func (mb *MySQLBackend) WriteSource(kbName, slug string, data []byte, ext string) error {
	_, err := mb.db.Exec(
		"UPDATE documents SET source_data = ?, source_ext = ? WHERE kb_name = ? AND slug = ?",
		data, ext, kbName, slug,
	)
	if err != nil {
		return fmt.Errorf("write source %q: %w", slug, err)
	}
	return nil
}

// ── INDEX.md ───────────────────────────────────────────────────────────────────

func (mb *MySQLBackend) ReadIndex(kbName string) (string, error) {
	// INDEX.md is stored in kb.json's description field for MySQL.
	// We use the knowledge_bases table to store it.
	var description string
	err := mb.db.QueryRow("SELECT COALESCE(description,'') FROM knowledge_bases WHERE name = ?", kbName).Scan(&description)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read index for %q: %w", kbName, err)
	}
	return description, nil
}

func (mb *MySQLBackend) WriteIndex(kbName, content string) error {
	_, err := mb.db.Exec(
		"INSERT INTO knowledge_bases (name, description) VALUES (?, ?) ON DUPLICATE KEY UPDATE description=VALUES(description)",
		kbName, content,
	)
	if err != nil {
		return fmt.Errorf("write index for %q: %w", kbName, err)
	}
	return nil
}

// ── Snapshot ───────────────────────────────────────────────────────────────────

func (mb *MySQLBackend) ReadSnapshot(kbName string) (string, []DocumentMeta, error) {
	var checksum string
	var snapshotData sql.NullString
	err := mb.db.QueryRow(
		"SELECT checksum, snapshot_data FROM list_snapshots WHERE kb_name = ?",
		kbName,
	).Scan(&checksum, &snapshotData)
	if err == sql.ErrNoRows {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("read snapshot for %q: %w", kbName, err)
	}
	if !snapshotData.Valid || snapshotData.String == "" {
		return "", nil, nil
	}
	var docs []DocumentMeta
	if err := json.Unmarshal([]byte(snapshotData.String), &docs); err != nil {
		return "", nil, fmt.Errorf("unmarshal snapshot data: %w", err)
	}
	return checksum, docs, nil
}

func (mb *MySQLBackend) WriteSnapshot(kbName string, docs []DocumentMeta) error {
	if len(docs) == 0 {
		_, err := mb.db.Exec("DELETE FROM list_snapshots WHERE kb_name = ?", kbName)
		return err
	}
	cs := ListChecksum(docs)
	data, err := json.Marshal(docs)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	_, err = mb.db.Exec(
		"INSERT INTO list_snapshots (kb_name, checksum, snapshot_data, updated_at) VALUES (?, ?, ?, NOW()) ON DUPLICATE KEY UPDATE checksum=VALUES(checksum), snapshot_data=VALUES(snapshot_data), updated_at=NOW()",
		kbName, cs, string(data),
	)
	if err != nil {
		return fmt.Errorf("write snapshot for %q: %w", kbName, err)
	}
	return nil
}

// ── Checksum ──────────────────────────────────────────────────────────────────

func (mb *MySQLBackend) ComputeChunksChecksum(kbName, slug string) (string, error) {
	ids, err := mb.ListChunkIDs(kbName, slug)
	if err != nil {
		return "", fmt.Errorf("list chunks: %w", err)
	}
	if len(ids) == 0 {
		return "", nil
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		content, readErr := mb.ReadChunk(kbName, slug, id)
		if readErr != nil {
			return "", fmt.Errorf("read chunk %s: %w", id, readErr)
		}
		io.WriteString(h, id)
		h.Write([]byte(content))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// isTableNotExists checks if the error is due to a missing table.
func isTableNotExists(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "No such table") ||
		strings.Contains(errStr, "doesn't exist") ||
		strings.Contains(errStr, "relation") && strings.Contains(errStr, "does not exist")
}

// Ensure interfaces are satisfied at compile time.
var _ StorageBackend = (*FileBackend)(nil)
var _ StorageBackend = (*MySQLBackend)(nil)
