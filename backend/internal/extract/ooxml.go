package extract

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"
)

// ErrEncrypted means the file is a password-protected Office document: those
// are stored as OLE compound files, not the zip a .docx/.xlsx/.pptx should be.
var ErrEncrypted = errors.New("file is encrypted")

// Bounds for reading Office packages. They exist because the archive is
// untrusted input from a network share: a zip bomb must cost a capped amount
// of memory and time, not an unbounded amount. Variables so tests can shrink
// them.
var (
	maxPartBytes  int64 = 128 << 20 // decompressed bytes read from any one XML part
	maxSharedText int64 = 32 << 20  // text kept from an xlsx shared-strings table
)

const maxArchiveEntries = 20000

var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// openPackage opens r as an OOXML zip package.
func openPackage(r io.ReaderAt, size int64) (*zip.Reader, error) {
	var magic [8]byte
	if n, _ := r.ReadAt(magic[:], 0); n == len(magic) && bytes.Equal(magic[:], oleMagic) {
		return nil, ErrEncrypted
	}
	zr, err := zip.NewReader(newBlockReaderAt(r, size), size)
	if err != nil {
		return nil, fmt.Errorf("not a valid Office file: %w", err)
	}
	if len(zr.File) > maxArchiveEntries {
		return nil, fmt.Errorf("archive has %d entries (limit %d)", len(zr.File), maxArchiveEntries)
	}
	return zr, nil
}

// findPart returns the archive entry with the given name.
func findPart(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

var errPartTooLarge = errors.New("part exceeds the decompressed size limit")

// openPart opens an entry for reading, stopping with errPartTooLarge instead
// of decompressing past maxPartBytes, whatever the entry's header claims.
func openPart(f *zip.File) (io.ReadCloser, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	return &cappedReadCloser{rc: rc, left: maxPartBytes}, nil
}

type cappedReadCloser struct {
	rc   io.ReadCloser
	left int64
}

func (c *cappedReadCloser) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errPartTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.rc.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *cappedReadCloser) Close() error { return c.rc.Close() }

// sink collects text up to a cap, so parsers can stop early.
type sink struct {
	b   bytes.Buffer
	max int64
}

func (s *sink) full() bool { return int64(s.b.Len()) >= s.max }

func (s *sink) write(text string) {
	if !s.full() {
		s.b.WriteString(text)
	}
}

// text returns what was collected, cut to the cap on a rune boundary.
func (s *sink) text() string {
	b := s.b.Bytes()
	if int64(len(b)) > s.max {
		b = b[:s.max]
		for len(b) > 0 && !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
	}
	return stripControls(string(b))
}

// errStop ends a parse once the sink is full; it is not a failure.
var errStop = errors.New("stop")

// blockReaderAt reads the underlying file in large blocks and keeps a few,
// because zip's many small reads would otherwise each be a round trip to the
// NAS. Safe for one extraction at a time (a mutex guards the cache anyway).
type blockReaderAt struct {
	r    io.ReaderAt
	size int64

	mu     sync.Mutex
	blocks map[int64][]byte
	order  []int64 // load order, oldest first, for eviction
}

const (
	blockSize   = 256 << 10
	cacheBlocks = 8
)

func newBlockReaderAt(r io.ReaderAt, size int64) *blockReaderAt {
	return &blockReaderAt{r: r, size: size, blocks: map[int64][]byte{}}
}

func (c *blockReaderAt) block(idx int64) ([]byte, error) {
	if b, ok := c.blocks[idx]; ok {
		return b, nil
	}
	start := idx * blockSize
	n := int64(blockSize)
	if start+n > c.size {
		n = c.size - start
	}
	buf := make([]byte, n)
	got, err := c.r.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buf = buf[:got]
	if len(c.order) >= cacheBlocks {
		delete(c.blocks, c.order[0])
		c.order = c.order[1:]
	}
	c.blocks[idx] = buf
	c.order = append(c.order, idx)
	return buf, nil
}

func (c *blockReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if off < 0 || off >= c.size {
		return 0, io.EOF
	}
	read := 0
	for read < len(p) && off < c.size {
		blk, err := c.block(off / blockSize)
		if err != nil {
			return read, err
		}
		within := int(off % blockSize)
		if within >= len(blk) {
			return read, io.ErrUnexpectedEOF
		}
		n := copy(p[read:], blk[within:])
		read += n
		off += int64(n)
	}
	if read < len(p) {
		return read, io.EOF
	}
	return read, nil
}
