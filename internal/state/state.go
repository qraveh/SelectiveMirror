// Package state provides SQLite-backed sync state tracking.
package state

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = "2"

const createSchema = `
CREATE TABLE IF NOT EXISTS sync_state (
    project     TEXT NOT NULL,
    rel_path    TEXT NOT NULL,
    local_hash  TEXT,
    file_size   INTEGER,
    mtime_ns    INTEGER NOT NULL DEFAULT 0,
    synced_at   TEXT NOT NULL,
    rclone_exit INTEGER,
    PRIMARY KEY (project, rel_path)
);

CREATE TABLE IF NOT EXISTS sync_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp   TEXT NOT NULL,
    project     TEXT NOT NULL,
    rel_path    TEXT NOT NULL,
    action      TEXT NOT NULL,
    detail      TEXT,
    duration_ms INTEGER
);

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT
);
`

// Store wraps a SQLite database for sync state management.
type Store struct {
	db *sql.DB
}

// FileState represents the last known state of a synced file.
type FileState struct {
	Project    string
	RelPath    string
	LocalHash  string
	FileSize   int64
	MtimeNs    int64     // local file mtime at last successful sync (nanoseconds since epoch); 0 = not yet recorded
	SyncedAt   time.Time
	RcloneExit int
}

// Open creates or opens the state database.
func Open(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, fmt.Errorf("opening state db: %w", err)
	}

	if _, err := db.Exec(createSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}

	// Migration: add mtime_ns column to existing databases (idempotent — error means column exists).
	_, _ = db.Exec(`ALTER TABLE sync_state ADD COLUMN mtime_ns INTEGER NOT NULL DEFAULT 0`)

	s := &Store{db: db}

	// Set schema version if not present
	s.SetMeta("schema_version", schemaVersion)
	s.SetMeta("last_startup", time.Now().UTC().Format(time.RFC3339))

	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// GetFileState retrieves the sync state for a file.
func (s *Store) GetFileState(project, relPath string) (*FileState, error) {
	row := s.db.QueryRow(
		"SELECT project, rel_path, local_hash, file_size, mtime_ns, synced_at, rclone_exit FROM sync_state WHERE project = ? AND rel_path = ?",
		project, relPath,
	)

	fs := &FileState{}
	var syncedAt string
	err := row.Scan(&fs.Project, &fs.RelPath, &fs.LocalHash, &fs.FileSize, &fs.MtimeNs, &syncedAt, &fs.RcloneExit)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fs.SyncedAt, _ = time.Parse(time.RFC3339, syncedAt)
	return fs, nil
}

// UpdateFileState inserts or updates the sync state for a file.
func (s *Store) UpdateFileState(project, relPath, localHash string, fileSize, mtimeNs int64, rcloneExit int) error {
	_, err := s.db.Exec(
		`INSERT INTO sync_state (project, rel_path, local_hash, file_size, mtime_ns, synced_at, rclone_exit)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project, rel_path) DO UPDATE SET
		   local_hash    = excluded.local_hash,
		   file_size     = excluded.file_size,
		   mtime_ns      = excluded.mtime_ns,
		   synced_at     = excluded.synced_at,
		   rclone_exit   = excluded.rclone_exit`,
		project, relPath, localHash, fileSize, mtimeNs, time.Now().UTC().Format(time.RFC3339), rcloneExit,
	)
	return err
}

// UpdateMtimeOnly updates just the mtime_ns for a file whose content was already
// successfully synced. Used when bootstrapping mtime tracking on existing DB rows.
func (s *Store) UpdateMtimeOnly(project, relPath string, mtimeNs int64) error {
	_, err := s.db.Exec(
		`UPDATE sync_state SET mtime_ns = ? WHERE project = ? AND rel_path = ?`,
		mtimeNs, project, relPath,
	)
	return err
}

// LogAction appends an entry to the sync_log table.
func (s *Store) LogAction(project, relPath, action, detail string, durationMs int64) error {
	_, err := s.db.Exec(
		"INSERT INTO sync_log (timestamp, project, rel_path, action, detail, duration_ms) VALUES (?, ?, ?, ?, ?, ?)",
		time.Now().UTC().Format(time.RFC3339), project, relPath, action, detail, durationMs,
	)
	return err
}

// GetAllSyncedPaths returns all synced paths for a project.
func (s *Store) GetAllSyncedPaths(project string) (map[string]*FileState, error) {
	rows, err := s.db.Query(
		"SELECT project, rel_path, local_hash, file_size, mtime_ns, synced_at, rclone_exit FROM sync_state WHERE project = ?",
		project,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*FileState)
	for rows.Next() {
		fs := &FileState{}
		var syncedAt string
		if err := rows.Scan(&fs.Project, &fs.RelPath, &fs.LocalHash, &fs.FileSize, &fs.MtimeNs, &syncedAt, &fs.RcloneExit); err != nil {
			return nil, err
		}
		fs.SyncedAt, _ = time.Parse(time.RFC3339, syncedAt)
		result[fs.RelPath] = fs
	}
	return result, rows.Err()
}

// GetFilesUnderDir returns all synced file paths under a directory prefix.
// Used for directory rename/delete cleanup: when a directory is renamed,
// we need to delete individual files from the old remote path.
func (s *Store) GetFilesUnderDir(project, dirPrefix string) ([]string, error) {
	// Match "dir/" prefix to find all files under that directory
	prefix := dirPrefix + "/"
	rows, err := s.db.Query(
		"SELECT rel_path FROM sync_state WHERE project = ? AND (rel_path = ? OR rel_path LIKE ?)",
		project, dirPrefix, prefix+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// DeleteFileState removes the sync state entry for a file.
// Used after remote delete to keep state DB consistent.
func (s *Store) DeleteFileState(project, relPath string) error {
	_, err := s.db.Exec(
		"DELETE FROM sync_state WHERE project = ? AND rel_path = ?",
		project, relPath,
	)
	return err
}

// GetPendingFiles returns files with non-zero rclone exit (failed syncs).
func (s *Store) GetPendingFiles(project string) ([]string, error) {
	rows, err := s.db.Query(
		"SELECT rel_path FROM sync_state WHERE project = ? AND rclone_exit != 0",
		project,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// GetLastSyncTime returns the most recent successful sync time for a project.
func (s *Store) GetLastSyncTime(project string) (time.Time, error) {
	row := s.db.QueryRow(
		"SELECT MAX(synced_at) FROM sync_state WHERE project = ? AND rclone_exit = 0",
		project,
	)
	var syncedAt sql.NullString
	if err := row.Scan(&syncedAt); err != nil {
		return time.Time{}, err
	}
	if !syncedAt.Valid {
		return time.Time{}, nil
	}
	t, _ := time.Parse(time.RFC3339, syncedAt.String)
	return t, nil
}

// SetMeta sets a key-value pair in the meta table.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	return err
}

// CountFiles returns the number of synced files for a project.
func (s *Store) CountFiles(project string) int {
	row := s.db.QueryRow("SELECT COUNT(*) FROM sync_state WHERE project = ?", project)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0
	}
	return count
}

// GetMeta retrieves a value from the meta table.
func (s *Store) GetMeta(key string) (string, error) {
	row := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key)
	var v string
	err := row.Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// HashFile computes the MD5 hash of a file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := md5.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(h.Sum(nil)), size, nil
}
