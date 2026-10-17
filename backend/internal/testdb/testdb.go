// Package testdb gives tests an isolated Postgres schema. The server
// comes from FINDO_TEST_DSN, which `make test` sets by starting a throwaway
// cluster (scripts/with-testdb.sh); without it, tests that need a database
// skip rather than fail.
package testdb

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DSN creates a fresh schema on the test server and returns a connection
// string whose search_path points at it, so each test sees empty tables and
// can run in parallel with the others. The schema is dropped when the test
// ends (after anything registered later with t.Cleanup, such as closing the
// index, has run).
func DSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv("FINDO_TEST_DSN")
	if base == "" {
		t.Skip("FINDO_TEST_DSN not set; run `make test` (it starts a throwaway Postgres)")
	}

	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(b[:])

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	// public stays on the path so extension objects (pg_trgm's operator
	// classes) resolve from inside the test schema.
	return withSearchPath(base, schema+",public")
}

func withSearchPath(dsn, path string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return dsn
		}
		q := u.Query()
		q.Set("search_path", path)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + path
}
