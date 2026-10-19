package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	pdfiumerrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// PDF text comes from PDFium (the engine Chrome uses) compiled to WebAssembly
// and run by wazero, so Findo stays one pure-Go binary with no cgo. A
// pure-Go PDF parser was tried first and was much worse on the PDFs people
// really have: no AES-256 encryption, nothing from some older producers,
// panics on damaged files. PDFium repairs most damage and reads all of them.

const pdfMaxInstances = 4 // matches the most workers the content settings allow

// pdfMemoryLimitPages caps each engine instance's WebAssembly memory (64 KiB
// pages, so 1 GiB). wazero's default is 4 GiB per instance, and a hostile
// PDF can ask for as much as it likes, times four workers.
const pdfMemoryLimitPages = 16384

// pdfIdleTimeout is how long the engine stays loaded after its last use. It
// holds a few hundred MB, which is worth freeing between indexing runs.
// Variable so tests can shorten it.
var pdfIdleTimeout = 5 * time.Minute

// pdfEngine lazily creates the shared WebAssembly pool (about 3 s the first
// time) and closes it again once idle.
var pdfEngine struct {
	mu    sync.Mutex
	pool  pdfium.Pool
	inUse int
	timer *time.Timer
}

func acquirePDF() (pdfium.Pdfium, error) {
	e := &pdfEngine
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	if e.pool == nil {
		pool, err := webassembly.Init(webassembly.Config{
			MaxIdle:  1,
			MaxTotal: pdfMaxInstances,
			// The engine parses untrusted files, so it gets nothing but the bytes we
			// hand it: go-pdfium's default mounts the host's whole root filesystem
			// read-write into the sandbox, which we never need (documents arrive
			// through a reader, not a path).
			FSConfig: wazero.NewFSConfig(),
			Stdout:   io.Discard,
			Stderr:   io.Discard,
			// CloseOnContextDone is what lets Kill interrupt a call that is stuck
			// inside the engine; the feature flags are PDFium's own needs.
			RuntimeConfig: wazero.NewRuntimeConfig().
				WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
				WithCloseOnContextDone(true).
				WithMemoryLimitPages(pdfMemoryLimitPages),
		})
		if err != nil {
			return nil, fmt.Errorf("start the PDF engine: %w", err)
		}
		e.pool = pool
	}
	inst, err := e.pool.GetInstance(30 * time.Second)
	if err != nil {
		scheduleIdleCloseLocked()
		return nil, fmt.Errorf("get a PDF engine instance: %w", err)
	}
	e.inUse++
	return inst, nil
}

func releasePDF() {
	e := &pdfEngine
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inUse--
	scheduleIdleCloseLocked()
}

func scheduleIdleCloseLocked() {
	e := &pdfEngine
	if e.inUse == 0 && e.pool != nil && e.timer == nil {
		e.timer = time.AfterFunc(pdfIdleTimeout, closeIdlePDF)
	}
}

func closeIdlePDF() {
	e := &pdfEngine
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inUse == 0 && e.pool != nil {
		_ = e.pool.Close()
		e.pool = nil
	}
	e.timer = nil
}

// pdfLigatures expands the Latin ligature characters PDFium returns when a PDF
// maps a glyph to one, so searching "office" finds "oﬃce".
var pdfLigatures = strings.NewReplacer(
	"ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi", "ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st",
)

// extractPDF reads a PDF's text layer page by page. A scanned PDF that is only
// pictures has no text layer and yields "", which the caller records as "no
// text". A password-protected PDF returns ErrEncrypted; one that merely
// restricts copying or printing (an owner password with an empty user
// password) opens normally.
func extractPDF(ctx context.Context, r io.ReaderAt, size, maxText int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	inst, err := acquirePDF()
	if err != nil {
		return "", err
	}
	defer releasePDF()

	// If the caller's context ends, Kill interrupts whatever the engine is
	// doing (it cancels the instance's WebAssembly execution), so a
	// pathological file can't outlive the deadline. Kill and Close must never
	// both run, and Close must not overlap a running call, so exactly one of
	// them is chosen below.
	killed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(killed)
		_ = inst.Kill()
	})
	defer func() {
		if stop() { // the context never ended: the instance is idle, close it normally
			_ = inst.Close()
		} else { // Kill is running or finished; it releases the instance
			<-killed
		}
	}()

	// Read through the block cache: the engine asks for small ranges, and each
	// one would otherwise be a round trip to the NAS.
	rs := io.NewSectionReader(newBlockReaderAt(r, size), 0, size)
	doc, err := inst.OpenDocument(&requests.OpenDocument{FileReader: rs, FileReaderSize: size})
	if err != nil {
		return "", pdfError(ctx, err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})

	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return "", pdfError(ctx, err)
	}

	out := &sink{max: maxText}
	for i := 0; i < count.PageCount && !out.full(); i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		res, err := inst.GetPageText(&requests.GetPageText{
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}},
		})
		if err != nil {
			if out.b.Len() > 0 {
				break // keep the pages that did read
			}
			return "", pdfError(ctx, fmt.Errorf("page %d: %w", i+1, err))
		}
		text := strings.ReplaceAll(strings.ReplaceAll(res.Text, "\r\n", "\n"), "\r", "\n")
		out.write(pdfLigatures.Replace(text))
		out.write("\n\n")
	}
	return out.text(), nil
}

// pdfError reports why an engine call failed: the caller's deadline or cancel
// (which closed the instance), a password, or a genuine problem with the file.
func pdfError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, pdfiumerrors.ErrPassword):
		return ErrEncrypted
	}
	return fmt.Errorf("not a readable PDF: %w", err)
}
