#!/bin/sh
# Build Findo from this checkout and (re)install it as a launchd agent that
# starts at login. Safe to re-run to update: an existing .env (and its
# FINDO_MASTER_KEY) is never overwritten, and neither is the database.
#
#   deploy/macos/install.sh
set -eu
umask 077 # .env holds the master key

LABEL=com.findo.server
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
BIN_DIR=$HOME/.local/bin
DATA_DIR="$HOME/Library/Application Support/Findo"
LOG_DIR=$HOME/Library/Logs/Findo
PLIST=$HOME/Library/LaunchAgents/$LABEL.plist

die() { echo "error: $*" >&2; exit 1; }

[ "$(uname)" = Darwin ] || die "this installer is for macOS (launchd)"
command -v go >/dev/null 2>&1 || die "Go is required to build Findo (brew install go)"
command -v openssl >/dev/null 2>&1 || die "openssl is required to generate FINDO_MASTER_KEY"

mkdir -p "$BIN_DIR" "$DATA_DIR" "$LOG_DIR" "$(dirname "$PLIST")"

make -C "$ROOT/backend" build BINARY="$BIN_DIR/findo-server.new"
mv "$BIN_DIR/findo-server.new" "$BIN_DIR/findo-server"

if [ ! -f "$DATA_DIR/.env" ]; then
  {
    echo "FINDO_MASTER_KEY=$(openssl rand -base64 32)"
    echo "HTTP_ADDR=:8080"
  } > "$DATA_DIR/.env"
  echo "Generated $DATA_DIR/.env: back up FINDO_MASTER_KEY; losing it means"
  echo "re-entering every stored SMB password."
fi

sed "s|@HOME@|$HOME|g" "$ROOT/deploy/macos/$LABEL.plist.template" > "$PLIST"
plutil -lint "$PLIST" >/dev/null

# Reload: bootout is asynchronous, so wait for the old instance to go away
# before bootstrapping the new one.
domain=gui/$(id -u)
if launchctl print "$domain/$LABEL" >/dev/null 2>&1; then
  launchctl bootout "$domain/$LABEL" || true
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    launchctl print "$domain/$LABEL" >/dev/null 2>&1 || break
    sleep 1
  done
fi
launchctl bootstrap "$domain" "$PLIST"

echo "Findo is running: see $LOG_DIR/findo.log (default http://localhost:8080/)"
