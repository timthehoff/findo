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

func TestParsePublicURL(t *testing.T) {
	for raw, want := range map[string]string{
		"": "http://localhost:8080", "https://findo.example.com": "https://findo.example.com",
		"https://Findo.Example.com:8443/": "https://findo.example.com:8443",
		"http://localhost:9000":           "http://localhost:9000",
	} {
		got, err := ParsePublicURL(raw, "127.0.0.1:8080")
		if err != nil || got.String() != want {
			t.Errorf("ParsePublicURL(%q) = %v, %v; want %s", raw, got, err, want)
		}
	}
	if got, _ := ParsePublicURL("", "127.0.0.1:8081"); got.String() != "http://localhost:8081" {
		t.Errorf("the default follows the listen port, got %s", got)
	}
	for _, bad := range []string{
		"findo.example.com",             // no scheme
		"ftp://findo.example.com",       // wrong scheme
		"http://findo.example.com",      // passkeys need https off localhost
		"https://192.168.1.5",           // an IP can't be a passkey's relying party
		"https://findo.example.com/app", // a path
		"https://u:p@findo.example.com", // credentials
	} {
		if got, err := ParsePublicURL(bad, ":8080"); err == nil {
			t.Errorf("ParsePublicURL(%q) = %v, want an error", bad, got)
		}
	}
}

func TestLoadAllowsThePublicHost(t *testing.T) {
	t.Setenv("FINDO_MASTER_KEY", "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU=")
	t.Setenv("FINDO_PUBLIC_URL", "https://findo.example.com")
	t.Setenv("FINDO_ALLOWED_HOSTS", "other.example.com")
	cfg, err := Load(filepath.Join(t.TempDir(), "none.env"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.AllowedHosts; len(got) != 2 || got[0] != "other.example.com" || got[1] != "findo.example.com" {
		t.Fatalf("AllowedHosts = %q, want the configured host plus the public one", got)
	}
}
