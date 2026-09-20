package smbclient

import (
	"context"
	"errors"
	"time"

	"github.com/hirochachacha/go-smb2"
)

// ChangeKind categorizes a ChangeEvent for the caller.
type ChangeKind int

const (
	// ChangeUpserted covers both an add and a modify: the caller should
	// Stat Path and upsert the result into the index.
	ChangeUpserted ChangeKind = iota
	ChangeRemoved
	// ChangeRenamed pairs the old and new name from a single SMB rename
	// notification: OldPath should be deleted, Path upserted (after a
	// Stat, since a rename carries no size/mtime of its own).
	ChangeRenamed
)

// ChangeEvent is one file-level change reported by the NAS, translated
// from the raw SMB2 CHANGE_NOTIFY action stream.
type ChangeEvent struct {
	Kind    ChangeKind
	Path    string // share-relative, forward-slash separated
	OldPath string // set only when Kind == ChangeRenamed
}

// ErrNeedsResync is sent on Watch's error channel whenever the notify
// stream can't be trusted incrementally right now — the server reported
// it dropped changes (STATUS_NOTIFY_ENUM_DIR, a buffer overflow) or the
// watch session had to reconnect after a transport error. Either way,
// individual events may have been missed; the caller should run a full
// crawl to catch up. The watch itself keeps running and resumes sending
// events once it can.
var ErrNeedsResync = errors.New("change notify: missed changes, full resync needed")

// DefaultChangeFilter covers everything Findo's index cares about: file
// and directory name changes (add/remove/rename) and content/metadata
// changes (write, size, attributes).
const DefaultChangeFilter = smb2.NotifyChangeFileName |
	smb2.NotifyChangeDirName |
	smb2.NotifyChangeAttributes |
	smb2.NotifyChangeSize |
	smb2.NotifyChangeLastWrite

// Watch streams file changes on the NAS until ctx is cancelled, at which
// point both returned channels are closed. It opens its own dedicated SMB
// session rather than reusing the Client's — a CHANGE_NOTIFY request sits
// outstanding on its connection until an event arrives, so it can't share
// a session with request/response traffic (Walk, Open, Stat) that expects
// a timely reply.
//
// The watch survives session errors: on a lost connection or a
// STATUS_NOTIFY_ENUM_DIR overflow it keeps going (reconnecting with
// backoff for the former) and sends ErrNeedsResync so the caller can run a
// full crawl to fill whatever gap opened up, then resumes streaming
// events. Only ctx cancellation stops it for good.
func (c *Client) Watch(ctx context.Context, filter uint32) (<-chan ChangeEvent, <-chan error) {
	events := make(chan ChangeEvent)
	errs := make(chan error)

	go func() {
		defer close(events)
		defer close(errs)
		c.watchLoop(ctx, filter, events, errs)
	}()

	return events, errs
}

func (c *Client) watchLoop(ctx context.Context, filter uint32, events chan<- ChangeEvent, errs chan<- error) {
	const baseBackoff = time.Second
	const maxBackoff = time.Minute
	backoff := baseBackoff

	for ctx.Err() == nil {
		err := c.watchOnce(ctx, filter, events, errs)
		if ctx.Err() != nil {
			return
		}

		select {
		case errs <- errors.Join(ErrNeedsResync, err):
		case <-ctx.Done():
			return
		}

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// watchOnce owns one dedicated session for its whole lifetime, repeatedly
// re-issuing CHANGE_NOTIFY on the same open directory handle. It only
// returns (with a non-nil error) on a genuine transport failure — an
// overflow is handled internally by signalling ErrNeedsResync and
// continuing to watch with the same handle, since the handle itself is
// still perfectly usable after an overflow.
func (c *Client) watchOnce(ctx context.Context, filter uint32, events chan<- ChangeEvent, errs chan<- error) error {
	conn, sess, fs, err := dial(c.host, c.share, c.user, c.pass)
	if err != nil {
		return err
	}
	defer sess.Logoff()
	defer conn.Close()
	fs = fs.WithContext(ctx)

	dir, err := fs.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()

	for {
		infos, err := dir.NotifyChange(filter, true)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if smb2.IsNotifyEnumDir(err) {
				select {
				case errs <- ErrNeedsResync:
				case <-ctx.Done():
					return nil
				}
				continue
			}
			return err
		}

		for _, ev := range translateChanges(infos) {
			select {
			case events <- ev:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// translateChanges converts a batch of raw SMB2 notify entries into
// ChangeEvents, pairing consecutive rename old-name/new-name entries.
func translateChanges(infos []smb2.NotifyChangeInfo) []ChangeEvent {
	var out []ChangeEvent
	for i := 0; i < len(infos); i++ {
		info := infos[i]
		switch info.Action {
		case smb2.ActionAdded, smb2.ActionModified:
			out = append(out, ChangeEvent{Kind: ChangeUpserted, Path: toSlashPath(info.FileName)})
		case smb2.ActionRemoved:
			out = append(out, ChangeEvent{Kind: ChangeRemoved, Path: toSlashPath(info.FileName)})
		case smb2.ActionRenamedOldName:
			if i+1 < len(infos) && infos[i+1].Action == smb2.ActionRenamedNewName {
				out = append(out, ChangeEvent{
					Kind:    ChangeRenamed,
					OldPath: toSlashPath(info.FileName),
					Path:    toSlashPath(infos[i+1].FileName),
				})
				i++ // consume the paired new-name entry
			} else {
				// Unpaired old-name with no following new-name in this
				// batch — shouldn't happen per spec, but don't leave a
				// stale row under the old path: treat it as a removal.
				out = append(out, ChangeEvent{Kind: ChangeRemoved, Path: toSlashPath(info.FileName)})
			}
		case smb2.ActionRenamedNewName:
			// Only reached if the old-name entry was missing/unpaired.
			// Don't drop the file: treat it as an upsert of the new path.
			out = append(out, ChangeEvent{Kind: ChangeUpserted, Path: toSlashPath(info.FileName)})
		}
	}
	return out
}
