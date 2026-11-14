#!/bin/bash
# Tests for deploy/macos/install.sh. It runs the real installer, on any machine
# with bash, against shims for the macOS and network bits (uname, brew,
# launchctl, curl, Postgres, Caddy, findo-server), in a throwaway HOME. Checks
# what it writes (Caddyfile, launchd plists, .env), what it refuses to do, and
# that a secret never ends up anywhere but caddy.env.
#
#   deploy/macos/test/install_test.sh
set -u

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
INSTALL=$HERE/../install.sh
FAILS=0
TESTS=0

fail() { echo "  FAIL: $*"; FAILS=$((FAILS + 1)); }
contains() { grep -qF -- "$2" "$1" 2>/dev/null; }
has() { contains "$1" "$2" || fail "$1 should contain: $2"; }
hasnt() { contains "$1" "$2" && fail "$1 should not contain: $2"; }
mode() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }

# ---- a fresh fake machine for each test ----
setup() {
  W=$(mktemp -d)
  HOME_DIR=$W/home
  DATA=$HOME_DIR/Library/Application\ Support/Findo
  BIN=$HOME_DIR/.local/bin
  AGENTS=$HOME_DIR/Library/LaunchAgents
  CALLS=$W/calls.log
  mkdir -p "$HOME_DIR" "$W/bin" "$W/pg/bin" "$W/brew/bin" "$W/stubs"
  : > "$CALLS"
  echo 0 > "$W/users"

  # macOS and its tools. (The single quotes are deliberate: $1 belongs to the shim.)
  # shellcheck disable=SC2016
  printf '#!/bin/sh\ncase "$1" in -m) echo arm64;; *) echo Darwin;; esac\n' > "$W/bin/uname"
  printf '#!/bin/sh\nexit 0\n' > "$W/bin/plutil"
  printf '#!/bin/sh\nexit 0\n' > "$W/bin/sleep"
  printf '#!/bin/sh\necho 192.168.1.50\n' > "$W/bin/ipconfig"
  cat > "$W/bin/launchctl" <<SH
#!/bin/sh
echo "launchctl \$*" >> "$CALLS"
[ "\$1" = print ] && exit 1
exit 0
SH
  cat > "$W/bin/brew" <<SH
#!/bin/sh
echo "brew \$*" >> "$CALLS"
case "\$1" in
  --prefix) [ -n "\${2:-}" ] && echo "$W/pg" || echo "$W/brew" ;;
  list) grep -qx "\$3" "$W/installed" 2>/dev/null && exit 0 || exit 1 ;;
  install) echo "\$2" >> "$W/installed"; [ "\$2" = caddy ] && cp "$W/stubs/caddy" "$W/brew/bin/caddy" ;;
esac
exit 0
SH
  # Postgres
  printf '#!/bin/sh\nexit 0\n' > "$W/pg/bin/pg_isready"
  printf '#!/bin/sh\nexit 0\n' > "$W/pg/bin/createdb"
  cat > "$W/pg/bin/psql" <<SH
#!/bin/sh
case "\$*" in
  *pg_database*) echo 1 ;;
  *"count(*) FROM users"*) cat "$W/users" ;;
esac
SH
  # Caddy and Findo themselves
  cat > "$W/stubs/caddy" <<SH
#!/bin/sh
echo "caddy \$*" >> "$CALLS"
case "\$1" in
  list-modules) [ -n "\${STUB_NO_CF:-}" ] || echo dns.providers.cloudflare; echo http ;;
  validate)
    if [ -n "\${STUB_VALIDATE_FAIL:-}" ]; then echo "Error: adapting config: bad" >&2; exit 1; fi
    # what a running Caddy leaves behind for a private CA
    d="\$XDG_DATA_HOME/caddy/pki/authorities/local"; mkdir -p "\$d"; echo ROOTCERT > "\$d/root.crt" ;;
esac
exit 0
SH
  cat > "$W/stubs/findo-server" <<SH
#!/bin/sh
echo "findo-server \$* (in \$(pwd))" >> "$CALLS"
if [ "\$1 \$2" = "user add" ]; then echo "Created admin."; echo "  https://stub/enroll#TOKEN123"; fi
exit 0
SH
  cat > "$W/bin/curl" <<SH
#!/bin/sh
echo "curl \$*" >> "$CALLS"
out=; url=
while [ \$# -gt 0 ]; do
  case "\$1" in -o) out=\$2; shift ;; http*) url=\$1 ;; esac
  shift
done
case "\$url" in
  */healthz) exit 0 ;;
  *caddyserver.com/api/download*)
    [ -n "\${STUB_DL_FAIL:-}" ] && exit 22
    cp "$W/stubs/caddy" "\$out" ;;
  *) exit 22 ;;
esac
SH
  chmod +x "$W"/bin/* "$W"/pg/bin/* "$W"/stubs/*
}

# run [VAR=value ...]: the installer on the fake machine; output in $OUT, status in $RC.
run() {
  OUT=$W/out
  env -i PATH="$W/bin:/usr/bin:/bin" HOME="$HOME_DIR" LC_ALL=C TMPDIR="$W" \
    FINDO_BINARY="$W/stubs/findo-server" FINDO_TTY=/nonexistent "$@" \
    sh "$INSTALL" > "$OUT" 2>&1
  RC=$?
}
ran_ok() { [ "$RC" = 0 ] || { fail "installer exited $RC"; sed 's/^/    | /' "$OUT"; }; }
ran_bad() { [ "$RC" != 0 ] || fail "installer should have failed"; }
cleanup() { rm -rf "$W"; }

t() { TESTS=$((TESTS + 1)); echo "- $1"; setup; "$1"; cleanup; }

# ---- tests ----

no_https_without_a_terminal_keeps_findo_local() {
  run
  ran_ok
  [ -f "$DATA/Caddyfile" ] && fail "no Caddyfile without HTTPS"
  [ -f "$AGENTS/com.findo.caddy.plist" ] && fail "no Caddy agent without HTTPS"
  [ -f "$AGENTS/com.findo.server.plist" ] || fail "Findo's own agent is always installed"
  has "$DATA/.env" FINDO_MASTER_KEY=
  hasnt "$DATA/.env" FINDO_PUBLIC_URL
  has "$OUT" "FINDO_HTTPS=public"
  [ "$(grep -c 'launchctl bootstrap' "$CALLS")" = 1 ] || fail "only Findo's agent should be started"
  has "$OUT" "user add --admin <name>"
}

private_ca_sets_up_caddy_and_explains_trust() {
  run FINDO_HTTPS=private FINDO_HOST=Findo.Home.Arpa
  ran_ok
  has "$DATA/Caddyfile" "findo.home.arpa {"
  has "$DATA/Caddyfile" "tls internal"
  has "$DATA/Caddyfile" "admin off"
  has "$DATA/Caddyfile" "reverse_proxy 127.0.0.1:8080"
  hasnt "$DATA/Caddyfile" dns
  has "$DATA/.env" "FINDO_PUBLIC_URL=https://findo.home.arpa"
  has "$CALLS" "brew install caddy"
  [ -f "$DATA/caddy.env" ] && fail "a private CA needs no secrets file"
  P=$AGENTS/com.findo.caddy.plist
  has "$P" "<string>$W/brew/bin/caddy</string>"
  has "$P" "<string>--adapter</string>"
  has "$P" "<string>$DATA/Caddyfile</string>"
  hasnt "$P" "--envfile"
  has "$P" "<key>XDG_DATA_HOME</key>"
  has "$P" "<string>$DATA/caddy-data</string>"
  has "$CALLS" "caddy validate --config $DATA/Caddyfile"
  [ "$(mode "$DATA/Caddyfile")" = 600 ] || fail "Caddyfile mode"
  # trusting the certificate authority
  [ -f "$DATA/findo-root-ca.crt" ] || fail "the root certificate should be copied somewhere easy to AirDrop"
  has "$OUT" "security add-trusted-cert"
  has "$OUT" "Certificate Trust Settings"
  has "$OUT" "root.key"
  # DNS reminder with the LAN address
  has "$OUT" "192.168.1.50"
  has "$OUT" "findo.home.arpa"
  has "$CALLS" "--resolve findo.home.arpa:443:127.0.0.1"
  has "$CALLS" "-k " # a private certificate can't be checked yet
}

public_mode_downloads_caddy_and_keeps_the_token_in_one_file() {
  tok=cfTOKEN_abc-123XYZ
  run FINDO_HTTPS=public FINDO_HOST=findo.example.com FINDO_DNS_TOKEN=$tok
  ran_ok
  has "$CALLS" "os=darwin&arch=arm64&p=github.com/caddy-dns/cloudflare"
  [ -x "$BIN/findo-caddy" ] || fail "the Cloudflare-enabled Caddy should be installed"
  hasnt "$CALLS" "brew install caddy"
  has "$DATA/Caddyfile" "findo.example.com {"
  has "$DATA/Caddyfile" "dns cloudflare {env.CLOUDFLARE_API_TOKEN}"
  hasnt "$DATA/Caddyfile" "tls internal"
  has "$DATA/caddy.env" "CLOUDFLARE_API_TOKEN=$tok"
  [ "$(mode "$DATA/caddy.env")" = 600 ] || fail "caddy.env must be private (mode $(mode "$DATA/caddy.env"))"
  P=$AGENTS/com.findo.caddy.plist
  has "$P" "<string>--envfile</string>"
  has "$P" "<string>$DATA/caddy.env</string>"
  has "$CALLS" "--envfile $DATA/caddy.env"
  # the secret lives only in caddy.env
  for f in "$DATA/Caddyfile" "$P" "$AGENTS/com.findo.server.plist" "$DATA/.env" "$OUT" "$CALLS"; do hasnt "$f" "$tok"; done
  hasnt "$CALLS" "-k " # a public certificate is checked properly
  has "$DATA/.env" "FINDO_PUBLIC_URL=https://findo.example.com"
}

public_mode_needs_a_token_and_changes_nothing_without_one() {
  run FINDO_HTTPS=public FINDO_HOST=findo.example.com
  ran_bad
  has "$OUT" FINDO_DNS_TOKEN
  [ -e "$BIN/findo-server" ] && fail "nothing should be installed before the questions are settled"
  run FINDO_HTTPS=public FINDO_HOST=findo.example.com 'FINDO_DNS_TOKEN=bad token!'
  ran_bad
  has "$OUT" "doesn't look like a Cloudflare API token"
}

bad_host_names_are_refused_before_anything_is_changed() {
  for h in http://findo.example.com findo.example.com:8443 192.168.1.5 findo -bad.example.com a..b.example.com findo.example.com. 'find o.example.com' ''; do
    run FINDO_HTTPS=private "FINDO_HOST=$h"
    ran_bad
    if [ -n "$h" ]; then has "$OUT" "isn't a usable host name"; else has "$OUT" "FINDO_HOST is required"; fi
    [ -e "$BIN/findo-server" ] && fail "'$h' was refused but things were installed"
  done
  run FINDO_HTTPS=sideways FINDO_HOST=findo.example.com
  ran_bad
  has "$OUT" "FINDO_HTTPS must be"
}

questions_are_asked_on_the_terminal() {
  printf '2\nfindo.home.arpa\nalice\n' > "$W/answers"
  run FINDO_TTY="$W/answers"
  ran_ok
  has "$OUT" "How should Findo get a certificate?"
  has "$DATA/Caddyfile" "findo.home.arpa {"
  has "$DATA/Caddyfile" "tls internal"
  has "$CALLS" "findo-server user add --admin alice (in $DATA)"
  has "$OUT" "https://stub/enroll#TOKEN123"

  cleanup; setup
  printf '1\nFindo.Example.com\ntermTOKEN987\n\n' > "$W/answers"
  run FINDO_TTY="$W/answers"
  ran_ok
  has "$DATA/Caddyfile" "findo.example.com {"
  has "$DATA/caddy.env" "CLOUDFLARE_API_TOKEN=termTOKEN987"
  hasnt "$OUT" "termTOKEN987"
  hasnt "$CALLS" "user add" # a blank name means "later"
  has "$OUT" "user add --admin <name>"

  cleanup; setup
  printf '3\n\n' > "$W/answers"
  run FINDO_TTY="$W/answers"
  ran_ok
  [ -f "$DATA/Caddyfile" ] && fail "choosing to skip must not set up Caddy"
}

a_rerun_updates_without_touching_the_setup() {
  run FINDO_HTTPS=private FINDO_HOST=findo.home.arpa
  ran_ok
  key=$(grep '^FINDO_MASTER_KEY=' "$DATA/.env")
  echo "# hand-edited" >> "$DATA/Caddyfile"
  : > "$CALLS"
  run # no answers, no variables: it's an update
  ran_ok
  has "$DATA/Caddyfile" "# hand-edited"
  has "$DATA/.env" "$key"
  has "$DATA/.env" "FINDO_PUBLIC_URL=https://findo.home.arpa"
  has "$OUT" "https://findo.home.arpa/"
  has "$CALLS" "launchctl bootstrap"
  [ "$(grep -c 'launchctl bootstrap' "$CALLS")" = 2 ] || fail "both agents should be restarted on an update"
  [ "$(grep -c '^FINDO_PUBLIC_URL=' "$DATA/.env")" = 1 ] || fail "FINDO_PUBLIC_URL should appear once"
}

a_public_rerun_refreshes_caddy_and_keeps_the_token() {
  run FINDO_HTTPS=public FINDO_HOST=findo.example.com FINDO_DNS_TOKEN=keepMe123
  ran_ok
  : > "$CALLS"
  run
  ran_ok
  has "$CALLS" "os=darwin&arch=arm64&p=github.com/caddy-dns/cloudflare"
  has "$DATA/caddy.env" "CLOUDFLARE_API_TOKEN=keepMe123"
  has "$CALLS" "--envfile $DATA/caddy.env"

  # a failed refresh keeps the Caddy that's there
  : > "$CALLS"
  run STUB_DL_FAIL=1
  ran_ok
  has "$OUT" "keeping the one already installed"
  [ -x "$BIN/findo-caddy" ] || fail "the existing Caddy was lost"
  # ...but there's nothing to keep on a fresh machine
  cleanup; setup
  run STUB_DL_FAIL=1 FINDO_HTTPS=public FINDO_HOST=findo.example.com FINDO_DNS_TOKEN=x1
  ran_bad
  has "$OUT" "could not download Caddy"
}

changing_the_host_name_needs_a_deliberate_yes() {
  run FINDO_HTTPS=private FINDO_HOST=one.home.arpa
  ran_ok
  run FINDO_HTTPS=private FINDO_HOST=two.home.arpa
  ran_bad
  has "$OUT" "enroll again"
  has "$OUT" FINDO_CHANGE_HOST=1
  has "$DATA/.env" "FINDO_PUBLIC_URL=https://one.home.arpa"
  has "$DATA/Caddyfile" "one.home.arpa {"
  run FINDO_HTTPS=private FINDO_HOST=two.home.arpa FINDO_CHANGE_HOST=1
  ran_ok
  has "$DATA/.env" "FINDO_PUBLIC_URL=https://two.home.arpa"
  has "$DATA/Caddyfile" "two.home.arpa {"
  hasnt "$DATA/Caddyfile" "one.home.arpa"
  # saying the same host again is not a change
  run FINDO_HTTPS=private FINDO_HOST=two.home.arpa
  ran_ok
}

a_caddyfile_caddy_rejects_stops_the_install() {
  run FINDO_HTTPS=private FINDO_HOST=findo.home.arpa STUB_VALIDATE_FAIL=1
  ran_bad
  has "$OUT" "rejected the generated"
  has "$OUT" "adapting config"
  hasnt "$CALLS" "launchctl bootstrap"
}

a_caddy_without_the_cloudflare_plugin_is_refused() {
  run FINDO_HTTPS=public FINDO_HOST=findo.example.com FINDO_DNS_TOKEN=abc STUB_NO_CF=1
  ran_bad
  has "$OUT" "Cloudflare DNS plugin"
}

the_first_administrator_is_only_offered_when_there_are_no_accounts() {
  run FINDO_ADMIN=bob
  ran_ok
  has "$CALLS" "findo-server user add --admin bob (in $DATA)"

  cleanup; setup
  echo 3 > "$W/users"
  run FINDO_ADMIN=bob
  ran_ok
  hasnt "$CALLS" "user add"
  has "$OUT" "Users page"

  cleanup; setup
  run 'FINDO_ADMIN=bad name!'
  ran_bad
  has "$OUT" "isn't a usable name"
}

widening_http_addr_is_called_out() {
  mkdir -p "$DATA"
  printf 'FINDO_MASTER_KEY=abc\nHTTP_ADDR=:8080\n' > "$DATA/.env"
  run FINDO_HTTPS=private FINDO_HOST=findo.home.arpa
  ran_ok
  has "$OUT" "also serves plain HTTP"
  has "$DATA/Caddyfile" "reverse_proxy 127.0.0.1:8080"
  has "$DATA/.env" FINDO_MASTER_KEY=abc # an existing .env is edited, never replaced
}

a_custom_findo_port_is_proxied() {
  mkdir -p "$DATA"
  printf 'FINDO_MASTER_KEY=abc\nHTTP_ADDR=127.0.0.1:9090\n' > "$DATA/.env"
  run FINDO_HTTPS=private FINDO_HOST=findo.home.arpa
  ran_ok
  has "$DATA/Caddyfile" "reverse_proxy 127.0.0.1:9090"
  has "$CALLS" "http://127.0.0.1:9090/healthz"
}

# Sourcing this file (INSTALL_TEST_SOURCE_ONLY=1) gives setup/run without running the tests.
[ "${INSTALL_TEST_SOURCE_ONLY:-}" = 1 ] && return 0 2>/dev/null

for fn in no_https_without_a_terminal_keeps_findo_local \
  private_ca_sets_up_caddy_and_explains_trust \
  public_mode_downloads_caddy_and_keeps_the_token_in_one_file \
  public_mode_needs_a_token_and_changes_nothing_without_one \
  bad_host_names_are_refused_before_anything_is_changed \
  questions_are_asked_on_the_terminal \
  a_rerun_updates_without_touching_the_setup \
  a_public_rerun_refreshes_caddy_and_keeps_the_token \
  changing_the_host_name_needs_a_deliberate_yes \
  a_caddyfile_caddy_rejects_stops_the_install \
  a_caddy_without_the_cloudflare_plugin_is_refused \
  the_first_administrator_is_only_offered_when_there_are_no_accounts \
  widening_http_addr_is_called_out \
  a_custom_findo_port_is_proxied; do
  t "$fn"
done

echo
if [ "$FAILS" = 0 ]; then echo "all $TESTS install tests passed"; else echo "$FAILS failure(s) in $TESTS tests"; exit 1; fi
