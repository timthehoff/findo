package httpapi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"time"

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

	// contents serves Open; a path without an entry is "not found". openErr
	// forces an error for a path; blockOpen makes reads on it hang until the
	// file is closed, like a stalled NAS.
	contents  map[string][]byte
	openErr   map[string]error
	blockOpen map[string]bool
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

// setContent registers a file with its bytes, so both Walk/Stat and Open see it.
func (f *fakeSMB) setContent(path string, data []byte, modTime int64) {
	f.mu.Lock()
	if f.contents == nil {
		f.contents = map[string][]byte{}
	}
	f.contents[path] = data
	f.mu.Unlock()
	f.set(smbclient.Entry{Path: path, Name: baseName(path), Size: int64(len(data)), ModTime: modTime})
}

func (f *fakeSMB) Open(path string) (smbclient.File, os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.openErr[path]; err != nil {
		return nil, nil, err
	}
	data, ok := f.contents[path]
	if !ok {
		return nil, nil, os.ErrNotExist
	}
	info := fakeInfo{name: baseName(path), size: int64(len(data))}
	if f.blockOpen[path] {
		return &blockingFile{release: make(chan struct{})}, info, nil
	}
	return &memFile{Reader: bytes.NewReader(data)}, info, nil
}

type memFile struct{ *bytes.Reader }

func (memFile) Close() error { return nil }

// blockingFile never returns from a read until it is closed.
type blockingFile struct {
	release chan struct{}
	once    sync.Once
}

func (b *blockingFile) wait() { <-b.release }
func (b *blockingFile) Read([]byte) (int, error) {
	b.wait()
	return 0, os.ErrClosed
}
func (b *blockingFile) ReadAt([]byte, int64) (int, error) {
	b.wait()
	return 0, os.ErrClosed
}
func (b *blockingFile) Seek(int64, int) (int64, error) { return 0, nil }
func (b *blockingFile) Close() error {
	b.once.Do(func() { close(b.release) })
	return nil
}

type fakeInfo struct {
	name string
	size int64
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return i.size }
func (i fakeInfo) Mode() os.FileMode  { return 0o644 }
func (i fakeInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (i fakeInfo) IsDir() bool        { return false }
func (i fakeInfo) Sys() interface{}   { return nil }

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
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
