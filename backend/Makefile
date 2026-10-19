BINARY := findo-server
PKG    := ./cmd/findo-server

# Release builds carry their git tag (v1.2.3 -> 1.2.3); anything else is "dev",
# which skips the dashboard's update check.
VERSION ?= $(or $(shell git describe --tags --exact-match 2>/dev/null | sed 's/^v//'),dev)
LDFLAGS := -X main.version=$(VERSION)

# Dev port; 8080 is the production default. Env beats .env, so this wins.
DEV_ADDR ?= 127.0.0.1:8081
# A separate database, so `make run` never touches an installed Findo's data.
DEV_DATABASE_URL ?= postgres:///findo_dev

.PHONY: build check fix test run samba-up samba-down

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

check:
	@fmtout="$$(gofmt -l .)"; \
	if [ -n "$$fmtout" ]; then \
		echo "gofmt needs to be run on:"; \
		echo "$$fmtout"; \
		exit 1; \
	fi
	go vet ./...

fix:
	gofmt -w .

# Tests need Postgres; with-testdb.sh starts a throwaway one (no Docker).
test:
	scripts/with-testdb.sh go test -race ./...

run:
	HTTP_ADDR=$(DEV_ADDR) DATABASE_URL=$(DEV_DATABASE_URL) go run $(PKG)

# Throwaway Samba server for trying Findo without the real NAS; see the
# comment in testdata/samba/compose.yml for registering it as a volume.
samba-up:
	docker compose -f testdata/samba/compose.yml up -d

samba-down:
	docker compose -f testdata/samba/compose.yml down
