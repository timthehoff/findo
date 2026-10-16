# Design: storage move to Postgres, then B — file-content search

Status: **proposal, for review. No code written.** Sections 1–2 are new: the index moves
from SQLite to Postgres + pgvector first, because C needs real ANN search and B's chunk
store depends on that choice. C (embeddings) builds on B's `chunks` table.

## 1. Spike: Postgres 17 + pgvector 0.8.7 on this Mac

Throwaway cluster (initdb in a scratch dir, port 54329, nothing started via `brew services`),
Homebrew `postgresql@17` + `pgvector` bottles, M-series arm64, 10 cores, 32 GB. 300,000
synthetic chunks: ~200 words of text each, 768-d float32 vectors.

| What | Result |
|---|---|
| Install | `brew install postgresql@17 pgvector`; the pgvector bottle ships for both 17 and 18; `CREATE EXTENSION vector` works on a stock `initdb` |
| Load 300k rows (text + vectors) / build GIN on `tsvector` | 21 s / 20 s (the `to_tsvector` UPDATE was 61 s) |
| HNSW build (m=16, ef_construction=64, 1 GB maintenance_work_mem) | **27 s**; index **1.1 GB** (it warned the graph outgrew 1 GB, so give the build more) |
| ANN top-50, `ef_search=100`, warm cache | **0.5–1.5 ms** |
| Exact cosine scan top-50, 7 parallel workers / 1 worker | 430 ms / 1.4 s |
| FTS `ts_rank_cd` top-50, 16,830 matching chunks (a common-ish term) | 92 ms |
| Hybrid RRF (FTS top-50 ⨝ ANN top-50, one SQL statement) | **~195 ms**, dominated by the FTS ranking |
| Total on disk, 300k chunks (text + tsvector + vectors + GIN + HNSW) | 3.4 GB (~11 KB/chunk) |

What this does **not** show: recall. Vectors were random, so there is no structure for the
graph to exploit; the timings are valid, the recall isn't measured. Recall and the right
`ef_search` get tested on real embeddings in C. Cold-cache latency and
`shared_buffers` tuning (default 128 MB is far too low for a 1 GB graph) also aren't
measured. Two details I couldn't confirm: that `ts_headline` accepts control characters
(`\x02`/`\x03`) as highlight markers (my test call had an invalid option, so it proved
nothing), and that `pg_trgm` is in the Homebrew build (almost certainly; it's core contrib).

Findings that shape the design:

- **Exact scan is 430 ms even parallelized**, vs ~1 ms for HNSW: the "brute force is fine"
  assumption doesn't hold in Postgres, which is the reason for the move.
- **Homebrew's service must set `LC_ALL=en_US.UTF-8`.** Without it the postmaster aborted
  at startup ("became multithreaded during startup") on this macOS; a bare launchd plist
  has no locale either. Brew's own `brew services` plist is expected to set it
  (verify at install time).
- Unix socket paths are limited to ~100 bytes on macOS; the test-DB script uses a short
  `/tmp/...` socket dir.
- Parallel scans can't read temp tables; irrelevant in production, but a trap in tests.

## 2. Move to Postgres (its own PR, before B)

Pure port, no new features, same HTTP API.

- **Driver:** `github.com/jackc/pgx/v5` (pure Go, so still no cgo) with its pool; replaces
  `modernc.org/sqlite`. `SetMaxOpenConns(1)` and its single-writer reasoning go away; the
  crawl's streaming batch upsert uses `COPY` into a temp staging table + `INSERT … ON CONFLICT`,
  or plain batched `INSERT … ON CONFLICT` with pgx `Batch` (the latter first; COPY only if
  the crawl benchmark needs it).
- **Config:** `DATABASE_URL` replaces `DB_PATH` (a connection string is bootstrap config,
  like `FINDO_MASTER_KEY`; everything else lives in the DB). Default
  `postgres:///findo?host=/tmp` style socket DSN for a local brew Postgres.
  Findo retries the initial connection the way volumes already do, since launchd has no
  start ordering.
- **Migrations:** the ad-hoc `CREATE TABLE IF NOT EXISTS` in `migrate()` can't add columns
  (B needs one). Replace with numbered embedded SQL files + a ~40-line runner and a
  `schema_migrations` table. Pre-release, so no SQLite data migration: re-add volumes
  and re-crawl. `dedupeVolumeNames` is dropped.
- **Schema changes in the port:** `volumes.name` unique via `UNIQUE INDEX ON volumes (lower(name))`;
  timestamps become `timestamptz`; `files(name)` gets a `pg_trgm` GIN index so
  `name ILIKE '%q%'` is index-assisted instead of a full scan (same substring semantics as
  today; `likeEscape` stays).
- **Package surface:** `internal/index` keeps its method signatures (`Open(dsn)` instead of
  `Open(path)`), so `httpapi` changes are limited to construction and any error-string
  matching (`wrapNameConflict` checks SQLite's message; becomes a `pgconn` `23505` check).
- **Tests:** a real Postgres is required, no SQLite-style in-memory fallback.
  `scripts/with-testdb.sh` creates a throwaway cluster (initdb in `mktemp -d`, short socket
  dir, `LC_ALL` set, `CREATE EXTENSION vector, pg_trgm`), exports `FINDO_TEST_DSN`, runs the
  command, and tears down; `make test` calls it. Each test gets its own schema
  (`CREATE SCHEMA t_<rand>` + `search_path` on the pool config) so tests stay isolated and
  can use `t.Parallel()`. If `FINDO_TEST_DSN` is set already it is used as-is. Plain
  `go test` without it **skips** DB tests with a message pointing at `make test`. Prerequisite
  (documented): `brew install postgresql@17 pgvector`. No Docker involved.
- **Deployment (native, under launchd) — three services:**
  1. **Postgres**: Homebrew's `brew services start postgresql@17` (brew writes and owns the
     plist; check it sets `LC_ALL`). The install doc includes the one-time `createdb findo`,
     `CREATE EXTENSION`s, and a `shared_buffers`/`maintenance_work_mem` bump in
     `postgresql.conf` sized for the HNSW index.
  2. **Findo**: `~/Library/LaunchAgents/…findo.plist` (`KeepAlive`, env for
     `FINDO_MASTER_KEY`/`DATABASE_URL`/`HTTP_ADDR`, logs to `~/Library/Logs/findo/`).
  3. **Model service** (C): own plist, later.
- **Backups:** `volumes` (encrypted passwords) and `settings` are the only irreplaceable
  rows; files/chunks are re-derivable (embeddings expensively so). The install doc suggests a
  nightly `pg_dump` via a fourth tiny launchd job; not built in this milestone.
- **Docker removal is a separate PR** as you decided (Dockerfile, compose, `install.sh`,
  release workflow, CLAUDE.md Deployment section; add the plists and install doc). The
  Samba dev container stays useful, but `smbclient.go` hard-codes port 445, so running
  Findo natively against `localhost:1445` needs the port to be configurable (small change
  in that PR).
- **CLAUDE.md updates in the port PR:** storage layout, the `SetMaxOpenConns` note removed,
  test setup.

Alternatives considered and set aside (details in the table at the end): SQLite FTS5 +
brute-force vectors (exact scan too slow at the target scale); a separate search engine
(Meilisearch/Typesense/OpenSearch) beside SQLite (filters and deletes must be mirrored and
reconciled); OpenSearch alone (JVM, no first-class macOS install).

## 3. B: settings

Plain key/value, typed in Go with defaults (no row = default):

```sql
CREATE TABLE settings (key text PRIMARY KEY, value text NOT NULL, updated_at timestamptz NOT NULL);
```

`index.ContentSettings` is loaded/saved as a whole. API: `GET /settings`, `PUT /settings`
(range-validated; 400 on nonsense). Keys for B only:

| key | default | meaning |
|---|---|---|
| `content.paused` | `false` | worker idles; pause/resume button on the dashboard |
| `content.max_file_bytes` | 100 MB | larger files are recorded `skipped: too_large`, never read |
| `content.max_text_bytes` | 1 MB | extractor stops after this much text per file |
| `content.workers` | 1 | files in flight at once (1–4) |
| `content.delay_ms` | 0 | sleep after each file, to go easy on the NAS/CPU |

Per-volume opt-in is a column (matching how volumes are configured): migration
`ALTER TABLE volumes ADD COLUMN content_index boolean NOT NULL DEFAULT false`. Opting out
purges that volume's chunks (derived data; the UI confirms).

C's keys (`embed.endpoint`, model names, `max_chunks_per_file`) come with C.

**Dashboard (volumes.html):** a "Content indexing" card above the volume list: pause/resume,
the four numeric fields (MB / KB units), Save → toast. Each volume row gets a "Search file
contents" checkbox in its edit form and a progress line under it (`done / pending / skipped /
failed`, thin bar) from `GET /content/status`, polled while the page is open. Failed/skipped
counts expand to a short list with the reason.

## 4. B: extraction queue

**No queue table; the queue is a query.** One state table, one row per attempted file:

```sql
CREATE TABLE content_state (
  volume_id   bigint NOT NULL,
  path        text   NOT NULL,
  size        bigint NOT NULL,      -- as of extraction
  mod_time    bigint NOT NULL,
  status      text   NOT NULL CHECK (status IN ('done','skipped','failed')),
  reason      text,                 -- too_large | unsupported | no_text | encrypted | <error>
  chunk_count int    NOT NULL DEFAULT 0,
  indexed_at  timestamptz NOT NULL,
  PRIMARY KEY (volume_id, path),
  FOREIGN KEY (volume_id, path) REFERENCES files (volume_id, path) ON DELETE CASCADE
);
```

A file is *pending* when it is a non-directory in a content-enabled volume, has a supported
type, and has no `content_state` row **or** `(size, mod_time)` differs from `files`. Also
re-pending: `skipped/too_large` rows whose size now fits a raised `max_file_bytes`. `failed`
rows retry only when the file changes or the user hits "Retry failed" (no retry storms on a
corrupt PDF). Crawls and the change-notify listener only write `files`, so neither needs a
hook.

Worker (`httpapi/content.go`, one coordinator goroutine, N workers):

1. Poll pending in batches of ~50, skipping disconnected or mid-crawl volumes and everything
   while `content.paused`.
2. Read via the volume's existing SMB session (`rt.smb.Open`), never a new one.
3. Pick the extractor from the file-type registry (section 6); run with a 60 s timeout
   (const) and `recover()`: malformed zip/PDF must not kill the process.
4. One short transaction per file: replace the file's chunks, upsert `content_state`.
5. Sleep `delay_ms`; re-read settings each batch so edits apply without a restart.

Cleanup: the **foreign keys with `ON DELETE CASCADE`** replace the SQLite triggers I'd
planned: `Sweep`, `Delete` and volume deletion remove a file's `content_state` and `chunks`
with no code changes. (A rename is delete + upsert, so the file is re-extracted; acceptable.)

**Bounds:** `max_file_bytes` checked against `files.size` *before* any read, then enforced
again with `io.LimitReader`; extractors stop at `max_text_bytes`; zip entries are read through
a decompressed-size limiter (zip-bomb guard). The per-file *chunk* cap belongs to C
(embedding cost); B's text cap already bounds chunk count.

**Extractors** (`internal/extract`, `Extract(r io.ReaderAt, size, maxText) (string, error)`):
plain text/markdown/csv/json/code (type allowlist + UTF-8 validity, so a binary named `.txt`
is `skipped: unsupported`); docx/xlsx/pptx via `archive/zip` + `encoding/xml` (docx `w:t`,
xlsx shared + inline strings, pptx `a:t`); PDF text layer via `github.com/ledongthuc/pdf`
(pure Go; **needs its own spike** on real PDFs before commit; fallback is skipping PDFs in
B). `smb2.File` has `ReadAt`, so zips and PDFs parse in place without downloading. Scanned
PDFs with no text layer → `skipped: no_text` (C's OCR picks them up). Legacy `.doc/.xls/.ppt`
and iWork: unsupported.

## 5. B: chunk schema and search

```sql
CREATE TABLE chunks (
  id        bigserial PRIMARY KEY,
  volume_id bigint NOT NULL,
  path      text   NOT NULL,
  seq       int    NOT NULL,
  body      text   NOT NULL,
  tsv       tsvector GENERATED ALWAYS AS (to_tsvector('english', body)) STORED,
  UNIQUE (volume_id, path, seq),
  FOREIGN KEY (volume_id, path) REFERENCES files (volume_id, path) ON DELETE CASCADE
);
CREATE INDEX chunks_tsv ON chunks USING gin (tsv);
```

Chunks are ~1500 chars cut on paragraph/sentence boundaries. Chunk granularity gives better
snippets and is exactly what C embeds: C adds `chunk_embeddings (chunk_id bigint REFERENCES
chunks ON DELETE CASCADE, model text, emb vector)` plus one HNSW index per configured model
(a partial/expression index on `emb::vector(N)`, since embedding dimension depends on the
model). B touches none of that. The `english` text-search config stems English only;
non-English documents still match but with weaker stemming (a config knob only if it hurts).

**Query:** user text → tsquery: tokens individually quoted, ANDed, last token gets `:*`
(type-as-you-search); a tiny sanitizer builds it, not `websearch_to_tsquery`, since that has
no prefix support. Ranking is `ts_rank_cd` (cover density), **not BM25**; Postgres core has
no BM25. It's the main quality give-up of this move, partly offset by RRF fusion with vectors
in C. Hits are grouped to the best chunk per file, joined to `files`, and filtered by
volume / hidden / date range exactly like name search today. `ts_headline` produces the
snippet for the top results only (~20 µs/row, applied after the limit).

**`/search` and ranking:** name matches stay as today (substring, now trigram-indexed) and
come first; content matches not already in the name set follow, up to `limit`. Each entry gains
`match: "name"|"content"` and, for content hits, `snippet` with `\x02…\x03` highlight markers
(the browser HTML-escapes it, then swaps markers for `<mark>`; the server never emits HTML).
Content hits are included whenever any volume has indexing on. In C, RRF fuses FTS + vector
results as in the spike (one SQL statement), but name matches still pin to the top.

**UI (index.html):** a muted snippet line under the path with `<mark>` highlights and an "in
contents" tag; a status line while indexing is incomplete ("content index 62%, results may
be partial"); the empty-state for a date-filtered search keeps its date label (found
missing during the A sanity check).

## 6. File-type registry, text view, clickable type tile

**Registry (`internal/filetype`).** One hard-coded table, `ext → {mime, kind, inline,
extractor}`, replaces three things that each know about extensions separately: the
`inlineTypes` map in `httpapi/content.go`, the extractor choice, and `fileTypeBadge()` in
`search.js`. Adding a type is one line.

- **Hard-coded, not `mime.TypeByExtension`:** the latter reads OS mime files, so macOS and a
  container disagree (why `inlineTypes` is explicit today). `content.go` keeps its header
  logic and looks types up here; HTML/SVG remain non-inline.
- **Sniffing is a fallback only:** `http.DetectContentType` on the first 512 bytes when the
  extension is missing/unknown, and as the guard that skips a binary named `.txt`.
- **Extractors are keyed by `kind`**, so `.docx`/`.docm`/`.dotx` share one.
- **Frontend:** `GET /filetypes` returns `{ext → {kind, label, mime}}` once at page load;
  `fileTypeBadge()` becomes a lookup instead of a second copy of the list.

**Text view.** `GET /files/text?volume=&path=` returns the extracted text (chunks
concatenated in `seq` order) as `text/plain; charset=utf-8` with the same `nosniff` +
`CSP: sandbox` as `/files/content`; 404 with a reason body if there is none (not indexed,
skipped, pending). That makes docx/xlsx/pptx readable in a browser. A content hit gets a
"View text" action next to Download; the snippet links to a small dashboard page
(`text.html`, fed by `/files/text?format=json`) that renders chunks as separate blocks with
`<mark>` on the query terms and scrolls to the matching chunk. Still read-only.

**Type tile click.** In `search.js`, the `.type-tile` (the "PDF"/"XLS"/folder-icon square at
the left of each row) becomes an `<a>` with exactly the same `href`/`target`/`rel` as
`.result-name`, or `data-dir`/`data-vol` for folders (the existing `a[data-dir]` handler at
`search.js:273` already picks those up). It gets `tabindex="-1"` and `aria-hidden="true"` so
keyboard and screen-reader users see one link per row. CSS: `cursor: pointer`, no underline,
colors unchanged.

## 7. Rollout

Three PRs, each leaving `make check && make test` green:

1. **Docker removal + native install**: delete Dockerfile, compose, `install.sh`, release
   workflow, CI docker job, CLAUDE.md Deployment section; add a launchd agent for Findo, a
   source-based `deploy/macos/install.sh`, a native-install doc, and `host:port` support in
   the SMB volume host (so the dev Samba container on `localhost:1445` works natively).
   Findo still uses SQLite at this point; the Postgres setup is added to the doc in PR 2.
2. **Postgres port** (section 2): pgx, migrations runner, `with-testdb.sh`, schema port,
   CLAUDE.md updates, Postgres (brew services) added to the install doc/script. No feature
   changes.
3. **B**, as commits:
   1. `internal/filetype` registry; `content.go` and `fileTypeBadge()` switch to it;
      clickable type tile. (Only visible change: the tile is a link.)
   2. `settings` table + `/settings` + `volumes.content_index` + dashboard card/checkbox (inert).
   3. `chunks`/`content_state` + text/markdown/code extractor + worker + `/content/status` +
      search integration + snippet UI + `/files/text` and the text view page. *Useful alone.*
   4. docx/xlsx/pptx extractors.
   5. PDF extractor (after its spike).

Tests: extractors on fixtures (including malformed zip, zip bomb, binary-as-`.txt`); queue
query (new / changed / unchanged / raised-limit / failed-no-retry); cascade cleanup; worker
with the existing fake SMB; tsquery sanitizer (quotes, operators, `-`, `:*`, stray `&|!<>`);
settings validation; search ordering (name before content, filters apply to both); registry
(every `inline` type's MIME, HTML/SVG never inline, sniff fallback).

## Decisions (resolved)

1. Defaults: **100 MB** file cap, 1 MB text cap, 1 worker.
2. Opting a volume out of content indexing **purges** its chunks.
3. Postgres on the same Mac over a local Unix socket, no password. Assumes it is not
   reachable from the LAN (`listen_addresses` stays at the default `localhost`, plus the
   socket; the install doc says so).
4. PR order: **Docker removal first**, then the Postgres port, then B.
5. "N days ago" date phrases: left out.

## Appendix: backend options considered

| Option | Full-text | ANN | RRF under our control | Joins with `files` (volume/hidden/date filters) | Ops on the Mac |
|---|---|---|---|---|---|
| SQLite FTS5 + brute force | BM25 + snippets | no (exact scan) | yes, in Go | yes | none |
| **Postgres + pgvector (chosen)** | `ts_rank_cd`, `ts_headline` | HNSW | yes, one SQL statement | yes | +1 service (brew-managed) |
| Meilisearch | BM25-like, typo-tolerant | yes | no: blends internally (`semanticRatio`) | no: filters must be mirrored and reconciled | +1 service, single binary |
| Typesense | yes | HNSW | built-in rank fusion | no (same) | +1 service; index in RAM; macOS binary unconfirmed |
| OpenSearch | best of the set | yes | RRF processor (2.19+/3.x) | no (same) | JVM, heaviest; no first-class macOS install |

Not evaluated: Qdrant, LanceDB (vector-only, so FTS would live in a second place anyway).
The macOS/Meilisearch/Typesense/OpenSearch rows rest on documentation searches, not on
running them; only the Postgres row was exercised.
