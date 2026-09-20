package smbclient

import (
	"testing"

	"github.com/hirochachacha/go-smb2"
)

func TestTranslateChangesBasicActions(t *testing.T) {
	infos := []smb2.NotifyChangeInfo{
		{Action: smb2.ActionAdded, FileName: `sub\new.txt`},
		{Action: smb2.ActionModified, FileName: `sub\changed.txt`},
		{Action: smb2.ActionRemoved, FileName: `gone.txt`},
	}

	got := translateChanges(infos)
	want := []ChangeEvent{
		{Kind: ChangeUpserted, Path: "sub/new.txt"},
		{Kind: ChangeUpserted, Path: "sub/changed.txt"},
		{Kind: ChangeRemoved, Path: "gone.txt"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestTranslateChangesPairsRename(t *testing.T) {
	infos := []smb2.NotifyChangeInfo{
		{Action: smb2.ActionRenamedOldName, FileName: `sub\old.txt`},
		{Action: smb2.ActionRenamedNewName, FileName: `sub\new.txt`},
	}

	got := translateChanges(infos)
	want := []ChangeEvent{
		{Kind: ChangeRenamed, OldPath: "sub/old.txt", Path: "sub/new.txt"},
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTranslateChangesHandlesUnpairedRenameEntries(t *testing.T) {
	// An old-name with no following new-name in the same batch — treated
	// conservatively as a removal so the old path doesn't linger.
	oldOnly := translateChanges([]smb2.NotifyChangeInfo{
		{Action: smb2.ActionRenamedOldName, FileName: "old.txt"},
		{Action: smb2.ActionAdded, FileName: "unrelated.txt"},
	})
	if len(oldOnly) != 2 || oldOnly[0] != (ChangeEvent{Kind: ChangeRemoved, Path: "old.txt"}) {
		t.Fatalf("unpaired old-name: got %+v", oldOnly)
	}

	// A new-name with no preceding old-name — treated as an upsert so the
	// file isn't dropped from the index.
	newOnly := translateChanges([]smb2.NotifyChangeInfo{
		{Action: smb2.ActionRenamedNewName, FileName: "new.txt"},
	})
	want := ChangeEvent{Kind: ChangeUpserted, Path: "new.txt"}
	if len(newOnly) != 1 || newOnly[0] != want {
		t.Fatalf("unpaired new-name: got %+v, want %+v", newOnly, want)
	}
}
