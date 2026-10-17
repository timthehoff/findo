BINARY := findo-server
PKG    := ./cmd/findo-server

# Release builds carry their git tag (v1.2.3 -> 1.2.3); anything else is "dev",
# which skips the dashboard's update check.
VERSION ?= $(or $(shell git describe --tags --exact-match 2>/dev/null | sed 's/^v//'),dev)
LDFLAGS := -X main.version=$(VERSION)

# Dev port; 8080 is the production default. Env beats .env, so this wins.
DEV_ADDR ?= :8081

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

test:
	go test -race ./...

run:
	HTTP_ADDR=$(DEV_ADDR) go run $(PKG)

# Throwaway Samba server for trying Findo without the real NAS; see the
# comment in testdata/samba/compose.yml for registering it as a volume.
samba-up:
	docker compose -f testdata/samba/compose.yml up -d

samba-down:
	docker compose -f testdata/samba/compose.yml down
