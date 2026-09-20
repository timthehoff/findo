package smb2

import (
	"errors"

	. "github.com/hirochachacha/go-smb2/internal/erref"
	. "github.com/hirochachacha/go-smb2/internal/smb2"
)

// Completion filter bits for NotifyChange, re-exported so callers outside
// this module don't need to import the internal wire-format package.
const (
	NotifyChangeFileName   = FILE_NOTIFY_CHANGE_FILE_NAME
	NotifyChangeDirName    = FILE_NOTIFY_CHANGE_DIR_NAME
	NotifyChangeAttributes = FILE_NOTIFY_CHANGE_ATTRIBUTES
	NotifyChangeSize       = FILE_NOTIFY_CHANGE_SIZE
	NotifyChangeLastWrite  = FILE_NOTIFY_CHANGE_LAST_WRITE
)

// File change actions reported by NotifyChange (MS-FSCC 2.7.1). A rename
// is reported as two consecutive entries, ActionRenamedOldName followed by
// ActionRenamedNewName.
const (
	ActionAdded          = FILE_ACTION_ADDED
	ActionRemoved        = FILE_ACTION_REMOVED
	ActionModified       = FILE_ACTION_MODIFIED
	ActionRenamedOldName = FILE_ACTION_RENAMED_OLD_NAME
	ActionRenamedNewName = FILE_ACTION_RENAMED_NEW_NAME
)

// NotifyChangeInfo is one reported change from NotifyChange.
type NotifyChangeInfo struct {
	Action   uint32
	FileName string
}

// NotifyChange issues an SMB2 CHANGE_NOTIFY request against this open
// directory handle and blocks until the server reports at least one change
// (or an error) — there's no polling involved on the client side. Set
// watchTree to also receive changes from subdirectories, not just direct
// children.
//
// This request stays outstanding on the connection until it resolves, so
// callers should give the directory its own File handle (and typically its
// own Session) rather than sharing one used for other traffic.
func (f *File) NotifyChange(completionFilter uint32, watchTree bool) ([]NotifyChangeInfo, error) {
	var flags uint16
	if watchTree {
		flags = SMB2_WATCH_TREE
	}

	req := &ChangeNotifyRequest{
		Flags:              flags,
		OutputBufferLength: uint32(f.maxTransactSize()),
		FileId:             f.fd,
		CompletionFilter:   completionFilter,
	}

	payloadSize := int(req.OutputBufferLength)
	if f.maxTransactSize() < payloadSize {
		return nil, &InternalError{"payload size exceeds max transact size"}
	}

	var err error
	req.CreditCharge, _, err = f.fs.loanCredit(payloadSize)
	defer func() {
		if err != nil {
			f.fs.chargeCredit(req.CreditCharge)
		}
	}()
	if err != nil {
		return nil, err
	}

	res, err := f.sendRecv(SMB2_CHANGE_NOTIFY, req)
	if err != nil {
		return nil, err
	}

	r := ChangeNotifyResponseDecoder(res)
	if r.IsInvalid() {
		return nil, &InvalidResponseError{"broken change notify response format"}
	}

	output := r.OutputBuffer()
	if len(output) == 0 {
		return nil, nil
	}

	var infos []NotifyChangeInfo
	for {
		info := FileNotifyInformationDecoder(output)
		if info.IsInvalid() {
			return nil, &InvalidResponseError{"broken change notify response format"}
		}

		infos = append(infos, NotifyChangeInfo{
			Action:   info.Action(),
			FileName: info.FileName(),
		})

		next := info.NextEntryOffset()
		if next == 0 {
			return infos, nil
		}
		output = output[next:]
	}
}

// IsNotifyEnumDir reports whether err is STATUS_NOTIFY_ENUM_DIR: the server
// had too many changes to report individually and the caller should
// re-enumerate the directory instead of trying to read specific entries.
// This is a normal, expected outcome of watching a busy tree, not a
// transport failure.
func IsNotifyEnumDir(err error) bool {
	var re *ResponseError
	if errors.As(err, &re) {
		return NtStatus(re.Code) == STATUS_NOTIFY_ENUM_DIR
	}
	return false
}
