// Package smbclient wraps a long-lived SMB2 session to a single NAS share,
// exposing directory listing and file-open operations used by the crawler
// and HTTP handlers.
package smbclient

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/hirochachacha/go-smb2"
)

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

// Connect dials the NAS and mounts the configured share. Safe to call again
// after Close to reconnect.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked()
}

func (c *Client) connectLocked() error {
	conn, err := net.Dial("tcp", net.JoinHostPort(c.host, "445"))
	if err != nil {
		return fmt.Errorf("dial %s:445: %w", c.host, err)
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     c.user,
			Password: c.pass,
		},
	}

	sess, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smb session setup: %w", err)
	}

	fs, err := sess.Mount(fmt.Sprintf(`\\%s\%s`, c.host, c.share))
	if err != nil {
		sess.Logoff()
		conn.Close()
		return fmt.Errorf("mount share %q: %w", c.share, err)
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

// Walk recursively lists every entry under root ("" or "." for share root),
// invoking fn for each file and directory encountered.
func (c *Client) Walk(root string, fn func(Entry) error) error {
	c.mu.Lock()
	fs := c.fs
	c.mu.Unlock()
	if fs == nil {
		return fmt.Errorf("not connected")
	}
	return c.walk(fs, toSMBPath(root), fn)
}

func (c *Client) walk(fs *smb2.Share, dir string, fn func(Entry) error) error {
	infos, err := fs.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("readdir %q: %w", dir, err)
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
		if err := fn(entry); err != nil {
			return err
		}
		if entry.IsDir {
			if err := c.walk(fs, smbPath, fn); err != nil {
				return err
			}
		}
	}
	return nil
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
