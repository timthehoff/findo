package smbclient

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hirochachacha/go-smb2"
)

// fakeDial swaps dialFunc for one that hands out fresh zero-value shares
// (identity is all do/reconnect compare) and counts calls.
func fakeDial(t *testing.T, fail error) *int32 {
	t.Helper()
	var n int32
	old := dialFunc
	t.Cleanup(func() { dialFunc = old })
	dialFunc = func(host, share, user, pass string) (net.Conn, *smb2.Session, *smb2.Share, error) {
		atomic.AddInt32(&n, 1)
		if fail != nil {
			return nil, nil, nil, fail
		}
		c, _ := net.Pipe()
		return c, nil, &smb2.Share{}, nil
	}
	return &n
}

func connectedClient(t *testing.T) (*Client, *smb2.Share) {
	t.Helper()
	c := New("h", "s", "u", "p")
	conn, _ := net.Pipe()
	c.conn, c.fs = conn, &smb2.Share{}
	return c, c.fs
}

func TestDoRedialsAfterTransportError(t *testing.T) {
	dials := fakeDial(t, nil)
	c, old := connectedClient(t)

	var calls int
	err := c.do(func(fs *smb2.Share) error {
		calls++
		if fs == old {
			return &smb2.TransportError{Err: errors.New("can't assign requested address")}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if calls != 2 || *dials != 1 || c.fs == old {
		t.Fatalf("calls=%d dials=%d swapped=%v", calls, *dials, c.fs != old)
	}
}

func TestDoLeavesOtherErrorsAlone(t *testing.T) {
	dials := fakeDial(t, nil)
	c, old := connectedClient(t)

	want := errors.New("file is locked")
	if err := c.do(func(*smb2.Share) error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
	if *dials != 0 || c.fs != old {
		t.Fatalf("redialed on a non-transport error")
	}
}

func TestDoKeepsOldShareWhenRedialFails(t *testing.T) {
	fakeDial(t, errors.New("host down"))
	c, old := connectedClient(t)

	err := c.do(func(*smb2.Share) error { return &smb2.TransportError{Err: errors.New("dead")} })
	if err == nil {
		t.Fatal("expected error")
	}
	if c.fs != old {
		t.Fatal("old share dropped; the next call could not retry")
	}
}

func TestDoRedialsOnceForConcurrentFailures(t *testing.T) {
	dials := fakeDial(t, nil)
	c, old := connectedClient(t)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := c.do(func(fs *smb2.Share) error {
				if fs == old {
					return &smb2.TransportError{Err: errors.New("dead")}
				}
				return nil
			})
			if err != nil {
				t.Errorf("do: %v", err)
			}
		}()
	}
	wg.Wait()
	if *dials != 1 {
		t.Fatalf("dials = %d, want 1", *dials)
	}
}

func TestDoNotConnected(t *testing.T) {
	if err := New("h", "s", "u", "p").do(func(*smb2.Share) error { return nil }); err == nil {
		t.Fatal("expected not connected")
	}
}
