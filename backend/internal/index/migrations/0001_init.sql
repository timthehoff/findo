CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE volumes (
	id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	name            text NOT NULL,
	host            text NOT NULL,
	share           text NOT NULL,
	username        text NOT NULL,
	password_enc    bytea NOT NULL,
	enabled         boolean NOT NULL DEFAULT true,
	created_at      timestamptz NOT NULL,
	updated_at      timestamptz NOT NULL,
	last_test_ok    boolean,
	last_test_at    timestamptz,
	last_test_error text
);
CREATE UNIQUE INDEX idx_volumes_name ON volumes (lower(name));

CREATE TABLE files (
	volume_id     bigint NOT NULL,
	path          text NOT NULL,
	name          text NOT NULL,
	dir           text NOT NULL,
	ext           text NOT NULL,
	is_dir        boolean NOT NULL,
	size          bigint NOT NULL,
	mod_time      bigint NOT NULL,
	last_seen_run bigint NOT NULL DEFAULT 0,
	PRIMARY KEY (volume_id, path)
);
CREATE INDEX idx_files_volume_dir ON files (volume_id, dir);
-- Backs the substring (ILIKE '%q%') name search.
CREATE INDEX idx_files_name_trgm ON files USING gin (name gin_trgm_ops);

CREATE TABLE crawl_runs (
	id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	volume_id     bigint NOT NULL,
	trigger       text NOT NULL DEFAULT 'manual',
	started_at    timestamptz NOT NULL,
	finished_at   timestamptz,
	duration_ms   bigint,
	error         text,
	files_seen    bigint NOT NULL DEFAULT 0,
	files_removed bigint NOT NULL DEFAULT 0,
	bytes_indexed bigint NOT NULL DEFAULT 0
);
CREATE INDEX idx_crawl_runs_volume ON crawl_runs (volume_id, id DESC);
