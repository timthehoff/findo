// Package config loads Findo backend configuration from environment
// variables (optionally via a local .env file for development).
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	SMBHost string
	SMBShare string
	SMBUser string
	SMBPass string
	HTTPAddr string
	DBPath   string
}

// Load reads configuration from the environment, first loading any KEY=VALUE
// pairs from a .env file at envPath if present (existing env vars win).
func Load(envPath string) (Config, error) {
	if err := loadDotEnv(envPath); err != nil {
		return Config{}, err
	}

	cfg := Config{
		SMBHost:  os.Getenv("SMB_HOST"),
		SMBShare: os.Getenv("SMB_SHARE"),
		SMBUser:  os.Getenv("SMB_USER"),
		SMBPass:  os.Getenv("SMB_PASS"),
		HTTPAddr: getEnvDefault("HTTP_ADDR", ":8080"),
		DBPath:   getEnvDefault("DB_PATH", "findo.db"),
	}

	var missing []string
	for name, val := range map[string]string{
		"SMB_HOST":  cfg.SMBHost,
		"SMB_SHARE": cfg.SMBShare,
		"SMB_USER":  cfg.SMBUser,
		"SMB_PASS":  cfg.SMBPass,
	} {
		if val == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
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
