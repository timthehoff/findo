# Findo

An iOS app that makes files on any SMB-accessible NAS (developed against a
UniFi UNAS/UNAS Pro, but not limited to it) searchable via Siri/Spotlight
and editable in native apps (Numbers, Word, etc.), without relying on the
Files app's limited SMB support.

## Why this exists (don't relitigate these constraints)

- iOS's native Siri/Spotlight indexing doesn't reach files on SMB network
  shares connected via the Files app.
- The NAS only speaks SMB and NFS natively — no REST/S3 API, no vendor SDK.
- Apple's own iWork apps (Numbers/Pages/Keynote) dropped WebDAV support in
  2020, so WebDAV won't give round-trip editing there either.
- The correct extension point for "appear as a location other apps can open
  AND save to" is a **File Provider Extension** (like Dropbox/OneDrive/Google
  Drive use) — not WebDAV, not a bespoke HTTP-only integration.

## Target architecture

1. **Backend index/proxy service** (self-hosted on the home network, e.g.
   Docker), in `backend/`:
   - Talks to the NAS over SMB (has full driver support, unlike iOS).
   - Crawls and indexes file metadata (name, path, type, mtime, size).
   - Exposes an HTTP API: search, list, streamed file read (must support
     HTTP Range / 206 Partial Content for scrubbing/resuming), and file write
     (for save-back from the iOS app).
   - Tracks a version/mtime per file so the client can detect conflicts if
     the file changed on the NAS outside of the app.

2. **iOS app "Findo"** (not started yet):
   - **File Provider Extension**: appears as a location in the Files/document
     picker UI in any app (Numbers, Word, Preview, etc.). Implements item
     enumeration, on-demand content fetch (streamed from the backend,
     Range-aware), and push-back of edited files to the backend on save.
     Surfaces a conflict if the backend's version doesn't match what the
     extension last fetched.
   - **App Intents + Core Spotlight**: registers indexed files as
     `IndexedEntity` so Siri/Spotlight surface them in search, plus an Open
     intent so Siri can open a matched file (deep-links into the File
     Provider Extension / opens via Quick Look).
   - **Quick Look integration**: `QLPreviewController` pointed at the
     backend's streaming URLs for fast in-app preview (works directly with
     remote URLs given correct Content-Type/Content-Length and Range
     support).
   - Branding: playful, dog-themed. App name is "Findo" (a good dog finds
     and fetches things). Icon: simple line-art dog, maybe with a magnifying
     glass or a file in its mouth. Loading/empty states can have light
     dog-themed copy ("Fetching your files...").

Build incrementally: backend first (connect to SMB, list files, serve one
back over HTTP with Range support), then the iOS side (minimal File Provider
Extension that browses/opens a file from the backend, then save-back, then
App Intents/Spotlight indexing).

## Current status

Backend Milestone 1, plus multi-volume config: SMB crawl → SQLite index →
read-only HTTP API (list, search, Range-aware content, health/stats, manual
reindex), kept fresh between crawls by a live change-notify listener. One
server can run several independently-crawled SMB volumes at once, configured
at runtime (not via `.env`) through `/volumes` and the dashboard, with
passwords encrypted at rest (`internal/crypto`, AES-256-GCM under
`FINDO_MASTER_KEY`). No write/save-back API yet, no conflict-version
tracking yet, no iOS project in the repo yet.

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
  (embedded static HTML/JS, no build step, no frontend framework).
- `httpapi.Server` holds a `map[int64]*volumeRuntime` (one SMB session +
  crawling flag + watch/periodic-crawl goroutines per configured volume),
  guarded by a mutex since volumes can be added/edited/removed at runtime.
  `Server.StartVolume` connects and registers a volume's runtime, then
  chains its initial crawl and change-notify watch; `Server.stopVolume`
  tears it down (used on delete, disable, or before reconnecting after an
  edit). Every `Index` method that touches `files`/`crawl_runs` takes an
  explicit `volumeID` — the same SQLite DB backs every volume, distinguished
  by that column, rather than one DB file per volume.
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
- Keep the dashboard (`internal/dashboard/static/index.html`) as plain
  HTML/CSS/JS calling the JSON API via `fetch` — no build tooling for it.

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
