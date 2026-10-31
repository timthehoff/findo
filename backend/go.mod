module github.com/thoff/findo/backend

go 1.26.9

require (
	github.com/golang/snappy v1.0.0
	github.com/hirochachacha/go-smb2 v1.1.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/klippa-app/go-pdfium v1.21.4
	github.com/tetratelabs/wazero v1.12.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/geoffgarside/ber v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jolestar/go-commons-pool/v2 v2.1.2 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/hirochachacha/go-smb2 => github.com/timthehoff/go-smb2 v0.0.0-20260920194049-d65dc3325691
