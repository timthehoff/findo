# Running Findo natively on macOS

Findo runs as a per-user launchd agent: it starts at login and is restarted if
it exits. (An agent needs the user logged in; for a headless box, enable
automatic login, or adapt the plist into a LaunchDaemon with a `UserName` key.)

## Install or update

```sh
curl -fsSL https://raw.githubusercontent.com/timthehoff/findo/main/deploy/macos/install.sh | sh
```

The script needs Homebrew: it installs and starts Postgres (`brew services start
postgresql@17`) and creates the `findo` database if they're missing, unless
`.env` already sets `DATABASE_URL`. It downloads the newest release for your CPU (Apple silicon or Intel),
checks it against the release's `SHA256SUMS`, and sets up:

| What | Where |
| --- | --- |
| Binary | `~/.local/bin/findo-server` |
| Config | `~/Library/Application Support/Findo/.env` |
| Index database | Postgres database `findo` (Homebrew's `postgresql@17`, `brew services`) |
| Log | `~/Library/Logs/Findo/findo.log` |
| Agent | `~/Library/LaunchAgents/com.findo.server.plist`, label `com.findo.server` |

The first run generates `.env` with a fresh `FINDO_MASTER_KEY`. **Back it up**:
losing it means re-entering every stored SMB password. Re-running the script
replaces the binary and restarts the agent, and never touches an existing
`.env` or the database. Edit `HTTP_ADDR` in `.env` to change the address or
port (default `127.0.0.1:8080`), then re-run the script to restart.

Environment overrides: `FINDO_VERSION=0.4.0` pins a release instead of the
newest; `FINDO_BINARY=/path/to/findo-server` installs a local build (for
example from `make build`) instead of downloading.

Open `http://localhost:8080/` and add your NAS on the Volumes page. Register it
by IP or a real DNS name rather than a `.local` mDNS name, which launchd
services may not resolve.

## Reaching it from your phone (and the security trade-off)

By default Findo only accepts connections from this Mac. **Findo has no login.**
Anyone who can connect can search every connected share and download its files
with the NAS credentials you gave Findo, and the connection is plain HTTP. To
use it from a phone or another computer, set `HTTP_ADDR=:8080` in
`~/Library/Application Support/Findo/.env` and re-run the install script, but
only on a network you trust (not shared Wi-Fi, not port-forwarded). The server
logs a warning when it listens beyond this Mac.

Even on your network, Findo refuses requests whose `Host` isn't a name it's meant
to be reached by (localhost, IP addresses, single-word names, `*.local`, `*.lan`,
this Mac's hostname), and refuses state-changing requests from other web origins.
That stops a malicious web page in your browser from using your Findo, but it is
not a login. If you reach Findo by another name (a Tailscale name, a reverse
proxy), list it in `.env`:

```
FINDO_ALLOWED_HOSTS=findo.tail1234.ts.net,findo.example.com
```

## Checking on it

```sh
launchctl print gui/$(id -u)/com.findo.server | head
tail -f ~/Library/Logs/Findo/findo.log
```

## Uninstall

```sh
launchctl bootout gui/$(id -u)/com.findo.server
rm ~/Library/LaunchAgents/com.findo.server.plist ~/.local/bin/findo-server
```

The config directory and the Postgres database are left in place. To discard the
index and stored credentials, delete `~/Library/Application Support/Findo` and
run `dropdb findo` (from `$(brew --prefix postgresql@17)/bin`).

## Cutting a release

Push a version tag (`git tag v0.4.0 && git push --tags`) or publish a release
for a new tag in GitHub's UI. Either way `.github/workflows/release.yml` builds `findo-server` for macOS
arm64 and amd64 (version stamped from the tag), writes `SHA256SUMS`, and
attaches them to the GitHub release, creating it first if the tag came from
`git push` (marked pre-release for 0.x). The dashboard's "update available"
badge reads these releases; the installer downloads from them.

## Postgres notes

Homebrew's Postgres listens on `localhost` and its Unix socket only, and trusts
local connections, so Findo connects as your user with no password
(`DATABASE_URL=postgres:///findo`). Don't expose its port to the LAN. To use a
different server, set `DATABASE_URL` in `.env`. Back up the index with
`pg_dump findo`; only the `volumes` table (encrypted NAS passwords) and settings
are irreplaceable, the rest is rebuilt by re-crawling.
