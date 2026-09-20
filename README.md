# Findo

Findo makes files on any SMB-accessible NAS (developed against a UniFi
UNAS/UNAS Pro, but not limited to it) searchable via Siri/Spotlight on iOS
and editable in native apps (Numbers, Word, etc.), without relying on the
Files app's limited SMB support.

## Why this exists

- iOS's native Siri/Spotlight indexing doesn't reach files on SMB network
  shares connected via the Files app.
- The NAS only speaks SMB and NFS natively — no REST/S3 API, no vendor SDK.
- Apple's own iWork apps (Numbers/Pages/Keynote) dropped WebDAV support in
  2020, so WebDAV won't give round-trip editing there either.
- The correct extension point for "appear as a location other apps can open
  AND save to" is a **File Provider Extension** (like Dropbox/OneDrive/Google
  Drive use), not WebDAV and not a bespoke HTTP-only integration.

## Architecture

Two components:

1. **Backend index/proxy service** (self-hosted on the home network, e.g.
   Docker) — talks to the NAS over SMB, crawls and indexes file metadata, and
   exposes an HTTP API for search, listing, streamed file read (Range-aware),
   and eventually file write (save-back from the iOS app). See
   [`backend/`](backend/).

2. **iOS app "Findo"** *(not started yet)* — a File Provider Extension so the
   NAS appears as a location in the Files/document picker UI in any app, plus
   App Intents + Core Spotlight so Siri/Spotlight can find and open indexed
   files, plus Quick Look integration for fast in-app preview.

For NAS credentials, network topology, iOS developer account status, and
other environment specifics, see `.env` (not committed — copy from
`backend/.env.example`) and ask the project owner rather than guessing.

## Current status

Only **Milestone 1** of the backend is built:

- Connects to a single SMB share and recursively crawls it into a SQLite
  index, listing directories concurrently. Each crawl reconciles the index
  against what it finds (upserts changed/new files, sweeps away anything no
  longer present) rather than wiping and rebuilding from scratch, and is
  best-effort — a directory that fails to list is recorded and skipped
  rather than aborting the whole crawl.
- A live change-notify listener (SMB2 `CHANGE_NOTIFY`, via
  [`timthehoff/go-smb2`](https://github.com/timthehoff/go-smb2) — a fork
  adding the wire format upstream never implemented) keeps the index in
  sync between crawls, applying individual add/modify/remove/rename events
  directly. Whenever it can't guarantee it saw everything (the NAS reports
  dropped changes, or the watch session had to reconnect), it triggers a
  full crawl to catch up — plus a daily full crawl runs regardless, as a
  safety net.
- Multiple SMB volumes can be configured at runtime — no restart required.
  Credentials are encrypted at rest (AES-256-GCM under a server-wide master
  key) and never round-trip back out of the API once set.
- Every crawl is recorded with why it ran (manual reindex, startup,
  the periodic safety net, or a change-notify-triggered resync), how long
  it took, and how many files/bytes it saw or removed — visible per volume
  in the dashboard and via `GET /volumes/{id}/crawl-runs`. Each volume's
  change-notify listener also reports its own live connected/last-event/
  resync-count state.
- HTTP API: `GET/POST /volumes`, `PUT/DELETE /volumes/{id}`,
  `POST /volumes/{id}/test` (or `POST /volumes/test` before saving),
  `POST /volumes/{id}/reindex`, `GET /volumes/{id}/crawl-runs`,
  `GET /files` (list by dir, `?volume=`),
  `GET /search` (name search, optionally scoped to `?volume=`),
  `GET /files/content` (Range-aware streamed read via `http.ServeContent`,
  `?volume=`), `GET /health`, `GET /stats`.
- A minimal built-in dashboard: `GET /` for configuring volumes and crawl/
  watch monitoring, `GET /search.html` for live search and breadcrumb
  directory browsing.

Not yet built: file write/save-back, conflict detection (version/mtime
tracking beyond what's indexed), the iOS app in its entirety (File Provider
Extension, App Intents/Spotlight indexing, Quick Look integration), and app
branding/icon assets.

## Backend: running it

Requires Go 1.25+ (see `backend/go.mod`).

```sh
cd backend
cp .env.example .env   # fill in FINDO_MASTER_KEY (openssl rand -base64 32)
make run
```

The server reads config from environment variables, optionally via a local
`.env` file (see `backend/internal/config/config.go`). The only required var
is `FINDO_MASTER_KEY`, which encrypts SMB volume passwords at rest. Optional:
`HTTP_ADDR` (default `:8080`), `DB_PATH` (default `findo.db`).

SMB volumes themselves aren't configured via environment variables — add one
at runtime through the dashboard (`GET /`) or `POST /volumes` once the server
is running; the server starts fine with zero volumes configured.

### Makefile targets

All run from `backend/`:

| Target | What it does |
| --- | --- |
| `make build` (default) | `go build` the server binary |
| `make check` | `gofmt -l` + `go vet` |
| `make fix` | `gofmt -w` to auto-format |
| `make test` | `go test -race ./...` |
| `make run` | `go run ./cmd/findo-server` |

CI runs `make check test build` on every push/PR.

### Dev stack without a real NAS

`backend/docker-compose.dev.yml` spins up a throwaway Samba container seeded
with sample files from `backend/testdata/seed/`, plus the findo backend
pointed at it — useful for exercising the HTTP API end-to-end without access
to the real NAS:

```sh
cd backend
docker compose -f docker-compose.dev.yml up --build
```

Then visit `http://localhost:8080/` for the dashboard and add the samba
container as a volume (host `samba`, share `testshare`, user/pass
`testuser`/`testpass`), or hit the API directly — see the compose file's
comment for the equivalent `curl -X POST /volumes` command.

## Repo layout

```
backend/
  cmd/findo-server/       # main entrypoint
  internal/config/        # env/.env config loading
  internal/crypto/        # AES-256-GCM at-rest encryption for volume passwords
  internal/smbclient/     # SMB2 session, walk, open, change-notify watch
  internal/index/         # SQLite-backed file metadata index + volume config
  internal/httpapi/       # HTTP routes, handlers, per-volume crawl orchestration
  internal/dashboard/     # embedded static multi-page dashboard (html/css/js)
  testdata/seed/          # sample files for the dev Samba container
```
