package smbclient

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hirochachacha/go-smb2"
)

func TestDescribeSaysOnlyWhatIsSafeToShow(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.7"), Port: 445}, Err: errors.New("connect: connection refused")}
	timeout := fmt.Errorf("dial 10.0.0.7:445: %w", &net.OpError{Op: "dial", Err: errors.New("i/o timeout")})

	unreachable := "Couldn't connect to an SMB server at that address."
	for name, err := range map[string]error{
		"refused":        fmt.Errorf("dial 10.0.0.7:445: %w", refused),
		"timed out":      timeout,
		"not SMB (EOF)":  fmt.Errorf("smb session setup: %w", io.ErrUnexpectedEOF),
		"not SMB (junk)": fmt.Errorf("smb session setup: %w", &smb2.InvalidResponseError{}),
		"something else": errors.New("anything"),
	} {
		msg, reached := Describe(err)
		if msg != unreachable || reached {
			t.Errorf("%s: %q reached=%v, want the one generic message and reached=false", name, msg, reached)
		}
		for _, leak := range []string{"refused", "timeout", "10.0.0.7", "EOF", "445"} {
			if strings.Contains(msg, leak) {
				t.Errorf("%s: message %q leaks %q", name, msg, leak)
			}
		}
	}

	for code, want := range map[uint32]string{
		statusLogonFailure:     "The server refused that username or password.",
		statusWrongPassword:    "The server refused that username or password.",
		statusAccountDisabled:  "The server refused that username or password.",
		statusAccountLockedOut: "The server refused that username or password.",
		statusBadNetworkName:   "The server has no share with that name.",
		statusAccessDenied:     "That account isn't allowed to open that share.",
		0xC0000001:             "The server refused the connection.",
	} {
		// Wrapped the way dial wraps them.
		err := fmt.Errorf("smb session setup: %w", &smb2.ResponseError{Code: code})
		if msg, reached := Describe(err); msg != want || !reached {
			t.Errorf("status %#x: %q reached=%v, want %q reached=true", code, msg, reached, want)
		}
	}
}

func withConnectTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := ConnectTimeout
	ConnectTimeout = d
	t.Cleanup(func() { ConnectTimeout = old })
}

// A server that accepts the connection and then says nothing must not hold a
// connection attempt forever.
func TestASilentServerTimesOut(t *testing.T) {
	withConnectTimeout(t, 300*time.Millisecond)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // held open, never answered
		}
	}()

	start := time.Now()
	_, _, _, err = dial(ln.Addr().String(), "share", "u", "p")
	took := time.Since(start)
	if err == nil {
		t.Fatal("a silent server can't have been connected to")
	}
	if took < 250*time.Millisecond || took > 3*time.Second {
		t.Fatalf("gave up after %v, want about the %v timeout", took, ConnectTimeout)
	}
	if msg, reached := Describe(err); reached {
		t.Fatalf("a silent server was described as an SMB server answering: %q", msg)
	}
}

func TestAServerThatIsNotSMBIsNotReported(t *testing.T) {
	withConnectTimeout(t, time.Second)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			c.Close()
		}
	}()
	_, _, _, err = dial(ln.Addr().String(), "share", "u", "p")
	if err == nil {
		t.Fatal("an HTTP server can't have been connected to as SMB")
	}
	if msg, reached := Describe(err); reached || !strings.Contains(msg, "Couldn't connect") {
		t.Fatalf("an HTTP server on the port: %q reached=%v", msg, reached)
	}
}

func TestAClosedPortFailsAsUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, _, _, err := dial(addr, "share", "u", "p")
	if msg, reached := Describe(err); err == nil || reached {
		t.Fatalf("a closed port: %v / %q reached=%v", err, msg, reached)
	}
}
