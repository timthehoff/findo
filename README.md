# Findo 🐾

Every good dog knows how to fetch. Findo does the same thing for a NAS: it
crawls your files over SMB, builds a searchable index, and serves a polished
web UI (pin it to your phone's home screen) for finding them fast and
previewing or downloading them over HTTP.

## Why this exists

- iOS's native Siri/Spotlight indexing doesn't reach files on SMB network
  shares connected via the Files app. A good dog can't fetch what it can't
  smell.
- The NAS only speaks SMB and NFS natively — no REST/S3 API, no vendor SDK
  to lean on instead, and browsers can't speak SMB at all.

## Architecture

One component: a **backend index/proxy service** (self-hosted on the home
network, running natively on macOS under launchd) — it talks to the NAS over SMB, crawls and indexes file
metadata, exposes an HTTP API (search, listing, Range-aware streamed read),
and serves the embedded web UI. See [`backend/`](backend/).

### Opening and editing files

- **Preview / download:** browser-renderable types (PDF, images, text, audio,
  video) open inline from the proxy; everything else (Office, iWork,
  archives, HTML/SVG) downloads.
- **Editing:** the web UI is read-only. Each result has a **Copy path** button
  that copies the file's `smb://host/share/path` address and shows its
  share-relative location. Connect once via Files > Connect to Server (iOS) or
  Finder > Connect to Server (macOS), then open the file there so Numbers,
  Word, etc. save back to the NAS in place. iOS doesn't open `smb://` links
  from Safari, which is why this is copy-and-paste.

For NAS credentials and other environment specifics, see `backend/.env`
(not committed — copy from `backend/.env.example`) and ask the project owner
rather than guessing.

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
at runtime through the Volumes page (`/volumes.html`) or `POST /volumes` once the server
is running; the server starts fine with zero volumes configured.

### Makefile targets

All run from `backend/`:

| Target | What it does |
| --- | --- |
| `make build` (default) | `go build` the server binary |
| `make check` | `gofmt -l` + `go vet` |
| `make fix` | `gofmt -w` to auto-format |
| `make test` | `go test -race ./...` |
| `make run` | `go run ./cmd/findo-server` on `:8081` (override with `DEV_ADDR`) |

CI runs `make check test build` on every push/PR.

### Dev fixture without a real NAS

`backend/docker-compose.dev.yml` starts a throwaway Samba container seeded with
sample files from `backend/testdata/seed/` (the one remaining use of Docker),
useful for exercising the HTTP API end-to-end without access to the real NAS:

```sh
cd backend
docker compose -f docker-compose.dev.yml up -d
make run
```

Then visit `http://localhost:8081/volumes.html` and add it as a volume (host
`localhost:1445`, share `testshare`, user/pass `testuser`/`testpass`), or use
the `curl -X POST /volumes` command in the compose file's comment. A volume's
host may carry a port (`host:port`) for servers not on the standard 445.

## Deploying (macOS)

Findo runs natively as a launchd agent that starts at login. From a checkout:

```sh
deploy/macos/install.sh
```

See [`docs/macos-install.md`](docs/macos-install.md) for what it sets up,
updating, and uninstalling.

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
└── testdata/
    └── seed/             # sample files for the dev Samba container
deploy/
└── macos/                # launchd agent template + install script
docs/                     # install guide, design notes
```
