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
- HTTP API: `GET /files` (list by dir), `GET /search` (name search),
  `GET /files/content` (Range-aware streamed read via `http.ServeContent`),
  `GET /health`, `GET /stats`, `POST /reindex`.
- A minimal built-in dashboard (`GET /`) for poking the index by hand.

Not yet built: file write/save-back, conflict detection (version/mtime
tracking beyond what's indexed), the iOS app in its entirety (File Provider
Extension, App Intents/Spotlight indexing, Quick Look integration), and app
branding/icon assets.

## Backend: running it

Requires Go 1.25+ (see `backend/go.mod`).

```sh
cd backend
cp .env.example .env   # fill in real SMB_HOST/SMB_SHARE/SMB_USER/SMB_PASS
make run
```

The server reads config from environment variables, optionally via a local
`.env` file (see `backend/internal/config/config.go`). Required vars:
`SMB_HOST`, `SMB_SHARE`, `SMB_USER`, `SMB_PASS`. Optional: `HTTP_ADDR`
(default `:8080`), `DB_PATH` (default `findo.db`).

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

Then visit `http://localhost:8080/` for the dashboard, or hit the API
directly, e.g. `curl http://localhost:8080/search?q=budget`.

## Repo layout

```
backend/
  cmd/findo-server/       # main entrypoint
  internal/config/        # env/.env config loading
  internal/smbclient/     # SMB2 session, walk, open (go-smb2)
  internal/index/         # SQLite-backed file metadata index
  internal/httpapi/       # HTTP routes, handlers, crawl orchestration
  internal/dashboard/     # embedded static HTML dashboard
  testdata/seed/          # sample files for the dev Samba container
```
