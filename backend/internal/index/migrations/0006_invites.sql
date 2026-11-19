-- Invite links: an administrator makes a link that lets whoever opens it create
-- their own (ordinary) account, up to max_uses times before it expires or is
-- revoked. Only the SHA-256 of the token is stored.
CREATE TABLE invites (
	id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	token_hash bytea NOT NULL UNIQUE,
	label      text NOT NULL DEFAULT '',
	created_by bigint REFERENCES users (id) ON DELETE SET NULL,
	created_at timestamptz NOT NULL,
	expires_at timestamptz NOT NULL,
	max_uses   integer NOT NULL CHECK (max_uses BETWEEN 1 AND 100),
	uses       integer NOT NULL DEFAULT 0,
	revoked_at timestamptz,
	CHECK (uses <= max_uses)
);

-- Which invite made an account, so a leaked link's accounts can be found.
ALTER TABLE users ADD COLUMN invite_id bigint REFERENCES invites (id) ON DELETE SET NULL;
