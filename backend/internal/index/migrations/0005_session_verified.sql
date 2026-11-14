-- When a passkey last proved presence for this session. Sensitive actions (add
-- or remove a passkey, issue a sign-in link, manage users) need a recent one,
-- so a stolen session alone can't plant a passkey that outlives it.
ALTER TABLE sessions ADD COLUMN verified_at timestamptz;
UPDATE sessions SET verified_at = created_at;
ALTER TABLE sessions ALTER COLUMN verified_at SET NOT NULL;
