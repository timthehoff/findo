module github.com/thoff/findo/backend

go 1.25.5

require (
	github.com/hirochachacha/go-smb2 v1.1.0
	modernc.org/sqlite v1.59.0
)

// go-smb2 doesn't implement the SMB2 CHANGE_NOTIFY request/response wire
// format (only the bare command opcode exists upstream) — vendored locally
// with that added so the crawler can watch for NAS changes instead of only
// polling full sweeps. See third_party/go-smb2/notify.go.
replace github.com/hirochachacha/go-smb2 => ./third_party/go-smb2

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/geoffgarside/ber v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.0.0-20200728195943-123391ffb6de // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
