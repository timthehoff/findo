package smb2

import (
	"testing"

	"github.com/hirochachacha/go-smb2/internal/utf16le"
)

func TestChangeNotifyRequestEncode(t *testing.T) {
	req := &ChangeNotifyRequest{
		Flags:              SMB2_WATCH_TREE,
		OutputBufferLength: 65536,
		FileId:             &FileId{Persistent: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}, Volatile: [8]byte{9, 10, 11, 12, 13, 14, 15, 16}},
		CompletionFilter:   FILE_NOTIFY_CHANGE_FILE_NAME | FILE_NOTIFY_CHANGE_DIR_NAME | FILE_NOTIFY_CHANGE_LAST_WRITE,
	}

	if got, want := req.Size(), 64+32; got != want {
		t.Fatalf("Size() = %d, want %d", got, want)
	}

	pkt := make([]byte, req.Size())
	req.Encode(pkt)

	body := pkt[64:]
	if got := le.Uint16(body[:2]); got != 32 {
		t.Fatalf("StructureSize = %d, want 32", got)
	}
	if got := le.Uint16(body[2:4]); got != SMB2_WATCH_TREE {
		t.Fatalf("Flags = %#x, want %#x", got, SMB2_WATCH_TREE)
	}
	if got := le.Uint32(body[4:8]); got != 65536 {
		t.Fatalf("OutputBufferLength = %d, want 65536", got)
	}
	if got := *FileIdDecoder(body[8:24]).Decode(); got != *req.FileId {
		t.Fatalf("FileId = %+v, want %+v", got, *req.FileId)
	}
	wantFilter := FILE_NOTIFY_CHANGE_FILE_NAME | FILE_NOTIFY_CHANGE_DIR_NAME | FILE_NOTIFY_CHANGE_LAST_WRITE
	if got := le.Uint32(body[24:28]); got != wantFilter {
		t.Fatalf("CompletionFilter = %#x, want %#x", got, wantFilter)
	}
}

func TestChangeNotifyResponseDecode(t *testing.T) {
	// Build a synthetic response: a 64-byte header (contents don't matter
	// to the decoder), an 8-byte CHANGE_NOTIFY response header, then two
	// chained FILE_NOTIFY_INFORMATION entries simulating a rename
	// (old-name, then new-name).
	oldName := "old.txt"
	newName := "new.txt"

	entry := func(action uint32, name string, last bool) []byte {
		nameBytes := utf16le.EncodeStringToBytes(name)
		size := 12 + len(nameBytes)
		// FILE_NOTIFY_INFORMATION entries are supposed to be 4-byte
		// aligned via NextEntryOffset; pad for realism.
		padded := (size + 3) &^ 3
		buf := make([]byte, padded)
		if !last {
			le.PutUint32(buf[:4], uint32(padded))
		}
		le.PutUint32(buf[4:8], action)
		le.PutUint32(buf[8:12], uint32(len(nameBytes)))
		copy(buf[12:], nameBytes)
		return buf
	}

	e1 := entry(FILE_ACTION_RENAMED_OLD_NAME, oldName, false)
	e2 := entry(FILE_ACTION_RENAMED_NEW_NAME, newName, true)
	buffer := append(append([]byte{}, e1...), e2...)

	respHeader := make([]byte, 8)
	le.PutUint16(respHeader[:2], 9) // StructureSize
	le.PutUint16(respHeader[2:4], uint16(64+8))
	le.PutUint32(respHeader[4:8], uint32(len(buffer)))

	// ChangeNotifyResponseDecoder, like the caller's res from sendRecv,
	// operates on the body only — the 64-byte SMB2 header is already
	// stripped by accept() before this decoder ever sees the bytes.
	body := append(append([]byte{}, respHeader...), buffer...)

	r := ChangeNotifyResponseDecoder(body)
	if r.IsInvalid() {
		t.Fatalf("expected valid response")
	}

	output := r.OutputBuffer()
	if len(output) != len(buffer) {
		t.Fatalf("OutputBuffer length = %d, want %d", len(output), len(buffer))
	}

	var got []NotifyEntry
	for {
		info := FileNotifyInformationDecoder(output)
		if info.IsInvalid() {
			t.Fatalf("unexpected invalid FILE_NOTIFY_INFORMATION entry")
		}
		got = append(got, NotifyEntry{Action: info.Action(), Name: info.FileName()})
		next := info.NextEntryOffset()
		if next == 0 {
			break
		}
		output = output[next:]
	}

	want := []NotifyEntry{
		{Action: FILE_ACTION_RENAMED_OLD_NAME, Name: oldName},
		{Action: FILE_ACTION_RENAMED_NEW_NAME, Name: newName},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// NotifyEntry is a tiny local mirror of the info this test cares about,
// so the assertions above stay readable.
type NotifyEntry struct {
	Action uint32
	Name   string
}
