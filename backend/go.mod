module github.com/thoff/findo/backend

go 1.25.5

require (
	github.com/hirochachacha/go-smb2 v1.1.0
	github.com/jackc/pgx/v5 v5.11.0
)

require (
	github.com/geoffgarside/ber v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.0.0-20200728195943-123391ffb6de // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace github.com/hirochachacha/go-smb2 => github.com/timthehoff/go-smb2 v0.0.0-20260920194049-d65dc3325691
