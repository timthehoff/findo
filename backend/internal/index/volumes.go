package index

import (
	"database/sql"
	"fmt"
	"time"
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

const volumeColumns = `id, name, host, share, username, enabled, created_at, updated_at, last_test_ok, COALESCE(last_test_at,''), COALESCE(last_test_error,'')`

// CreateVolume inserts a new volume, encrypted-password included, and
// returns it.
func (idx *Index) CreateVolume(name, host, share, username string, passwordEnc []byte) (Volume, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := idx.db.Exec(
		`INSERT INTO volumes (name, host, share, username, password_enc, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		name, host, share, username, passwordEnc, now, now,
	)
	if err != nil {
		return Volume{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Volume{}, err
	}
	v, ok, err := idx.GetVolume(id)
	if err != nil {
		return Volume{}, err
	}
	if !ok {
		return Volume{}, fmt.Errorf("volume %d not found after insert", id)
	}
	return v, nil
}

// UpdateVolume applies u to the volume with the given id.
func (idx *Index) UpdateVolume(id int64, u VolumeUpdate) (Volume, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	enabled := 0
	if u.Enabled {
		enabled = 1
	}

	var err error
	if u.PasswordEnc != nil {
		_, err = idx.db.Exec(
			`UPDATE volumes SET name=?, host=?, share=?, username=?, password_enc=?, enabled=?, updated_at=? WHERE id=?`,
			u.Name, u.Host, u.Share, u.Username, u.PasswordEnc, enabled, now, id,
		)
	} else {
		_, err = idx.db.Exec(
			`UPDATE volumes SET name=?, host=?, share=?, username=?, enabled=?, updated_at=? WHERE id=?`,
			u.Name, u.Host, u.Share, u.Username, enabled, now, id,
		)
	}
	if err != nil {
		return Volume{}, err
	}

	v, ok, err := idx.GetVolume(id)
	if err != nil {
		return Volume{}, err
	}
	if !ok {
		return Volume{}, fmt.Errorf("volume %d not found", id)
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

	if _, err := tx.Exec(`DELETE FROM files WHERE volume_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM crawl_runs WHERE volume_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM volumes WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// GetVolume returns a single volume by id.
func (idx *Index) GetVolume(id int64) (Volume, bool, error) {
	row := idx.db.QueryRow(`SELECT `+volumeColumns+` FROM volumes WHERE id = ?`, id)
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
	row := idx.db.QueryRow(`SELECT host, share, username, password_enc FROM volumes WHERE id = ?`, id)
	if err := row.Scan(&s.Host, &s.Share, &s.Username, &s.PasswordEnc); err != nil {
		return VolumeSecret{}, err
	}
	return s, nil
}

// SetVolumeTestResult records the outcome of a connection test, whether
// triggered explicitly or as a side effect of trying to (re)connect.
func (idx *Index) SetVolumeTestResult(id int64, ok bool, errMsg string) error {
	okInt := 0
	if ok {
		okInt = 1
	}
	_, err := idx.db.Exec(
		`UPDATE volumes SET last_test_ok=?, last_test_at=?, last_test_error=? WHERE id=?`,
		okInt, time.Now().UTC().Format(time.RFC3339), nullIfEmpty(errMsg), id,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanVolume(row rowScanner) (Volume, error) {
	var v Volume
	var enabled int
	var lastTestOK sql.NullInt64
	if err := row.Scan(
		&v.ID, &v.Name, &v.Host, &v.Share, &v.Username, &enabled,
		&v.CreatedAt, &v.UpdatedAt, &lastTestOK, &v.LastTestAt, &v.LastTestError,
	); err != nil {
		return Volume{}, err
	}
	v.Enabled = enabled == 1
	if lastTestOK.Valid {
		ok := lastTestOK.Int64 != 0
		v.LastTestOK = &ok
	}
	return v, nil
}
