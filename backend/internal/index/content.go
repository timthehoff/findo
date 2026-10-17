package index

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrFileGone means the file's row disappeared (a crawl or change event
// removed it) between being queued and being saved. The caller drops it.
var ErrFileGone = errors.New("file no longer indexed")

// Statuses recorded in content_state.
const (
	ContentDone    = "done"
	ContentSkipped = "skipped"
	ContentFailed  = "failed"
)

// Hidden files and anything under a dot-directory aren't indexed for content,
// matching what search hides by default (and keeping .git internals out).
const visibleOnly = ` AND f.path NOT LIKE '.%' AND f.path NOT LIKE '%/.%'`

// PendingFile is a file waiting for text extraction.
type PendingFile struct {
	VolumeID int64
	Path     string
	Ext      string // as stored; callers lowercase it
	Size     int64
	ModTime  int64
}

// PendingContent returns up to limit files, among the given volumes, whose
// extension is in exts and whose text isn't extracted yet or is out of date
// (size or mtime changed since). A file recorded as skipped for being too
// large is retried once maxFileBytes grows to fit it. Failed files wait for
// the file to change or an explicit RetryFailedContent.
func (idx *Index) PendingContent(volumeIDs []int64, exts []string, maxFileBytes int64, limit int) ([]PendingFile, error) {
	if len(volumeIDs) == 0 || len(exts) == 0 {
		return nil, nil
	}
	rows, err := idx.db.Query(`
		SELECT f.volume_id, f.path, f.ext, f.size, f.mod_time
		FROM files f
		LEFT JOIN content_state cs ON cs.volume_id = f.volume_id AND cs.path = f.path
		WHERE f.volume_id = ANY($1) AND NOT f.is_dir AND lower(f.ext) = ANY($2)`+visibleOnly+`
		  AND (cs.path IS NULL
		       OR cs.size <> f.size OR cs.mod_time <> f.mod_time
		       OR (cs.status = 'skipped' AND cs.reason = 'too_large' AND f.size <= $3))
		ORDER BY f.volume_id, f.path COLLATE "C"
		LIMIT $4`, volumeIDs, exts, maxFileBytes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PendingFile
	for rows.Next() {
		var p PendingFile
		if err := rows.Scan(&p.VolumeID, &p.Path, &p.Ext, &p.Size, &p.ModTime); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveContent replaces a file's chunks and records it as done. size and
// modTime are the file's values when it was read.
func (idx *Index) SaveContent(volumeID int64, path string, size, modTime int64, chunks []string) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM chunks WHERE volume_id = $1 AND path = $2`, volumeID, path); err != nil {
		return err
	}
	if len(chunks) > 0 {
		seqs := make([]int32, len(chunks))
		for i := range chunks {
			seqs[i] = int32(i)
		}
		if _, err := tx.Exec(
			`INSERT INTO chunks (volume_id, path, seq, body)
			 SELECT $1, $2, s, b FROM unnest($3::int[], $4::text[]) AS t(s, b)`,
			volumeID, path, seqs, chunks,
		); err != nil {
			return wrapFileGone(err)
		}
	}
	if err := upsertContentState(tx, volumeID, path, size, modTime, ContentDone, "", len(chunks)); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveContentSkip records a file as skipped (too large, not text, no text) or
// failed, dropping any chunks from an earlier version of it.
func (idx *Index) SaveContentSkip(volumeID int64, path string, size, modTime int64, status, reason string) error {
	if status != ContentSkipped && status != ContentFailed {
		return fmt.Errorf("SaveContentSkip: status must be %q or %q", ContentSkipped, ContentFailed)
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM chunks WHERE volume_id = $1 AND path = $2`, volumeID, path); err != nil {
		return err
	}
	if err := upsertContentState(tx, volumeID, path, size, modTime, status, reason, 0); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertContentState(tx *sql.Tx, volumeID int64, path string, size, modTime int64, status, reason string, chunkCount int) error {
	_, err := tx.Exec(
		`INSERT INTO content_state (volume_id, path, size, mod_time, status, reason, chunk_count, indexed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (volume_id, path) DO UPDATE SET
			size = excluded.size, mod_time = excluded.mod_time, status = excluded.status,
			reason = excluded.reason, chunk_count = excluded.chunk_count, indexed_at = excluded.indexed_at`,
		volumeID, path, size, modTime, status, nullIfEmpty(reason), chunkCount, time.Now().UTC(),
	)
	return wrapFileGone(err)
}

// wrapFileGone maps a foreign-key violation (the files row is gone) to
// ErrFileGone.
func wrapFileGone(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrFileGone
	}
	return err
}

// RetryFailedContent forgets volumeID's failed files so they queue again, and
// returns how many.
func (idx *Index) RetryFailedContent(volumeID int64) (int64, error) {
	res, err := idx.db.Exec(`DELETE FROM content_state WHERE volume_id = $1 AND status = 'failed'`, volumeID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ContentReason counts files recorded as skipped or failed for one reason.
type ContentReason struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// ContentProgress is one opted-in volume's extraction progress. Pending
// includes files changed since they were read.
type ContentProgress struct {
	VolumeID int64           `json:"volumeId"`
	Eligible int             `json:"eligible"`
	Done     int             `json:"done"`
	Skipped  int             `json:"skipped"`
	Failed   int             `json:"failed"`
	Pending  int             `json:"pending"`
	Reasons  []ContentReason `json:"reasons"`
}

const maxReasonsPerVolume = 5

// ContentProgress reports, for every volume opted in to content indexing,
// how many eligible files (extension in exts) are done, skipped, failed or
// still pending. A "too large" skip that now fits maxFileBytes counts as
// pending, as it is in the queue.
func (idx *Index) ContentProgress(exts []string, maxFileBytes int64) ([]ContentProgress, error) {
	out := []ContentProgress{}
	byVolume := map[int64]*ContentProgress{}

	rows, err := idx.db.Query(`
		SELECT f.volume_id,
		       count(*),
		       count(*) FILTER (WHERE cs.status = 'done'    AND cs.size = f.size AND cs.mod_time = f.mod_time),
		       count(*) FILTER (WHERE cs.status = 'skipped' AND cs.size = f.size AND cs.mod_time = f.mod_time
		                          AND NOT (cs.reason = 'too_large' AND f.size <= $2)),
		       count(*) FILTER (WHERE cs.status = 'failed'  AND cs.size = f.size AND cs.mod_time = f.mod_time)
		FROM files f
		JOIN volumes v ON v.id = f.volume_id AND v.content_index
		LEFT JOIN content_state cs ON cs.volume_id = f.volume_id AND cs.path = f.path
		WHERE NOT f.is_dir AND lower(f.ext) = ANY($1)`+visibleOnly+`
		GROUP BY f.volume_id ORDER BY f.volume_id`, exts, maxFileBytes)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p ContentProgress
		if err := rows.Scan(&p.VolumeID, &p.Eligible, &p.Done, &p.Skipped, &p.Failed); err != nil {
			rows.Close()
			return nil, err
		}
		p.Pending = p.Eligible - p.Done - p.Skipped - p.Failed
		p.Reasons = []ContentReason{}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		byVolume[out[i].VolumeID] = &out[i]
	}
	// Volumes that are opted in but have nothing eligible yet still report.
	vrows, err := idx.db.Query(`SELECT id FROM volumes WHERE content_index ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for vrows.Next() {
		var id int64
		if err := vrows.Scan(&id); err != nil {
			vrows.Close()
			return nil, err
		}
		if byVolume[id] == nil {
			out = append(out, ContentProgress{VolumeID: id, Reasons: []ContentReason{}})
		}
	}
	if err := vrows.Err(); err != nil {
		vrows.Close()
		return nil, err
	}
	vrows.Close()
	sort.Slice(out, func(i, j int) bool { return out[i].VolumeID < out[j].VolumeID })
	for i := range out {
		byVolume[out[i].VolumeID] = &out[i]
	}

	rrows, err := idx.db.Query(`
		SELECT cs.volume_id, cs.status, COALESCE(cs.reason, ''), count(*)
		FROM content_state cs
		JOIN files f ON f.volume_id = cs.volume_id AND f.path = cs.path
		JOIN volumes v ON v.id = cs.volume_id AND v.content_index
		WHERE cs.status IN ('skipped', 'failed') AND cs.size = f.size AND cs.mod_time = f.mod_time
		  AND NOT (cs.status = 'skipped' AND cs.reason = 'too_large' AND f.size <= $1)
		GROUP BY cs.volume_id, cs.status, cs.reason
		ORDER BY count(*) DESC, cs.status, cs.reason
		LIMIT 500`, maxFileBytes)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var vid int64
		var r ContentReason
		if err := rrows.Scan(&vid, &r.Status, &r.Reason, &r.Count); err != nil {
			return nil, err
		}
		if p := byVolume[vid]; p != nil && len(p.Reasons) < maxReasonsPerVolume {
			p.Reasons = append(p.Reasons, r)
		}
	}
	return out, rrows.Err()
}

// ContentInfo is what is known about one file's extracted text.
type ContentInfo struct {
	Status string // done, skipped or failed
	Reason string // why it was skipped or failed
	Chunks []string
}

// ContentFor returns a file's extraction state and, if it was extracted, its
// text in order. ok is false when extraction was never attempted.
func (idx *Index) ContentFor(volumeID int64, path string) (info ContentInfo, ok bool, err error) {
	var reason sql.NullString
	err = idx.db.QueryRow(
		`SELECT status, reason FROM content_state WHERE volume_id = $1 AND path = $2`, volumeID, path,
	).Scan(&info.Status, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentInfo{}, false, nil
	}
	if err != nil {
		return ContentInfo{}, false, err
	}
	info.Reason = reason.String

	rows, err := idx.db.Query(`SELECT body FROM chunks WHERE volume_id = $1 AND path = $2 ORDER BY seq`, volumeID, path)
	if err != nil {
		return ContentInfo{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return ContentInfo{}, false, err
		}
		info.Chunks = append(info.Chunks, body)
	}
	return info, true, rows.Err()
}
