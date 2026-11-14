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
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Share    string `json:"share"`
	Username string `json:"username"`
	Enabled  bool   `json:"enabled"`
	// ContentIndex opts the volume in to file-content indexing.
	ContentIndex bool `json:"contentIndex"`
	// OwnerID is who the volume belongs to; nil only for a volume that existed
	// before accounts did and hasn't been adopted by the first admin yet.
	OwnerID       *int64 `json:"ownerId"`
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
	ID          int64
	OwnerID     *int64
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

const volumeColumns = `id, name, host, share, username, enabled, content_index, owner_id, created_at, updated_at, last_test_ok, last_test_at, COALESCE(last_test_error,'')`

// VolumeAAD is the authenticated data a volume's password is sealed with: its
// id and owner, so a ciphertext copied onto another volume or owner won't
// open. An unowned (pre-accounts) volume's password was sealed with none.
func VolumeAAD(volumeID int64, ownerID *int64) []byte {
	if ownerID == nil {
		return nil
	}
	return []byte(fmt.Sprintf("findo/volume-password/v1:volume=%d:owner=%d", volumeID, *ownerID))
}

// CreateVolume inserts a new volume owned by ownerID and returns it. seal
// encrypts the password once the volume's id is known (the id is part of what
// the ciphertext is bound to), so the insert and the seal are one transaction.
func (idx *Index) CreateVolume(ownerID int64, name, host, share, username string, seal func(volumeID int64) ([]byte, error)) (Volume, error) {
	tx, err := idx.db.Begin()
	if err != nil {
		return Volume{}, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var id int64
	err = tx.QueryRow(
		`INSERT INTO volumes (name, host, share, username, password_enc, enabled, owner_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, ''::bytea, true, $5, $6, $6) RETURNING id`,
		name, host, share, username, ownerID, now,
	).Scan(&id)
	if err != nil {
		return Volume{}, wrapNameConflict(err)
	}
	passwordEnc, err := seal(id)
	if err != nil {
		return Volume{}, err
	}
	v, err := scanVolume(tx.QueryRow(
		`UPDATE volumes SET password_enc = $2 WHERE id = $1 RETURNING `+volumeColumns, id, passwordEnc))
	if err != nil {
		return Volume{}, err
	}
	return v, tx.Commit()
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

// SetVolumeContentIndex turns file-content indexing on or off for a volume
// and returns the updated volume. Turning it off also deletes the volume's
// extracted text and extraction state: it's derived data, and keeping it
// would leave stale text searchable.
func (idx *Index) SetVolumeContentIndex(id int64, on bool) (Volume, error) {
	tx, err := idx.db.Begin()
	if err != nil {
		return Volume{}, err
	}
	defer tx.Rollback()

	v, err := scanVolume(tx.QueryRow(
		`UPDATE volumes SET content_index = $1 WHERE id = $2 RETURNING `+volumeColumns, on, id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Volume{}, fmt.Errorf("volume %d not found", id)
	}
	if err != nil {
		return Volume{}, err
	}
	if !on {
		if _, err := tx.Exec(`DELETE FROM chunks WHERE volume_id = $1`, id); err != nil {
			return Volume{}, err
		}
		if _, err := tx.Exec(`DELETE FROM content_state WHERE volume_id = $1`, id); err != nil {
			return Volume{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Volume{}, err
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

// ListVolumesByOwner returns the volumes ownerID owns, in creation order.
func (idx *Index) ListVolumesByOwner(ownerID int64) ([]Volume, error) {
	rows, err := idx.db.Query(`SELECT `+volumeColumns+` FROM volumes WHERE owner_id = $1 ORDER BY id ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Volume{}
	for rows.Next() {
		v, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VolumeIDsByOwner returns the ids of the volumes ownerID owns: the scope every
// per-user query is limited to.
func (idx *Index) VolumeIDsByOwner(ownerID int64) ([]int64, error) {
	rows, err := idx.db.Query(`SELECT id FROM volumes WHERE owner_id = $1 ORDER BY id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetVolumeOwner hands a volume to newOwnerID. The password is bound to the
// owner, so reseal turns the stored ciphertext into one for the new owner.
func (idx *Index) SetVolumeOwner(id, newOwnerID int64, reseal func(passwordEnc []byte) ([]byte, error)) (Volume, error) {
	tx, err := idx.db.Begin()
	if err != nil {
		return Volume{}, err
	}
	defer tx.Rollback()

	var enc []byte
	if err := tx.QueryRow(`SELECT password_enc FROM volumes WHERE id = $1 FOR UPDATE`, id).Scan(&enc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Volume{}, fmt.Errorf("volume %d not found", id)
		}
		return Volume{}, err
	}
	newEnc, err := reseal(enc)
	if err != nil {
		return Volume{}, err
	}
	v, err := scanVolume(tx.QueryRow(
		`UPDATE volumes SET owner_id = $2, password_enc = $3, updated_at = $4 WHERE id = $1 RETURNING `+volumeColumns,
		id, newOwnerID, newEnc, time.Now().UTC()))
	if err != nil {
		return Volume{}, wrapNameConflict(err)
	}
	return v, tx.Commit()
}

// AdoptUnownedVolumes gives every ownerless volume to ownerID: how volumes that
// predate accounts end up with the first admin. reseal re-encrypts each
// password for its new owner. It returns how many were adopted.
func (idx *Index) AdoptUnownedVolumes(ownerID int64, reseal func(volumeID int64, passwordEnc []byte) ([]byte, error)) (int, error) {
	tx, err := idx.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	type pending struct {
		id  int64
		enc []byte
	}
	rows, err := tx.Query(`SELECT id, password_enc FROM volumes WHERE owner_id IS NULL ORDER BY id FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	var vols []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.enc); err != nil {
			rows.Close()
			return 0, err
		}
		vols = append(vols, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, p := range vols {
		enc, err := reseal(p.id, p.enc)
		if err != nil {
			return 0, fmt.Errorf("volume %d: %w", p.id, err)
		}
		if _, err := tx.Exec(`UPDATE volumes SET owner_id = $2, password_enc = $3 WHERE id = $1`, p.id, ownerID, enc); err != nil {
			return 0, wrapNameConflict(err)
		}
	}
	return len(vols), tx.Commit()
}

// GetVolumeSecret returns the connection details needed to open an SMB
// session for id, password still encrypted.
func (idx *Index) GetVolumeSecret(id int64) (VolumeSecret, error) {
	var s VolumeSecret
	row := idx.db.QueryRow(`SELECT id, owner_id, host, share, username, password_enc FROM volumes WHERE id = $1`, id)
	var owner sql.NullInt64
	if err := row.Scan(&s.ID, &owner, &s.Host, &s.Share, &s.Username, &s.PasswordEnc); err != nil {
		return VolumeSecret{}, err
	}
	if owner.Valid {
		s.OwnerID = &owner.Int64
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
	var owner sql.NullInt64
	if err := row.Scan(
		&v.ID, &v.Name, &v.Host, &v.Share, &v.Username, &v.Enabled, &v.ContentIndex, &owner,
		&created, &updated, &lastTestOK, &lastTestAt, &v.LastTestError,
	); err != nil {
		return Volume{}, err
	}
	if owner.Valid {
		v.OwnerID = &owner.Int64
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
