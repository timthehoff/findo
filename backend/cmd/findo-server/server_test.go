package main

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPServerHasLimits(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.IdleTimeout == 0 || srv.MaxHeaderBytes == 0 {
		t.Fatalf("every limit must be set: %+v", srv)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout must stay unset: it would cut off long downloads and video, got %s", srv.WriteTimeout)
	}
}

// A client that sends part of a request and then stalls (slowloris) must be
// dropped, not held open forever.
func TestSlowHeadersAreDropped(t *testing.T) {
	srv := newHTTPServer("", http.NotFoundHandler())
	srv.ReadHeaderTimeout = 300 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\nX-Slow: "))

	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadAll(conn)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the server held a stalled connection for %s (err %v)", elapsed, err)
	}
	if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
		t.Fatal("the server never closed the stalled connection")
	}
}

func TestOversizedHeadersAreRefused(t *testing.T) {
	srv := newHTTPServer("", http.NotFoundHandler())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	req.Header.Set("X-Big", strings.Repeat("a", 40<<10))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
			t.Fatalf("status = %d, want 431", resp.StatusCode)
		}
	}
}
