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
two pages — `index.html`, the landing page (live search, breadcrumb
directory browsing, copy-path and download actions; dotfiles — and anything under a dot-directory — are
hidden unless `hidden=1` ("Show hidden" toggle; filtered in SQL by
`Index.Search`/`List` so counts and limits stay right); search text, volume
and browse path live in the URL query — `?q=&volume=<name>&path=&hidden=1` — volume names are unique
(case-insensitively; a `volumes.name` unique index, 409 on clashes, existing
duplicates renamed on migrate) — so
back/forward, reloads and links work), and `volumes.html`
(volume config, crawl/watch monitoring, a per-volume storage/largest-files
breakdown) — with an icon-only light/dark/system theme toggle, a clickable
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

- Module: `github.com/thoff/findo/backend`, Go 1.25.
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
  `volumes.html` for volume config/monitoring, sharing `css/`/`js/` between
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
