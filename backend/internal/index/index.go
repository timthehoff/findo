// Package index persists crawled file metadata to SQLite and serves
// listing/search queries over it. One database backs every configured NAS
// volume: files and crawl_runs rows are tagged with a volume_id so a single
// index can hold multiple independently-crawled shares.
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
	VolumeID int64  `json:"volumeId"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Ext      string `json:"ext"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	ModTime  int64  `json:"modTime"`
}

// Stats is an aggregate file/dir count, either across every volume
// (Index.Stats) or scoped to one (Index.VolumeStats).
type Stats struct {
	FileCount int `json:"fileCount"`
	DirCount  int `json:"dirCount"`
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
		CREATE TABLE IF NOT EXISTS volumes (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			name            TEXT NOT NULL,
			host            TEXT NOT NULL,
			share           TEXT NOT NULL,
			username        TEXT NOT NULL,
			password_enc    BLOB NOT NULL,
			enabled         INTEGER NOT NULL DEFAULT 1,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			last_test_ok    INTEGER,
			last_test_at    TEXT,
			last_test_error TEXT
		);

		CREATE TABLE IF NOT EXISTS files (
			volume_id     INTEGER NOT NULL,
			path          TEXT NOT NULL,
			name          TEXT NOT NULL,
			dir           TEXT NOT NULL,
			ext           TEXT NOT NULL,
			is_dir        INTEGER NOT NULL,
			size          INTEGER NOT NULL,
			mod_time      INTEGER NOT NULL,
			last_seen_run INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (volume_id, path)
		);
		CREATE INDEX IF NOT EXISTS idx_files_volume_dir ON files(volume_id, dir);
		CREATE INDEX IF NOT EXISTS idx_files_volume_name ON files(volume_id, name);

		CREATE TABLE IF NOT EXISTS crawl_runs (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			volume_id     INTEGER NOT NULL,
			trigger       TEXT NOT NULL DEFAULT 'manual',
			started_at    TEXT NOT NULL,
			finished_at   TEXT,
			duration_ms   INTEGER,
			error         TEXT,
			files_seen    INTEGER NOT NULL DEFAULT 0,
			files_removed INTEGER NOT NULL DEFAULT 0,
			bytes_indexed INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_crawl_runs_volume ON crawl_runs(volume_id, id DESC);
	`)
	return err
}

// UpsertBatch inserts or updates every given file in one transaction,
// stamping each row with runID so a later Sweep(volumeID, runID) can tell
// which rows weren't seen in this crawl. Callers with a large crawl to
// index should call this in chunks as entries are discovered rather than
// buffering the whole tree in memory first.
func (idx *Index) UpsertBatch(files []File, runID int64) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO files (volume_id, path, name, dir, ext, is_dir, size, mod_time, last_seen_run)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(volume_id, path) DO UPDATE SET
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
		if _, err := stmt.Exec(f.VolumeID, f.Path, f.Name, f.Dir, f.Ext, isDir, f.Size, f.ModTime, runID); err != nil {
			return fmt.Errorf("upsert %q: %w", f.Path, err)
		}
	}

	return tx.Commit()
}

// StreamUpsert reads files from entries until the channel is closed,
// upserting them in batches of up to batchSize via UpsertBatch rather than
// buffering the whole crawl in memory first. Returns the total number of
// files written and the first batch-write error encountered, if any — it
// keeps draining entries after a write error rather than stopping, since a
// producer that's still sending on the channel would otherwise block
// forever.
func (idx *Index) StreamUpsert(entries <-chan File, runID int64, batchSize int) (int, error) {
	var written int
	var firstErr error
	batch := make([]File, 0, batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := idx.UpsertBatch(batch, runID); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			written += len(batch)
		}
		batch = batch[:0]
	}

	for f := range entries {
		batch = append(batch, f)
		if len(batch) >= batchSize {
			flush()
		}
	}
	flush()

	return written, firstErr
}

// Upsert inserts or updates a single file, e.g. from a live change-notify
// event rather than a crawl run.
func (idx *Index) Upsert(f File, runID int64) error {
	return idx.UpsertBatch([]File{f}, runID)
}

// Sweep deletes every file in volumeID not stamped with runID — the
// mark-and-sweep half of a reconciling crawl: anything not seen during run
// runID no longer exists on the NAS (or wasn't reachable this run) and is
// dropped from the index. It returns how many rows were removed, for crawl
// run reporting.
func (idx *Index) Sweep(volumeID, runID int64) (int64, error) {
	res, err := idx.db.Exec(`DELETE FROM files WHERE volume_id = ? AND last_seen_run != ?`, volumeID, runID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Delete removes a single file by path, e.g. from a live change-notify
// event.
func (idx *Index) Delete(volumeID int64, p string) error {
	_, err := idx.db.Exec(`DELETE FROM files WHERE volume_id = ? AND path = ?`, volumeID, strings.Trim(p, "/"))
	return err
}

// Reconcile upserts every file in files, then sweeps anything in volumeID
// not seen in this batch — the whole-batch convenience path for callers
// that already have the complete file list in memory.
func (idx *Index) Reconcile(volumeID int64, files []File, runID int64) error {
	if err := idx.UpsertBatch(files, runID); err != nil {
		return err
	}
	_, err := idx.Sweep(volumeID, runID)
	return err
}

// StartCrawlRun records the start of a crawl for volumeID and returns its
// run id. trigger records why the crawl started (manual, startup, periodic,
// or resync) for the crawl history shown in the dashboard.
func (idx *Index) StartCrawlRun(volumeID int64, trigger string) (int64, error) {
	res, err := idx.db.Exec(
		`INSERT INTO crawl_runs (volume_id, trigger, started_at) VALUES (?, ?, ?)`,
		volumeID, trigger, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CrawlRunResult is what Crawl learns about its own run, recorded by
// FinishCrawlRun once the crawl completes (crawlErr == nil for success).
type CrawlRunResult struct {
	Error        error
	FilesSeen    int
	FilesRemoved int64
	BytesIndexed int64
	DurationMS   int64
}

// FinishCrawlRun records how a crawl run completed.
func (idx *Index) FinishCrawlRun(id int64, res CrawlRunResult) error {
	errMsg := ""
	if res.Error != nil {
		errMsg = res.Error.Error()
	}
	_, err := idx.db.Exec(
		`UPDATE crawl_runs SET finished_at = ?, duration_ms = ?, error = ?,
		 files_seen = ?, files_removed = ?, bytes_indexed = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), res.DurationMS, nullIfEmpty(errMsg),
		res.FilesSeen, res.FilesRemoved, res.BytesIndexed, id,
	)
	return err
}

// CrawlRun is one recorded crawl run, as shown in the dashboard's crawl
// history.
type CrawlRun struct {
	ID           int64  `json:"id"`
	VolumeID     int64  `json:"volumeId"`
	Trigger      string `json:"trigger"`
	StartedAt    string `json:"startedAt"`
	FinishedAt   string `json:"finishedAt,omitempty"`
	DurationMS   int64  `json:"durationMs,omitempty"`
	Error        string `json:"error,omitempty"`
	FilesSeen    int    `json:"filesSeen"`
	FilesRemoved int64  `json:"filesRemoved"`
	BytesIndexed int64  `json:"bytesIndexed"`
}

const crawlRunColumns = `id, volume_id, trigger, started_at, COALESCE(finished_at,''), COALESCE(duration_ms,0), COALESCE(error,''), files_seen, files_removed, bytes_indexed`

// LatestCrawlRun returns volumeID's most recent crawl run, if it has one.
func (idx *Index) LatestCrawlRun(volumeID int64) (CrawlRun, bool, error) {
	row := idx.db.QueryRow(
		`SELECT `+crawlRunColumns+` FROM crawl_runs WHERE volume_id = ? ORDER BY id DESC LIMIT 1`,
		volumeID,
	)
	cr, err := scanCrawlRun(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return CrawlRun{}, false, nil
		}
		return CrawlRun{}, false, err
	}
	return cr, true, nil
}

// CrawlRuns returns volumeID's most recent crawl runs, newest first.
func (idx *Index) CrawlRuns(volumeID int64, limit int) ([]CrawlRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := idx.db.Query(
		`SELECT `+crawlRunColumns+` FROM crawl_runs WHERE volume_id = ? ORDER BY id DESC LIMIT ?`,
		volumeID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []CrawlRun{}
	for rows.Next() {
		cr, err := scanCrawlRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

func scanCrawlRun(row rowScanner) (CrawlRun, error) {
	var cr CrawlRun
	if err := row.Scan(
		&cr.ID, &cr.VolumeID, &cr.Trigger, &cr.StartedAt, &cr.FinishedAt, &cr.DurationMS,
		&cr.Error, &cr.FilesSeen, &cr.FilesRemoved, &cr.BytesIndexed,
	); err != nil {
		return CrawlRun{}, err
	}
	return cr, nil
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// List returns immediate children of dir ("" for the volume's root).
func (idx *Index) List(volumeID int64, dir string) ([]File, error) {
	dir = normalizeDir(dir)
	rows, err := idx.db.Query(
		`SELECT volume_id, path, name, dir, ext, is_dir, size, mod_time FROM files
		 WHERE volume_id = ? AND dir = ? ORDER BY is_dir DESC, name ASC`,
		volumeID, dir,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

// Search returns files/dirs whose name contains q (case-insensitive).
// volumeID == 0 searches across every volume; otherwise it's scoped to one.
func (idx *Index) Search(volumeID int64, q string, limit int) ([]File, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	pattern := "%" + likeEscape(q) + "%"

	var rows *sql.Rows
	var err error
	if volumeID == 0 {
		rows, err = idx.db.Query(
			`SELECT volume_id, path, name, dir, ext, is_dir, size, mod_time FROM files
			 WHERE name LIKE ? ESCAPE '\' ORDER BY name ASC LIMIT ?`,
			pattern, limit,
		)
	} else {
		rows, err = idx.db.Query(
			`SELECT volume_id, path, name, dir, ext, is_dir, size, mod_time FROM files
			 WHERE volume_id = ? AND name LIKE ? ESCAPE '\' ORDER BY name ASC LIMIT ?`,
			volumeID, pattern, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

// Stat returns metadata for a single path within volumeID, if indexed.
func (idx *Index) Stat(volumeID int64, p string) (File, bool, error) {
	row := idx.db.QueryRow(
		`SELECT volume_id, path, name, dir, ext, is_dir, size, mod_time FROM files
		 WHERE volume_id = ? AND path = ?`,
		volumeID, strings.Trim(p, "/"),
	)
	var f File
	var isDir int
	if err := row.Scan(&f.VolumeID, &f.Path, &f.Name, &f.Dir, &f.Ext, &isDir, &f.Size, &f.ModTime); err != nil {
		if err == sql.ErrNoRows {
			return File{}, false, nil
		}
		return File{}, false, err
	}
	f.IsDir = isDir == 1
	return f, true, nil
}

// Stats returns file/dir counts aggregated across every volume.
func (idx *Index) Stats() (Stats, error) {
	var s Stats
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE is_dir = 0`).Scan(&s.FileCount); err != nil {
		return s, err
	}
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE is_dir = 1`).Scan(&s.DirCount); err != nil {
		return s, err
	}
	return s, nil
}

// VolumeStats returns file/dir counts for a single volume.
func (idx *Index) VolumeStats(volumeID int64) (Stats, error) {
	var s Stats
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE volume_id = ? AND is_dir = 0`, volumeID).Scan(&s.FileCount); err != nil {
		return s, err
	}
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE volume_id = ? AND is_dir = 1`, volumeID).Scan(&s.DirCount); err != nil {
		return s, err
	}
	return s, nil
}

// ExtStat is one extension's aggregate footprint within a volume, used for
// the dashboard's storage-by-file-type chart.
type ExtStat struct {
	Ext       string `json:"ext"`
	Count     int    `json:"count"`
	TotalSize int64  `json:"totalSize"`
}

// ExtensionBreakdown returns volumeID's file extensions ranked by total
// size, largest first. Extensionless files are grouped under "(none)".
func (idx *Index) ExtensionBreakdown(volumeID int64, limit int) ([]ExtStat, error) {
	if limit <= 0 || limit > 100 {
		limit = 8
	}
	rows, err := idx.db.Query(
		`SELECT CASE WHEN ext = '' THEN '(none)' ELSE ext END, COUNT(*), COALESCE(SUM(size), 0)
		 FROM files WHERE volume_id = ? AND is_dir = 0
		 GROUP BY ext ORDER BY SUM(size) DESC LIMIT ?`,
		volumeID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ExtStat{}
	for rows.Next() {
		var s ExtStat
		if err := rows.Scan(&s.Ext, &s.Count, &s.TotalSize); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LargestFiles returns volumeID's largest files, biggest first.
func (idx *Index) LargestFiles(volumeID int64, limit int) ([]File, error) {
	if limit <= 0 || limit > 100 {
		limit = 8
	}
	rows, err := idx.db.Query(
		`SELECT volume_id, path, name, dir, ext, is_dir, size, mod_time FROM files
		 WHERE volume_id = ? AND is_dir = 0 ORDER BY size DESC LIMIT ?`,
		volumeID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

func scanFiles(rows *sql.Rows) ([]File, error) {
	var out []File
	for rows.Next() {
		var f File
		var isDir int
		if err := rows.Scan(&f.VolumeID, &f.Path, &f.Name, &f.Dir, &f.Ext, &isDir, &f.Size, &f.ModTime); err != nil {
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
