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

**Backend index/proxy service** (self-hosted on the home network, e.g.
Docker), in `backend/`:
- Talks to the NAS over SMB (has full driver support, unlike iOS).
- Crawls and indexes file metadata (name, path, type, mtime, size).
- Exposes an HTTP API: search, list, and streamed file read (HTTP Range /
  206 Partial Content for scrubbing/resuming).
- Serves the embedded dashboard/search web UI.

**Opening and editing files:** the UI is read-only.
- Browser-renderable types (PDF, images, text, audio, video) are served
  `Content-Disposition: inline`; everything else, and anything with
  `?download=1`, is an attachment. HTML/SVG are never inline (XSS risk on the
  UI's origin). See `internal/httpapi/content.go`.
- To edit, each search result has a "Copy path" button (copies
  `smb://host/share/path`) and shows its share-relative location. The user
  connects once via Files/Finder > Connect to Server and edits in place there.
  Researched: iOS does not hand tapped `smb://` links to Files, hence
  copy-and-paste rather than a link.
- Branding: playful, dog-themed. App name is "Findo" (a good dog finds and
  fetches things). Loading/empty states can have light dog-themed copy.

## Current status

Backend is feature-complete for the web-only scope: SMB crawl → SQLite
index → read-only HTTP API (list, search, Range-aware content with
inline/download handling, health/stats, manual reindex), kept fresh between
crawls by a live change-notify listener. One server can run several
independently-crawled SMB volumes at once, configured at runtime (not via
`.env`) through `/volumes` and the dashboard, with passwords encrypted at
rest (`internal/crypto`, AES-256-GCM under `FINDO_MASTER_KEY`). Every crawl
run is recorded with why it started (`manual`/`startup`/`periodic`/
`resync`), duration, and files/bytes seen or removed
(`GET /volumes/{id}/crawl-runs`), and each volume's change-notify listener
reports its own connected/last-event/resync-count state. The dashboard is
two pages — `index.html` (volume config, crawl/watch monitoring, a
per-volume storage/largest-files breakdown) and `search.html` (live search,
breadcrumb directory browsing, copy-path and download actions) — with an
explicit light/dark/system theme toggle and toast notifications for action
feedback.

## Deployment

Docker-first. `backend/Dockerfile` builds a static, non-root distroless
image (version injected via `-X main.version`); `deploy/docker-compose.yml`
is the production stack (named volume `findo-data` at `/data`, settings from
an adjacent `.env`); `install.sh` is the curl-and-run installer/updater (it
generates `FINDO_MASTER_KEY` once and never overwrites an existing `.env`).
`.github/workflows/release.yml` publishes multi-arch images to
`ghcr.io/timthehoff/findo` on `vX.Y.Z` tags. Register NAS volumes by IP or
DNS name, not `.local` mDNS names (they don't resolve in a bridge-networked
container).
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
end-to-end testing without the real NAS, use `backend/docker-compose.dev.yml`
(throwaway Samba container seeded from `backend/testdata/seed/`; see its
comment for the `curl` command that registers it as a volume).

## Backend conventions (Go, `backend/`)

- Module: `github.com/thoff/findo/backend`, Go 1.25.
- Layout: `cmd/findo-server` (entrypoint) · `internal/config` (env/.env
  loading; just `FINDO_MASTER_KEY`/`HTTP_ADDR`/`DB_PATH` — no SMB connection
  details) · `internal/crypto` (AES-256-GCM encrypt/decrypt for volume
  passwords at rest) · `internal/smbclient` (SMB2 session via `go-smb2`,
  wraps one long-lived session per volume — don't reconnect per request) ·
  `internal/index` (SQLite-backed metadata store, `modernc.org/sqlite`, pure
  Go/no cgo; also owns volume config CRUD in `volumes.go`) · `internal/httpapi`
  (routes/handlers/per-volume crawl orchestration) · `internal/dashboard`
  (embedded static multi-page site: `index.html` for volume config/
  monitoring, `search.html` for search/browse, sharing `css/`/`js/` between
  them — no build step, no frontend framework, ES modules loaded directly).
- `httpapi.Server` holds a `map[int64]*volumeRuntime` (one SMB session +
  crawling flag + watch/periodic-crawl goroutines per configured volume),
  guarded by a mutex since volumes can be added/edited/removed at runtime.
  `Server.StartVolume` connects and registers a volume's runtime, then
  chains its initial crawl and change-notify watch; `Server.stopVolume`
  tears it down (used on delete, disable, or before reconnecting after an
  edit). Every `Index` method that touches `files`/`crawl_runs` takes an
  explicit `volumeID` — the same SQLite DB backs every volume, distinguished
  by that column, rather than one DB file per volume.
- Every crawl records a `trigger` (`manual` from `/reindex`, `startup` from
  `StartVolume`'s initial crawl, `periodic` from the 24h safety net,
  `resync` from a change-notify gap) plus `files_seen`/`files_removed`/
  `bytes_indexed`/`duration_ms`, written by `Index.FinishCrawlRun` and
  queryable via `Index.CrawlRuns`/`GET /volumes/{id}/crawl-runs`.
  `volumeRuntime` also tracks change-notify health in memory (connected,
  last event time, resync count) — not persisted, since it's only
  meaningful for the currently-running process.
- SQLite is opened with `SetMaxOpenConns(1)` since the crawler and HTTP
  handlers share one `*sql.DB` — don't raise this without also handling
  concurrent-writer locking (WAL mode, busy_timeout, etc.). Only one
  goroutine (`Index.StreamUpsert`'s consumer, in `Crawl`) ever writes during
  a crawl, so this doesn't need its own locking on top.
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
  (volume config + monitoring) and `search.html` (search/browse) each pull
  in shared `css/base.css` plus their own page CSS, and shared `js/util.js`
  (formatting/escaping helpers) + `js/api.js` (fetch wrapper) via native ES
  module `import`/`export` — no bundler needed since browsers load `.js`
  modules directly. Add a new page the same way: its own `.html` + page-
  specific `css/`/`js/` files, reusing `base.css`/`util.js`/`api.js` rather
  than duplicating them.
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
  `SetMaxOpenConns(1)`).
