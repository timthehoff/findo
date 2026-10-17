package index

import (
	"errors"
	"testing"
)

func TestContentSettingsDefaultsWhenNothingSaved(t *testing.T) {
	idx := openTest(t)
	got, err := idx.ContentSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != DefaultContentSettings() {
		t.Fatalf("got %+v, want defaults %+v", got, DefaultContentSettings())
	}
	if got.MaxFileBytes != 100<<20 || got.MaxTextBytes != 1<<20 || got.Workers != 1 || got.Paused {
		t.Fatalf("defaults drifted from the documented 100 MiB / 1 MiB / 1 worker / running: %+v", got)
	}
}

func TestContentSettingsRoundTrip(t *testing.T) {
	idx := openTest(t)
	want := ContentSettings{Paused: true, MaxFileBytes: 5 << 20, MaxTextBytes: 4096, Workers: 3, DelayMS: 250}
	if err := idx.SetContentSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := idx.ContentSettings()
	if err != nil || got != want {
		t.Fatalf("got %+v err=%v, want %+v", got, err, want)
	}

	// Saving again overwrites rather than duplicating rows.
	want.Workers = 2
	if err := idx.SetContentSettings(want); err != nil {
		t.Fatal(err)
	}
	if got, _ := idx.ContentSettings(); got != want {
		t.Fatalf("after overwrite got %+v, want %+v", got, want)
	}
	var n int
	if err := idx.db.QueryRow(`SELECT count(*) FROM settings`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("settings rows = %d err=%v, want 5", n, err)
	}
}

func TestContentSettingsValidation(t *testing.T) {
	idx := openTest(t)
	ok := DefaultContentSettings()
	for name, mutate := range map[string]func(*ContentSettings){
		"file size too small": func(c *ContentSettings) { c.MaxFileBytes = 1 },
		"file size too large": func(c *ContentSettings) { c.MaxFileBytes = 11 << 30 },
		"text cap too small":  func(c *ContentSettings) { c.MaxTextBytes = 10 },
		"text cap too large":  func(c *ContentSettings) { c.MaxTextBytes = 64 << 20 },
		"no workers":          func(c *ContentSettings) { c.Workers = 0 },
		"too many workers":    func(c *ContentSettings) { c.Workers = 5 },
		"negative delay":      func(c *ContentSettings) { c.DelayMS = -1 },
		"huge delay":          func(c *ContentSettings) { c.DelayMS = 120_000 },
	} {
		c := ok
		mutate(&c)
		if err := idx.SetContentSettings(c); !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("%s: got %v, want ErrInvalidSettings", name, err)
		}
	}
	// A rejected save must leave nothing behind.
	if got, _ := idx.ContentSettings(); got != ok {
		t.Fatalf("rejected saves changed the stored settings: %+v", got)
	}
}

func TestVolumeContentIndexOptIn(t *testing.T) {
	idx := openTest(t)
	v, err := idx.CreateVolume("nas", "h", "s", "u", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if v.ContentIndex {
		t.Fatal("a new volume must not be opted in")
	}

	on, err := idx.SetVolumeContentIndex(v.ID, true)
	if err != nil || !on.ContentIndex {
		t.Fatalf("SetVolumeContentIndex(true) = %+v, %v", on, err)
	}
	// Editing connection details must not silently reset the opt-in.
	updated, err := idx.UpdateVolume(v.ID, VolumeUpdate{Name: "nas", Host: "h2", Share: "s", Username: "u", Enabled: true})
	if err != nil || !updated.ContentIndex {
		t.Fatalf("UpdateVolume dropped the opt-in: %+v, %v", updated, err)
	}
	off, err := idx.SetVolumeContentIndex(v.ID, false)
	if err != nil || off.ContentIndex {
		t.Fatalf("SetVolumeContentIndex(false) = %+v, %v", off, err)
	}
	if _, err := idx.SetVolumeContentIndex(9999, true); err == nil {
		t.Fatal("expected an error for an unknown volume")
	}
}
