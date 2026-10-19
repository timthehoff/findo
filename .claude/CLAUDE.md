# Findo

A self-hosted web UI (pinnable to a phone's home screen) that makes files on
any SMB-accessible NAS (developed against a UniFi UNAS/UNAS Pro, but not
limited to it) searchable and previewable from any device.

## Why this exists (don't relitigate these constraints)

- iOS's native Siri/Spotlight indexing doesn't reach files on SMB network
  shares connected via the Files app.
- The NAS only speaks SMB and NFS natively — no REST/S3 API, no vendor SDK —
  and browsers can't speak SMB, so a backend service has to crawl and proxy.
- **Scope is deliberately web-only.** There is no iOS app, File Provider
  Extension, App Intents/Spotlight integration, or write/save-back API, and
  none is planned. WebDAV was also ruled out (iWork dropped it in 2020).

## Target architecture

**Backend index/proxy service** (self-hosted on the home network, running
natively on macOS), in `backend/`:
- Talks to the NAS over SMB (has full driver support, unlike iOS).
- Crawls and indexes file metadata (name, path, type, mtime, size).
- Exposes an HTTP API: search, list, and streamed file read (HTTP Range /
  206 Partial Content for scrubbing/resuming).
- Serves the embedded dashboard/search web UI.

**Opening and editing files:** the UI is read-only.
- Browser-renderable types (PDF, images, text, audio, video) are served
  `Content-Disposition: inline`; everything else, and anything with
  `?download=1`, is an attachment. HTML/SVG are never inline (XSS risk on the
  UI's origin). The extension → MIME/kind/inline table lives in
  `internal/filetype`; see `internal/httpapi/content.go` for the headers.
- To edit, each search result has a "Copy path" button (copies
  `smb://host/share/path`) and shows its share-relative location. The user
  connects once via Files/Finder > Connect to Server and edits in place there.
  Researched: iOS does not hand tapped `smb://` links to Files, hence
  copy-and-paste rather than a link.
- Branding: playful, dog-themed. App name is "Findo" (a good dog finds and
  fetches things). Loading/empty states can have light dog-themed copy.

## Current status

Backend is feature-complete for the web-only scope: SMB crawl → Postgres
index → read-only HTTP API (list, search, Range-aware content with
inline/download handling, health/stats, manual reindex), kept fresh between
crawls by a live change-notify listener. One server can run several
independently-crawled SMB volumes at once, configured at runtime (not via
`.env`) through `/volumes` and the dashboard, with passwords encrypted at
rest (`internal/crypto`, AES-256-GCM under `FINDO_MASTER_KEY`). Every crawl
run is recorded with why it started (`manual`/`startup`/`periodic`/
`resync`), duration, and files/bytes seen or removed
(`GET /volumes/{id}/crawl-runs`), and each volume's change-notify listener
reports its own connected/last-event/resync-count state. The web UI is
three pages — `index.html`, the landing page (live search, breadcrumb
directory browsing, copy-path and download actions; dotfiles — and anything under a dot-directory — are
hidden unless `hidden=1` ("Show hidden" toggle; filtered in SQL by
`Index.Search`/`List` so counts and limits stay right); search text, volume
and browse path live in the URL query — `?q=&volume=<name>&path=&hidden=1` — volume names are unique
(case-insensitively; a `volumes.name` unique index, 409 on clashes, existing
duplicates renamed on migrate) — so
back/forward, reloads and links work), and `volumes.html`
(volume config, crawl/watch monitoring, a per-volume storage/largest-files
breakdown), and `text.html` (a file's extracted text, opened from a search
hit) — with an icon-only light/dark/system theme toggle, a clickable
logo back to `/`, and toast notifications for action feedback. (`/search` and
`/volumes` are JSON API routes, so those pages can't live at the bare path —
hence `/volumes.html`; `/search.html` 301s to `/`.)

## Deployment

Native macOS, no Docker. `deploy/macos/install.sh` is a curl-able installer
that downloads the newest GitHub release binary (arm64 or amd64, verified
against `SHA256SUMS`) and installs a launchd agent (`com.findo.server`, plist
written inline by the script): binary in `~/.local/bin`, `.env` in
`~/Library/Application Support/Findo` (the agent's working directory), the
index in a Homebrew-managed local Postgres (`postgresql@17`, database
`findo`, installed and started by the script), log
in `~/Library/Logs/Findo`. It generates `FINDO_MASTER_KEY` once and never
overwrites an existing `.env`; `FINDO_VERSION` pins a release and
`FINDO_BINARY` installs a local build. See `docs/macos-install.md`.
`.github/workflows/release.yml` builds the macOS binaries on a `vX.Y.Z` tag
(`make build VERSION=…`, `CGO_ENABLED=0`) and publishes the release
(pre-release for 0.x). `make build` stamps the version from the git tag
(`-X main.version`, `dev` when `HEAD` isn't tagged). Register NAS volumes by
IP or DNS name, not `.local` mDNS names. A volume's host may be `host:port`
(the dev Samba container listens on `localhost:1445`).
`GET /version` (`httpapi/version.go`) reports the running version and, for
release builds, whether GitHub's latest release is newer (looked up
server-side, cached 6h, failures retried hourly); `js/version.js` shows it
as a nav badge on both pages. `dev` builds skip the lookup.
The lookup lists releases (not `/releases/latest`, which 404s while every
release is a pre-release) and picks the highest semver; 0.x installs
consider pre-releases, 1.x+ installs only stable ones.

## Environment / secrets

Never invent NAS hostnames, ports, or credentials. `FINDO_MASTER_KEY`
(encrypts volume passwords at rest) goes in `backend/.env` (gitignored,
copied from `backend/.env.example`) or is supplied by the project owner —
ask rather than guessing. SMB volume connection details themselves aren't
environment config: they're added at runtime via `POST /volumes` or the
dashboard, and the server starts fine with zero configured. For local
end-to-end testing without the real NAS, run `make run` and `make samba-up`
(`backend/testdata/samba/compose.yml`, the only Docker left and only a test
fixture: a throwaway Samba container seeded from `backend/testdata/seed/`; see its
comment for the `curl` command that registers it as a volume).

## Backend conventions (Go, `backend/`)

- Module: `github.com/thoff/findo/backend`, Go 1.26 (required by go-pdfium).
- Search dates: `internal/datequery` hand-parses a time phrase out of the
  query ("report last month", "in March 2024", "since 2023") into a
  `[from,to)` mtime range; the rest is the name match. `last/past N units` is
  a trailing window, `this X` is calendar-to-date, months/years need a keyword
  (so "invoice 2023" stays a name search). `/search` also takes
  `modified_after`/`modified_before` (YYYY-MM-DD or unix) and `tz`, and
  reports the detected range as `modified`. A bounded range sorts newest-first.
- Layout: `cmd/findo-server` (entrypoint) · `internal/config` (env/.env
  loading; just `FINDO_MASTER_KEY`/`HTTP_ADDR`/`DATABASE_URL` — no SMB connection
  details) · `internal/filetype` (the one extension registry: Content-Type,
  inline-or-download, and tile `kind`; served to the dashboard at
  `GET /filetypes` so `js/search.js` colors tiles from it instead of keeping
  its own list; a new kind needs `--type-<kind>-*` colors in `css/search.css`) ·
  `internal/crypto` (AES-256-GCM encrypt/decrypt for volume
  passwords at rest) · `internal/smbclient` (SMB2 session via `go-smb2`,
  wraps one long-lived session per volume — don't reconnect per request) ·
  `internal/index` (Postgres-backed metadata store via `pgx` through
  `database/sql`, pure Go/no cgo; schema in numbered `migrations/*.sql` applied
  by `migrate.go` — add a new file, never edit an applied one; also owns volume
  config CRUD in `volumes.go`) · `internal/testdb` (per-test Postgres schema) · `internal/httpapi`
  (routes/handlers/per-volume crawl orchestration) · `internal/dashboard`
  (embedded static multi-page site: `index.html` for search/browse,
  `volumes.html` for volume config/monitoring, `text.html` for extracted text, sharing `css/`/`js/` between
  them — no build step, no frontend framework, ES modules loaded directly).
- `httpapi.Server` holds a `map[int64]*volumeRuntime` (one SMB session +
  crawling flag + watch/periodic-crawl goroutines per configured volume),
  guarded by a mutex since volumes can be added/edited/removed at runtime.
  `Server.StartVolume` connects and registers a volume's runtime, then
  chains its initial crawl and change-notify watch; `Server.stopVolume`
  tears it down (used on delete, disable, or before reconnecting after an
  edit). Every `Index` method that touches `files`/`crawl_runs` takes an
  explicit `volumeID` — the same database backs every volume, distinguished
  by that column, rather than one DB file per volume.
- Every crawl records a `trigger` (`manual` from `/reindex`, `startup` from
  `StartVolume`'s initial crawl, `periodic` from the 24h safety net,
  `resync` from a change-notify gap) plus `files_seen`/`files_removed`/
  `bytes_indexed`/`duration_ms`, written by `Index.FinishCrawlRun` and
  queryable via `Index.CrawlRuns`/`GET /volumes/{id}/crawl-runs`.
  `volumeRuntime` also tracks change-notify health in memory (connected,
  last event time, resync count) — not persisted, since it's only
  meaningful for the currently-running process.
- Postgres is reached through a small `database/sql` pool (`SetMaxOpenConns(10)`),
  so the crawler's batch upserts and HTTP reads don't block each other.
  `UpsertBatch` is one `INSERT … SELECT FROM unnest(arrays) ON CONFLICT`
  statement per batch (repeated paths in a batch are collapsed first, since
  one statement can't update a row twice). File names sort with `COLLATE "C"`
  to keep byte order independent of the server's locale. Name search is
  `ILIKE '%q%'`, backed by a `pg_trgm` GIN index.
- Runtime settings live in the DB, not env vars: a key/value `settings` table
  (`index/settings.go`; a missing row means the default), exposed as
  `GET/PUT /settings` (`{"content": {...}}`; PUT merges over the saved values
  and returns 400 for out-of-range ones) and edited in the "Content indexing"
  card on `volumes.html`, which is loaded once, not on the page's 5s refresh,
  so polling can't overwrite a field mid-edit. File-content indexing is opt-in
  per volume (`volumes.content_index`, written only through
  `Index.SetVolumeContentIndex`, so editing connection details never resets
  it; turning it off also deletes the volume's extracted text).
- Content indexing is a background worker (`httpapi/contentindex.go`,
  `Server.RunContentIndexer`, started from `main`) separate from the metadata
  crawl. There is no queue table: the queue is a query
  (`Index.PendingContent`) over `files LEFT JOIN content_state` for opted-in
  volumes: files with a supported extension whose text isn't extracted yet
  or whose size/mtime changed since (a "skipped: too_large" file retries when
  the size limit grows; failed files wait for the file to change or
  `POST /volumes/{id}/content/retry`). Hidden files and dot-directories are
  not indexed. Each pass takes 50 files, re-reads the settings (so pause and
  limit edits apply within a batch), and runs `workers` goroutines. A volume
  that is disconnected or mid-crawl is skipped; extraction has a 60 s
  per-file timeout (a stalled read is cut off by closing the file) and
  `recover()`s from extractor panics. It sleeps 30 s when idle and is woken
  by `Server.kickContent` (crawl finished, settings saved, opt-in changed,
  retry). Text lands in `chunks` (about 1500 bytes each, `extract.Chunk`) with
  a generated `tsvector`; both it and `content_state` cascade-delete with
  their `files` row, so sweeps and live deletes need no extra code.
  `extract.Extract` picks an extractor from the registry's `Extractor` field:
  `text` (UTF-8 text, markup and source; a NUL byte or mostly invalid UTF-8
  means "not text") and the pure-Go Office ones, `docx`/`xlsx`/`pptx`
  (also the macro and template variants), which read the zip and its XML
  in place via `ReadAt` with no download. Word text includes footnotes,
  endnotes and comments but not headers; Excel yields sheet names and text
  cells only (numbers, dates and booleans aren't searchable words); PowerPoint
  yields slides in numeric order, then speaker notes. They are bounded:
  decompressed XML is capped per part (a zip bomb costs a fixed amount, not
  an unbounded one), archives are capped at 20,000 entries, shared-string
  text at 32 MiB, and output at the text cap; a `blockReaderAt` cache turns
  the zip reader's many tiny reads into 256 KiB reads, since each read is a
  round trip to the NAS. A password-protected file (an OLE file with an
  OOXML name) is recorded as `skipped: encrypted`; legacy `.doc/.xls/.ppt`
  and iWork have no extractor.
  `pdf` uses PDFium (Chrome's engine) compiled to WebAssembly and run by
  wazero via `go-pdfium` (`extract/pdf.go`): still one pure-Go binary, no
  cgo, but about +10 MB and ~300 MB resident while loaded, so the engine
  starts lazily on the first PDF (about 3 s) and is closed after 5 idle
  minutes. A pure-Go parser (`ledongthuc/pdf`) was tried first and rejected:
  no AES-256, nothing from some older producers, panics on damaged files.
  PDFium reads owner-password-only PDFs (copy/print restrictions) normally,
  repairs most damage, and reports a real password as `skipped: encrypted`; an
  image-only scan gives no text, so it is `skipped: no_text` (OCR belongs to
  C). The caller's deadline interrupts a stuck page with `Instance.Kill()`
  (never `Close()` while a call is running: that races), so `Extract` takes a
  `context.Context`. Under `-race` the engine takes ~20 s to compile, so the
  restart half of the idle test only runs without `-race`. `GET /content/status` reports per-volume
  progress and skip/failure reasons for the Volumes page. Fixtures in
  `testdata/seed/office/` and `testdata/seed/pdf/` (also in the dev Samba share)
  were written by python-docx, openpyxl, python-pptx and PDF producers
  (Chrome, macOS, PyMuPDF), so tests check real-world structure.
- `/search` returns name matches first (unchanged substring search), then,
  if there is room under `limit`, files whose extracted text matches
  (`Index.SearchContent`). Each entry has `match` (`name` or `content`) and,
  for content, a `snippet`; the response also has `contentMatches`. The query
  text goes through `tsQuery` (index/contentsearch.go): only letters and
  digits survive, words are ANDed, the last word is a prefix, and queries
  with no word of 2+ characters skip content search; date phrases are
  already stripped, so "last month" alone never searches text. Matches are
  one row per file (its best chunk by `ts_rank_cd`), exclude files whose name
  already matched, and honor the volume, hidden and modified-time filters.
  Only the final page of rows pays for `ts_headline`. Snippets wrap matches
  in control characters (`index.MarkStart`/`MarkEnd`, which the extractor
  strips from stored text); `js/search.js` HTML-escapes the snippet and
  then swaps the markers for `<mark>`, so nothing from a file is ever
  inserted as HTML. `ts_headline` itself drops HTML-like tags from snippets.
  Ranking is `ts_rank_cd`, not BM25 (Postgres has none).
- `GET /files/text?volume=&path=` serves a file's extracted text (the text
  content search runs on) as `text/plain` with `nosniff` and a sandbox CSP,
  or as JSON chunks with `&format=json`; 404 with a reason when there is
  none (not extracted, too large, not text, failed). It is normalized text,
  not a copy of the file, so it isn't for viewing source code (the original
  is already inline); it's for formats a browser can't render. `text.html`
  (+ `js/text.js`, `css/text.css`) is the viewer: it shows the chunks,
  highlights the `q` words, and scrolls to `#chunk-N`. Content hits in
  `/search` carry the best chunk's number (`chunk`), so the search page
  links each snippet, and a "view extracted text" icon, straight there.
- Network exposure (**Findo has no login**): the server listens on `127.0.0.1:8080`
  by default and logs a warning when `HTTP_ADDR` is widened. `main` wraps the
  routes in `httpapi.HostGuard` (not inside `Routes()`, so route tests aren't
  affected): it refuses a `Host` that isn't loopback, an IP literal, a
  single-label name, a private suffix (`.local .lan .localhost .home.arpa
  .internal .localdomain`), the machine's hostname, or `FINDO_ALLOWED_HOSTS`
  (this defeats DNS rebinding, which always arrives with the attacker's public
  domain as `Host`), and refuses state-changing requests whose `Origin` isn't the
  request's own host (cross-site POSTs need no preflight, and JSON bodies are
  parsed whatever their Content-Type). It's browser protection, not
  authentication. The server has header/read/idle timeouts and a 32 KiB header
  cap but deliberately no `WriteTimeout` (it would cut off long downloads and
  video); JSON bodies are capped at 64 KiB (`decodeJSON`); request paths may not
  contain `..` (`smbclient.ValidPath`, also enforced in `Client.Open`);
  `PUT /volumes/{id}` requires the password when host, share or username
  change, so a stored password can't be sent to a server chosen by whoever
  made the request; dashboard pages carry a strict CSP, `X-Frame-Options:
  DENY`, `nosniff` and `no-referrer`.
- Tests need Postgres: `make test` runs them through `scripts/with-testdb.sh`,
  which starts a throwaway cluster on a Unix socket in a temp dir and sets
  `FINDO_TEST_DSN`; each test then gets its own schema from `testdb.DSN(t)`.
  Plain `go test` without that variable **skips** database tests (with a
  message) instead of failing. Requires `brew install postgresql@17`.
- Crawl strategy is mark-and-sweep reconciliation: rows are upserted (not
  blindly reinserted) and stamped with the crawl's `last_seen_run`; a final
  sweep deletes rows not stamped with the current run, i.e. anything no
  longer on the NAS. `Index.Upsert`/`Index.Delete` give the same primitives
  for single-file updates outside a full crawl (e.g. a live change-notify
  event). `Index.Reconcile` is the whole-batch convenience form (upsert all
  + sweep) for callers that already have the complete file list in memory;
  `Crawl` itself uses the lower-level `Index.StreamUpsert` so it never
  buffers the whole tree.
- `smbclient.Client.Walk` lists directories concurrently (a bounded worker
  pool sharing the one SMB session — SMB2 multiplexes many outstanding
  requests over one connection, so this doesn't need multiple
  sessions/connections). It's best-effort: a directory that fails to list
  is recorded and skipped rather than aborting the walk. `Crawl` skips the
  sweep step entirely if any directory failed to list, since a sweep can't
  tell a genuine deletion from a subtree it simply couldn't observe that
  run — better to leave stale rows in place than risk deleting real files.
- Live updates: `smbclient.Client.Watch` streams NAS changes via SMB2
  `CHANGE_NOTIFY` on a dedicated session (a notify request sits outstanding
  until an event arrives, so it can't share the session used for
  request/response traffic). `httpapi.Server.Watch` applies each event
  directly (`Index.Upsert`/`Index.Delete`, stamped with the sentinel run id
  0 so the next real crawl's sweep can confirm or correct it) and triggers
  a full `Crawl` (`Server.resync`, sharing `Server.triggerCrawl`'s
  per-volume crawling guard with the manual `/volumes/{id}/reindex`
  endpoint) whenever the notify stream can't guarantee continuity — a
  `STATUS_NOTIFY_ENUM_DIR` overflow or a lost connection
  (`smbclient.ErrNeedsResync`). `Server.PeriodicCrawl` runs the same resync
  path on a fixed interval (24h, hardcoded in `main.go`) as a safety net
  regardless of notify health.
  - `github.com/hirochachacha/go-smb2` only defines the `CHANGE_NOTIFY`
    command opcode, not its request/response wire format, so we depend on
    [`timthehoff/go-smb2`](https://github.com/timthehoff/go-smb2) — a real
    GitHub fork, not a vendored copy — via a `go.mod` `replace` pointing at
    its `change-notify` branch. The added code
    (`notify.go` in the fork's root package;
    `internal/smb2/{request,response,fscc,const}.go` for the wire format,
    per MS-SMB2/MS-FSCC) lives entirely in that fork's own repo/PR, tested
    there independently of a live server. Pull upstream fixes into the
    fork with normal git tooling (`git fetch upstream && git rebase
    upstream/master` from a clone of the fork with `upstream` pointed at
    `hirochachacha/go-smb2`) rather than hand-reapplying a diff — that's
    the whole reason this is a real fork and not vendored source.
  - After pushing new commits to the fork, re-resolve the pin here with
    `go mod tidy` (from `backend/`) — it turns the branch name back into a
    pseudo-version pinned to that exact commit; don't hand-edit the
    `replace` line's version.
- Keep the dashboard (`internal/dashboard/static/`) as plain HTML/CSS/JS
  calling the JSON API via `fetch` — no build tooling, no frontend
  framework. It's multiple pages/files, not one monolith: `index.html`
  (search/browse) and `volumes.html` (volume config + monitoring) each pull
  in shared `css/base.css` plus their own page CSS, and shared `js/util.js`
  (formatting/escaping helpers) + `js/api.js` (fetch wrapper) via native ES
  module `import`/`export` — no bundler needed since browsers load `.js`
  modules directly. Add a new page the same way: its own `.html` + page-
  specific `css/`/`js/` files, reusing `base.css`/`util.js`/`api.js` rather
  than duplicating them.
- Shared look (`css/base.css`): quiet outlined buttons, one `button.primary`
  (accent fill) per page, and `.icon-btn` for row actions — icon-only with a
  tooltip + `aria-label`, plus `.with-label` only where an icon alone isn't
  obvious (`lineIcon()` in `js/util.js` draws the monochrome line icons). Lists
  are hairline-separated rows, not boxed cards; stat tiles use `--tile-bg`;
  each page opens with a `.page-head` (h1 + one action/control on the right).
- Also shared across pages: `js/theme.js` (`initTheme()`, called once per
  page) drives an explicit light/dark/system toggle — every button with
  `[data-theme-toggle]` cycles it, storing the choice in `localStorage`
  under `findo-theme` and setting `<html data-theme>`, which `base.css`'s
  CSS custom properties key off alongside the `prefers-color-scheme`
  media query (the explicit toggle wins over the OS setting either way).
  `js/toast.js` (`toast(message, kind)`) is the shared notification stack
  for action feedback (save/delete/reindex/test results) — prefer it over
  `alert()` or a silently-refreshed UI. Colors (chart hue, badge/status
  tones) follow the dataviz skill's validated default palette
  (`references/palette.md`) — run its `validate_palette.js` before adding
  any new chart or categorical color, don't eyeball it.
- `GET /volumes/{id}/insights` (`Index.ExtensionBreakdown`/`LargestFiles`)
  backs the dashboard's small per-volume charts (storage by file
  extension, largest files) — plain aggregate SQL (`GROUP BY ext`/
  `ORDER BY size DESC`), not computed client-side from a capped search
  result, so it stays accurate on a NAS too large to page through in the
  browser.

## Dependency and security upkeep

`.github/dependabot.yml` opens weekly PRs for Go modules (in `backend/`) and
workflow actions. go-pdfium and wazero get their own PRs because they parse
untrusted files: the PDFium engine is a WebAssembly blob inside go-pdfium, so a
go-pdfium bump *is* a PDFium bump (its release notes don't name the upstream
version, so read them and the Chrome/PDFium security bulletins). Review those
against CI, which runs the PDF fixtures and the sandbox test.
`.github/workflows/govulncheck.yml` runs `govulncheck` weekly and on go.mod
changes; Dependabot doesn't bump the Go toolchain in go.mod's `go` line, so that
is how a stale toolchain gets noticed: when it fails on a stdlib fix, raise the
`go` line to the patch release it names. govulncheck can't see inside the PDFium
blob.

## Workflow

After making changes to `backend/`, run `make check` and `make test` from
`backend/` before considering the change done. Run `make fix` first if
`check` fails on formatting; fix the code if it fails `go vet`. New logic
needs test coverage alongside it, not bolted on later.

## General conventions

- Don't add abstractions, config knobs, or defensive error handling beyond
  what the current milestone needs (see repo-wide philosophy: build for what
  exists, not hypothetical future requirements).
- Keep comments to the "why", not the "what" — this repo's existing code
  favors short package-level doc comments plus the occasional inline comment
  explaining a non-obvious tradeoff (e.g. why full-replace crawl, why
  `COLLATE "C"`).
