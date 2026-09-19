// Package index persists crawled file metadata to SQLite and serves
// listing/search queries over it.
package index

import (
	"database/sql"
	"fmt"
	"path"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type File struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Ext     string `json:"ext"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
}

type Stats struct {
	FileCount    int    `json:"fileCount"`
	DirCount     int    `json:"dirCount"`
	LastCrawlAt  string `json:"lastCrawlAt,omitempty"`
	LastCrawlErr string `json:"lastCrawlError,omitempty"`
}

type Index struct {
	db *sql.DB
}

func Open(dbPath string) (*Index, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite has one writer at a time; the crawler and HTTP handlers share
	// this *sql.DB, so keep it to a single connection to avoid "database is
	// locked" errors rather than tuning busy_timeout/WAL for milestone 1.
	db.SetMaxOpenConns(1)

	idx := &Index{db: db}
	if err := idx.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

func (idx *Index) Close() error {
	return idx.db.Close()
}

func (idx *Index) migrate() error {
	_, err := idx.db.Exec(`
		CREATE TABLE IF NOT EXISTS files (
			path          TEXT PRIMARY KEY,
			name          TEXT NOT NULL,
			dir           TEXT NOT NULL,
			ext           TEXT NOT NULL,
			is_dir        INTEGER NOT NULL,
			size          INTEGER NOT NULL,
			mod_time      INTEGER NOT NULL,
			last_seen_run INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_files_dir ON files(dir);
		CREATE INDEX IF NOT EXISTS idx_files_name ON files(name);

		CREATE TABLE IF NOT EXISTS crawl_runs (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			error      TEXT
		);
	`)
	return err
}

// UpsertBatch inserts or updates every given file in one transaction,
// stamping each row with runID so a later Sweep(runID) can tell which rows
// weren't seen in this crawl. Callers with a large crawl to index should
// call this in chunks as entries are discovered rather than buffering the
// whole tree in memory first.
func (idx *Index) UpsertBatch(files []File, runID int64) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO files (path, name, dir, ext, is_dir, size, mod_time, last_seen_run)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			name = excluded.name,
			dir = excluded.dir,
			ext = excluded.ext,
			is_dir = excluded.is_dir,
			size = excluded.size,
			mod_time = excluded.mod_time,
			last_seen_run = excluded.last_seen_run
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, f := range files {
		isDir := 0
		if f.IsDir {
			isDir = 1
		}
		if _, err := stmt.Exec(f.Path, f.Name, f.Dir, f.Ext, isDir, f.Size, f.ModTime, runID); err != nil {
			return fmt.Errorf("upsert %q: %w", f.Path, err)
		}
	}

	return tx.Commit()
}

// Upsert inserts or updates a single file, e.g. from a live change-notify
// event rather than a crawl run.
func (idx *Index) Upsert(f File, runID int64) error {
	return idx.UpsertBatch([]File{f}, runID)
}

// Sweep deletes every file not stamped with runID — the mark-and-sweep half
// of a reconciling crawl: anything not seen during run runID no longer
// exists on the NAS (or wasn't reachable this run) and is dropped from the
// index.
func (idx *Index) Sweep(runID int64) error {
	_, err := idx.db.Exec(`DELETE FROM files WHERE last_seen_run != ?`, runID)
	return err
}

// Delete removes a single file by path, e.g. from a live change-notify
// event.
func (idx *Index) Delete(p string) error {
	_, err := idx.db.Exec(`DELETE FROM files WHERE path = ?`, strings.Trim(p, "/"))
	return err
}

// Reconcile upserts every file in files, then sweeps anything not seen in
// this batch — the whole-batch convenience path for callers (today, a full
// crawl) that already have the complete file list in memory.
func (idx *Index) Reconcile(files []File, runID int64) error {
	if err := idx.UpsertBatch(files, runID); err != nil {
		return err
	}
	return idx.Sweep(runID)
}

// StartCrawlRun records the start of a crawl and returns its id.
func (idx *Index) StartCrawlRun() (int64, error) {
	res, err := idx.db.Exec(`INSERT INTO crawl_runs (started_at) VALUES (?)`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishCrawlRun records completion (crawlErr == nil for success).
func (idx *Index) FinishCrawlRun(id int64, crawlErr error) error {
	errMsg := ""
	if crawlErr != nil {
		errMsg = crawlErr.Error()
	}
	_, err := idx.db.Exec(
		`UPDATE crawl_runs SET finished_at = ?, error = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), nullIfEmpty(errMsg), id,
	)
	return err
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// List returns immediate children of dir ("" for share root).
func (idx *Index) List(dir string) ([]File, error) {
	dir = normalizeDir(dir)
	rows, err := idx.db.Query(
		`SELECT path, name, dir, ext, is_dir, size, mod_time FROM files WHERE dir = ? ORDER BY is_dir DESC, name ASC`,
		dir,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

// Search returns files/dirs whose name contains q (case-insensitive).
func (idx *Index) Search(q string, limit int) ([]File, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := idx.db.Query(
		`SELECT path, name, dir, ext, is_dir, size, mod_time FROM files
		 WHERE name LIKE ? ESCAPE '\' ORDER BY name ASC LIMIT ?`,
		"%"+likeEscape(q)+"%", limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

// Stat returns metadata for a single path, if indexed.
func (idx *Index) Stat(p string) (File, bool, error) {
	row := idx.db.QueryRow(
		`SELECT path, name, dir, ext, is_dir, size, mod_time FROM files WHERE path = ?`,
		strings.Trim(p, "/"),
	)
	var f File
	var isDir int
	if err := row.Scan(&f.Path, &f.Name, &f.Dir, &f.Ext, &isDir, &f.Size, &f.ModTime); err != nil {
		if err == sql.ErrNoRows {
			return File{}, false, nil
		}
		return File{}, false, err
	}
	f.IsDir = isDir == 1
	return f, true, nil
}

func (idx *Index) Stats() (Stats, error) {
	var s Stats
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE is_dir = 0`).Scan(&s.FileCount); err != nil {
		return s, err
	}
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE is_dir = 1`).Scan(&s.DirCount); err != nil {
		return s, err
	}

	row := idx.db.QueryRow(`SELECT started_at, COALESCE(finished_at,''), COALESCE(error,'') FROM crawl_runs ORDER BY id DESC LIMIT 1`)
	var started, finished, crawlErr string
	if err := row.Scan(&started, &finished, &crawlErr); err == nil {
		if finished != "" {
			s.LastCrawlAt = finished
		} else {
			s.LastCrawlAt = started
		}
		s.LastCrawlErr = crawlErr
	} else if err != sql.ErrNoRows {
		return s, err
	}

	return s, nil
}

func scanFiles(rows *sql.Rows) ([]File, error) {
	var out []File
	for rows.Next() {
		var f File
		var isDir int
		if err := rows.Scan(&f.Path, &f.Name, &f.Dir, &f.Ext, &isDir, &f.Size, &f.ModTime); err != nil {
			return nil, err
		}
		f.IsDir = isDir == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

func normalizeDir(dir string) string {
	dir = strings.Trim(dir, "/")
	return dir
}

// SplitPath returns the parent dir and extension for a slash-separated path,
// matching how ReplaceAll/List expect dir to be stored.
func SplitPath(p string) (dir, ext string) {
	dir = strings.Trim(path.Dir(p), "./")
	if dir == "." {
		dir = ""
	}
	ext = strings.TrimPrefix(path.Ext(p), ".")
	return dir, ext
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
