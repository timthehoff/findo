#!/bin/sh
# Run a command with FINDO_TEST_DSN pointing at a throwaway Postgres, started
# in a temp dir on a Unix socket and removed afterwards (no Docker, no
# network port). Uses FINDO_TEST_DSN as-is if it's already set.
#
#   scripts/with-testdb.sh go test ./...
#
# Needs Postgres with pg_trgm: brew install postgresql@17 (macOS), or the
# postgresql package (Linux, including GitHub's runners). Postgres refuses to
# run as root, so when this script does (containers, cloud sandboxes) the
# server runs as the postgres user, or nobody if there isn't one.
set -eu

if [ -n "${FINDO_TEST_DSN:-}" ]; then
  exec "$@"
fi

find_bin() {
  if command -v "$1" >/dev/null 2>&1; then command -v "$1"; return; fi
  for v in 17 18 16; do
    if command -v brew >/dev/null 2>&1; then
      p=$(brew --prefix "postgresql@$v" 2>/dev/null || true)
      [ -x "$p/bin/$1" ] && { echo "$p/bin/$1"; return; }
    fi
  done
  for p in /usr/lib/postgresql/*/bin/"$1"; do
    [ -x "$p" ] && { echo "$p"; return; }
  done
  echo "error: $1 not found; install Postgres (brew install postgresql@17)" >&2
  exit 1
}
INITDB=$(find_bin initdb)
PG_CTL=$(find_bin pg_ctl)

pguser=""
if [ "$(id -u)" = 0 ]; then
  if id postgres >/dev/null 2>&1; then pguser=postgres; else pguser=nobody; fi
fi
# Runs a Postgres command as the server's user (just runs it when not root).
as_pg() {
  if [ -n "$pguser" ]; then runuser -u "$pguser" -- "$@"; else "$@"; fi
}

# Unix socket paths are limited to ~100 bytes on macOS, so keep this short.
dir=$(mktemp -d /tmp/findo-pg.XXXXXX)
[ -z "$pguser" ] || chown "$pguser" "$dir"
cleanup() {
  as_pg "$PG_CTL" -D "$dir/data" -m immediate stop >/dev/null 2>&1 || true
  rm -rf "$dir"
}
trap cleanup EXIT INT TERM

# Postgres aborts at startup on macOS without a valid locale in the env.
if locale -a 2>/dev/null | grep -qi '^en_US\.utf-\?8$'; then
  export LC_ALL=en_US.UTF-8
else
  export LC_ALL=C.UTF-8
fi
as_pg "$INITDB" -D "$dir/data" -E UTF8 -A trust >/dev/null
as_pg "$PG_CTL" -D "$dir/data" -l "$dir/log" -w \
  -o "-c listen_addresses='' -k $dir -c fsync=off -c synchronous_commit=off -c full_page_writes=off" \
  start >/dev/null || { cat "$dir/log" >&2; exit 1; }

PSQL=$(dirname "$PG_CTL")/psql
as_pg "$PSQL" -h "$dir" -d postgres -qc "CREATE DATABASE findo_test"
as_pg "$PSQL" -h "$dir" -d findo_test -qc "CREATE EXTENSION pg_trgm"

# The cluster's superuser is whoever ran initdb, which as root isn't "root".
export FINDO_TEST_DSN="host=$dir dbname=findo_test${pguser:+ user=$pguser}"
"$@"
