package extract

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
)

type zipFile = zip.File

// optionalParts returns whichever of the named parts exist, in order.
func optionalParts(zr *zip.Reader, names ...string) []*zipFile {
	var out []*zipFile
	for _, n := range names {
		if f := findPart(zr, n); f != nil {
			out = append(out, f)
		}
	}
	return out
}

// numberedParts returns entries matching re (whose first group is a number),
// in numeric order: slide2 before slide10, which a plain name sort gets wrong.
func numberedParts(zr *zip.Reader, re *regexp.Regexp) []*zipFile {
	type numbered struct {
		n int
		f *zipFile
	}
	var found []numbered
	for _, f := range zr.File {
		if m := re.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			found = append(found, numbered{n, f})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	out := make([]*zipFile, len(found))
	for i, x := range found {
		out[i] = x.f
	}
	return out
}

var (
	slideRe = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)
	notesRe = regexp.MustCompile(`^ppt/notesSlides/notesSlide(\d+)\.xml$`)
)

// extractPptx reads slide text in slide order, then the speaker notes.
func extractPptx(r io.ReaderAt, size, maxText int64) (string, error) {
	zr, err := openPackage(r, size)
	if err != nil {
		return "", err
	}
	slides := numberedParts(zr, slideRe)
	if len(slides) == 0 {
		return "", errors.New("not a PowerPoint file: it has no slides")
	}

	out := &sink{max: maxText}
	for _, f := range append(slides, numberedParts(zr, notesRe)...) {
		if out.full() {
			break
		}
		if err := parseSlideXML(f, out); err != nil && !errors.Is(err, errStop) {
			if out.b.Len() == 0 {
				return "", fmt.Errorf("%s: %w", f.Name, err)
			}
			break
		}
		out.write("\n")
	}
	return out.text(), nil
}

func parseSlideXML(f *zipFile, out *sink) error {
	rc, err := openPart(f)
	if err != nil {
		return err
	}
	defer rc.Close()

	d := xml.NewDecoder(rc)
	inText := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "Fallback":
				if err := d.Skip(); err != nil {
					return err
				}
			case "t":
				inText = true
			case "br":
				out.write("\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				out.write("\n")
			}
		case xml.CharData:
			if inText {
				out.write(string(t))
			}
		}
		if out.full() {
			return errStop
		}
	}
}
