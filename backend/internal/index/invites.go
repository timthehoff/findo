package index

import (
	"database/sql"
	"errors"
	"time"
)

// ErrInviteInvalid covers an unknown, expired, revoked and used-up invite alike,
// so a caller can't tell them apart.
var ErrInviteInvalid = errors.New("this invite link is invalid, used up or has expired")

// Invite statuses, as Invite.Status reports them.
const (
	InviteActive  = "active"
	InviteUsedUp  = "used up"
	InviteExpired = "expired"
	InviteRevoked = "revoked"
)

// Invite is a link that lets people create their own accounts.
type Invite struct {
	ID            int64  `json:"id"`
	Label         string `json:"label"`
	CreatedByName string `json:"createdBy,omitempty"`
	CreatedAt     string `json:"createdAt"`
	ExpiresAt     string `json:"expiresAt"`
	MaxUses       int    `json:"maxUses"`
	Uses          int    `json:"uses"`
	Status        string `json:"status"`
}

const inviteColumns = `i.id, i.label, COALESCE(u.username, ''), i.created_at, i.expires_at, i.max_uses, i.uses, i.revoked_at`

func scanInvite(row rowScanner, now time.Time) (Invite, error) {
	var v Invite
	var created, expires time.Time
	var revoked sql.NullTime
	if err := row.Scan(&v.ID, &v.Label, &v.CreatedByName, &created, &expires, &v.MaxUses, &v.Uses, &revoked); err != nil {
		return Invite{}, err
	}
	v.CreatedAt, v.ExpiresAt = formatTime(created), formatTime(expires)
	switch {
	case revoked.Valid:
		v.Status = InviteRevoked
	case !expires.After(now):
		v.Status = InviteExpired
	case v.Uses >= v.MaxUses:
		v.Status = InviteUsedUp
	default:
		v.Status = InviteActive
	}
	return v, nil
}

// CreateInvite records an invite valid for ttl and up to maxUses accounts.
// Only the token's hash is stored. Invites that ended long ago are pruned.
func (idx *Index) CreateInvite(label string, tokenHash []byte, createdBy int64, ttl time.Duration, maxUses int) (Invite, error) {
	now := time.Now().UTC()
	if _, err := idx.db.Exec(`DELETE FROM invites WHERE expires_at < $1`, now.Add(-30*24*time.Hour)); err != nil {
		return Invite{}, err
	}
	var id int64
	if err := idx.db.QueryRow(
		`INSERT INTO invites (token_hash, label, created_by, created_at, expires_at, max_uses)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		tokenHash, label, createdBy, now, now.Add(ttl), maxUses).Scan(&id); err != nil {
		return Invite{}, err
	}
	return scanInvite(idx.db.QueryRow(
		`SELECT `+inviteColumns+` FROM invites i LEFT JOIN users u ON u.id = i.created_by WHERE i.id = $1`, id), now)
}

// ListInvites returns every invite, newest first.
func (idx *Index) ListInvites() ([]Invite, error) {
	now := time.Now().UTC()
	rows, err := idx.db.Query(`SELECT ` + inviteColumns + ` FROM invites i LEFT JOIN users u ON u.id = i.created_by ORDER BY i.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invite{}
	for rows.Next() {
		v, err := scanInvite(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RevokeInvite ends an invite now; accounts already made from it stay. False if
// there is no such invite.
func (idx *Index) RevokeInvite(id int64) (bool, error) {
	res, err := idx.db.Exec(`UPDATE invites SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, time.Now().UTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// LookupInvite finds the usable invite for a token hash: not expired, not
// revoked, with uses left. Anything else is ErrInviteInvalid.
func (idx *Index) LookupInvite(tokenHash []byte) (Invite, error) {
	now := time.Now().UTC()
	v, err := scanInvite(idx.db.QueryRow(
		`SELECT `+inviteColumns+` FROM invites i LEFT JOIN users u ON u.id = i.created_by WHERE i.token_hash = $1`, tokenHash), now)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && v.Status != InviteActive) {
		return Invite{}, ErrInviteInvalid
	}
	return v, err
}

// ReserveUserID takes the next user id without creating the user. A passkey's
// user handle is the user's id and is fixed when it is registered, so someone
// joining by invite needs their id before their account exists; the account is
// only made once the passkey has been verified, leaving no half-made users
// behind. Abandoned reservations leave harmless gaps in the ids.
func (idx *Index) ReserveUserID() (int64, error) {
	var id int64
	err := idx.db.QueryRow(`SELECT nextval(pg_get_serial_sequence('users', 'id'))`).Scan(&id)
	return id, err
}

// CompleteInvite spends one use of the invite, creates the ordinary user with
// the id reserved earlier, and stores their first passkey, all or nothing. A
// name already taken (ErrUsernameTaken), a passkey already registered
// (ErrCredentialUsed) or an invite used up by someone else in the meantime
// (ErrInviteInvalid) leaves everything, including the use, untouched.
func (idx *Index) CompleteInvite(inviteID, userID int64, username string, c Credential) (User, Credential, error) {
	if !ValidUsername(username) {
		return User{}, Credential{}, ErrInvalidUser
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return User{}, Credential{}, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	// The row lock makes concurrent joins take turns, so the last use goes to one.
	var spent int64
	err = tx.QueryRow(
		`UPDATE invites SET uses = uses + 1
		 WHERE id = $1 AND revoked_at IS NULL AND expires_at > $2 AND uses < max_uses RETURNING id`,
		inviteID, now).Scan(&spent)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, Credential{}, ErrInviteInvalid
	}
	if err != nil {
		return User{}, Credential{}, err
	}

	user, err := scanUser(tx.QueryRow(
		`INSERT INTO users (id, username, role, created_at, invite_id) OVERRIDING SYSTEM VALUE
		 VALUES ($1, $2, $3, $4, $5) RETURNING `+userColumns,
		userID, username, RoleUser, now, inviteID))
	if isUniqueViolation(err) {
		return User{}, Credential{}, ErrUsernameTaken
	}
	if err != nil {
		return User{}, Credential{}, err
	}

	c.UserID = userID
	cred, err := insertCredential(tx, c)
	if err != nil {
		return User{}, Credential{}, err
	}
	return user, cred, tx.Commit()
}
