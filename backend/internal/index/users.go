package index

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Roles. Admins manage users and global settings; they get no extra access to
// other users' files.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

var (
	ErrUsernameTaken  = errors.New("that username is already taken")
	ErrUserNotFound   = errors.New("user not found")
	ErrInvalidUser    = errors.New("invalid user")
	ErrCredentialUsed = errors.New("that passkey is already registered")
	// ErrEnrollmentInvalid covers an unknown, used and expired enrollment link
	// alike, so a caller can't tell them apart.
	ErrEnrollmentInvalid = errors.New("this enrollment link is invalid or has expired")
)

// User is a Findo account. It holds no secret: sign-in is by passkey.
type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   string `json:"createdAt"`
	LastLoginAt string `json:"lastLoginAt,omitempty"`
}

const userColumns = `id, username, role, disabled, created_at, last_login_at`

var usernameRE = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._-]{0,63}$`)

// ValidUsername reports whether name is usable: 1 to 64 letters, digits, dots,
// underscores and hyphens, starting with a letter or digit.
func ValidUsername(name string) bool { return usernameRE.MatchString(name) }

func scanUser(row rowScanner) (User, error) {
	var u User
	var created time.Time
	var last sql.NullTime
	if err := row.Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &created, &last); err != nil {
		return User{}, err
	}
	u.CreatedAt = formatTime(created)
	if last.Valid {
		u.LastLoginAt = formatTime(last.Time)
	}
	return u, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CountUsers returns how many users exist, enabled or not.
func (idx *Index) CountUsers() (int, error) {
	var n int
	err := idx.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser adds a user with no passkey yet; an enrollment link registers it.
func (idx *Index) CreateUser(username, role string) (User, error) {
	if !ValidUsername(username) || (role != RoleAdmin && role != RoleUser) {
		return User{}, ErrInvalidUser
	}
	u, err := scanUser(idx.db.QueryRow(
		`INSERT INTO users (username, role, created_at) VALUES ($1, $2, $3) RETURNING `+userColumns,
		username, role, time.Now().UTC(),
	))
	if isUniqueViolation(err) {
		return User{}, ErrUsernameTaken
	}
	return u, err
}

// GetUser returns a user by id.
func (idx *Index) GetUser(id int64) (User, bool, error) {
	u, err := scanUser(idx.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	return u, err == nil, err
}

// GetUserByName returns a user by name, ignoring case.
func (idx *Index) GetUserByName(name string) (User, bool, error) {
	u, err := scanUser(idx.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE lower(username) = lower($1)`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	return u, err == nil, err
}

// UserSummary is a user with the counts the admin Users page shows.
type UserSummary struct {
	User
	Passkeys int `json:"passkeys"`
	Volumes  int `json:"volumes"`
}

// ListUsers returns every user, oldest first.
func (idx *Index) ListUsers() ([]UserSummary, error) {
	rows, err := idx.db.Query(`
		SELECT u.id, u.username, u.role, u.disabled, u.created_at, u.last_login_at,
		       (SELECT count(*) FROM credentials c WHERE c.user_id = u.id),
		       (SELECT count(*) FROM volumes v WHERE v.owner_id = u.id)
		FROM users u ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserSummary{}
	for rows.Next() {
		var s UserSummary
		var created time.Time
		var last sql.NullTime
		if err := rows.Scan(&s.ID, &s.Username, &s.Role, &s.Disabled, &created, &last, &s.Passkeys, &s.Volumes); err != nil {
			return nil, err
		}
		s.CreatedAt = formatTime(created)
		if last.Valid {
			s.LastLoginAt = formatTime(last.Time)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateUser changes a user's role and/or disabled flag (nil leaves a field
// alone). Disabling a user signs them out everywhere.
func (idx *Index) UpdateUser(id int64, role *string, disabled *bool) (User, error) {
	if role != nil && *role != RoleAdmin && *role != RoleUser {
		return User{}, ErrInvalidUser
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	u, err := scanUser(tx.QueryRow(
		`UPDATE users SET role = COALESCE($2, role), disabled = COALESCE($3, disabled)
		 WHERE id = $1 RETURNING `+userColumns, id, role, disabled))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	if u.Disabled {
		if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return User{}, err
		}
	}
	return u, tx.Commit()
}

// DeleteUser removes a user with their passkeys, sessions and enrollment
// links. The user must own no volumes (delete those first): volumes aren't
// cascaded because each holds a password bound to its owner.
func (idx *Index) DeleteUser(id int64) error {
	res, err := idx.db.Exec(`DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// Credential is one registered passkey.
type Credential struct {
	ID             int64    `json:"id"`
	UserID         int64    `json:"-"`
	CredentialID   []byte   `json:"-"`
	PublicKey      []byte   `json:"-"`
	SignCount      uint32   `json:"-"`
	Transports     []string `json:"transports"`
	BackupEligible bool     `json:"backupEligible"`
	BackupState    bool     `json:"backupState"`
	Label          string   `json:"label"`
	CreatedAt      string   `json:"createdAt"`
	LastUsedAt     string   `json:"lastUsedAt,omitempty"`
}

const credentialColumns = `id, user_id, credential_id, public_key, sign_count, transports, backup_eligible, backup_state, label, created_at, last_used_at`

func scanCredential(row rowScanner) (Credential, error) {
	var c Credential
	var signCount int64
	var transports string
	var created time.Time
	var last sql.NullTime
	if err := row.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &signCount, &transports,
		&c.BackupEligible, &c.BackupState, &c.Label, &created, &last); err != nil {
		return Credential{}, err
	}
	c.SignCount = uint32(signCount)
	c.CreatedAt = formatTime(created)
	if last.Valid {
		c.LastUsedAt = formatTime(last.Time)
	}
	c.Transports = []string{}
	if transports != "" {
		c.Transports = strings.Split(transports, ",")
	}
	return c, nil
}

type execer interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}

func insertCredential(q execer, c Credential) (Credential, error) {
	out, err := scanCredential(q.QueryRow(
		`INSERT INTO credentials (user_id, credential_id, public_key, sign_count, transports, backup_eligible, backup_state, label, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+credentialColumns,
		c.UserID, c.CredentialID, c.PublicKey, int64(c.SignCount), strings.Join(c.Transports, ","),
		c.BackupEligible, c.BackupState, c.Label, time.Now().UTC(),
	))
	if isUniqueViolation(err) {
		return Credential{}, ErrCredentialUsed
	}
	return out, err
}

// AddCredential stores a new passkey for c.UserID.
func (idx *Index) AddCredential(c Credential) (Credential, error) {
	return insertCredential(idx.db, c)
}

// ListCredentials returns a user's passkeys, oldest first.
func (idx *Index) ListCredentials(userID int64) ([]Credential, error) {
	rows, err := idx.db.Query(`SELECT `+credentialColumns+` FROM credentials WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Credential{}
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordCredentialUse stores the new sign count and backup state after a
// successful sign-in, and stamps the user's last login.
func (idx *Index) RecordCredentialUse(credentialRowID int64, signCount uint32, backupState bool) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var userID int64
	if err := tx.QueryRow(
		`UPDATE credentials SET sign_count = $2, backup_state = $3, last_used_at = $4 WHERE id = $1 RETURNING user_id`,
		credentialRowID, int64(signCount), backupState, now).Scan(&userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET last_login_at = $2 WHERE id = $1`, userID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// RenameCredential relabels one of userID's passkeys; false if it isn't theirs.
func (idx *Index) RenameCredential(userID, id int64, label string) (bool, error) {
	res, err := idx.db.Exec(`UPDATE credentials SET label = $3 WHERE id = $2 AND user_id = $1`, userID, id, label)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

var ErrLastCredential = errors.New("this is your only passkey; add another before removing it")

// DeleteCredential removes one of userID's passkeys, and with it the sessions
// it signed in. It refuses to remove a user's last one, which would lock them
// out until someone issues a recovery link; false if it isn't theirs.
func (idx *Index) DeleteCredential(userID, id int64) (bool, error) {
	tx, err := idx.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM credentials WHERE user_id = $1`, userID).Scan(&n); err != nil {
		return false, err
	}
	var owned bool
	if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM credentials WHERE id = $2 AND user_id = $1)`, userID, id).Scan(&owned); err != nil {
		return false, err
	}
	if !owned {
		return false, nil
	}
	if n <= 1 {
		return true, ErrLastCredential
	}
	if _, err := tx.Exec(`DELETE FROM credentials WHERE id = $1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Enrollment purposes.
const (
	EnrollFirst    = "first"    // a new user's first passkey
	EnrollRecovery = "recovery" // a replacement after losing every device
)

// CreateEnrollment records a single-use link for userID, valid for ttl, and
// revokes the user's earlier unused links so only the newest works. Only the
// token's hash is stored. createdBy is nil when the CLI issued it.
func (idx *Index) CreateEnrollment(userID int64, tokenHash []byte, purpose string, createdBy *int64, ttl time.Duration) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM enrollments WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return err
	}
	// Spent and expired rows are only history; don't let them pile up.
	if _, err := tx.Exec(`DELETE FROM enrollments WHERE expires_at < $1`, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO enrollments (user_id, token_hash, purpose, expires_at, created_by) VALUES ($1, $2, $3, $4, $5)`,
		userID, tokenHash, purpose, time.Now().UTC().Add(ttl), createdBy); err != nil {
		return err
	}
	return tx.Commit()
}

// Enrollment is a usable enrollment link and who it's for.
type Enrollment struct {
	ID   int64
	User User
}

// LookupEnrollment finds the unused, unexpired enrollment for a token hash, or
// ErrEnrollmentInvalid. A disabled user's link is refused too.
func (idx *Index) LookupEnrollment(tokenHash []byte) (Enrollment, error) {
	var e Enrollment
	var created time.Time
	var last sql.NullTime
	err := idx.db.QueryRow(`
		SELECT e.id, u.id, u.username, u.role, u.disabled, u.created_at, u.last_login_at
		FROM enrollments e JOIN users u ON u.id = e.user_id
		WHERE e.token_hash = $1 AND e.used_at IS NULL AND e.expires_at > $2 AND NOT u.disabled`,
		tokenHash, time.Now().UTC(),
	).Scan(&e.ID, &e.User.ID, &e.User.Username, &e.User.Role, &e.User.Disabled, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentInvalid
	}
	if err != nil {
		return Enrollment{}, err
	}
	e.User.CreatedAt = formatTime(created)
	if last.Valid {
		e.User.LastLoginAt = formatTime(last.Time)
	}
	return e, nil
}

// CompleteEnrollment spends the enrollment and stores c in one step, so a link
// can't be used twice and a failed registration doesn't burn it. A concurrent
// second use gets ErrEnrollmentInvalid.
func (idx *Index) CompleteEnrollment(enrollmentID int64, c Credential) (Credential, error) {
	tx, err := idx.db.Begin()
	if err != nil {
		return Credential{}, err
	}
	defer tx.Rollback()

	var userID int64
	err = tx.QueryRow(
		`UPDATE enrollments SET used_at = $2 WHERE id = $1 AND used_at IS NULL AND expires_at > $2 RETURNING user_id`,
		enrollmentID, time.Now().UTC()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrEnrollmentInvalid
	}
	if err != nil {
		return Credential{}, err
	}
	c.UserID = userID
	out, err := insertCredential(tx, c)
	if err != nil {
		return Credential{}, err
	}
	return out, tx.Commit()
}

// SessionTTL is how long a session lasts without use; each use pushes it out.
const SessionTTL = 30 * 24 * time.Hour

// sessionRefreshAfter limits how often a busy session writes to the database.
const sessionRefreshAfter = 5 * time.Minute

// Session is a signed-in browser.
type Session struct {
	ID         int64  `json:"id"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt"`
	ExpiresAt  string `json:"expiresAt"`
	UserAgent  string `json:"userAgent"`
	IP         string `json:"ip"`
}

// CreateSession stores a session for userID (made with passkey credentialRowID,
// if known) and returns its id. ttl is normally SessionTTL; a negative one
// makes an already-expired session. Expired sessions are swept as a side effect.
func (idx *Index) CreateSession(userID int64, credentialRowID *int64, tokenHash []byte, ttl time.Duration, userAgent, ip string) (int64, error) {
	now := time.Now().UTC()
	if _, err := idx.db.Exec(`DELETE FROM sessions WHERE expires_at < $1`, now); err != nil {
		return 0, err
	}
	var id int64
	err := idx.db.QueryRow(
		`INSERT INTO sessions (user_id, credential_id, token_hash, created_at, last_used_at, expires_at, user_agent, ip)
		 VALUES ($1, $2, $3, $4, $4, $5, $6, $7) RETURNING id`,
		userID, credentialRowID, tokenHash, now, now.Add(ttl), truncate(userAgent, 300), ip,
	).Scan(&id)
	return id, err
}

// SessionUser is who a valid session belongs to.
type SessionUser struct {
	SessionID int64
	User      User
	// Refreshed is set when this lookup pushed the session's expiry out, so the
	// caller can push the cookie's out too.
	Refreshed bool
}

// SessionByToken resolves a token hash to its user, or ok=false if there is no
// such session, it has expired, or the user is disabled. A hit pushes the
// expiry out (at most every few minutes).
func (idx *Index) SessionByToken(tokenHash []byte) (su SessionUser, ok bool, err error) {
	now := time.Now().UTC()
	var created time.Time
	var last sql.NullTime
	var lastUsed time.Time
	err = idx.db.QueryRow(`
		SELECT s.id, s.last_used_at, u.id, u.username, u.role, u.disabled, u.created_at, u.last_login_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > $2 AND NOT u.disabled`,
		tokenHash, now,
	).Scan(&su.SessionID, &lastUsed, &su.User.ID, &su.User.Username, &su.User.Role, &su.User.Disabled, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionUser{}, false, nil
	}
	if err != nil {
		return SessionUser{}, false, err
	}
	su.User.CreatedAt = formatTime(created)
	if last.Valid {
		su.User.LastLoginAt = formatTime(last.Time)
	}
	if now.Sub(lastUsed) > sessionRefreshAfter {
		if _, err := idx.db.Exec(`UPDATE sessions SET last_used_at = $2, expires_at = $3 WHERE id = $1`,
			su.SessionID, now, now.Add(SessionTTL)); err != nil {
			return SessionUser{}, false, err
		}
		su.Refreshed = true
	}
	return su, true, nil
}

// ListSessions returns a user's live sessions, newest first.
func (idx *Index) ListSessions(userID int64) ([]Session, error) {
	rows, err := idx.db.Query(
		`SELECT id, created_at, last_used_at, expires_at, user_agent, ip FROM sessions
		 WHERE user_id = $1 AND expires_at > $2 ORDER BY last_used_at DESC, id DESC`, userID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		var created, used, expires time.Time
		if err := rows.Scan(&s.ID, &created, &used, &expires, &s.UserAgent, &s.IP); err != nil {
			return nil, err
		}
		s.CreatedAt, s.LastUsedAt, s.ExpiresAt = formatTime(created), formatTime(used), formatTime(expires)
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSession signs out one of userID's sessions; false if it isn't theirs.
func (idx *Index) DeleteSession(userID, id int64) (bool, error) {
	res, err := idx.db.Exec(`DELETE FROM sessions WHERE id = $2 AND user_id = $1`, userID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteSessionByToken signs out the session behind a token hash.
func (idx *Index) DeleteSessionByToken(tokenHash []byte) error {
	_, err := idx.db.Exec(`DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// String lets a User read well in log lines.
func (u User) String() string { return fmt.Sprintf("%s(#%d)", u.Username, u.ID) }
