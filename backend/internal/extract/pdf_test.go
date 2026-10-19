package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klippa-app/go-pdfium/requests"
)

func readPDFFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/seed/pdf/" + name + ".pdf")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func extractPDFBytes(ctx context.Context, data []byte, maxText int64) (string, error) {
	return Extract(ctx, "pdf", bytes.NewReader(data), int64(len(data)), maxText)
}

// syntheticPDF builds a valid PDF with n one-line pages ("page N ..."), using
// a standard font so no font file is needed. It lets tests make documents
// big enough to be interrupted, without committing megabytes of fixtures.
func syntheticPDF(n int) []byte {
	var buf bytes.Buffer
	offsets := []int{0}
	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets)-1, body)
	}
	buf.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	var kids strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&kids, "%d 0 R ", 4+i*2)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i := 0; i < n; i++ {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", 5+i*2))
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (page %d alpha%d bravo charlie delta echo) Tj ET", i+1, i+1)
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return buf.Bytes()
}

func TestPDFFilesFromDifferentProducers(t *testing.T) {
	core := []string{"Zeppelin logistics ledger", "hangar rent was approved on Tuesday", "alpha7 beta42", "hydrogen safety review"}
	for _, tc := range []struct {
		file string
		want []string
	}{
		{"compressed-objstm", core}, // PyMuPDF with compressed object streams
		{"aes256-owner-only", core}, // restricted, but opens without a password
		{"rc4-owner-only", core},    // same, older encryption
		{"macos-text", []string{"Plain text through Apple Quartz", "zeppelin schedule", "12345"}},
		{"chrome-unicode", []string{
			"Quarterly Zeppelin Report", "café", "résumé", "naïve",
			"efficient", "office", "flower", "affluent", // ligatures arrive as real letters
			"日本語", "中文文本", "한국어", "Ελληνικά", "Русский", "العربية",
			"Acme Airships 4,200", "hydrogen safety", "rotated text", "Second page: the hangar rent",
		}},
	} {
		got, err := extractPDFBytes(context.Background(), readPDFFixture(t, tc.file), 1<<20)
		if err != nil {
			t.Errorf("%s: %v", tc.file, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in %q", tc.file, w, got)
			}
		}
	}
}

func TestPDFPasswordProtectedIsEncryptedAndScannedHasNoText(t *testing.T) {
	if _, err := extractPDFBytes(context.Background(), readPDFFixture(t, "password-protected"), 1<<20); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("a PDF needing a password: got %v, want ErrEncrypted", err)
	}
	got, err := extractPDFBytes(context.Background(), readPDFFixture(t, "scanned-imageonly"), 1<<20)
	if err != nil || strings.TrimSpace(got) != "" {
		t.Fatalf("an image-only PDF should extract to nothing, not an error: %q, %v", got, err)
	}
}

func TestPDFNotAPDFIsAnError(t *testing.T) {
	for name, data := range map[string][]byte{
		"text":  []byte("this is plain text pretending to be a PDF"),
		"empty": {},
		"zip":   zipOf(t, "a.txt", "hi"),
	} {
		if _, err := extractPDFBytes(context.Background(), data, 1<<20); err == nil || errors.Is(err, ErrEncrypted) {
			t.Errorf("%s: got %v, want a plain error", name, err)
		}
	}
}

// Damaged PDFs are routine on a NAS (interrupted copies, bit rot). None may
// panic or hang; most should still give up their text.
func TestPDFDamagedFilesNeverPanicOrHang(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	recovered, total := 0, 0
	for _, name := range []string{"macos-text", "compressed-objstm", "chrome-unicode"} {
		orig := readPDFFixture(t, name)
		for i := 0; i < 24; i++ {
			data := append([]byte{}, orig...)
			switch i % 4 {
			case 0: // truncated
				data = data[:len(data)/4+rng.Intn(len(data)*3/4)]
			case 1: // flipped bytes
				for j := 0; j < 1+rng.Intn(40); j++ {
					data[rng.Intn(len(data))] = byte(rng.Intn(256))
				}
			case 2: // zeroed block
				a := rng.Intn(len(data))
				for j := a; j < a+rng.Intn(1500) && j < len(data); j++ {
					data[j] = 0
				}
			case 3: // chunk removed
				a := rng.Intn(len(data))
				b := min(a+rng.Intn(2000), len(data))
				data = append(data[:a], data[b:]...)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			got, err := extractPDFBytes(ctx, data, 1<<20)
			if ctx.Err() != nil {
				t.Fatalf("%s variant %d hung past the deadline", name, i)
			}
			cancel()
			total++
			if err == nil && strings.TrimSpace(got) != "" {
				recovered++
			}
		}
	}
	if recovered < total/3 {
		t.Errorf("only %d of %d damaged files gave up any text; the engine is expected to repair many of them", recovered, total)
	}
}

type failingReaderAt struct {
	data []byte
	fail int64
}

func (f failingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.fail {
		return 0, errors.New("simulated read error from the NAS")
	}
	return copy(p, f.data[off:]), nil
}

func TestPDFReadErrorsFromTheShareSurfaceAsErrors(t *testing.T) {
	data := readPDFFixture(t, "chrome-unicode")
	_, err := Extract(context.Background(), "pdf", failingReaderAt{data, int64(len(data) / 2)}, int64(len(data)), 1<<20)
	if err == nil {
		t.Fatal("a file that can't be fully read should not look like a success")
	}
}

func TestPDFTextCapStopsReadingPages(t *testing.T) {
	data := syntheticPDF(4000)
	start := time.Now()
	got, err := extractPDFBytes(context.Background(), data, 400)
	if err != nil || len(got) > 400 || len(got) < 200 {
		t.Fatalf("len = %d, err = %v; want just under the 400-byte cap", len(got), err)
	}
	if !strings.Contains(got, "page 1 alpha1") || strings.Contains(got, "page 3000") {
		t.Fatalf("expected the first pages only: %q", got)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s: it should stop after the first pages, not read all 4000", d)
	}
}

func TestPDFCancellationInterruptsAnExtractionInFlight(t *testing.T) {
	data := syntheticPDF(30000) // seconds of work, far longer than the cancel below
	// Warm the engine so the cancel lands during page extraction, not startup.
	if _, err := extractPDFBytes(context.Background(), readPDFFixture(t, "macos-text"), 1<<20); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		err error
		dur time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, err := extractPDFBytes(ctx, data, 1<<40)
		done <- result{err, time.Since(start)}
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", r.err)
		}
		if r.dur > 3*time.Second {
			t.Fatalf("cancel took %s to take effect", r.dur)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the extraction ignored the cancel")
	}

	// The shared engine must still work for the next file.
	if got, err := extractPDFBytes(context.Background(), readPDFFixture(t, "macos-text"), 1<<20); err != nil || !strings.Contains(got, "zeppelin") {
		t.Fatalf("after a cancelled extraction: %q, %v", got, err)
	}
}

func TestPDFDeadlineIsReportedAsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := extractPDFBytes(ctx, syntheticPDF(30000), 1<<40)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context.DeadlineExceeded", err)
	}
}

func TestPDFAlreadyCancelledDoesNotStartTheEngine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractPDFBytes(ctx, readPDFFixture(t, "macos-text"), 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestPDFConcurrentExtractions(t *testing.T) {
	data := readPDFFixture(t, "chrome-unicode")
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				got, err := extractPDFBytes(context.Background(), data, 1<<20)
				if err != nil || !strings.Contains(got, "Quarterly Zeppelin Report") {
					errs <- fmt.Errorf("got %q, %v", got, err)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPDFEngineIsReleasedWhenIdleAndComesBack(t *testing.T) {
	old := pdfIdleTimeout
	pdfIdleTimeout = 100 * time.Millisecond
	defer func() { pdfIdleTimeout = old }()

	data := readPDFFixture(t, "macos-text")
	if _, err := extractPDFBytes(context.Background(), data, 1<<20); err != nil {
		t.Fatal(err)
	}
	loaded := func() bool {
		pdfEngine.mu.Lock()
		defer pdfEngine.mu.Unlock()
		return pdfEngine.pool != nil
	}
	deadline := time.Now().Add(10 * time.Second)
	for loaded() && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if loaded() {
		t.Fatal("the PDF engine stayed loaded after going idle")
	}
	// It starts again on demand. Under the race detector a restart costs
	// another 20 s of compiling, so that half only runs in ordinary test runs.
	if raceEnabled {
		return
	}
	if got, err := extractPDFBytes(context.Background(), data, 1<<20); err != nil || !strings.Contains(got, "zeppelin") {
		t.Fatalf("after release: %q, %v", got, err)
	}
}

func TestPDFLigaturesAreExpanded(t *testing.T) {
	if got := pdfLigatures.Replace("oﬃce ﬁrst ﬂow diﬀ baﬄe ﬅ"); got != "office first flow diff baffle st" {
		t.Fatalf("got %q", got)
	}
}

// The engine parses untrusted files, so it must not be able to see the host's
// filesystem. go-pdfium mounts "/" read-write by default; we replace that with
// an empty one. Opening a real file by path therefore has to fail, while the
// same file opened through a reader (how Findo does it) works.
func TestPDFEngineHasNoFilesystemAccess(t *testing.T) {
	abs, err := filepath.Abs("../../testdata/seed/pdf/macos-text.pdf")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := acquirePDF()
	if err != nil {
		t.Fatal(err)
	}
	defer releasePDF()
	defer inst.Close()

	if _, err := inst.OpenDocument(&requests.OpenDocument{FilePath: &abs}); err == nil {
		t.Fatal("the PDF engine opened a file by host path: its sandbox has filesystem access")
	}
	if got, err := extractPDFBytes(context.Background(), readPDFFixture(t, "macos-text"), 1<<20); err != nil || !strings.Contains(got, "zeppelin") {
		t.Fatalf("reading through a reader must still work: %q, %v", got, err)
	}
}
