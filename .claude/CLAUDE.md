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

Backend Milestone 1 only: SMB crawl → SQLite index → read-only HTTP API
(list, search, Range-aware content, health/stats, manual reindex), kept
fresh between crawls by a live change-notify listener. No write/save-back
API yet, no conflict-version tracking yet, no iOS project in the repo yet.

## Environment / secrets

Never invent NAS hostnames, ports, or credentials. Real values go in
`backend/.env` (gitignored, copied from `backend/.env.example`) or are
supplied by the project owner — ask rather than guessing. For local
end-to-end testing without the real NAS, use `backend/docker-compose.dev.yml`
(throwaway Samba container seeded from `backend/testdata/seed/`).

## Backend conventions (Go, `backend/`)

- Module: `github.com/thoff/findo/backend`, Go 1.25.
- Layout: `cmd/findo-server` (entrypoint) · `internal/config` (env/.env
  loading) · `internal/smbclient` (SMB2 session via `go-smb2`, wraps one
  long-lived session — don't reconnect per request) · `internal/index`
  (SQLite-backed metadata store, `modernc.org/sqlite`, pure Go/no cgo) ·
  `internal/httpapi` (routes/handlers/crawl orchestration) ·
  `internal/dashboard` (embedded static HTML/JS, no build step, no frontend
  framework).
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
  a full `Crawl` (`Server.resync`, sharing the `/reindex` crawling guard)
  whenever the notify stream can't guarantee continuity — a
  `STATUS_NOTIFY_ENUM_DIR` overflow or a lost connection
  (`smbclient.ErrNeedsResync`). `Server.PeriodicCrawl` runs the same resync
  path on a fixed interval (24h, hardcoded in `main.go`) as a safety net
  regardless of notify health.
  - `github.com/hirochachacha/go-smb2` only defines the `CHANGE_NOTIFY`
    command opcode, not its request/response wire format, so it's vendored
    locally at `backend/third_party/go-smb2` (via a `go.mod` `replace`)
    with that added in `notify.go` (root package) and
    `internal/smb2/{request,response,fscc,const}.go` (wire format, per
    MS-SMB2/MS-FSCC), tested independently of a live server in
    `internal/smb2/notify_test.go`. Vendored, not forked on GitHub, since
    this is a single-repo project — no reason to host it separately. Don't
    reformat or otherwise touch the rest of `third_party/` beyond what a
    real upstream sync would change; `make check`/`make fix` exclude it
    from the gofmt pass for that reason (`go vet`/`go test` already skip it
    naturally — it's a separate Go module).
- Use the `backend/Makefile` rather than raw `go` commands: `make build`
  (default), `make check` (`gofmt -l` + `go vet`, excluding
  `third_party/`), `make fix` (`gofmt -w`, same exclusion), `make test`
  (`go test -race ./...`), `make run` (`go run ./cmd/findo-server`, reads
  `.env` in the working directory). CI runs `make check test build`.
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
