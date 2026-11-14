package smbclient

import (
	"errors"

	"github.com/hirochachacha/go-smb2"
)

// NTSTATUS values a server returns while a session is being set up or a share
// mounted.
const (
	statusAccessDenied      = 0xC0000022
	statusWrongPassword     = 0xC000006A
	statusLogonFailure      = 0xC000006D
	statusAccountRestricted = 0xC000006E
	statusPasswordExpired   = 0xC0000071
	statusAccountDisabled   = 0xC0000072
	statusBadNetworkName    = 0xC00000CC
	statusAccountLockedOut  = 0xC0000234
)

// Describe turns a failed connection attempt into a message that is safe to show
// the person who asked for it. Findo will try to connect to whatever address a
// signed-in user types, so the raw error (connection refused, i/o timeout,
// unexpected EOF) would let them map the network behind the server; instead
// everything short of an SMB server answering collapses into one message.
// reached says whether an SMB server answered at all; callers also hide the
// difference in how long it took when it didn't.
func Describe(err error) (msg string, reached bool) {
	var re *smb2.ResponseError
	if !errors.As(err, &re) {
		return "Couldn't connect to an SMB server at that address.", false
	}
	switch re.Code {
	case statusLogonFailure, statusWrongPassword, statusAccountRestricted,
		statusPasswordExpired, statusAccountDisabled, statusAccountLockedOut:
		return "The server refused that username or password.", true
	case statusBadNetworkName:
		return "The server has no share with that name.", true
	case statusAccessDenied:
		return "That account isn't allowed to open that share.", true
	}
	return "The server refused the connection.", true
}
