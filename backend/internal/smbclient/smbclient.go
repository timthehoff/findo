// Package smbclient wraps a long-lived SMB2 session to a single NAS share,
// exposing directory listing and file-open operations used by the crawler
// and HTTP handlers.
package smbclient

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/hirochachacha/go-smb2"
)

// walkConcurrency bounds how many directory listings run at once during a
// Walk. SMB2 multiplexes many outstanding requests over one connection
// (the negotiated default credit balance is 128), so this doesn't need its
// own connection pool — just a bounded goroutine pool sharing the one
// long-lived session.
const walkConcurrency = 16

// Client holds a single reused SMB session + mounted share. SMB session
// setup is expensive, so callers should keep one Client alive for the life
// of the process rather than reconnecting per request.
type Client struct {
	host  string
	share string
	user  string
	pass  string

	mu   sync.Mutex
	conn net.Conn
	sess *smb2.Session
	fs   *smb2.Share
}

func New(host, share, user, pass string) *Client {
	return &Client{host: host, share: share, user: user, pass: pass}
}

// dial opens a fresh TCP connection, SMB2 session, and mounted share.
// Factored out of connectLocked so Watch can open its own dedicated
// session (a CHANGE_NOTIFY request sits outstanding until an event
// arrives, so it can't share a session with request/response traffic that
// expects a timely reply) without duplicating the setup/error-cleanup
// dance.
func dial(host, share, user, pass string) (net.Conn, *smb2.Session, *smb2.Share, error) {
	conn, err := net.Dial("tcp", net.JoinHostPort(host, "445"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial %s:445: %w", host, err)
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     user,
			Password: pass,
		},
	}

	sess, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("smb session setup: %w", err)
	}

	fs, err := sess.Mount(fmt.Sprintf(`\\%s\%s`, host, share))
	if err != nil {
		sess.Logoff()
		conn.Close()
		return nil, nil, nil, fmt.Errorf("mount share %q: %w", share, err)
	}

	return conn, sess, fs, nil
}

// Connect dials the NAS and mounts the configured share. Safe to call again
// after Close to reconnect.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked()
}

func (c *Client) connectLocked() error {
	conn, sess, fs, err := dial(c.host, c.share, c.user, c.pass)
	if err != nil {
		return err
	}

	c.conn = conn
	c.sess = sess
	c.fs = fs
	return nil
}

// Close tears down the session. Safe to call on a never-connected Client.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
}

func (c *Client) closeLocked() {
	if c.fs != nil {
		c.fs.Umount()
		c.fs = nil
	}
	if c.sess != nil {
		c.sess.Logoff()
		c.sess = nil
	}
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// Ping verifies the share is reachable by statting its root.
func (c *Client) Ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fs == nil {
		return fmt.Errorf("not connected")
	}
	_, err := c.fs.Stat(".")
	return err
}

// Entry describes one file or directory found while crawling.
type Entry struct {
	Path    string // share-relative, forward-slash separated, e.g. "docs/budget.xlsx"
	Name    string
	IsDir   bool
	Size    int64
	ModTime int64 // unix seconds
}

// Stat returns metadata for a single share-relative path, or ok=false if
// it doesn't exist — e.g. a change-notify event for a file that was
// already removed again by the time it's processed.
func (c *Client) Stat(path string) (Entry, bool, error) {
	c.mu.Lock()
	fs := c.fs
	c.mu.Unlock()
	if fs == nil {
		return Entry{}, false, fmt.Errorf("not connected")
	}

	info, err := fs.Stat(toSMBPath(path))
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, false, nil
		}
		return Entry{}, false, fmt.Errorf("stat %q: %w", path, err)
	}

	return Entry{
		Path:    strings.Trim(path, "/"),
		Name:    info.Name(),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime().Unix(),
	}, true, nil
}

// Walk recursively lists every entry under root ("" or "." for share root),
// calling fn for each file and directory found. Directory listings fan out
// across a bounded pool of goroutines sharing the one SMB session (see
// walkConcurrency), rather than listing one directory at a time.
//
// A directory that fails to list doesn't abort the walk — this is
// best-effort, since a permission error or a transient failure on one
// subtree shouldn't prevent indexing everything else reachable. Every such
// error is recorded and returned together (via errors.Join) once the walk
// finishes; a nil return means every directory was listed cleanly.
func (c *Client) Walk(root string, fn func(Entry)) error {
	c.mu.Lock()
	fs := c.fs
	c.mu.Unlock()
	if fs == nil {
		return fmt.Errorf("not connected")
	}
	return errors.Join(walk(toSMBPath(root), fs.ReadDir, fn)...)
}

// walk drives the actual traversal against a readDir function, kept
// separate from Client.Walk so it can be exercised with a fake directory
// tree in tests without a real SMB session.
func walk(root string, readDir func(dir string) ([]os.FileInfo, error), fn func(Entry)) []error {
	q := newDirQueue()
	q.push(root)

	var mu sync.Mutex
	var errs []error

	var workers sync.WaitGroup
	workers.Add(walkConcurrency)
	for i := 0; i < walkConcurrency; i++ {
		go func() {
			defer workers.Done()
			for {
				dir, ok := q.pop()
				if !ok {
					return
				}

				infos, err := readDir(dir)
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("readdir %q: %w", dir, err))
					mu.Unlock()
					q.done()
					continue
				}

				for _, info := range infos {
					name := info.Name()
					smbPath := name
					if dir != "" && dir != "." {
						smbPath = dir + `\` + name
					}

					entry := Entry{
						Path:    toSlashPath(smbPath),
						Name:    name,
						IsDir:   info.IsDir(),
						Size:    info.Size(),
						ModTime: info.ModTime().Unix(),
					}
					fn(entry)
					if entry.IsDir {
						q.push(smbPath)
					}
				}
				q.done()
			}
		}()
	}
	workers.Wait()

	return errs
}

// dirQueue is an unbounded queue of pending directory paths for the walk
// worker pool, tracking in-flight work so pop() can tell a caller "nothing
// left to do" apart from "nothing available right now". A plain buffered
// channel doesn't work here: workers both consume and produce (a listed
// directory's subdirectories go back on the queue), so a bounded channel
// can deadlock if every worker is blocked trying to push new work while
// none are left to drain it.
type dirQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []string
	pending int
	closed  bool
}

func newDirQueue() *dirQueue {
	q := &dirQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push adds a directory to the queue, counting it as pending work.
func (q *dirQueue) push(dir string) {
	q.mu.Lock()
	q.pending++
	q.items = append(q.items, dir)
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pop blocks until a directory is available, returning ok=false once the
// queue is fully drained: no items queued and nothing still being
// processed that could queue more.
func (q *dirQueue) pop() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return "", false
	}
	item := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	return item, true
}

// done marks one previously popped directory as fully handled, including
// any subdirectories it queued. Once nothing is pending anywhere, the
// queue closes and every blocked pop() wakes up and returns.
func (q *dirQueue) done() {
	q.mu.Lock()
	q.pending--
	if q.pending == 0 {
		q.closed = true
		q.cond.Broadcast()
	}
	q.mu.Unlock()
}

// Open opens a file for reading, given a share-relative slash path. The
// returned handle implements io.ReaderAt + io.Seeker, suitable for
// http.ServeContent.
func (c *Client) Open(path string) (*smb2.File, os.FileInfo, error) {
	c.mu.Lock()
	fs := c.fs
	c.mu.Unlock()
	if fs == nil {
		return nil, nil, fmt.Errorf("not connected")
	}

	smbPath := toSMBPath(path)
	f, err := fs.Open(smbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open %q: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("stat %q: %w", path, err)
	}
	return f, info, nil
}

func toSMBPath(path string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return "."
	}
	return strings.ReplaceAll(path, "/", `\`)
}

func toSlashPath(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}
