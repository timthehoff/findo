CREATE TABLE settings (
	key        text PRIMARY KEY,
	value      text NOT NULL,
	updated_at timestamptz NOT NULL
);

-- Per-volume opt-in for file-content indexing.
ALTER TABLE volumes ADD COLUMN content_index boolean NOT NULL DEFAULT false;
