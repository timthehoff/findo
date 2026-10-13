package main

import "testing"

func TestListenURL(t *testing.T) {
	tests := map[string]string{
		":8081":          "http://localhost:8081/",
		"0.0.0.0:8080":   "http://localhost:8080/",
		"[::]:8080":      "http://localhost:8080/",
		"127.0.0.1:9000": "http://127.0.0.1:9000/",
		"bogus":          "bogus",
	}
	for in, want := range tests {
		if got := listenURL(in); got != want {
			t.Errorf("listenURL(%q) = %q, want %q", in, got, want)
		}
	}
}
