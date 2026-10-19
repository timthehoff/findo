package extract

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// extractDocx reads Word text: the body, then footnotes, endnotes and
// comments (all searchable, none boilerplate like headers and footers).
// Paragraphs are separated by blank lines so the chunker can split on them.
func extractDocx(r io.ReaderAt, size, maxText int64) (string, error) {
	zr, err := openPackage(r, size)
	if err != nil {
		return "", err
	}
	main := findPart(zr, "word/document.xml")
	if main == nil {
		return "", errors.New("not a Word document: word/document.xml is missing")
	}

	out := &sink{max: maxText}
	for _, f := range append([]*zipFile{main}, optionalParts(zr, "word/footnotes.xml", "word/endnotes.xml", "word/comments.xml")...) {
		if out.full() {
			break
		}
		if err := parseWordXML(f, out); err != nil && !errors.Is(err, errStop) {
			if out.b.Len() == 0 {
				return "", fmt.Errorf("%s: %w", f.Name, err)
			}
			break // keep the text we got from a damaged tail
		}
	}
	return out.text(), nil
}

func parseWordXML(f *zipFile, out *sink) error {
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
			case "Fallback": // mc:AlternateContent repeats its Choice here
				if err := d.Skip(); err != nil {
					return err
				}
			case "t":
				inText = true
			case "tab":
				out.write("\t")
			case "br", "cr":
				out.write("\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				out.write("\n\n")
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
