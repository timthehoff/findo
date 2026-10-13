#!/bin/sh
# Install or update Findo with Docker Compose. Safe to re-run: an existing
# .env (and its FINDO_MASTER_KEY) is never overwritten.
#
#   curl -fsSL https://raw.githubusercontent.com/timthehoff/findo/main/install.sh | sh
#
# Env overrides: FINDO_DIR (default ~/findo), FINDO_VERSION (image tag,
# default latest), FINDO_REF (git ref to fetch the compose file from,
# default main).
set -eu
umask 077 # .env holds the master key

REPO=timthehoff/findo
DIR=${FINDO_DIR:-$HOME/findo}
REF=${FINDO_REF:-main}

die() { echo "error: $*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "docker is required (https://docs.docker.com/engine/install/)"
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is required"

mkdir -p "$DIR"
cd "$DIR"

curl -fsSL "https://raw.githubusercontent.com/$REPO/$REF/deploy/docker-compose.yml" -o docker-compose.yml.new \
  || die "could not download docker-compose.yml"
mv docker-compose.yml.new docker-compose.yml

if [ ! -f .env ]; then
  command -v openssl >/dev/null 2>&1 || die "openssl is required to generate FINDO_MASTER_KEY"
  {
    echo "# Losing FINDO_MASTER_KEY means re-entering every stored SMB password."
    echo "FINDO_MASTER_KEY=$(openssl rand -base64 32)"
  } > .env
  echo "Generated $DIR/.env — back up FINDO_MASTER_KEY somewhere safe."
fi

# Pin a version by exporting FINDO_VERSION; otherwise keep whatever is set.
if [ -n "${FINDO_VERSION:-}" ]; then
  grep -v '^FINDO_VERSION=' .env > .env.new || true
  echo "FINDO_VERSION=$FINDO_VERSION" >> .env.new
  mv .env.new .env
fi

docker compose pull
docker compose up -d

port=$(sed -n 's/^FINDO_PORT=//p' .env)
echo "Findo is running: http://localhost:${port:-8080}"
echo "Update later by re-running this script."
