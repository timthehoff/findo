BINARY := findo-server
PKG    := ./cmd/findo-server

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
	go test ./...

run:
	go run $(PKG)
