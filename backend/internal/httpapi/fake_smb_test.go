package httpapi

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/hirochachacha/go-smb2"

	"github.com/thoff/findo/backend/internal/smbclient"
)

// errFakeListing is a stand-in directory-listing error for tests exercising
// Crawl's best-effort/skip-sweep behavior.
var errFakeListing = errors.New("fake: directory listing failed")

// fakeSMB is a minimal in-memory stand-in for *smbclient.Client, letting
// tests drive Crawl/Watch/handlers without a real SMB session.
type fakeSMB struct {
	mu      sync.Mutex
	entries map[string]smbclient.Entry // path -> metadata, for Walk and Stat
	walkErr error

	watchEvents chan smbclient.ChangeEvent
	watchErrs   chan error

	pingErr    error
	connectErr error
}

func newFakeSMB() *fakeSMB {
	return &fakeSMB{entries: map[string]smbclient.Entry{}}
}

func (f *fakeSMB) set(e smbclient.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[e.Path] = e
}

func (f *fakeSMB) remove(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, path)
}

func (f *fakeSMB) Walk(root string, fn func(smbclient.Entry)) error {
	f.mu.Lock()
	entries := make([]smbclient.Entry, 0, len(f.entries))
	for _, e := range f.entries {
		entries = append(entries, e)
	}
	err := f.walkErr
	f.mu.Unlock()

	for _, e := range entries {
		fn(e)
	}
	return err
}

func (f *fakeSMB) Open(path string) (*smb2.File, os.FileInfo, error) {
	return nil, nil, os.ErrNotExist
}

func (f *fakeSMB) Ping() error {
	return f.pingErr
}

func (f *fakeSMB) Stat(path string) (smbclient.Entry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[path]
	return e, ok, nil
}

func (f *fakeSMB) Watch(ctx context.Context, filter uint32) (<-chan smbclient.ChangeEvent, <-chan error) {
	return f.watchEvents, f.watchErrs
}

func (f *fakeSMB) Connect() error {
	return f.connectErr
}

func (f *fakeSMB) Close() {}
