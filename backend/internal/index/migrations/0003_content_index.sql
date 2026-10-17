-- One row per file whose text extraction was attempted. The extraction queue
-- is a query over files LEFT JOIN content_state, not a table of its own.
CREATE TABLE content_state (
	volume_id   bigint NOT NULL,
	path        text   NOT NULL,
	size        bigint NOT NULL,   -- the file's size and mtime when it was read
	mod_time    bigint NOT NULL,
	status      text   NOT NULL CHECK (status IN ('done', 'skipped', 'failed')),
	reason      text,              -- too_large | unsupported | no_text | <error>
	chunk_count integer NOT NULL DEFAULT 0,
	indexed_at  timestamptz NOT NULL,
	PRIMARY KEY (volume_id, path),
	FOREIGN KEY (volume_id, path) REFERENCES files (volume_id, path) ON DELETE CASCADE
);

-- Extracted text, cut into chunks for focused snippets (and, later, embeddings).
CREATE TABLE chunks (
	id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	volume_id bigint NOT NULL,
	path      text   NOT NULL,
	seq       integer NOT NULL,
	body      text   NOT NULL,
	tsv       tsvector GENERATED ALWAYS AS (to_tsvector('english', body)) STORED,
	UNIQUE (volume_id, path, seq),
	FOREIGN KEY (volume_id, path) REFERENCES files (volume_id, path) ON DELETE CASCADE
);
CREATE INDEX chunks_tsv ON chunks USING gin (tsv);
