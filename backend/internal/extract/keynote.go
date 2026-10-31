package extract

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"sort"

	"google.golang.org/protobuf/encoding/protowire"
)

// extractKeynote reads each slide's text, comments and speaker notes, slide
// by slide in presentation order, then table titles and cells. The theme's
// TemplateSlide parts (placeholder prompts like "Slide Title") are never
// opened, and neither are charts: their labels are sample data until edited
// and live in a layout this reader doesn't walk.
func extractKeynote(_ context.Context, r io.ReaderAt, size, maxText int64) (string, error) {
	zr, err := openPackage(r, size)
	if err != nil {
		return "", err
	}
	doc := findPart(zr, "Index/Document.iwa")
	if doc == nil {
		return "", errors.New("not a Keynote file: it has no Index/Document.iwa")
	}
	data, err := readIWA(doc)
	if err != nil {
		return "", err
	}
	// A show this reader can't follow only costs the slide order.
	order, _ := slideOrder(data)

	var files []*zip.File
	for _, f := range zr.File {
		if iwaSlideRe.MatchString(f.Name) {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	type slide struct {
		id          uint64
		text, notes []string
	}
	var slides []slide
	var collected int64 // text bytes held, so a huge deck can't outgrow the cap
	for _, f := range files {
		if collected >= maxText {
			break
		}
		s := slide{}
		collect := func(id, typ uint64, payload []byte, _ func(string)) error {
			switch typ {
			case iwSlide:
				if s.id == 0 {
					s.id = id
				}
			case iwText:
				kind, lines, err := storageLines(payload)
				if kind == iwNotes {
					s.notes = append(s.notes, lines...)
				} else {
					s.text = append(s.text, lines...)
				}
				return err
			case iwComment:
				return stringField(payload, 1, trimmed(func(c string) { s.text = append(s.text, c) }))
			}
			return nil
		}
		if err := readMessages(f, collect, nil); err != nil {
			continue // one damaged slide shouldn't cost the others
		}
		for _, l := range s.text {
			collected += int64(len(l))
		}
		for _, l := range s.notes {
			collected += int64(len(l))
		}
		slides = append(slides, s)
	}
	// Slides missing from the show (shouldn't happen) go last, in name order.
	pos := func(id uint64) int {
		if i, ok := order[id]; ok {
			return i
		}
		return len(order)
	}
	sort.SliceStable(slides, func(i, j int) bool { return pos(slides[i].id) < pos(slides[j].id) })

	out := &sink{max: maxText}
	emit := lineEmitter(out)
	for _, s := range slides {
		emitAll(emit, s.text)
		emitAll(emit, s.notes)
	}
	for _, f := range tableParts(zr) {
		if out.full() {
			break
		}
		_ = readMessages(f, numbersMessage, emit)
	}
	return out.text(), nil
}

// slideOrder maps each slide's object id to its position in the show. The
// show (KN.ShowArchive, field 3) lists slide nodes in presentation order,
// and each node (KN.SlideNodeArchive, field 2) refers to its slide.
func slideOrder(doc []byte) (map[uint64]int, error) {
	var nodes []uint64
	slideOf := map[uint64]uint64{}
	err := walkArchive(doc, func(id, typ uint64, payload []byte, _ func(string)) error {
		switch typ {
		case iwShow:
			return eachField(payload, func(num protowire.Number, _ uint64, v []byte) error {
				if num != 3 {
					return nil
				}
				return eachField(v, func(num protowire.Number, _ uint64, ref []byte) error {
					if num == 2 {
						nodes = append(nodes, refID(ref))
					}
					return nil
				})
			})
		case iwSlideNode:
			return eachField(payload, func(num protowire.Number, _ uint64, ref []byte) error {
				if num == 2 {
					slideOf[id] = refID(ref)
				}
				return nil
			})
		}
		return nil
	}, nil)
	if err != nil {
		return nil, err
	}
	order := make(map[uint64]int, len(nodes))
	for i, n := range nodes {
		order[slideOf[n]] = i
	}
	return order, nil
}

// refID returns the object id in a TSP.Reference message (field 1).
func refID(ref []byte) (id uint64) {
	_ = eachField(ref, func(num protowire.Number, x uint64, _ []byte) error {
		if num == 1 {
			id = x
		}
		return nil
	})
	return id
}
