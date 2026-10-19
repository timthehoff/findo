package extract

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// extractXlsx reads spreadsheet text, sheet by sheet in workbook order: each
// sheet's name, then its text cells, one row per line with tabs between cells.
// Numbers, dates and booleans are skipped: only words are worth searching.
func extractXlsx(_ context.Context, r io.ReaderAt, size, maxText int64) (string, error) {
	zr, err := openPackage(r, size)
	if err != nil {
		return "", err
	}
	if findPart(zr, "xl/workbook.xml") == nil {
		return "", errors.New("not an Excel file: xl/workbook.xml is missing")
	}

	shared, err := readSharedStrings(zr)
	if err != nil {
		return "", fmt.Errorf("shared strings: %w", err)
	}
	sheets, err := readSheets(zr)
	if err != nil {
		return "", err
	}

	out := &sink{max: maxText}
	for _, sh := range sheets {
		if out.full() {
			break
		}
		f := findPart(zr, sh.path)
		if f == nil {
			continue
		}
		out.write(sh.name + "\n")
		if err := parseSheetXML(f, shared, out); err != nil && !errors.Is(err, errStop) {
			if out.b.Len() == 0 {
				return "", fmt.Errorf("%s: %w", f.Name, err)
			}
			break
		}
		out.write("\n")
	}
	return out.text(), nil
}

type sheetRef struct{ name, path string }

// readSheets lists the worksheets in workbook order, resolving each sheet's
// relationship id to its part name.
func readSheets(zr *zip.Reader) ([]sheetRef, error) {
	wb := findPart(zr, "xl/workbook.xml")
	rc, err := openPart(wb)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	type named struct{ name, rid string }
	var order []named
	d := xml.NewDecoder(rc)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xl/workbook.xml: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			var n named
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "name":
					n.name = a.Value
				case "id": // r:id
					n.rid = a.Value
				}
			}
			order = append(order, n)
		}
	}

	targets := map[string]string{}
	if rels := findPart(zr, "xl/_rels/workbook.xml.rels"); rels != nil {
		rrc, err := openPart(rels)
		if err != nil {
			return nil, err
		}
		defer rrc.Close()
		rd := xml.NewDecoder(rrc)
		for {
			tok, err := rd.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("xl/_rels/workbook.xml.rels: %w", err)
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
				var id, target string
				for _, a := range se.Attr {
					switch a.Name.Local {
					case "Id":
						id = a.Value
					case "Target":
						target = a.Value
					}
				}
				targets[id] = target
			}
		}
	}

	var out []sheetRef
	for _, n := range order {
		target := targets[n.rid]
		if target == "" {
			continue
		}
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Join("xl", target)
		}
		out = append(out, sheetRef{name: n.name, path: target})
	}
	return out, nil
}

// readSharedStrings loads xl/sharedStrings.xml (cells refer to its entries by
// index). Text beyond maxSharedText is dropped, keeping indices aligned.
func readSharedStrings(zr *zip.Reader) ([]string, error) {
	f := findPart(zr, "xl/sharedStrings.xml")
	if f == nil {
		return nil, nil
	}
	rc, err := openPart(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var out []string
	var cur strings.Builder
	var kept int64
	inT := false
	d := xml.NewDecoder(rc)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "rPh": // phonetic (furigana) runs repeat the text
				if err := d.Skip(); err != nil {
					return out, err
				}
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				s := cur.String()
				cur.Reset()
				if kept+int64(len(s)) > maxSharedText {
					s = ""
				}
				kept += int64(len(s))
				out = append(out, s)
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
}

func parseSheetXML(f *zipFile, shared []string, out *sink) error {
	rc, err := openPart(f)
	if err != nil {
		return err
	}
	defer rc.Close()

	var (
		cellType  string
		inV, inIS bool
		inT       bool
		cell      strings.Builder
		rowCells  []string
	)
	d := xml.NewDecoder(rc)
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
			case "row":
				rowCells = rowCells[:0]
			case "c":
				cellType = ""
				cell.Reset()
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "is":
				inIS = false
			case "t":
				inT = false
			case "c":
				text := cell.String()
				if cellType == "s" {
					idx, err := strconv.Atoi(strings.TrimSpace(text))
					text = ""
					if err == nil && idx >= 0 && idx < len(shared) {
						text = shared[idx]
					}
				} else if cellType != "inlineStr" && cellType != "str" {
					text = "" // numbers, dates, booleans, errors
				}
				if strings.TrimSpace(text) != "" {
					rowCells = append(rowCells, text)
				}
			case "row":
				if len(rowCells) > 0 {
					out.write(strings.Join(rowCells, "\t") + "\n")
				}
			}
		case xml.CharData:
			if (inV && !inIS) || inT {
				cell.Write(t)
			}
		}
		if out.full() {
			return errStop
		}
	}
}
