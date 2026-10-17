// Package index persists crawled file metadata to Postgres and serves
// listing/search queries over it. One database backs every configured NAS
// volume: files and crawl_runs rows are tagged with a volume_id so a single
// index can hold multiple independently-crawled shares.
package index

import (
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
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

// Open connects to the Postgres database at dsn (a postgres:// URL or
// key=value string), then applies any pending schema migrations.
func Open(dsn string) (*Index, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)

	idx := &Index{db: db}
	if err := idx.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect and migrate: %w", err)
	}
	return idx, nil
}

func (idx *Index) Close() error {
	return idx.db.Close()
}

// ErrVolumeNameTaken is returned when a volume create/update would reuse
// another volume's name (compared case-insensitively). Names are what the
// UI puts in URLs, so they have to identify a volume uniquely.
var ErrVolumeNameTaken = errors.New("a volume with that name already exists")

// UpsertBatch inserts or updates every given file in one statement,
// stamping each row with runID so a later Sweep(volumeID, runID) can tell
// which rows weren't seen in this crawl. Callers with a large crawl to
// index should call this in chunks as entries are discovered rather than
// buffering the whole tree in memory first.
func (idx *Index) UpsertBatch(files []File, runID int64) error {
	if len(files) == 0 {
		return nil
	}

	// A single INSERT ... ON CONFLICT can't touch the same row twice, so keep
	// only the last entry for any repeated path.
	type key struct {
		vol  int64
		path string
	}
	last := make(map[key]int, len(files))
	for i, f := range files {
		last[key{f.VolumeID, f.Path}] = i
	}

	n := len(last)
	vols := make([]int64, 0, n)
	paths := make([]string, 0, n)
	names := make([]string, 0, n)
	dirs := make([]string, 0, n)
	exts := make([]string, 0, n)
	isDirs := make([]bool, 0, n)
	sizes := make([]int64, 0, n)
	mtimes := make([]int64, 0, n)
	for i, f := range files {
		if last[key{f.VolumeID, f.Path}] != i {
			continue
		}
		vols = append(vols, f.VolumeID)
		paths = append(paths, f.Path)
		names = append(names, f.Name)
		dirs = append(dirs, f.Dir)
		exts = append(exts, f.Ext)
		isDirs = append(isDirs, f.IsDir)
		sizes = append(sizes, f.Size)
		mtimes = append(mtimes, f.ModTime)
	}

	_, err := idx.db.Exec(`
		INSERT INTO files (volume_id, path, name, dir, ext, is_dir, size, mod_time, last_seen_run)
		SELECT v, p, n, d, e, i, s, m, $9::bigint
		FROM unnest($1::bigint[], $2::text[], $3::text[], $4::text[], $5::text[], $6::boolean[], $7::bigint[], $8::bigint[])
			AS t(v, p, n, d, e, i, s, m)
		ON CONFLICT (volume_id, path) DO UPDATE SET
			name = excluded.name,
			dir = excluded.dir,
			ext = excluded.ext,
			is_dir = excluded.is_dir,
			size = excluded.size,
			mod_time = excluded.mod_time,
			last_seen_run = excluded.last_seen_run
	`, vols, paths, names, dirs, exts, isDirs, sizes, mtimes, runID)
	if err != nil {
		return fmt.Errorf("upsert %d files: %w", len(paths), err)
	}
	return nil
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
	res, err := idx.db.Exec(`DELETE FROM files WHERE volume_id = $1 AND last_seen_run != $2`, volumeID, runID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Delete removes a single file by path, e.g. from a live change-notify
// event.
func (idx *Index) Delete(volumeID int64, p string) error {
	_, err := idx.db.Exec(`DELETE FROM files WHERE volume_id = $1 AND path = $2`, volumeID, strings.Trim(p, "/"))
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
	var id int64
	err := idx.db.QueryRow(
		`INSERT INTO crawl_runs (volume_id, trigger, started_at) VALUES ($1, $2, $3) RETURNING id`,
		volumeID, trigger, time.Now().UTC(),
	).Scan(&id)
	return id, err
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
		`UPDATE crawl_runs SET finished_at = $1, duration_ms = $2, error = $3,
		 files_seen = $4, files_removed = $5, bytes_indexed = $6 WHERE id = $7`,
		time.Now().UTC(), res.DurationMS, nullIfEmpty(errMsg),
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

const crawlRunColumns = `id, volume_id, trigger, started_at, finished_at, COALESCE(duration_ms,0), COALESCE(error,''), files_seen, files_removed, bytes_indexed`

// LatestCrawlRun returns volumeID's most recent crawl run, if it has one.
func (idx *Index) LatestCrawlRun(volumeID int64) (CrawlRun, bool, error) {
	row := idx.db.QueryRow(
		`SELECT `+crawlRunColumns+` FROM crawl_runs WHERE volume_id = $1 ORDER BY id DESC LIMIT 1`,
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
		`SELECT `+crawlRunColumns+` FROM crawl_runs WHERE volume_id = $1 ORDER BY id DESC LIMIT $2`,
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
	var started time.Time
	var finished sql.NullTime
	if err := row.Scan(
		&cr.ID, &cr.VolumeID, &cr.Trigger, &started, &finished, &cr.DurationMS,
		&cr.Error, &cr.FilesSeen, &cr.FilesRemoved, &cr.BytesIndexed,
	); err != nil {
		return CrawlRun{}, err
	}
	cr.StartedAt = formatTime(started)
	if finished.Valid {
		cr.FinishedAt = formatTime(finished.Time)
	}
	return cr, nil
}

// formatTime renders a stored timestamp as RFC3339 UTC, the form the JSON
// API has always reported.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// List returns immediate children of dir ("" for the volume's root).
func (idx *Index) List(volumeID int64, dir string, includeHidden bool) ([]File, error) {
	dir = normalizeDir(dir)
	hidden := ""
	if !includeHidden {
		hidden = ` AND name NOT LIKE '.%'`
	}
	rows, err := idx.db.Query(
		`SELECT volume_id, path, name, dir, ext, is_dir, size, mod_time FROM files
		 WHERE volume_id = $1 AND dir = $2`+hidden+` ORDER BY is_dir DESC, name COLLATE "C" ASC`,
		volumeID, dir,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFiles(rows)
}

// TimeRange is a half-open [From, To) span of modification times in unix
// seconds; zero means unbounded on that side.
type TimeRange struct {
	From int64
	To   int64
}

// Search returns files/dirs whose name contains q (case-insensitive), limited
// to those modified within modified. volumeID == 0 searches across every
// volume; otherwise it's scoped to one. Unless includeHidden is set, dotfiles
// and anything inside a dot-directory (any path component starting with ".")
// are left out. A bounded modified range sorts newest-first, since "what
// changed lately" is the point; otherwise results are ordered by name.
func (idx *Index) Search(volumeID int64, q string, limit int, includeHidden bool, modified TimeRange) ([]File, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var args []interface{}
	arg := func(v interface{}) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	var where []string
	if volumeID != 0 {
		where = append(where, `volume_id = `+arg(volumeID))
	}
	where = append(where, `name ILIKE `+arg("%"+likeEscape(q)+"%"))
	order := `name COLLATE "C" ASC`
	if modified.From != 0 {
		where = append(where, `mod_time >= `+arg(modified.From))
		order = `mod_time DESC, name COLLATE "C" ASC`
	}
	if modified.To != 0 {
		where = append(where, `mod_time < `+arg(modified.To))
		order = `mod_time DESC, name COLLATE "C" ASC`
	}
	if !includeHidden {
		where = append(where, `path NOT LIKE '.%' AND path NOT LIKE '%/.%'`)
	}
	rows, err := idx.db.Query(
		`SELECT volume_id, path, name, dir, ext, is_dir, size, mod_time FROM files
		 WHERE `+strings.Join(where, ` AND `)+` ORDER BY `+order+` LIMIT `+arg(limit),
		args...,
	)
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
		 WHERE volume_id = $1 AND path = $2`,
		volumeID, strings.Trim(p, "/"),
	)
	var f File
	if err := row.Scan(&f.VolumeID, &f.Path, &f.Name, &f.Dir, &f.Ext, &f.IsDir, &f.Size, &f.ModTime); err != nil {
		if err == sql.ErrNoRows {
			return File{}, false, nil
		}
		return File{}, false, err
	}
	return f, true, nil
}

// Stats returns file/dir counts aggregated across every volume.
func (idx *Index) Stats() (Stats, error) {
	var s Stats
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE NOT is_dir`).Scan(&s.FileCount); err != nil {
		return s, err
	}
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE is_dir`).Scan(&s.DirCount); err != nil {
		return s, err
	}
	return s, nil
}

// VolumeStats returns file/dir counts for a single volume.
func (idx *Index) VolumeStats(volumeID int64) (Stats, error) {
	var s Stats
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE volume_id = $1 AND NOT is_dir`, volumeID).Scan(&s.FileCount); err != nil {
		return s, err
	}
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM files WHERE volume_id = $1 AND is_dir`, volumeID).Scan(&s.DirCount); err != nil {
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
		`SELECT CASE WHEN ext = '' THEN '(none)' ELSE ext END, COUNT(*), COALESCE(SUM(size), 0)::bigint
		 FROM files WHERE volume_id = $1 AND NOT is_dir
		 GROUP BY ext ORDER BY SUM(size) DESC, ext LIMIT $2`,
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
		 WHERE volume_id = $1 AND NOT is_dir ORDER BY size DESC, path COLLATE "C" LIMIT $2`,
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
		if err := rows.Scan(&f.VolumeID, &f.Path, &f.Name, &f.Dir, &f.Ext, &f.IsDir, &f.Size, &f.ModTime); err != nil {
			return nil, err
		}
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
