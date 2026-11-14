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
checks it against the release's `SHA256SUMS`, and asks how Findo should get an HTTPS
certificate (see [HTTPS](#https-with-caddy)), then sets up:

| What | Where |
| --- | --- |
| Binary | `~/.local/bin/findo-server` |
| Config | `~/Library/Application Support/Findo/.env` |
| Index database | Postgres database `findo` (Homebrew's `postgresql@17`, `brew services`) |
| Log | `~/Library/Logs/Findo/findo.log` |
| Agent | `~/Library/LaunchAgents/com.findo.server.plist`, label `com.findo.server` |
| Caddy (HTTPS) | agent `com.findo.caddy`; config `Caddyfile` and, for a public certificate, its Cloudflare token in `caddy.env`, both in the config directory next to `.env`; its certificates in `caddy-data/`; log `~/Library/Logs/Findo/caddy.log` |

The first run generates `.env` with a fresh `FINDO_MASTER_KEY`. **Back it up**:
losing it means re-entering every stored SMB password. Re-running the script
replaces the binary and restarts the agent, and never touches an existing
`.env` or the database. Edit `HTTP_ADDR` in `.env` to change the address or
port (default `127.0.0.1:8080`), then re-run the script to restart.

Environment overrides: `FINDO_VERSION=0.4.0` pins a release instead of the
newest; `FINDO_BINARY=/path/to/findo-server` installs a local build (for
example from `make build`) instead of downloading. The HTTPS choices below can be
given as variables too, so the script can run without a terminal.

When it finishes it offers to create the first account, an administrator, and
prints the link that registers their passkey (see [Accounts](#accounts-and-signing-in)).
Add your NAS on the Volumes page. Register it by IP or a real DNS name rather
than a `.local` mDNS name, which launchd services may not resolve.

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

The installer offers to create the first account, an administrator. To do it
yourself, on this Mac, from the config directory:

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

## HTTPS with Caddy

Browsers only allow passkeys over HTTPS (or on `localhost`), and a passkey is
bound to the host name it was created on, so **settle the name before anyone
enrolls**; changing it later means everyone enrolls again. Findo itself serves
plain HTTP on `127.0.0.1:8080` and does no TLS. The installer puts
[Caddy](https://caddyserver.com/) in front, listening on ports 80 and 443 on this
Mac, as a second launchd agent. Caddy passes the `Host` header through and tells
Findo the connection was HTTPS (`X-Forwarded-Proto`, which Findo trusts, for the
session cookie's `Secure` flag, only from this Mac).

The installer asks how to get a certificate:

| | Public certificate | Private certificate authority |
| --- | --- | --- |
| Needs | A domain you own, with its DNS on Cloudflare (about $10 a year) | Nothing |
| Devices | Nothing to install; every device already trusts it | Every phone and computer must install and trust a root certificate |
| Caddy | A build with the Cloudflare DNS plugin, downloaded from caddyserver.com | Homebrew's `caddy` |
| Good for | A household or group where people bring their own devices | A few devices you control |

Either way, add a **local DNS record on your router** that points the name at this
Mac (give the Mac a fixed address so it stays put). Nothing is exposed to the
internet, and away from home you need a VPN into your network, since the name only
resolves there. Don't widen `HTTP_ADDR` to reach Findo from other devices: that
serves plain HTTP, where passkeys won't work and sessions aren't protected. The
server logs a warning if you do, and the installer does too.

### Public certificate (Cloudflare)

Caddy proves you own the domain by adding a temporary DNS record
(the "DNS-01" challenge), which is why no inbound port needs to be open.

1. Put your domain's DNS on Cloudflare.
2. At <https://dash.cloudflare.com/profile/api-tokens> create an API token with
   **Zone:Zone:Read** and **Zone:DNS:Edit**, limited to that one zone, so a leaked
   token can't touch your other domains.
3. Run the installer, choose option 1, and give it a name in the zone
   (`findo.example.com`) and the token. The token goes only in `caddy.env`
   (readable by you alone), which Caddy loads when it starts; it isn't in the
   Caddyfile, the launchd plist or the logs.

The certificate name is published in public Certificate Transparency logs, which
is harmless but visible. Homebrew's Caddy has no DNS plugins, so this mode uses
the custom build from Caddy's own download service
(`caddyserver.com/api/download?...&p=github.com/caddy-dns/cloudflare`) and saves
it as `~/.local/bin/findo-caddy`. That service publishes no checksums, so you're
trusting it over HTTPS; to avoid that, build your own
(`xcaddy build --with github.com/caddy-dns/cloudflare`), save it at that path, and
pass `FINDO_CADDY_BINARY=/path/to/caddy` to the installer. Re-running the
installer refreshes it so Caddy's security fixes arrive.

### Private certificate authority

Caddy makes its own certificate authority (`tls internal`) and signs the name
with it; any name works (`findo.home.arpa`, `findo.lan`). The catch is that every
device has to trust that authority's root certificate, or browsers will refuse the
passkey pages. The installer copies it to
`~/Library/Application Support/Findo/findo-root-ca.crt` and prints the steps:

- **This Mac:** `sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain <the file>`
- **iPhone or iPad:** AirDrop the file, open it, then Settings > Profile Downloaded
  > Install, and finally Settings > General > About > Certificate Trust Settings
  and turn it on. (Skipping that last step is the usual reason it "doesn't work".)
- **Android:** Settings > Security > Install a certificate > CA certificate.
- **Firefox** keeps its own list: import it under Settings > Privacy & Security >
  Certificates.

Anyone holding the authority's key
(`caddy-data/caddy/pki/authorities/local/root.key`) can make certificates that your
devices trust for any website, and Caddy's authority isn't limited to your name. So
keep that directory private, and remove the root from your devices if the Mac is
ever compromised or retired. A guest's phone needs the same install, which is the
main reason to prefer a public certificate for more than a few devices.

### Running the installer without prompts

| Variable | Meaning |
| --- | --- |
| `FINDO_HTTPS` | `public`, `private` or `none` |
| `FINDO_HOST` | the name to serve, e.g. `findo.example.com` |
| `FINDO_DNS_TOKEN` | public only: the Cloudflare token |
| `FINDO_ADMIN` | create this first administrator if there are no accounts |
| `FINDO_CHANGE_HOST=1` | allow changing the name on a re-run |

For example: `FINDO_HTTPS=private FINDO_HOST=findo.home.arpa FINDO_ADMIN=tim sh install.sh`.
Without a terminal and without `FINDO_HTTPS`, it installs Findo only and says how
to add HTTPS later. Once Caddy is set up, a plain re-run just updates Findo
(and refreshes a public-certificate Caddy) and **leaves the Caddyfile, `.env` and
token alone**, so hand edits survive. Setting `FINDO_HTTPS` again rewrites them,
and refuses to change the name without `FINDO_CHANGE_HOST=1`, since that would
force everyone to enroll again. The result is written to `.env` as
`FINDO_PUBLIC_URL=https://<name>`, and its host is allowed automatically.

Tailscale Serve, or any other TLS proxy, works too if you set it up yourself:
point it at `127.0.0.1:8080` and set `FINDO_PUBLIC_URL` to the HTTPS address you
reach it by.

### If HTTPS doesn't come up

The installer waits for `https://<name>/` and says if it doesn't answer. Look at
`~/Library/Logs/Findo/caddy.log`. The usual causes: a Cloudflare token that is
mistyped, or lacks Zone:DNS:Edit on that zone (Caddy rejects a token that isn't
the right shape before it starts, and prints why); or something else already using
port 80 or 443 (`sudo lsof -nP -iTCP:443 -sTCP:LISTEN`; macOS's Internet Sharing
and some local web servers do).

### Other names and limits

Findo also refuses requests whose `Host` isn't a name it's meant to be reached by
(localhost, IP addresses, single-word names, `*.local`, `*.lan`,
`*.home.arpa`, this Mac's hostname, or the public name), and refuses
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
tail -f ~/Library/Logs/Findo/caddy.log   # HTTPS
```

## Uninstall

```sh
launchctl bootout gui/$(id -u)/com.findo.server
launchctl bootout gui/$(id -u)/com.findo.caddy        # if HTTPS was set up
rm ~/Library/LaunchAgents/com.findo.server.plist ~/Library/LaunchAgents/com.findo.caddy.plist
rm ~/.local/bin/findo-server ~/.local/bin/findo-caddy
```

The config directory and the Postgres database are left in place. To discard the
index, stored credentials, certificates and (for a private authority) its root
key, delete `~/Library/Application Support/Findo` and run `dropdb findo` (from
`$(brew --prefix postgresql@17)/bin`). If you used a private authority, also remove
its root certificate from your devices. A Homebrew Caddy is removed with
`brew uninstall caddy`.

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
