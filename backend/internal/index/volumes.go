package index

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Volume is a configured SMB volume, safe to hand back over the API: it
// never carries a password, encrypted or otherwise. A password is always
// present in storage (required on create) so there's no meaningful
// "hasPassword" state to report.
type Volume struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Share         string `json:"share"`
	Username      string `json:"username"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	LastTestOK    *bool  `json:"lastTestOk,omitempty"`
	LastTestAt    string `json:"lastTestAt,omitempty"`
	LastTestError string `json:"lastTestError,omitempty"`
}

// VolumeSecret carries what's needed to open an SMB session, with the
// password still encrypted — the index package stores and returns
// ciphertext only; callers (httpapi, holding the master key) decrypt it.
type VolumeSecret struct {
	Host        string
	Share       string
	Username    string
	PasswordEnc []byte
}

// VolumeUpdate is the mutable subset of a Volume. PasswordEnc == nil keeps
// the volume's existing password unchanged.
type VolumeUpdate struct {
	Name        string
	Host        string
	Share       string
	Username    string
	PasswordEnc []byte
	Enabled     bool
}

const volumeColumns = `id, name, host, share, username, enabled, created_at, updated_at, last_test_ok, last_test_at, COALESCE(last_test_error,'')`

// CreateVolume inserts a new volume, encrypted-password included, and
// returns it.
func (idx *Index) CreateVolume(name, host, share, username string, passwordEnc []byte) (Volume, error) {
	now := time.Now().UTC()
	row := idx.db.QueryRow(
		`INSERT INTO volumes (name, host, share, username, password_enc, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, true, $6, $6)
		 RETURNING `+volumeColumns,
		name, host, share, username, passwordEnc, now,
	)
	v, err := scanVolume(row)
	if err != nil {
		return Volume{}, wrapNameConflict(err)
	}
	return v, nil
}

// wrapNameConflict maps Postgres's unique violation on volumes.name (the
// only unique constraint besides the primary key) to ErrVolumeNameTaken.
func wrapNameConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrVolumeNameTaken
	}
	return err
}

// UpdateVolume applies u to the volume with the given id.
func (idx *Index) UpdateVolume(id int64, u VolumeUpdate) (Volume, error) {
	now := time.Now().UTC()

	var row *sql.Row
	if u.PasswordEnc != nil {
		row = idx.db.QueryRow(
			`UPDATE volumes SET name=$1, host=$2, share=$3, username=$4, password_enc=$5, enabled=$6, updated_at=$7
			 WHERE id=$8 RETURNING `+volumeColumns,
			u.Name, u.Host, u.Share, u.Username, u.PasswordEnc, u.Enabled, now, id,
		)
	} else {
		row = idx.db.QueryRow(
			`UPDATE volumes SET name=$1, host=$2, share=$3, username=$4, enabled=$5, updated_at=$6
			 WHERE id=$7 RETURNING `+volumeColumns,
			u.Name, u.Host, u.Share, u.Username, u.Enabled, now, id,
		)
	}
	v, err := scanVolume(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Volume{}, fmt.Errorf("volume %d not found", id)
	}
	if err != nil {
		return Volume{}, wrapNameConflict(err)
	}
	return v, nil
}

// DeleteVolume removes a volume along with every file and crawl run
// recorded under it.
func (idx *Index) DeleteVolume(id int64) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM files WHERE volume_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM crawl_runs WHERE volume_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM volumes WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// GetVolume returns a single volume by id.
func (idx *Index) GetVolume(id int64) (Volume, bool, error) {
	row := idx.db.QueryRow(`SELECT `+volumeColumns+` FROM volumes WHERE id = $1`, id)
	v, err := scanVolume(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Volume{}, false, nil
		}
		return Volume{}, false, err
	}
	return v, true, nil
}

// ListVolumes returns every configured volume, in creation order.
func (idx *Index) ListVolumes() ([]Volume, error) {
	rows, err := idx.db.Query(`SELECT ` + volumeColumns + ` FROM volumes ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Volume
	for rows.Next() {
		v, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVolumeSecret returns the connection details needed to open an SMB
// session for id, password still encrypted.
func (idx *Index) GetVolumeSecret(id int64) (VolumeSecret, error) {
	var s VolumeSecret
	row := idx.db.QueryRow(`SELECT host, share, username, password_enc FROM volumes WHERE id = $1`, id)
	if err := row.Scan(&s.Host, &s.Share, &s.Username, &s.PasswordEnc); err != nil {
		return VolumeSecret{}, err
	}
	return s, nil
}

// SetVolumeTestResult records the outcome of a connection test, whether
// triggered explicitly or as a side effect of trying to (re)connect.
func (idx *Index) SetVolumeTestResult(id int64, ok bool, errMsg string) error {
	_, err := idx.db.Exec(
		`UPDATE volumes SET last_test_ok=$1, last_test_at=$2, last_test_error=$3 WHERE id=$4`,
		ok, time.Now().UTC(), nullIfEmpty(errMsg), id,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanVolume(row rowScanner) (Volume, error) {
	var v Volume
	var created, updated time.Time
	var lastTestOK sql.NullBool
	var lastTestAt sql.NullTime
	if err := row.Scan(
		&v.ID, &v.Name, &v.Host, &v.Share, &v.Username, &v.Enabled,
		&created, &updated, &lastTestOK, &lastTestAt, &v.LastTestError,
	); err != nil {
		return Volume{}, err
	}
	v.CreatedAt = formatTime(created)
	v.UpdatedAt = formatTime(updated)
	if lastTestOK.Valid {
		ok := lastTestOK.Bool
		v.LastTestOK = &ok
	}
	if lastTestAt.Valid {
		v.LastTestAt = formatTime(lastTestAt.Time)
	}
	return v, nil
}
