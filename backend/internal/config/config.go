// Package config loads Findo backend configuration from environment
// variables (optionally via a local .env file for development).
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/thoff/findo/backend/internal/crypto"
)

type Config struct {
	HTTPAddr  string
	DBPath    string
	MasterKey []byte // decoded, KeySize bytes; encrypts volume passwords at rest
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
		HTTPAddr:  getEnvDefault("HTTP_ADDR", ":8080"),
		DBPath:    getEnvDefault("DB_PATH", "findo.db"),
		MasterKey: key,
	}, nil
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
