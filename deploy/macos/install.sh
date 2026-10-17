#!/bin/sh
# Install or update Findo on macOS as a launchd agent that starts at login,
# using the prebuilt binary from the newest GitHub release. Safe to re-run to
# update: an existing .env (and its FINDO_MASTER_KEY) and the database are
# never overwritten.
#
#   curl -fsSL https://raw.githubusercontent.com/timthehoff/findo/main/deploy/macos/install.sh | sh
#
# Env overrides: FINDO_VERSION (pin a release, e.g. 0.4.0; default newest) and
# FINDO_BINARY (install this local binary instead of downloading, e.g. one
# built with `make build`).
set -eu
umask 077 # .env holds the master key

REPO=timthehoff/findo
LABEL=com.findo.server
BIN_DIR=$HOME/.local/bin
DATA_DIR="$HOME/Library/Application Support/Findo"
LOG_DIR=$HOME/Library/Logs/Findo
PLIST=$HOME/Library/LaunchAgents/$LABEL.plist

die() { echo "error: $*" >&2; exit 1; }

[ "$(uname)" = Darwin ] || die "this installer is for macOS (launchd)"
command -v openssl >/dev/null 2>&1 || die "openssl is required to generate FINDO_MASTER_KEY"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if [ -n "${FINDO_BINARY:-}" ]; then
  cp "$FINDO_BINARY" "$tmp/findo-server"
else
  case $(uname -m) in
    arm64) arch=arm64 ;;
    x86_64) arch=amd64 ;;
    *) die "unsupported CPU: $(uname -m)" ;;
  esac
  # Not /releases/latest: that skips pre-releases, which every 0.x is.
  tag=${FINDO_VERSION:+v${FINDO_VERSION#v}}
  if [ -z "$tag" ]; then
    tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  fi
  [ -n "$tag" ] || die "could not find a Findo release"
  asset=findo-server_${tag#v}_darwin_${arch}.tar.gz
  base=https://github.com/$REPO/releases/download/$tag
  echo "Downloading Findo $tag ($arch)..."
  curl -fsSL "$base/$asset" -o "$tmp/$asset" || die "could not download $asset"
  curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || die "could not download SHA256SUMS"
  (cd "$tmp" && grep " $asset\$" SHA256SUMS | shasum -a 256 -c - >/dev/null) \
    || die "checksum mismatch for $asset"
  tar -xzf "$tmp/$asset" -C "$tmp"
fi

mkdir -p "$BIN_DIR" "$DATA_DIR" "$LOG_DIR" "$(dirname "$PLIST")"
chmod 755 "$tmp/findo-server"
mv "$tmp/findo-server" "$BIN_DIR/findo-server"

if [ ! -f "$DATA_DIR/.env" ]; then
  {
    echo "FINDO_MASTER_KEY=$(openssl rand -base64 32)"
    echo "HTTP_ADDR=:8080"
  } > "$DATA_DIR/.env"
  echo "Generated $DATA_DIR/.env: back up FINDO_MASTER_KEY; losing it means"
  echo "re-entering every stored SMB password."
fi

# launchd doesn't expand ~ or $HOME, so the real paths are written out. Findo
# reads .env and creates findo.db relative to its working directory.
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
