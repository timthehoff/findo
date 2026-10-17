package index

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ContentSettings tunes file-content indexing. It's one struct loaded and
// saved as a whole; each field is a row in the key/value settings table, and
// a missing row means the default.
type ContentSettings struct {
	// Paused idles the extraction worker without losing its place.
	Paused bool `json:"paused"`
	// MaxFileBytes is the largest file the worker will read at all.
	MaxFileBytes int64 `json:"maxFileBytes"`
	// MaxTextBytes caps the text kept per file.
	MaxTextBytes int64 `json:"maxTextBytes"`
	// Workers is how many files are read at once.
	Workers int `json:"workers"`
	// DelayMS is the pause after each file, to go easy on the NAS and CPU.
	DelayMS int `json:"delayMs"`
}

const (
	minMaxFileBytes = 1 << 20  // 1 MiB
	maxMaxFileBytes = 10 << 30 // 10 GiB
	minMaxTextBytes = 1 << 10  // 1 KiB
	maxMaxTextBytes = 32 << 20 // 32 MiB
	maxWorkers      = 4
	maxDelayMS      = 60_000
)

// ErrInvalidSettings wraps every Validate failure, so callers can tell a bad
// request from a storage error.
var ErrInvalidSettings = errors.New("invalid settings")

// DefaultContentSettings are what a fresh install runs with.
func DefaultContentSettings() ContentSettings {
	return ContentSettings{
		MaxFileBytes: 100 << 20, // 100 MiB
		MaxTextBytes: 1 << 20,   // 1 MiB
		Workers:      1,
	}
}

// Validate rejects values outside the supported ranges.
func (c ContentSettings) Validate() error {
	switch {
	case c.MaxFileBytes < minMaxFileBytes || c.MaxFileBytes > maxMaxFileBytes:
		return fmt.Errorf("%w: maxFileBytes must be between %d and %d", ErrInvalidSettings, minMaxFileBytes, int64(maxMaxFileBytes))
	case c.MaxTextBytes < minMaxTextBytes || c.MaxTextBytes > maxMaxTextBytes:
		return fmt.Errorf("%w: maxTextBytes must be between %d and %d", ErrInvalidSettings, minMaxTextBytes, maxMaxTextBytes)
	case c.Workers < 1 || c.Workers > maxWorkers:
		return fmt.Errorf("%w: workers must be between 1 and %d", ErrInvalidSettings, maxWorkers)
	case c.DelayMS < 0 || c.DelayMS > maxDelayMS:
		return fmt.Errorf("%w: delayMs must be between 0 and %d", ErrInvalidSettings, maxDelayMS)
	}
	return nil
}

const contentKeyPrefix = "content."

// ContentSettings loads the saved settings, defaults filling any gaps.
func (idx *Index) ContentSettings() (ContentSettings, error) {
	cs := DefaultContentSettings()

	rows, err := idx.db.Query(`SELECT key, value FROM settings WHERE key LIKE 'content.%'`)
	if err != nil {
		return cs, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return cs, err
		}
		var perr error
		switch strings.TrimPrefix(key, contentKeyPrefix) {
		case "paused":
			cs.Paused, perr = strconv.ParseBool(value)
		case "max_file_bytes":
			cs.MaxFileBytes, perr = strconv.ParseInt(value, 10, 64)
		case "max_text_bytes":
			cs.MaxTextBytes, perr = strconv.ParseInt(value, 10, 64)
		case "workers":
			cs.Workers, perr = strconv.Atoi(value)
		case "delay_ms":
			cs.DelayMS, perr = strconv.Atoi(value)
		}
		if perr != nil {
			return cs, fmt.Errorf("setting %s: %w", key, perr)
		}
	}
	return cs, rows.Err()
}

// SetContentSettings validates and saves cs, all keys in one transaction.
func (idx *Index) SetContentSettings(cs ContentSettings) error {
	if err := cs.Validate(); err != nil {
		return err
	}
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	for key, value := range map[string]string{
		"paused":         strconv.FormatBool(cs.Paused),
		"max_file_bytes": strconv.FormatInt(cs.MaxFileBytes, 10),
		"max_text_bytes": strconv.FormatInt(cs.MaxTextBytes, 10),
		"workers":        strconv.Itoa(cs.Workers),
		"delay_ms":       strconv.Itoa(cs.DelayMS),
	} {
		if _, err := tx.Exec(
			`INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, $3)
			 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			contentKeyPrefix+key, value, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
