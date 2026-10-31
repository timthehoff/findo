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
		_ = walkArchive(b, pagesMessage, func(string) {})
		_ = walkArchive(b, numbersMessage, func(string) {})
		_, _ = slideOrder(b)
	})
}

func TestPagesFixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/seed/iwork/FindoDocument.pages")
	if err != nil {
		t.Fatal(err)
	}
	got, err := extractBytes("pages", data, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("extracted:\n%s", got)
	for _, want := range []string{
		"Interesting Data",                      // heading
		"I’m a little teapot, short and stout.", // body
		"Here is my handle, here is my spout.",
		"Tip", "Me", "Over", // bulleted list
		"They’re a-sea!",                // text before the footnote
		"Not actually",                  // footnote
		"Floating in the deep blue sea", // text box
		"This needs more elaboration",   // comment on the text box
		"And", "Pour", "Out",            // table cells
		"Table 1", // table title
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, not := range []string{"BaseballCap", "Footsies", "Tim Hoff", "￼", "Blank"} {
		if strings.Contains(got, not) {
			t.Errorf("must not contain %q", not)
		}
	}
	if strings.Contains(got, "\n\n") {
		t.Errorf("blank lines should be dropped: %q", got)
	}
}

func TestPagesStorageKindsAndAttachments(t *testing.T) {
	storage := func(kind uint64, text string) iwMsg {
		p := varField(1, kind)
		return iwMsg{iwText, append(p, strField(3, text)...)}
	}
	doc := iwaOf(
		storage(0, "Title￼\n\n  body line\x05 \n"),
		storage(1, "header or footer"),
		storage(2, "￼ footnote"),
		iwMsg{iwText, strField(3, "text box")}, // no kind field
		iwMsg{iwComment, strField(1, "a comment")},
		iwMsg{iwSheet, strField(1, "not a Pages message")},
	)
	got, err := extractBytes("pages", numbersZip(t, doc), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	want := "Title\nbody line\nfootnote\ntext box\na comment\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPagesReadsNumberedCalculationEngine(t *testing.T) {
	calc := iwaOf(iwMsg{iwTable, strField(8, "Budget table")})
	got, err := extractBytes("pages", numbersZip(t, iwaOf(iwMsg{iwText, strField(3, "body")}),
		"Index/CalculationEngine-1732636.iwa", string(calc)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got != "body\nBudget table\n" {
		t.Errorf("got %q", got)
	}
}

// A Numbers file's text boxes are not indexed by the Numbers extractor, and
// a Pages file's sheet names mean nothing: each app reads only its messages.
func TestNumbersIgnoresPagesMessages(t *testing.T) {
	doc := iwaOf(iwMsg{iwText, strField(3, "stray text")}, iwMsg{iwSheet, strField(1, "Sheet")})
	got, err := extractBytes("numbers", numbersZip(t, doc), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Sheet\n" {
		t.Errorf("got %q", got)
	}
}

func TestKeynoteFixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/seed/iwork/FindoPresentation.key")
	if err != nil {
		t.Fatal(err)
	}
	got, err := extractBytes("key", data, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("extracted:\n%s", got)

	// Presentation order, each slide's text before its notes, then tables.
	// Within a slide the order is the file's, so only the groups are ordered.
	groups := [][]string{
		{"Halloween in America", "Jane Doe", "Ghost Towns"},
		{"Requirements", "Jack o’ Lanterns", "Candy", "Costumes", "Monochrome"},
		{"All the good stuff"},
		{"Moods", "Spooooky"},
		{"Table 1", "Spider", "Grid"},
	}
	prevEnd := 0
	for _, g := range groups {
		end := prevEnd
		for _, w := range g {
			i := strings.Index(got, w)
			switch {
			case i < 0:
				t.Errorf("missing %q", w)
			case i < prevEnd:
				t.Errorf("%q comes before the previous group", w)
			default:
				end = max(end, i)
			}
		}
		prevEnd = end
	}
	// Template placeholders, chart sample data and shape names are not text.
	for _, not := range []string{
		"Slide Title", "Body Level", "Presentation Title", "Section Title",
		"Region 1", "April", "Category Axis", "Parallelogram", "￼",
	} {
		if strings.Contains(got, not) {
			t.Errorf("must not contain %q", not)
		}
	}
}

// Slide order comes from the show in Document.iwa, not from file names.
func TestKeynoteFollowsShowOrderAndSeparatesNotes(t *testing.T) {
	ref := func(id uint64) []byte { return varField(1, id) }
	node := func(slide uint64) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), ref(slide))
	}
	entry := func(nodeID uint64) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, 3, protowire.BytesType),
			protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), ref(nodeID)))
	}
	// Presentation order: slide 20 (file "Slide-b"), then slide 10 ("Slide-a").
	show := iwMsg{iwShow, append(entry(101), entry(102)...)}
	doc := archiveWithIDs(
		idMsg{900, show},
		idMsg{101, iwMsg{iwSlideNode, node(20)}},
		idMsg{102, iwMsg{iwSlideNode, node(10)}},
	)
	slideA := archiveWithIDs(
		idMsg{10, iwMsg{iwSlide, nil}},
		idMsg{11, iwMsg{iwText, strField(3, "second slide")}},
	)
	slideB := archiveWithIDs(
		idMsg{20, iwMsg{iwSlide, nil}},
		idMsg{21, iwMsg{iwText, append(varField(1, iwNotes), strField(3, "notes of first")...)}},
		idMsg{22, iwMsg{iwText, strField(3, "first slide\n￼")}},
	)
	template := iwaOf(iwMsg{iwText, strField(3, "Slide Title")})
	data := zipOf(t,
		"Index/Document.iwa", string(chunkOf(snappy.Encode(nil, doc))),
		"Index/Slide-a.iwa", string(chunkOf(snappy.Encode(nil, slideA))),
		"Index/Slide-b.iwa", string(chunkOf(snappy.Encode(nil, slideB))),
		"Index/TemplateSlide-1.iwa", string(template),
	)
	got, err := extractBytes("key", data, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if want := "first slide\nnotes of first\nsecond slide\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

type idMsg struct {
	id  uint64
	msg iwMsg
}

// archiveWithIDs is archiveOf with an explicit object id per message.
func archiveWithIDs(msgs ...idMsg) []byte {
	var out []byte
	for _, m := range msgs {
		mi := protowire.AppendTag(nil, 1, protowire.VarintType)
		mi = protowire.AppendVarint(mi, m.msg.typ)
		mi = protowire.AppendTag(mi, 3, protowire.VarintType)
		mi = protowire.AppendVarint(mi, uint64(len(m.msg.payload)))
		info := protowire.AppendTag(nil, 1, protowire.VarintType)
		info = protowire.AppendVarint(info, m.id)
		info = protowire.AppendTag(info, 2, protowire.BytesType)
		info = protowire.AppendBytes(info, mi)
		out = protowire.AppendBytes(out, info)
		out = append(out, m.msg.payload...)
	}
	return out
}
