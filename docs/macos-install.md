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

## Accounts and signing in

Everyone signs in with a **passkey** (Face ID, Touch ID, or a password manager's
passkey); there are no passwords. Each person has their own volumes, with their
own NAS login, and can only search, browse and download their own. The screens
never show an administrator anyone else's files, but **an administrator can take
over any account**: by handing a volume to themselves, or by making a sign-in link
for someone and registering their own passkey on it. Both are logged and visible
to the person (a lost volume, a passkey they didn't add), but not prevented, so
only make people administrators if you'd trust them with everyone's files.

Changes that could let someone into an account for good (adding or removing a
passkey, making sign-in links, managing users and owners) ask you to confirm with
a passkey first, if you haven't in the last five minutes. A stolen session
cookie alone can't plant a passkey that outlives it.

Create the first account, an administrator, on this Mac, from the config
directory:

```sh
cd ~/Library/Application\ Support/Findo
~/.local/bin/findo-server user add --admin <name>
```

It prints a one-time link (valid 15 minutes) that registers that person's first
passkey. Volumes that were configured before accounts existed go to this first
admin. Add more people with `user add <name>` (or on the Users page), and give
someone a fresh link, for a new device or after losing every passkey, with
`findo-server user enroll <name>`. Register a passkey on each device or password
manager you use, so losing one doesn't lock you out; the Account page can add
more once you're signed in.

**What this protects, and what it doesn't.** It keeps users apart, and keeps
strangers on your network out. It does not hide anything from whoever runs the
server: Findo holds every volume's NAS password (it has to, to crawl unattended)
and its database holds every user's index and extracted text. Give Findo
read-only NAS accounts.

### The address passkeys are bound to

Browsers only allow passkeys over HTTPS (or on `localhost`), and a passkey is
bound to the host name it was created on, so **settle the HTTPS name before
anyone enrolls**; changing it later means everyone enrolls again. Set it in
`~/Library/Application Support/Findo/.env`:

```
FINDO_PUBLIC_URL=https://findo.example.com
```

Without it Findo uses `http://localhost:8080`, which works only in a browser on
this Mac. Findo itself serves plain HTTP on `127.0.0.1:8080`; put a TLS proxy
such as [Caddy](https://caddyserver.com/) in front, which passes the `Host`
header through and sets `X-Forwarded-Proto` (Findo trusts that header, for the
cookie's `Secure` flag, only from this Mac). Don't widen `HTTP_ADDR` to reach it
from other devices: that serves plain HTTP, where passkeys won't work and
sessions aren't protected. The server logs a warning if you do.

The public URL's host is allowed automatically. Findo also refuses requests whose
`Host` isn't a name it's meant to be reached by (localhost, IP addresses,
single-word names, `*.local`, `*.lan`, this Mac's hostname), and refuses
state-changing requests from other web origins, which stops a malicious page in
your browser from using your Findo. Other names you reach it by go in `.env`:

```
FINDO_ALLOWED_HOSTS=findo.tail1234.ts.net
```

Sign-in failures are throttled per address and every sign-in, success or failure,
is logged to `~/Library/Logs/Findo/findo.log`. A passkey whose sign-in counter
goes backwards is logged as a possibly cloned authenticator.

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
