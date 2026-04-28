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
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// baseSchema creates the core tables. Applied once when the database is first created.
const baseSchema = `
CREATE TABLE IF NOT EXISTS sync_state (
    project     TEXT NOT NULL,
    rel_path    TEXT NOT NULL,
    local_hash  TEXT,
    file_size   INTEGER,
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

// migrations is the ordered list of schema migrations. Each function is
// idempotent (safe to re-run). The framework tracks which migrations have
// been applied via the "schema_version" key in the meta table.
var migrations = []func(db *sql.DB) error{
	// Migration 0: add mtime_ns column (was manual ALTER TABLE in v0.3.x)
	func(db *sql.DB) error {
		_, err := db.Exec(`ALTER TABLE sync_state ADD COLUMN mtime_ns INTEGER NOT NULL DEFAULT 0`)
		if err != nil {
			errMsg := err.Error()
			if strings.Contains(errMsg, "duplicate column name") || strings.Contains(errMsg, "already exists") {
				return nil
			}
			return err
		}
		return nil
	},
	// Migration 1: add remote verification columns (SM-083 trust model).
	// Distinguishes local-wishful (synced_at = "we ran rclone") from
	// remote-verified (remote_verified_at = "lsjson confirmed file exists").
	func(db *sql.DB) error {
		cols := []string{
			`ALTER TABLE sync_state ADD COLUMN remote_verified_at TEXT DEFAULT ''`,
			`ALTER TABLE sync_state ADD COLUMN remote_hash TEXT DEFAULT ''`,
			`ALTER TABLE sync_state ADD COLUMN remote_size INTEGER DEFAULT 0`,
		}
		for _, stmt := range cols {
			if _, err := db.Exec(stmt); err != nil {
				errMsg := err.Error()
				if strings.Contains(errMsg, "duplicate column name") || strings.Contains(errMsg, "already exists") {
					continue
				}
				return err
			}
		}
		return nil
	},
}

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
	SyncedAt   time.Time // when we last ran rclone for this file (local-wishful)
	RcloneExit int

	// Remote verification (SM-083 trust model):
	// These fields are populated by lsjson verification, NOT by rclone copy/copyto.
	// Empty = file has not been independently verified on remote.
	RemoteVerifiedAt time.Time // when lsjson last confirmed this file exists on remote
	RemoteHash       string    // hash from remote (lsjson --hash)
	RemoteSize       int64     // size from remote (lsjson)
}

// Open creates or opens the state database.
func Open(dbPath string) (*Store, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}

	// SEC-H7: refuse to open the state DB if the path is a symlink. Opening
	// state.db via os.OpenFile would follow the symlink; in service mode
	// (LocalSystem) a user-writable symlink to e.g. C:\Windows\System32\
	// drivers\etc\hosts would let a non-admin overwrite system files via
	// the SQLite WAL/journal writes. Lstat first; reject if symlink.
	if linfo, lerr := os.Lstat(dbPath); lerr == nil && linfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("state DB path %q is a symlink; refused (SEC-H7)", dbPath)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=ON&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening state db: %w", err)
	}

	// Serialize all DB access through a single connection. SQLite supports
	// only one writer at a time; with multiple connections from database/sql's
	// pool, concurrent writers get SQLITE_BUSY even with busy_timeout because
	// each connection holds its own lock state. A single connection + WAL mode
	// gives safe concurrent goroutine access via database/sql's internal mutex (SM-047).
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(baseSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}

	// GAP-7 (panel review 2026-04-28): refuse to open a state DB whose
	// schema_version is HIGHER than this binary supports. A downgrade
	// scenario (newer binary writes schema 17 → user runs older 0.9.12
	// binary that knows only schemas 0..12) used to silently skip the
	// missing migrations and operate as if at the older schema, with
	// undefined behavior on rows written by the newer binary. We now
	// detect this before any migration runs and refuse with a clear
	// error pointing the user at the right remedy (upgrade smirror or
	// restore an older state DB).
	{
		var v string
		row := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'")
		if err := row.Scan(&v); err == nil {
			recorded := atoiOrZero(v)
			if recorded > len(migrations) {
				db.Close()
				return nil, fmt.Errorf("state DB schema version %d is newer than this binary supports (%d). Upgrade smirror or restore an older state DB", recorded, len(migrations))
			}
		}
	}

	// Run auto-migration framework
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migration: %w", err)
	}

	s := &Store{db: db}

	// schema_version: never downgrade. An older binary running a read-only
	// command (smirror status / dry-run / explain) used to overwrite the
	// meta entry with its own (lower) migration count, which then triggered
	// migration re-runs on the next daemon startup. Only write if we are at
	// or above the recorded version.
	if existing, _ := s.GetMeta("schema_version"); existing == "" || atoiOrZero(existing) < len(migrations) {
		s.SetMeta("schema_version", fmt.Sprintf("%d", len(migrations)))
	}

	// last_startup is NOT written here. Read-only commands open the state
	// store too; the user expects last_startup to be the daemon's last
	// start time, not "the last time anyone touched the DB". The daemon
	// calls MarkDaemonStartup() explicitly during startup.

	return s, nil
}

// atoiOrZero converts a string to int, returning 0 on error. Used by Open
// to compare schema_version values without inflating the package surface.
func atoiOrZero(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

// MarkDaemonStartup records "last_startup" in the meta table. Called only
// by the long-running daemon (foreground / Windows service / scheduled
// task). Read-only commands intentionally do not update this field.
func (s *Store) MarkDaemonStartup() {
	s.SetMeta("last_startup", time.Now().UTC().Format(time.RFC3339))
}

// runMigrations applies pending schema migrations. Reads the current version
// from the meta table, runs all migrations from currentVersion onward, and
// updates the version. Each migration is idempotent.
func runMigrations(db *sql.DB) error {
	currentVersion := 0

	// Read current schema version (may not exist on first run)
	row := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'")
	var vStr string
	if err := row.Scan(&vStr); err == nil {
		fmt.Sscanf(vStr, "%d", &currentVersion)
	}

	for i := currentVersion; i < len(migrations); i++ {
		if err := migrations[i](db); err != nil {
			return fmt.Errorf("migration %d: %w", i, err)
		}
	}

	return nil
}

// PruneOldLogs deletes sync_log entries older than retentionDays.
// Returns the number of rows deleted.
func (s *Store) PruneOldLogs(retentionDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	result, err := s.db.Exec("DELETE FROM sync_log WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteProject removes all sync_state and sync_log rows for the given
// project. Returns the total number of rows deleted across both tables.
// Used by `smirror unmirror` to clean the state DB when a mirror is removed.
func (s *Store) DeleteProject(project string) (int64, error) {
	var total int64
	r1, err := s.db.Exec("DELETE FROM sync_state WHERE project = ?", project)
	if err != nil {
		return 0, fmt.Errorf("delete sync_state: %w", err)
	}
	if n, err := r1.RowsAffected(); err == nil {
		total += n
	}
	r2, err := s.db.Exec("DELETE FROM sync_log WHERE project = ?", project)
	if err != nil {
		return total, fmt.Errorf("delete sync_log: %w", err)
	}
	if n, err := r2.RowsAffected(); err == nil {
		total += n
	}
	return total, nil
}

// PruneOrphanedProjects removes state entries for projects not in the active config.
// Returns the number of entries removed.
func (s *Store) PruneOrphanedProjects(activeProjects []string) (int64, error) {
	if len(activeProjects) == 0 {
		return 0, nil
	}
	// Build placeholders for IN clause
	placeholders := make([]string, len(activeProjects))
	args := make([]interface{}, len(activeProjects))
	for i, p := range activeProjects {
		placeholders[i] = "?"
		args[i] = p
	}
	query := "DELETE FROM sync_state WHERE project NOT IN (" + strings.Join(placeholders, ",") + ")" //nolint:gosec // placeholders are all "?" — no user input
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// GetFileState retrieves the sync state for a file.
func (s *Store) GetFileState(project, relPath string) (*FileState, error) {
	row := s.db.QueryRow(
		`SELECT project, rel_path, local_hash, file_size, mtime_ns, synced_at, rclone_exit,
		        remote_verified_at, remote_hash, remote_size
		 FROM sync_state WHERE project = ? AND rel_path = ?`,
		project, relPath,
	)

	fs := &FileState{}
	var syncedAt, remoteVerifiedAt string
	err := row.Scan(&fs.Project, &fs.RelPath, &fs.LocalHash, &fs.FileSize, &fs.MtimeNs,
		&syncedAt, &fs.RcloneExit, &remoteVerifiedAt, &fs.RemoteHash, &fs.RemoteSize)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fs.SyncedAt, _ = time.Parse(time.RFC3339, syncedAt)
	if remoteVerifiedAt != "" {
		fs.RemoteVerifiedAt, _ = time.Parse(time.RFC3339, remoteVerifiedAt)
	}
	return fs, nil
}

// IsRemoteVerified returns true if this file has been independently verified
// on the remote via lsjson (not just locally "synced" via rclone copy).
func (fs *FileState) IsRemoteVerified() bool {
	return fs != nil && !fs.RemoteVerifiedAt.IsZero()
}

// UpdateRemoteVerification records that a file was verified on the remote
// via lsjson --hash. Does NOT touch synced_at or other local fields.
func (s *Store) UpdateRemoteVerification(project, relPath, remoteHash string, remoteSize int64) error {
	_, err := s.db.Exec(
		`UPDATE sync_state SET remote_verified_at = ?, remote_hash = ?, remote_size = ?
		 WHERE project = ? AND rel_path = ?`,
		time.Now().UTC().Format(time.RFC3339), remoteHash, remoteSize, project, relPath,
	)
	return err
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

// escapeLIKE escapes SQL LIKE wildcard characters (%, _, \) in s so that
// they are matched literally when used with ESCAPE '\'.
func escapeLIKE(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// GetFilesUnderDir returns all synced file paths under a directory prefix.
// Used for directory rename/delete cleanup: when a directory is renamed,
// we need to delete individual files from the old remote path.
func (s *Store) GetFilesUnderDir(project, dirPrefix string) ([]string, error) {
	// Match "dir/" prefix to find all files under that directory.
	// Escape LIKE wildcards (%, _, \) in the directory name so that
	// characters like _ and % are matched literally (SM-046).
	prefix := escapeLIKE(dirPrefix) + "/"
	rows, err := s.db.Query(
		`SELECT rel_path FROM sync_state WHERE project = ? AND (rel_path = ? OR rel_path LIKE ? ESCAPE '\')`,
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

// ClearStaleExitCodes resets rclone_exit to 0 for all files in a project that
// have a non-zero exit code. Called after a successful batch reconciliation
// (rclone copy) to clear stale per-file failure flags (SM-101).
func (s *Store) ClearStaleExitCodes(project string) (int64, error) {
	result, err := s.db.Exec(
		"UPDATE sync_state SET rclone_exit = 0 WHERE project = ? AND rclone_exit != 0",
		project,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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
