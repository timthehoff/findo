package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestListensBeyondLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": false, "localhost:8080": false, "[::1]:8080": false, "127.1.2.3:80": false,
		":8080": true, "0.0.0.0:8080": true, "[::]:8080": true, "192.168.1.5:8080": true,
		"macstudio.local:8080": true, "8080": true, "": true,
	} {
		if got := ListensBeyondLoopback(addr); got != want {
			t.Errorf("ListensBeyondLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestSplitList(t *testing.T) {
	if got := splitList(" a.example.com, b.example.com ,, c "); !reflect.DeepEqual(got, []string{"a.example.com", "b.example.com", "c"}) {
		t.Fatalf("got %q", got)
	}
	if got := splitList(""); got != nil {
		t.Fatalf("empty list = %q", got)
	}
}

func TestLoadDefaultsToThisMachineOnly(t *testing.T) {
	t.Setenv("FINDO_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("FINDO_ALLOWED_HOSTS", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "none.env"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" || ListensBeyondLoopback(cfg.HTTPAddr) {
		t.Fatalf("the default address must be loopback only, got %q", cfg.HTTPAddr)
	}
	if len(cfg.AllowedHosts) != 0 {
		t.Fatalf("AllowedHosts = %q", cfg.AllowedHosts)
	}

	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("FINDO_ALLOWED_HOSTS", "findo.tail1234.ts.net, findo.example.com")
	cfg, err = Load(filepath.Join(t.TempDir(), "none.env"))
	if err != nil || cfg.HTTPAddr != ":8080" || !reflect.DeepEqual(cfg.AllowedHosts, []string{"findo.tail1234.ts.net", "findo.example.com"}) {
		t.Fatalf("explicit settings: %+v, %v", cfg, err)
	}
}
