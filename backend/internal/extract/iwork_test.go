package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math/rand"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestNumbersFixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/seed/iwork/FindoSpreadsheet.numbers")
	if err != nil {
		t.Fatal(err)
	}
	got, err := extractBytes("numbers", data, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("extracted:\n%s", got)
	for _, want := range []string{
		"Companeez", "TootieFrootie", // sheet names
		"Fruit of the Loom", "WorkWorkWork", // table titles
		"Zebrafoxtrot", "DragonBallFruit", "I’m merged!", // cells
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Sheet names come before table titles, which come before cells.
	if !(strings.Index(got, "Companeez") < strings.Index(got, "Fruit of the Loom") &&
		strings.Index(got, "Fruit of the Loom") < strings.Index(got, "Zebrafoxtrot")) {
		t.Errorf("unexpected order in %q", got)
	}
}

// iwaOf frames messages {type, payload} as one Apple-style .iwa chunk.
type iwMsg struct {
	typ     uint64
	payload []byte
}

func archiveOf(msgs ...iwMsg) []byte {
	var out []byte
	for _, m := range msgs {
		var mi []byte
		mi = protowire.AppendTag(mi, 1, protowire.VarintType)
		mi = protowire.AppendVarint(mi, m.typ)
		mi = protowire.AppendTag(mi, 3, protowire.VarintType)
		mi = protowire.AppendVarint(mi, uint64(len(m.payload)))
		var info []byte
		info = protowire.AppendTag(info, 1, protowire.VarintType)
		info = protowire.AppendVarint(info, 1)
		info = protowire.AppendTag(info, 2, protowire.BytesType)
		info = protowire.AppendBytes(info, mi)
		out = protowire.AppendBytes(out, info)
		out = append(out, m.payload...)
	}
	return out
}

func chunkOf(comp []byte) []byte {
	h := make([]byte, 4)
	binary.LittleEndian.PutUint32(h, uint32(len(comp))<<8)
	return append(h, comp...)
}

func iwaOf(msgs ...iwMsg) []byte { return chunkOf(snappy.Encode(nil, archiveOf(msgs...))) }

func strField(num protowire.Number, s string) []byte {
	return protowire.AppendString(protowire.AppendTag(nil, num, protowire.BytesType), s)
}

func varField(num protowire.Number, x uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, num, protowire.VarintType), x)
}

func dataList(listType uint64, entries ...[]byte) iwMsg {
	p := varField(1, listType)
	for _, e := range entries {
		p = protowire.AppendBytes(protowire.AppendTag(p, 3, protowire.BytesType), e)
	}
	return iwMsg{iwDataList, p}
}

func numbersZip(t *testing.T, doc []byte, extra ...string) []byte {
	t.Helper()
	return zipOf(t, append([]string{"Index/Document.iwa", string(doc)}, extra...)...)
}

func TestNumbersSkipsUnusedAndNonStringEntries(t *testing.T) {
	live := append(varField(2, 1), strField(3, "live cell")...)
	dead := append(varField(2, 0), strField(3, "deleted cell")...)
	doc := iwaOf(
		iwMsg{iwSheet, strField(1, "Sheet one")},
		dataList(iwStringLst, live, dead),
		dataList(2, strField(3, "formula text")),
		iwMsg{9999, strField(1, "some other message")},
	)
	got, err := extractBytes("numbers", numbersZip(t, doc), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Sheet one\nlive cell\n" {
		t.Errorf("got %q", got)
	}
}

func TestNumbersHonorsTextCap(t *testing.T) {
	doc := iwaOf(iwMsg{iwSheet, strField(1, strings.Repeat("word ", 1000))})
	got, err := extractBytes("numbers", numbersZip(t, doc), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 100 {
		t.Errorf("len = %d, want ≤ 100", len(got))
	}
}

func TestNumbersRejectsHostileInput(t *testing.T) {
	// A Snappy header declaring a huge decoded size, with almost no data.
	bomb := chunkOf(protowire.AppendVarint(nil, 1<<40))
	// A message whose declared length runs past the stream.
	long := chunkOf(snappy.Encode(nil, func() []byte {
		b := archiveOf(iwMsg{iwSheet, strField(1, "x")})
		return b[:len(b)-2]
	}()))
	// A chunk whose declared compressed length exceeds the limit.
	big := []byte{0, 0xff, 0xff, 0xff}
	cases := map[string][]byte{
		"snappy bomb":      bomb,
		"lying length":     long,
		"bad chunk marker": {1, 0, 0, 0},
		"huge chunk":       big,
		"truncated chunk":  {0, 10, 0, 0, 1, 2},
		"not snappy":       chunkOf([]byte{5, 0xff, 0xff, 0xff, 0xff}),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := extractBytes("numbers", numbersZip(t, doc), 1<<20); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestNumbersDecodedSizeCap(t *testing.T) {
	old := maxIWABytes
	maxIWABytes = 1 << 10
	t.Cleanup(func() { maxIWABytes = old })
	doc := iwaOf(iwMsg{iwSheet, strField(1, strings.Repeat("a", 4<<10))})
	if _, err := extractBytes("numbers", numbersZip(t, doc), 1<<20); err == nil {
		t.Error("expected the decoded size cap to stop extraction")
	}
}

func TestNumbersRequiresDocument(t *testing.T) {
	if _, err := extractBytes("numbers", zipOf(t, "other.txt", "x"), 1<<20); err == nil {
		t.Error("expected an error for a zip with no Index/Document.iwa")
	}
}

// byteCountReaderAt records the bytes read from the underlying file.
type byteCountReaderAt struct {
	r io.ReaderAt
	n atomic.Int64
}

func (c *byteCountReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}

// Embedded media (Data/) is most of a real document and must never be read.
func TestNumbersDoesNotReadMedia(t *testing.T) {
	media := make([]byte, 16<<20)
	rand.New(rand.NewSource(1)).Read(media)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "Index/Document.iwa", Method: zip.Store})
	w.Write(iwaOf(iwMsg{iwSheet, strField(1, "Sheet one")}))
	w, _ = zw.CreateHeader(&zip.FileHeader{Name: "Data/movie.mov", Method: zip.Store})
	w.Write(media)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	cr := &byteCountReaderAt{r: bytes.NewReader(buf.Bytes())}
	got, err := Extract(context.Background(), "numbers", cr, int64(buf.Len()), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Sheet one\n" {
		t.Errorf("got %q", got)
	}
	// The block cache reads 256 KiB at a time, so allow a few blocks.
	if read := cr.n.Load(); read > 2<<20 {
		t.Errorf("read %d bytes of a %d byte file; media must be skipped", read, buf.Len())
	}
}

func FuzzNumbersText(f *testing.F) {
	f.Add(archiveOf(iwMsg{iwSheet, strField(1, "Sheet")}))
	f.Add(archiveOf(dataList(iwStringLst, strField(3, "cell"))))
	f.Fuzz(func(t *testing.T, b []byte) {
		_ = numbersText(b, func(string) {})
	})
}
