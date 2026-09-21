# Findo 🐾

Every good dog knows how to fetch. Findo does the same thing for a NAS: it
crawls your files over SMB, builds a searchable index, and serves them back
over HTTP so Siri and Spotlight on iOS can find them, and native apps
(Numbers, Word, Preview, etc.) can open and edit them directly.

## Why this exists

- iOS's native Siri/Spotlight indexing doesn't reach files on SMB network
  shares connected via the Files app. A good dog can't fetch what it can't
  smell.
- The NAS only speaks SMB and NFS natively — no REST/S3 API, no vendor SDK
  to lean on instead.
- Apple's own iWork apps (Numbers/Pages/Keynote) dropped WebDAV support in
  2020, so WebDAV wouldn't give round-trip editing there either, even if we
  wanted to fetch that way.
- The correct extension point for "appear as a location other apps can open
  AND save to" is a **File Provider Extension** (the same mechanism
  Dropbox/OneDrive/Google Drive use) — not WebDAV, not a bespoke HTTP-only
  integration.

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

Current build status and what's implemented lives in
[`.claude/CLAUDE.md`](.claude/CLAUDE.md).

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
├── cmd/
│   └── findo-server/     # main entrypoint
├── internal/
│   ├── config/           # env/.env config loading
│   ├── crypto/           # encrypts volume passwords at rest
│   ├── smbclient/        # SMB2 client
│   ├── index/            # file metadata index + volume config
│   ├── httpapi/          # HTTP routes and handlers
│   └── dashboard/        # embedded static dashboard
├── testdata/
│   └── seed/             # sample files for the dev Samba container
├── Dockerfile
├── docker-compose.dev.yml  # dev stack: throwaway Samba + findo
├── Makefile
├── go.mod, go.sum
└── .env.example
```
