BINARY := findo-server
PKG    := ./cmd/findo-server

# Dev port; 8080 is the production default. Env beats .env, so this wins.
DEV_ADDR ?= :8081

.PHONY: build check fix test run

build:
	go build -o $(BINARY) $(PKG)

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
