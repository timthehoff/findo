#!/bin/sh
# Install or update Findo on macOS as a launchd agent that starts at login,
# using the prebuilt binary from the newest GitHub release, and put HTTPS in
# front of it with Caddy. Safe to re-run to update: an existing .env (and its
# FINDO_MASTER_KEY), the database and an existing Caddy setup are never
# overwritten. Needs Homebrew, which provides the Postgres that holds Findo's
# index (and Caddy, for a private certificate authority).
#
#   curl -fsSL https://raw.githubusercontent.com/timthehoff/findo/main/deploy/macos/install.sh | sh
#
# Findo signs people in with passkeys, and browsers only allow those over HTTPS,
# so a first install asks how to get a certificate (it reads your answers from
# the terminal even when piped). Everything it asks can be set instead:
#
#   FINDO_HTTPS   public   a domain you own on Cloudflare DNS (Let's Encrypt)
#                 private  Caddy's own certificate authority, no domain; every
#                          device must install and trust its root certificate
#                 none     skip: Findo works only on this Mac, at localhost
#   FINDO_HOST    the host name to serve, e.g. findo.example.com. Passkeys are
#                 bound to it, so a re-run refuses to change it without
#                 FINDO_CHANGE_HOST=1 (everyone would have to enroll again).
#   FINDO_DNS_TOKEN  public only: a Cloudflare API token with Zone:Zone:Read
#                 and Zone:DNS:Edit on your zone
#   FINDO_ADMIN   create this first administrator when there are no accounts
#
# Other overrides: FINDO_VERSION (pin a release, e.g. 0.4.0; default newest),
# FINDO_BINARY (install this local findo-server instead of downloading, e.g. one
# built with `make build`), FINDO_CADDY_BINARY (use this Caddy instead of
# Homebrew's or the Cloudflare-enabled build downloaded from caddyserver.com).
set -eu
umask 077 # .env and caddy.env hold secrets

REPO=timthehoff/findo
LABEL=com.findo.server
CADDY_LABEL=com.findo.caddy
BIN_DIR=$HOME/.local/bin
DATA_DIR="$HOME/Library/Application Support/Findo"
LOG_DIR=$HOME/Library/Logs/Findo
AGENTS=$HOME/Library/LaunchAgents
PLIST=$AGENTS/$LABEL.plist
CADDY_PLIST=$AGENTS/$CADDY_LABEL.plist
CADDYFILE=$DATA_DIR/Caddyfile
CADDY_ENV=$DATA_DIR/caddy.env
CADDY_STATE=$DATA_DIR/caddy-data
ROOT_CA=$DATA_DIR/findo-root-ca.crt
TTY=${FINDO_TTY:-/dev/tty}

die() { echo "error: $*" >&2; exit 1; }
say() { echo "$*"; }

have_tty() { [ -r "$TTY" ] && [ -w "$TTY" ]; }

# The terminal is opened once (fd 3), since stdin is the script when piped from
# curl; prompts go to stderr.
TTY_OPEN=
tty_open() {
  [ -n "$TTY_OPEN" ] && return 0
  exec 3< "$TTY"
  TTY_OPEN=1
}

# ask PROMPT: read one line from the terminal into ANSWER.
ask() {
  tty_open
  printf '%s' "$1" >&2
  ANSWER=
  IFS= read -r ANSWER <&3 || true
}

# ask_secret PROMPT: the same, without echoing what is typed.
ask_secret() {
  tty_open
  printf '%s' "$1" >&2
  stty -echo <&3 2>/dev/null || true
  ANSWER=
  IFS= read -r ANSWER <&3 || true
  stty echo <&3 2>/dev/null || true
  printf '\n' >&2
}

# valid_host: a lower-case DNS name with at least one dot, and not an IP address
# (a passkey can't be bound to one).
valid_host() {
  case $1 in
    '' | *[!a-z0-9.-]* | .* | *. | *..* | -* | *.-* | *-.*) return 1 ;;
    *.*) ;;
    *) return 1 ;;
  esac
  case $1 in *[!0-9.]*) return 0 ;; esac
  return 1
}

valid_username() {
  case $1 in
    '' | [!A-Za-z0-9]* | *[!A-Za-z0-9._-]*) return 1 ;;
  esac
  [ ${#1} -le 64 ]
}

# set_env KEY VALUE: set a line in .env, keeping everything else as it is.
set_env() {
  { grep -v "^$1=" "$DATA_DIR/.env" || true; echo "$1=$2"; } > "$tmp/env.new"
  cat "$tmp/env.new" > "$DATA_DIR/.env"
}

get_env() { sed -n "s/^$1=//p" "$DATA_DIR/.env" | tail -n 1; }

# reload_agent LABEL PLIST: (re)start a launchd agent. bootout is asynchronous,
# so wait for the old instance to go away before bootstrapping the new one.
reload_agent() {
  domain=gui/$(id -u)
  if launchctl print "$domain/$1" >/dev/null 2>&1; then
    launchctl bootout "$domain/$1" || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      launchctl print "$domain/$1" >/dev/null 2>&1 || break
      sleep 1
    done
  fi
  launchctl bootstrap "$domain" "$2"
}

[ "$(uname)" = Darwin ] || die "this installer is for macOS (launchd)"
command -v openssl >/dev/null 2>&1 || die "openssl is required to generate FINDO_MASTER_KEY"
command -v brew >/dev/null 2>&1 || die "Homebrew is required for Postgres (https://brew.sh)"
case $(uname -m) in
  arm64) arch=arm64 ;;
  x86_64) arch=amd64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# ---- decide about HTTPS before changing anything --------------------------

HTTPS=${FINDO_HTTPS:-}
HOST=${FINDO_HOST:-}
if [ -z "$HTTPS" ]; then
  if [ -f "$CADDYFILE" ]; then
    HTTPS=keep # already set up: just update
  elif have_tty; then
    say "Findo signs people in with passkeys, which browsers only allow over HTTPS."
    say "How should Findo get a certificate?"
    say "  1) A domain you own, with its DNS on Cloudflare. Trusted by every device"
    say "     with nothing to install; costs about \$10 a year for the domain."
    say "  2) A private certificate authority. Free, no domain, but every phone and"
    say "     computer must install and trust its root certificate."
    say "  3) Skip for now. Findo then works only on this Mac, at http://localhost:8080."
    ask "Choice [1/2/3]: "
    case $ANSWER in
      1) HTTPS=public ;;
      2) HTTPS=private ;;
      *) HTTPS=none ;;
    esac
  else
    HTTPS=none
  fi
fi
case $HTTPS in
  public | private | none | keep) ;;
  *) die "FINDO_HTTPS must be public, private or none (got '$HTTPS')" ;;
esac

if [ "$HTTPS" = public ] || [ "$HTTPS" = private ]; then
  if [ -z "$HOST" ]; then
    have_tty || die "FINDO_HOST is required (the host name to serve, e.g. findo.example.com)"
    if [ "$HTTPS" = public ]; then
      say "Passkeys are bound to this name, so pick it once: a name in your Cloudflare zone."
      ask "Host name (e.g. findo.example.com): "
    else
      say "Passkeys are bound to this name, so pick it once. Any name works; you point"
      say "it at this Mac in your router's local DNS."
      ask "Host name (e.g. findo.home.arpa): "
    fi
    HOST=$ANSWER
  fi
  HOST=$(printf '%s' "$HOST" | tr '[:upper:]' '[:lower:]')
  valid_host "$HOST" || die "'$HOST' isn't a usable host name: use a name like findo.example.com (letters, digits, dots and hyphens; no http://, port or IP address)"

  if [ -f "$DATA_DIR/.env" ]; then
    existing=$(get_env FINDO_PUBLIC_URL)
    if [ -n "$existing" ] && [ "$existing" != "https://$HOST" ] && [ "${FINDO_CHANGE_HOST:-}" != 1 ]; then
      die "FINDO_PUBLIC_URL is already $existing. Passkeys are bound to the host name, so changing it to https://$HOST means everyone must enroll again. If that's what you want, re-run with FINDO_CHANGE_HOST=1."
    fi
  fi

  if [ "$HTTPS" = public ]; then
    TOKEN=${FINDO_DNS_TOKEN:-}
    if [ -z "$TOKEN" ] && [ -f "$CADDY_ENV" ]; then
      TOKEN=$(sed -n 's/^CLOUDFLARE_API_TOKEN=//p' "$CADDY_ENV" | tail -n 1)
    fi
    if [ -z "$TOKEN" ]; then
      have_tty || die "FINDO_DNS_TOKEN is required: a Cloudflare API token with Zone:Zone:Read and Zone:DNS:Edit on the zone for $HOST"
      say "Caddy proves you own the domain by adding a temporary DNS record. Create a token"
      say "at https://dash.cloudflare.com/profile/api-tokens with Zone:Zone:Read and"
      say "Zone:DNS:Edit, limited to your domain's zone."
      ask_secret "Cloudflare API token (input hidden): "
      TOKEN=$ANSWER
    fi
    [ -n "$TOKEN" ] || die "no Cloudflare API token given"
    case $TOKEN in *[!A-Za-z0-9_-]*) die "that doesn't look like a Cloudflare API token (letters, digits, - and _ only)" ;; esac
  fi
fi

# ---- Findo ---------------------------------------------------------------

if [ -n "${FINDO_BINARY:-}" ]; then
  cp "$FINDO_BINARY" "$tmp/findo-server"
else
  # Not /releases/latest: that skips pre-releases, which every 0.x is.
  tag=${FINDO_VERSION:+v${FINDO_VERSION#v}}
  if [ -z "$tag" ]; then
    tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  fi
  [ -n "$tag" ] || die "could not find a Findo release"
  asset=findo-server_${tag#v}_darwin_${arch}.tar.gz
  base=https://github.com/$REPO/releases/download/$tag
  say "Downloading Findo $tag ($arch)..."
  curl -fsSL "$base/$asset" -o "$tmp/$asset" || die "could not download $asset"
  curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || die "could not download SHA256SUMS"
  (cd "$tmp" && grep " $asset\$" SHA256SUMS | shasum -a 256 -c - >/dev/null) \
    || die "checksum mismatch for $asset"
  tar -xzf "$tmp/$asset" -C "$tmp"
fi

mkdir -p "$BIN_DIR" "$DATA_DIR" "$LOG_DIR" "$AGENTS"
chmod 755 "$tmp/findo-server"
mv "$tmp/findo-server" "$BIN_DIR/findo-server"

if [ ! -f "$DATA_DIR/.env" ]; then
  {
    echo "FINDO_MASTER_KEY=$(openssl rand -base64 32)"
    echo "HTTP_ADDR=127.0.0.1:8080"
  } > "$DATA_DIR/.env"
  say "Generated $DATA_DIR/.env: back up FINDO_MASTER_KEY; losing it means"
  say "re-entering every stored SMB password."
fi

# The index lives in a local Postgres managed by Homebrew's service (which
# sets the LC_ALL Postgres needs under launchd). Skipped when .env points
# DATABASE_URL somewhere else.
PG_BIN=
if ! grep -q '^DATABASE_URL=' "$DATA_DIR/.env"; then
  PG=postgresql@17
  brew list --formula "$PG" >/dev/null 2>&1 || brew install "$PG"
  brew services start "$PG" >/dev/null
  PG_BIN=$(brew --prefix "$PG")/bin
  for _ in $(seq 1 30); do
    "$PG_BIN/pg_isready" -q && break
    sleep 1
  done
  "$PG_BIN/pg_isready" -q || die "Postgres did not start (see: brew services info $PG)"
  "$PG_BIN/psql" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname = 'findo'" | grep -q 1 \
    || "$PG_BIN/createdb" findo
fi

# ---- Caddy ---------------------------------------------------------------

CADDY=
if [ "$HTTPS" = public ] || [ "$HTTPS" = private ] || [ "$HTTPS" = keep ]; then
  if [ "$HTTPS" = keep ]; then
    # Already configured: the Caddyfile says which kind of Caddy it needs.
    if grep -q 'dns cloudflare' "$CADDYFILE"; then KIND=public; else KIND=private; fi
  else
    KIND=$HTTPS
  fi

  if [ "$KIND" = private ]; then
    # Stock Caddy is enough for its own certificate authority.
    if [ -n "${FINDO_CADDY_BINARY:-}" ]; then
      CADDY=$FINDO_CADDY_BINARY
    else
      brew list --formula caddy >/dev/null 2>&1 || brew install caddy
      CADDY=$(brew --prefix)/bin/caddy
    fi
  else
    # Homebrew's Caddy has no DNS plugins, so this is Caddy's own build service
    # making a binary with the Cloudflare one. It's refreshed on every run so
    # Caddy's security fixes arrive; if that fails, the existing one is kept.
    CADDY=$BIN_DIR/findo-caddy
    if [ -n "${FINDO_CADDY_BINARY:-}" ]; then
      cp "$FINDO_CADDY_BINARY" "$tmp/findo-caddy"
    else
      say "Downloading Caddy with the Cloudflare DNS plugin from caddyserver.com..."
      url="https://caddyserver.com/api/download?os=darwin&arch=$arch&p=github.com/caddy-dns/cloudflare"
      if ! curl -fsSL "$url" -o "$tmp/findo-caddy"; then
        [ -x "$CADDY" ] || die "could not download Caddy from caddyserver.com (https://caddyserver.com/download, with the caddy-dns/cloudflare package, works too: save it as $CADDY)"
        say "warning: could not refresh Caddy; keeping the one already installed" >&2
        rm -f "$tmp/findo-caddy"
      fi
    fi
    if [ -f "$tmp/findo-caddy" ]; then
      chmod 755 "$tmp/findo-caddy"
      mv "$tmp/findo-caddy" "$CADDY"
    fi
    "$CADDY" list-modules 2>/dev/null | grep -q '^dns.providers.cloudflare$' \
      || die "$CADDY doesn't include the Cloudflare DNS plugin"
  fi

  if [ "$HTTPS" != keep ]; then
    port=$(get_env HTTP_ADDR)
    port=${port##*:}
    : "${port:=8080}"
    {
      echo "# Written by the Findo installer; re-run it with FINDO_HTTPS to rewrite this."
      # Caddy's admin API (an unauthenticated one on localhost:2019) isn't used:
      # the installer restarts the agent to change anything.
      echo "{"
      echo "	admin off"
      echo "}"
      echo
      echo "$HOST {"
      if [ "$HTTPS" = public ]; then
        echo "	tls {"
        echo "		dns cloudflare {env.CLOUDFLARE_API_TOKEN}"
        echo "	}"
      else
        echo "	tls internal"
      fi
      echo "	reverse_proxy 127.0.0.1:$port"
      echo "}"
    } > "$CADDYFILE"
    if [ "$HTTPS" = public ]; then
      echo "CLOUDFLARE_API_TOKEN=$TOKEN" > "$CADDY_ENV"
    else
      rm -f "$CADDY_ENV"
    fi
    chmod 600 "$CADDYFILE" "$CADDY_ENV" 2>/dev/null || true
    set_env FINDO_PUBLIC_URL "https://$HOST"
  else
    HOST=$(sed -n 's/^\([^ #{}]*\) {$/\1/p' "$CADDYFILE" | head -n 1)
  fi

  # Caddy keeps its certificates (and, for a private CA, the root key) here.
  mkdir -p "$CADDY_STATE" "$DATA_DIR/caddy-config"
  if [ -f "$CADDY_ENV" ]; then
    validate_env=--envfile
  else
    validate_env=
  fi
  # shellcheck disable=SC2086 # validate_env is an option and its value, or nothing
  if ! XDG_DATA_HOME=$CADDY_STATE XDG_CONFIG_HOME=$DATA_DIR/caddy-config \
      "$CADDY" validate --config "$CADDYFILE" --adapter caddyfile ${validate_env:+$validate_env "$CADDY_ENV"} \
      > "$tmp/validate.out" 2>&1; then
    cat "$tmp/validate.out" >&2
    die "Caddy rejected the generated $CADDYFILE"
  fi

  caddy_args="<string>$CADDY</string>
		<string>run</string>
		<string>--config</string>
		<string>$CADDYFILE</string>
		<string>--adapter</string>
		<string>caddyfile</string>"
  if [ -f "$CADDY_ENV" ]; then
    caddy_args="$caddy_args
		<string>--envfile</string>
		<string>$CADDY_ENV</string>"
  fi
  cat > "$CADDY_PLIST" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$CADDY_LABEL</string>
	<key>ProgramArguments</key>
	<array>
		$caddy_args
	</array>
	<key>WorkingDirectory</key>
	<string>$DATA_DIR</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>XDG_DATA_HOME</key>
		<string>$CADDY_STATE</string>
		<key>XDG_CONFIG_HOME</key>
		<string>$DATA_DIR/caddy-config</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>$LOG_DIR/caddy.log</string>
	<key>StandardErrorPath</key>
	<string>$LOG_DIR/caddy.log</string>
</dict>
</plist>
PLISTEOF
  plutil -lint "$CADDY_PLIST" >/dev/null
fi

# launchd doesn't expand ~ or $HOME, so the real paths are written out. Findo
# reads .env from its working directory.
cat > "$PLIST" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>$BIN_DIR/findo-server</string>
	</array>
	<key>WorkingDirectory</key>
	<string>$DATA_DIR</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>$LOG_DIR/findo.log</string>
	<key>StandardErrorPath</key>
	<string>$LOG_DIR/findo.log</string>
</dict>
</plist>
PLISTEOF
plutil -lint "$PLIST" >/dev/null

reload_agent "$LABEL" "$PLIST"
if [ -n "$CADDY" ]; then
  reload_agent "$CADDY_LABEL" "$CADDY_PLIST"
fi

# ---- wait for it, and say what's left to do -------------------------------

HTTP_PORT=$(get_env HTTP_ADDR)
HTTP_PORT=${HTTP_PORT##*:}
: "${HTTP_PORT:=8080}"

findo_up=
for _ in $(seq 1 30); do
  if curl -fsS --max-time 3 "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null 2>&1; then
    findo_up=1
    break
  fi
  sleep 1
done
[ -n "$findo_up" ] || say "warning: Findo hasn't answered yet; see $LOG_DIR/findo.log" >&2

LAN_IP=$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true)

https_up=
if [ -n "$CADDY" ]; then
  # --resolve points the name at this Mac so this works before any DNS exists.
  # A public certificate is checked properly, which also proves the DNS
  # challenge worked; a private one can't be trusted by curl yet.
  if [ "$KIND" = public ]; then insecure=; wait_for=120; else insecure=-k; wait_for=30; fi
  say "Waiting for Caddy to serve https://$HOST/ (a new public certificate can take a minute)..."
  i=0
  while [ "$i" -lt "$wait_for" ]; do
    # shellcheck disable=SC2086 # insecure is an option or nothing
    if curl -fsS $insecure --max-time 5 --resolve "$HOST:443:127.0.0.1" "https://$HOST/healthz" >/dev/null 2>&1; then
      https_up=1
      break
    fi
    i=$((i + 1))
    sleep 1
  done
  if [ -z "$https_up" ]; then
    say "warning: https://$HOST/ isn't answering yet. See $LOG_DIR/caddy.log;" >&2
    if [ "$KIND" = public ]; then
      say "         the usual causes are a Cloudflare token without Zone:DNS:Edit on this zone," >&2
      say "         or something else already using ports 80/443 on this Mac." >&2
    else
      say "         the usual cause is something else already using ports 80/443 on this Mac." >&2
    fi
  fi

  if [ "$KIND" = private ]; then
    root=$CADDY_STATE/caddy/pki/authorities/local/root.crt
    for _ in $(seq 1 15); do [ -f "$root" ] && break; sleep 1; done
    if [ -f "$root" ]; then
      cp "$root" "$ROOT_CA"
      chmod 644 "$ROOT_CA"
    fi
  fi
fi

echo
public_url=$(get_env FINDO_PUBLIC_URL)
if [ -n "$CADDY" ]; then
  say "Findo is running at $public_url/ (logs: $LOG_DIR/findo.log and caddy.log)."
  say
  say "Next, on your router, add a local DNS record that points $HOST at this Mac"
  if [ -n "$LAN_IP" ]; then
    say "($LAN_IP right now; give it a fixed address in your router so it stays put)."
  else
    say "(its LAN address; give it a fixed address in your router so it stays put)."
  fi
  say "Away from home you'll need a VPN into your network, since the name only resolves there."
  if [ "$KIND" = private ]; then
    say
    say "Because this uses a private certificate authority, every device must trust its"
    say "root certificate before the passkey pages will work:"
    if [ -f "$ROOT_CA" ]; then
      say "  Certificate: $ROOT_CA"
    else
      say "  Certificate: $CADDY_STATE/caddy/pki/authorities/local/root.crt (created the first time Caddy runs)"
    fi
    say "  This Mac:  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain \"$ROOT_CA\""
    say "  iPhone/iPad: AirDrop it, open it, Settings > Profile Downloaded > Install, then"
    say "               Settings > General > About > Certificate Trust Settings > turn it on."
    say "  Android:   Settings > Security > Install a certificate > CA certificate."
    say "  Firefox keeps its own list: import it under Settings > Privacy & Security > Certificates."
    say "Keep $CADDY_STATE/caddy/pki/authorities/local/root.key private: anyone with it can"
    say "make certificates your devices will trust."
  fi
  if grep -q '^HTTP_ADDR=\(:\|0\.0\.0\.0\)' "$DATA_DIR/.env"; then
    say
    say "warning: HTTP_ADDR in $DATA_DIR/.env also serves plain HTTP to your network." >&2
    say "         Set it to 127.0.0.1:$HTTP_PORT so Findo is reachable only through Caddy." >&2
  fi
else
  say "Findo is running: see $LOG_DIR/findo.log (open http://localhost:$HTTP_PORT/)"
  say "It works only on this Mac until HTTPS is set up, because passkeys need it. Re-run"
  say "this script with FINDO_HTTPS=public or FINDO_HTTPS=private (and FINDO_HOST); see"
  say "docs/macos-install.md."
fi

# The first account is an administrator, made here on the Mac running Findo.
users=
if [ -n "$findo_up" ] && [ -n "$PG_BIN" ]; then
  users=$("$PG_BIN/psql" -d findo -Atqc 'SELECT count(*) FROM users' 2>/dev/null || true)
fi
if [ "$users" = 0 ]; then
  admin=${FINDO_ADMIN:-}
  if [ -z "$admin" ] && have_tty; then
    say
    ask "Name for the first administrator (blank to do it later): "
    admin=$ANSWER
  fi
  if [ -n "$admin" ]; then
    valid_username "$admin" || die "'$admin' isn't a usable name: letters, digits, dots, underscores and hyphens"
    say
    (cd "$DATA_DIR" && "$BIN_DIR/findo-server" user add --admin "$admin") \
      || die "could not create the administrator; try it yourself from $DATA_DIR: $BIN_DIR/findo-server user add --admin $admin"
  else
    say
    say "Create the first account (an administrator) from $DATA_DIR with:"
    say "  $BIN_DIR/findo-server user add --admin <name>"
    say "and open the link it prints on the device you'll sign in with."
  fi
elif [ -n "$users" ]; then
  say
  say "Add people from the Users page, or with: $BIN_DIR/findo-server user add <name>"
  say "(run from $DATA_DIR). Lost every passkey? $BIN_DIR/findo-server user enroll <name>"
fi
