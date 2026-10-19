// Package config loads Findo backend configuration from environment
// variables (optionally via a local .env file for development).
package config

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/thoff/findo/backend/internal/crypto"
)

type Config struct {
	// HTTPAddr is where the server listens. The default is this machine only:
	// Findo has no login, so reaching it from other devices is an explicit
	// choice (HTTP_ADDR=:8080), not something that happens by default.
	HTTPAddr    string
	DatabaseURL string // Postgres connection string
	MasterKey   []byte // decoded, KeySize bytes; encrypts volume passwords at rest
	// AllowedHosts are extra names (beyond local and LAN ones) the server may be
	// addressed by, from FINDO_ALLOWED_HOSTS: a Tailscale name, a reverse proxy.
	AllowedHosts []string
}

// Load reads configuration from the environment, first loading any KEY=VALUE
// pairs from a .env file at envPath if present (existing env vars win).
// SMB connection details aren't loaded here: volumes are configured at
// runtime (see internal/index Volume/httpapi /volumes) and stored encrypted
// under MasterKey, not read from the environment.
func Load(envPath string) (Config, error) {
	if err := loadDotEnv(envPath); err != nil {
		return Config{}, err
	}

	keyB64 := os.Getenv("FINDO_MASTER_KEY")
	if keyB64 == "" {
		return Config{}, fmt.Errorf("missing required env var: FINDO_MASTER_KEY (generate one with: openssl rand -base64 32)")
	}
	key, err := crypto.ParseKey(keyB64)
	if err != nil {
		return Config{}, fmt.Errorf("FINDO_MASTER_KEY: %w", err)
	}

	return Config{
		HTTPAddr: getEnvDefault("HTTP_ADDR", "127.0.0.1:8080"),
		// Default: the local Postgres over its Unix socket, database "findo".
		DatabaseURL: getEnvDefault("DATABASE_URL", "postgres:///findo"),
		MasterKey:   key,

		AllowedHosts: splitList(os.Getenv("FINDO_ALLOWED_HOSTS")),
	}, nil
}

// splitList splits a comma-separated list, dropping blanks.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ListensBeyondLoopback reports whether a listen address accepts connections
// from other machines: a wildcard or empty host, or any non-loopback address.
func ListensBeyondLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return true
	}
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadDotEnv sets environment variables from a simple KEY=VALUE file,
// skipping blank lines and lines starting with '#'. Vars already set in the
// environment are left untouched.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}
