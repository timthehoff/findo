-- Multi-user accounts with passkeys (issue #37). Users are keyed by username;
-- each has one or more passkeys. Volumes belong to exactly one owner.

CREATE TABLE users (
	id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	username      text NOT NULL,
	role          text NOT NULL CHECK (role IN ('admin', 'user')),
	disabled      boolean NOT NULL DEFAULT false,
	created_at    timestamptz NOT NULL,
	last_login_at timestamptz
);
CREATE UNIQUE INDEX idx_users_username ON users (lower(username));

-- One row per passkey. Only public keys are stored.
CREATE TABLE credentials (
	id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	user_id         bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	credential_id   bytea NOT NULL UNIQUE,
	public_key      bytea NOT NULL,
	sign_count      bigint NOT NULL DEFAULT 0,
	transports      text NOT NULL DEFAULT '', -- comma-separated: usb, nfc, internal, hybrid...
	backup_eligible boolean NOT NULL,
	backup_state    boolean NOT NULL,
	label           text NOT NULL,
	created_at      timestamptz NOT NULL,
	last_used_at    timestamptz
);
CREATE INDEX idx_credentials_user ON credentials (user_id);

-- Single-use links that register a user's first (or a replacement) passkey.
-- Only the SHA-256 of the token is stored; created_by is NULL for the CLI.
CREATE TABLE enrollments (
	id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	user_id    bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	token_hash bytea NOT NULL UNIQUE,
	purpose    text NOT NULL CHECK (purpose IN ('first', 'recovery')),
	expires_at timestamptz NOT NULL,
	used_at    timestamptz,
	created_by bigint REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX idx_enrollments_user ON enrollments (user_id);

-- Server-side sessions. credential_id ties a session to the passkey that made
-- it, so removing a passkey signs out whatever it signed in.
CREATE TABLE sessions (
	id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	user_id       bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	credential_id bigint REFERENCES credentials (id) ON DELETE CASCADE,
	token_hash    bytea NOT NULL UNIQUE,
	created_at    timestamptz NOT NULL,
	last_used_at  timestamptz NOT NULL,
	expires_at    timestamptz NOT NULL,
	user_agent    text NOT NULL DEFAULT '',
	ip            text NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user ON sessions (user_id);

-- Existing volumes stay unowned (NULL) until the first admin is created, who
-- adopts them. No ON DELETE action: a user's volumes are deleted explicitly,
-- so a delete can't orphan a volume whose password is bound to its owner.
ALTER TABLE volumes ADD COLUMN owner_id bigint REFERENCES users (id);

-- Names are unique per owner, not globally: another user's names must not be
-- visible (or collide) through a 409.
DROP INDEX idx_volumes_name;
CREATE UNIQUE INDEX idx_volumes_owner_name ON volumes (owner_id, lower(name));
