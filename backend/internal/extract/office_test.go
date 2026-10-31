package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// zipOf builds a zip from name/content pairs, in order.
func zipOf(t *testing.T, parts ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i+1 < len(parts); i += 2 {
		w, err := zw.Create(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(parts[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func extractBytes(ext string, data []byte, maxText int64) (string, error) {
	return Extract(context.Background(), ext, bytes.NewReader(data), int64(len(data)), maxText)
}

const (
	wNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	aNS = `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"`
	sNS = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
)

func TestDocxParagraphsRunsTabsAndBreaks(t *testing.T) {
	doc := `<w:document ` + wNS + `><w:body>
		<w:p><w:r><w:t>Quarterly </w:t></w:r><w:r><w:t>report</w:t></w:r></w:p>
		<w:p><w:r><w:t>Name</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>Amount &amp; notes</w:t></w:r><w:r><w:br/></w:r><w:r><w:t>second line</w:t></w:r></w:p>
		<w:p><w:r><w:delText>deleted text</w:delText></w:r></w:p>
		<w:p><w:pPr><w:rPr/></w:pPr></w:p>
		<w:p><mc:AlternateContent xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><mc:Choice><w:r><w:t>textbox</w:t></w:r></mc:Choice><mc:Fallback><w:r><w:t>textbox</w:t></w:r></mc:Fallback></mc:AlternateContent></w:p>
	</w:body></w:document>`
	got, err := extractBytes("docx", zipOf(t, "word/document.xml", doc), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Quarterly report", "Name\tAmount & notes\nsecond line"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "deleted text") {
		t.Errorf("tracked-deletion text must not be indexed: %q", got)
	}
	if strings.Count(got, "textbox") != 1 {
		t.Errorf("AlternateContent text should appear once, got %q", got)
	}
	if !strings.Contains(got, "report\n\n") {
		t.Errorf("paragraphs should be separated by blank lines: %q", got)
	}
}

func TestDocxIncludesFootnotesAndComments(t *testing.T) {
	data := zipOf(t,
		"word/document.xml", `<w:document `+wNS+`><w:body><w:p><w:r><w:t>Body text</w:t></w:r></w:p></w:body></w:document>`,
		"word/footnotes.xml", `<w:footnotes `+wNS+`><w:footnote><w:p><w:r><w:t>A footnote</w:t></w:r></w:p></w:footnote></w:footnotes>`,
		"word/comments.xml", `<w:comments `+wNS+`><w:comment><w:p><w:r><w:t>A review comment</w:t></w:r></w:p></w:comment></w:comments>`,
		"word/header1.xml", `<w:hdr `+wNS+`><w:p><w:r><w:t>Letterhead boilerplate</w:t></w:r></w:p></w:hdr>`,
	)
	got, err := extractBytes("docx", data, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Body text", "A footnote", "A review comment"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Letterhead") {
		t.Errorf("headers are boilerplate and are left out: %q", got)
	}
}

func TestDocxRespectsTheTextCap(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&body, `<w:p><w:r><w:t>paragraph number %d with some words</w:t></w:r></w:p>`, i)
	}
	data := zipOf(t, "word/document.xml", `<w:document `+wNS+`><w:body>`+body.String()+`</w:body></w:document>`)
	got, err := extractBytes("docx", data, 300)
	if err != nil || len(got) > 300 || len(got) < 200 {
		t.Fatalf("len = %d, err = %v; want just under the 300-byte cap", len(got), err)
	}
}

func TestDocxEmptyAndWrongPackages(t *testing.T) {
	empty := zipOf(t, "word/document.xml", `<w:document `+wNS+`><w:body><w:p/></w:body></w:document>`)
	if got, err := extractBytes("docx", empty, 1<<20); err != nil || strings.TrimSpace(got) != "" {
		t.Fatalf("empty document: %q, %v", got, err)
	}
	if _, err := extractBytes("docx", zipOf(t, "xl/workbook.xml", "<x/>"), 1<<20); err == nil || !strings.Contains(err.Error(), "not a Word document") {
		t.Fatalf("a zip without word/document.xml: %v", err)
	}
	if _, err := extractBytes("docx", []byte("this is not a zip at all"), 1<<20); err == nil || errors.Is(err, ErrEncrypted) {
		t.Fatalf("garbage should fail, but not as 'encrypted': %v", err)
	}
}

func TestPasswordProtectedOfficeFilesAreReportedAsEncrypted(t *testing.T) {
	ole := append(append([]byte{}, oleMagic...), make([]byte, 512)...)
	for _, ext := range []string{"docx", "xlsx", "pptx"} {
		if _, err := extractBytes(ext, ole, 1<<20); !errors.Is(err, ErrEncrypted) {
			t.Errorf("%s: got %v, want ErrEncrypted", ext, err)
		}
	}
}

func TestDocxZipBombIsStopped(t *testing.T) {
	old := maxPartBytes
	maxPartBytes = 1 << 20
	defer func() { maxPartBytes = old }()

	// ~8 MiB of decompressed whitespace squeezed into a few KiB: no text to
	// satisfy the text cap, so only the part-size cap can stop it.
	bomb := `<w:document ` + wNS + `><w:body>` + strings.Repeat(" ", 8<<20) + `</w:body></w:document>`
	data := zipOf(t, "word/document.xml", bomb)
	if len(data) > 64<<10 {
		t.Fatalf("test fixture isn't compressible enough (%d bytes)", len(data))
	}
	_, err := extractBytes("docx", data, 1<<20)
	if err == nil || !errors.Is(err, errPartTooLarge) {
		t.Fatalf("got %v, want errPartTooLarge", err)
	}
}

func TestTooManyArchiveEntriesIsRejected(t *testing.T) {
	var parts []string
	for i := 0; i < maxArchiveEntries+1; i++ {
		parts = append(parts, fmt.Sprintf("junk/%d.txt", i), "")
	}
	parts = append(parts, "word/document.xml", "<w:document/>")
	if _, err := extractBytes("docx", zipOf(t, parts...), 1<<20); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("got %v", err)
	}
}

func xlsxFixture(t *testing.T) []byte {
	return zipOf(t,
		"xl/workbook.xml", `<workbook `+sNS+`><sheets>
			<sheet name="Budget" sheetId="1" r:id="rId1"/>
			<sheet name="Notes" sheetId="2" r:id="rId2"/>
		</sheets></workbook>`,
		"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
			<Relationship Id="rId1" Type="x" Target="worksheets/sheet2.xml"/>
			<Relationship Id="rId2" Type="x" Target="/xl/worksheets/sheet1.xml"/>
		</Relationships>`,
		"xl/sharedStrings.xml", `<sst `+sNS+`>
			<si><t>Revenue</t></si>
			<si><r><t>Cost of </t></r><r><t>goods</t></r></si>
			<si><t>漢字</t><rPh sb="0" eb="2"><t>ふりがな</t></rPh></si>
			<si><t>Remember the audit</t></si>
		</sst>`,
		// Workbook order is Budget (sheet2.xml), then Notes (sheet1.xml).
		"xl/worksheets/sheet2.xml", `<worksheet `+sNS+`><sheetData>
			<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1"><v>12345.5</v></c></row>
			<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="b"><v>1</v></c><c r="C2" t="s"><v>99</v></c></row>
			<row r="3"/>
			<row r="4"><c r="A4" t="inlineStr"><is><t>inline text</t></is></c><c r="B4" t="str"><f>A1&amp;"!"</f><v>formula text</v></c></row>
		</sheetData></worksheet>`,
		"xl/worksheets/sheet1.xml", `<worksheet `+sNS+`><sheetData>
			<row r="1"><c r="A1" t="s"><v>3</v></c></row>
		</sheetData></worksheet>`,
	)
}

func TestXlsxSheetsInWorkbookOrderWithTextCellsOnly(t *testing.T) {
	got, err := extractBytes("xlsx", xlsxFixture(t), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	want := "Budget\nRevenue\tCost of goods\n漢字\ninline text\tformula text\n\nNotes\nRemember the audit\n\n"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	for _, not := range []string{"12345", "ふりがな", "true"} {
		if strings.Contains(got, not) {
			t.Errorf("%q must not be indexed: %q", not, got)
		}
	}
}

func TestXlsxCapAndSharedStringBudget(t *testing.T) {
	got, err := extractBytes("xlsx", xlsxFixture(t), 20)
	if err != nil || len(got) > 20 || !strings.HasPrefix(got, "Budget\nRevenue") {
		t.Fatalf("capped = %q, %v", got, err)
	}

	old := maxSharedText
	maxSharedText = 10 // room for "Revenue" only: later strings keep their index but lose their text
	defer func() { maxSharedText = old }()
	got, err = extractBytes("xlsx", xlsxFixture(t), 1<<20)
	if err != nil || !strings.Contains(got, "Revenue") || strings.Contains(got, "audit") {
		t.Fatalf("with a tiny shared-strings budget: %q, %v", got, err)
	}
}

func TestXlsxWrongPackage(t *testing.T) {
	if _, err := extractBytes("xlsx", zipOf(t, "word/document.xml", "<x/>"), 1<<20); err == nil || !strings.Contains(err.Error(), "not an Excel file") {
		t.Fatalf("got %v", err)
	}
}

func TestPptxSlidesInNumericOrderThenNotes(t *testing.T) {
	slide := func(text string) string {
		return `<p:sld ` + aNS + ` xmlns:p="x"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p><a:p><a:r><a:t>second</a:t></a:r><a:br/><a:r><a:t>line</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	data := zipOf(t,
		"ppt/slides/slide10.xml", slide("Tenth slide"),
		"ppt/slides/slide2.xml", slide("Second slide"),
		"ppt/slides/slide1.xml", slide("First &amp; foremost"),
		"ppt/notesSlides/notesSlide2.xml", `<p:notes `+aNS+` xmlns:p="x"><a:p><a:r><a:t>Speaker note two</a:t></a:r></a:p></p:notes>`,
		"ppt/slideLayouts/slideLayout1.xml", slide("Layout boilerplate"),
	)
	got, err := extractBytes("pptx", data, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"First & foremost", "Second slide", "Tenth slide", "Speaker note two"}
	last := -1
	for _, s := range order {
		i := strings.Index(got, s)
		if i < 0 || i < last {
			t.Fatalf("%q out of order or missing in %q", s, got)
		}
		last = i
	}
	if strings.Contains(got, "Layout boilerplate") {
		t.Errorf("layouts are not slides: %q", got)
	}
	if !strings.Contains(got, "second\nline") {
		t.Errorf("a:br should become a line break: %q", got)
	}
	if _, err := extractBytes("pptx", zipOf(t, "ppt/presentation.xml", "<p/>"), 1<<20); err == nil || !strings.Contains(err.Error(), "no slides") {
		t.Fatalf("a package without slides: %v", err)
	}
}

func TestOfficeVariantExtensionsUseTheSameExtractors(t *testing.T) {
	doc := zipOf(t, "word/document.xml", `<w:document `+wNS+`><w:body><w:p><w:r><w:t>macro doc</w:t></w:r></w:p></w:body></w:document>`)
	for _, ext := range []string{"docx", "docm", "dotx"} {
		if got, err := extractBytes(ext, doc, 1<<20); err != nil || !strings.Contains(got, "macro doc") {
			t.Errorf("%s: %q, %v", ext, got, err)
		}
	}
	supported := map[string]bool{}
	for _, e := range Supported() {
		supported[e] = true
	}
	for _, e := range []string{"docx", "docm", "dotx", "xlsx", "xlsm", "xltx", "pptx", "pptm", "ppsx", "potx", "pdf"} {
		if !supported[e] {
			t.Errorf("Supported() is missing %s", e)
		}
	}
	for _, e := range []string{"doc", "xls", "ppt", "key"} {
		if supported[e] {
			t.Errorf("%s must not be claimed (no extractor for it)", e)
		}
	}
}

// countingReaderAt records how many times the underlying file is read.
type countingReaderAt struct {
	r     io.ReaderAt
	reads int
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.reads++
	return c.r.ReadAt(p, off)
}

func TestBlockCacheTurnsManySmallReadsIntoFew(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&body, `<w:p><w:r><w:t>line %d of a longish document body</w:t></w:r></w:p>`, i)
	}
	data := zipOf(t, "word/document.xml", `<w:document `+wNS+`><w:body>`+body.String()+`</w:body></w:document>`)

	counter := &countingReaderAt{r: bytes.NewReader(data)}
	got, err := Extract(context.Background(), "docx", counter, int64(len(data)), 1<<30)
	if err != nil || !strings.Contains(got, "line 19999") {
		t.Fatalf("extract: %v", err)
	}
	blocks := len(data)/blockSize + 3
	if counter.reads > blocks*2 {
		t.Fatalf("%d reads of the file for %d bytes (block size %d): the cache isn't batching", counter.reads, len(data), blockSize)
	}
}

func TestBlockReaderAtMatchesTheUnderlyingFile(t *testing.T) {
	data := make([]byte, 3*blockSize+1234)
	for i := range data {
		data[i] = byte(i * 7)
	}
	c := newBlockReaderAt(bytes.NewReader(data), int64(len(data)))
	for _, tc := range []struct{ off, n int }{{0, 10}, {blockSize - 5, 10}, {blockSize, blockSize}, {len(data) - 20, 20}, {5, 3 * blockSize}} {
		buf := make([]byte, tc.n)
		n, err := c.ReadAt(buf, int64(tc.off))
		if err != nil && err != io.EOF || n != tc.n || !bytes.Equal(buf, data[tc.off:tc.off+tc.n]) {
			t.Fatalf("ReadAt(%d, %d) = %d, %v (mismatch with the file)", tc.off, tc.n, n, err)
		}
	}
	buf := make([]byte, 50)
	if n, err := c.ReadAt(buf, int64(len(data)-10)); n != 10 || err != io.EOF {
		t.Fatalf("read across the end = %d, %v; want 10, EOF", n, err)
	}
	if n, err := c.ReadAt(buf, int64(len(data))); n != 0 || err != io.EOF {
		t.Fatalf("read at the end = %d, %v", n, err)
	}
	// Eviction: touching more than cacheBlocks distinct blocks still reads correctly.
	big := make([]byte, (cacheBlocks+3)*blockSize)
	for i := range big {
		big[i] = byte(i % 251)
	}
	c = newBlockReaderAt(bytes.NewReader(big), int64(len(big)))
	for blk := 0; blk < cacheBlocks+3; blk++ {
		b := make([]byte, 100)
		if _, err := c.ReadAt(b, int64(blk*blockSize+7)); err != nil || !bytes.Equal(b, big[blk*blockSize+7:blk*blockSize+107]) {
			t.Fatalf("block %d after eviction: %v", blk, err)
		}
	}
}

// The files in testdata/seed/office were written by python-docx, openpyxl and
// python-pptx, so they check the extractors against structure this package
// didn't write itself.
func TestFilesWrittenByRealLibraries(t *testing.T) {
	for _, tc := range []struct {
		file    string
		want    []string
		notWant []string
	}{
		{"memo.docx",
			[]string{"Quarterly Planning Memo", "zeppelin logistics budget", "Bold and italic runs stay in one paragraph.", "Acme Airships", "4,200", "Café résumé naïve", "日本語"},
			nil},
		{"budget.xlsx",
			[]string{"Budget 2026", "Item\tCost\tOwner", "Zeppelin fuel\tAda", "Hangar rent\tGrace", "Notes", "Remember the quarterly audit"},
			[]string{"1234", "9000"}},
		{"roadmap.pptx",
			[]string{"Airship Roadmap", "Phase one: build the envelope", "Phase two: test flights", "Wrap-up", "Questions & answers", "Mention the hydrogen safety review"},
			nil},
	} {
		data, err := os.ReadFile("../../testdata/seed/office/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		got, err := extractBytes(tc.file[strings.LastIndex(tc.file, ".")+1:], data, 1<<20)
		if err != nil {
			t.Errorf("%s: %v", tc.file, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in %q", tc.file, w, got)
			}
		}
		for _, w := range tc.notWant {
			if strings.Contains(got, w) {
				t.Errorf("%s: should not contain %q (numbers aren't indexed): %q", tc.file, w, got)
			}
		}
	}
}
