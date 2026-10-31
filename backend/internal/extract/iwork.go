package extract

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"unicode/utf8"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// Numbers (and later Pages and Keynote) are zips of .iwa files: Snappy blocks
// in Apple's own framing, holding length-prefixed protobuf messages with no
// published schema. Rather than decode whole documents, this reads only the
// few message types that carry user text, at fixed field numbers, so
// styles, fonts and internal identifiers never become searchable words and
// nothing nested is walked.
//
// Everything here is untrusted input from a network share, so each step is
// bounded: the zip layer by the Office limits (openPart), a Snappy chunk by
// its declared size before it is allocated, an .iwa by the total decoded
// size, and message lengths by what remains in the buffer.

// Variables so tests can shrink them.
var (
	maxIWAChunk int64 = 1 << 20  // compressed or decoded bytes in one chunk (Apple writes ≤ 64 KiB)
	maxIWABytes int64 = 64 << 20 // decoded bytes parsed from one .iwa
)

// Message type ids (from the reverse-engineered iWork schemas) and the field
// numbers that hold text.
const (
	iwSheet     = 2    // TN.SheetArchive: name = 1
	iwTable     = 6001 // TST.TableModelArchive: table_name = 8
	iwDataList  = 6005 // TST.TableDataList: list_type = 1, entries = 3
	iwStringLst = 1    // TableDataList.ListType.STRING
)

var iwaDataListRe = regexp.MustCompile(`^Index/Tables/DataList[^/]*\.iwa$`)

// extractNumbers reads sheet names, table titles, then the text cells. Cells
// holding only numbers, dates or formulas have no text and add nothing, the
// same as for Excel.
func extractNumbers(_ context.Context, r io.ReaderAt, size, maxText int64) (string, error) {
	zr, err := openPackage(r, size)
	if err != nil {
		return "", err
	}
	doc := findPart(zr, "Index/Document.iwa")
	if doc == nil {
		return "", errors.New("not a Numbers file: it has no Index/Document.iwa")
	}

	var lists []*zip.File
	for _, f := range zr.File {
		if iwaDataListRe.MatchString(f.Name) {
			lists = append(lists, f)
		}
	}
	sort.Slice(lists, func(i, j int) bool { return lists[i].Name < lists[j].Name })

	// Sheet names live in Document.iwa and table titles in
	// CalculationEngine.iwa; both come before the cells.
	parts := append([]*zip.File{doc}, optionalParts(zr, "Index/CalculationEngine.iwa")...)
	parts = append(parts, lists...)

	out := &sink{max: maxText}
	emit := func(s string) {
		if s != "" && utf8.ValidString(s) {
			out.write(s)
			out.write("\n")
		}
	}
	for _, f := range parts {
		if out.full() {
			break
		}
		data, err := readIWA(f)
		if err != nil {
			if out.b.Len() == 0 {
				return "", fmt.Errorf("%s: %w", f.Name, err)
			}
			break
		}
		if err := numbersText(data, emit); err != nil && out.b.Len() == 0 {
			return "", fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return out.text(), nil
}

// readIWA returns an .iwa entry's decoded message stream. Each chunk is a
// zero byte, a 3-byte little-endian length, then a raw Snappy block (no CRC
// or stream framing, so snappy.NewReader can't read it).
func readIWA(f *zip.File) ([]byte, error) {
	rc, err := openPart(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var out []byte
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(rc, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("truncated chunk header: %w", err)
		}
		if hdr[0] != 0 {
			return nil, errors.New("not an iWork archive chunk")
		}
		n := int64(binary.LittleEndian.Uint32(hdr[:]) >> 8)
		if n > maxIWAChunk {
			return nil, fmt.Errorf("chunk of %d bytes exceeds the limit", n)
		}
		comp := make([]byte, n)
		if _, err := io.ReadFull(rc, comp); err != nil {
			return nil, fmt.Errorf("truncated chunk: %w", err)
		}
		// Check the declared size before decoding allocates it.
		dn, err := snappy.DecodedLen(comp)
		if err != nil {
			return nil, err
		}
		if int64(dn) > maxIWAChunk || int64(len(out))+int64(dn) > maxIWABytes {
			return nil, errPartTooLarge
		}
		dec, err := snappy.Decode(nil, comp)
		if err != nil {
			return nil, err
		}
		out = append(out, dec...)
	}
}

// numbersText walks an .iwa message stream: repeated (varint length,
// ArchiveInfo, message payloads). It calls emit with each user-text string
// in the message types above.
func numbersText(b []byte, emit func(string)) error {
	for len(b) > 0 {
		n, k := protowire.ConsumeVarint(b)
		if k < 0 || n > uint64(len(b)-k) {
			return errors.New("bad archive header length")
		}
		info := b[k : k+int(n)]
		b = b[k+int(n):]

		// ArchiveInfo.message_infos (field 2) each give a payload's type
		// (field 1) and byte length (field 3); payloads follow back to back.
		err := eachField(info, func(num protowire.Number, _ uint64, v []byte) error {
			if num != 2 {
				return nil
			}
			var typ, length uint64
			if err := eachField(v, func(num protowire.Number, x uint64, _ []byte) error {
				switch num {
				case 1:
					typ = x
				case 3:
					length = x
				}
				return nil
			}); err != nil {
				return err
			}
			if length > uint64(len(b)) {
				return errors.New("message length exceeds the archive")
			}
			payload := b[:length]
			b = b[length:]
			return messageText(typ, payload, emit)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func messageText(typ uint64, payload []byte, emit func(string)) error {
	switch typ {
	case iwSheet:
		return eachField(payload, func(num protowire.Number, _ uint64, v []byte) error {
			if num == 1 && v != nil {
				emit(string(v))
			}
			return nil
		})
	case iwTable:
		return eachField(payload, func(num protowire.Number, _ uint64, v []byte) error {
			if num == 8 && v != nil {
				emit(string(v))
			}
			return nil
		})
	case iwDataList:
		var listType uint64
		var entries [][]byte
		if err := eachField(payload, func(num protowire.Number, x uint64, v []byte) error {
			switch num {
			case 1:
				listType = x
			case 3:
				entries = append(entries, v)
			}
			return nil
		}); err != nil {
			return err
		}
		if listType != iwStringLst {
			return nil
		}
		for _, e := range entries {
			// Entry: key = 1, refcount = 2, string = 3. A zero refcount is
			// text no cell uses any more.
			var text []byte
			refs := uint64(1)
			if err := eachField(e, func(num protowire.Number, x uint64, v []byte) error {
				switch num {
				case 2:
					refs = x
				case 3:
					text = v
				}
				return nil
			}); err != nil {
				return err
			}
			if refs > 0 && text != nil {
				emit(string(text))
			}
		}
	}
	return nil
}

// eachField calls fn for every top-level field of a protobuf message: x holds
// a varint field's value and v a length-delimited field's bytes (nil for
// fixed-width fields, which are skipped; groups are rejected). It never descends into nested
// messages, so malformed nesting can't cost stack or time.
func eachField(b []byte, fn func(num protowire.Number, x uint64, v []byte) error) error {
	for len(b) > 0 {
		num, typ, k := protowire.ConsumeTag(b)
		if k < 0 {
			return errors.New("malformed protobuf tag")
		}
		b = b[k:]
		var x uint64
		var v []byte
		switch typ {
		case protowire.VarintType:
			x, k = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			v, k = protowire.ConsumeBytes(b)
			if v == nil {
				v = []byte{}
			}
		case protowire.Fixed32Type:
			k = 4
		case protowire.Fixed64Type:
			k = 8
		default:
			return errors.New("unsupported protobuf wire type")
		}
		if k < 0 || k > len(b) {
			return errors.New("malformed protobuf field")
		}
		b = b[k:]
		if err := fn(num, x, v); err != nil {
			return err
		}
	}
	return nil
}
