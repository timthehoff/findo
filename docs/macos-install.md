# Running Findo natively on macOS

Findo runs as a per-user launchd agent: it starts at login and is restarted if
it exits. (An agent needs the user logged in; for a headless box, enable
automatic login, or adapt the plist into a LaunchDaemon with a `UserName` key.)

## Install or update

```sh
curl -fsSL https://raw.githubusercontent.com/timthehoff/findo/main/deploy/macos/install.sh | sh
```

The script downloads the newest release for your CPU (Apple silicon or Intel),
checks it against the release's `SHA256SUMS`, and sets up:

| What | Where |
| --- | --- |
| Binary | `~/.local/bin/findo-server` |
| Config and database | `~/Library/Application Support/Findo/` (`.env`, `findo.db`) |
| Log | `~/Library/Logs/Findo/findo.log` |
| Agent | `~/Library/LaunchAgents/com.findo.server.plist`, label `com.findo.server` |

The first run generates `.env` with a fresh `FINDO_MASTER_KEY`. **Back it up**:
losing it means re-entering every stored SMB password. Re-running the script
replaces the binary and restarts the agent, and never touches an existing
`.env` or the database. Edit `HTTP_ADDR` in `.env` to change the port
(default `:8080`), then re-run the script to restart.

Environment overrides: `FINDO_VERSION=0.4.0` pins a release instead of the
newest; `FINDO_BINARY=/path/to/findo-server` installs a local build (for
example from `make build`) instead of downloading.

Open `http://localhost:8080/` (or the Mac's hostname from other devices on your
network) and add your NAS on the Volumes page. Register it by IP or a real DNS
name rather than a `.local` mDNS name, which launchd services may not resolve.

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

The data directory is left in place; delete `~/Library/Application Support/Findo`
too to discard the index and stored credentials.

## Cutting a release

Push a version tag:

```sh
git tag v0.4.0 && git push --tags
```

`.github/workflows/release.yml` builds `findo-server` for macOS arm64 and amd64
(version stamped from the tag), writes `SHA256SUMS`, and publishes a GitHub
release (marked pre-release for 0.x). The dashboard's "update available" badge
reads these releases; the installer downloads from them.
