// Command findo-server crawls a NAS SMB share into a local SQLite index and
// serves it over HTTP: directory listing, search, Range-aware file
// streaming, health/stats, and a small dashboard.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/thoff/findo/backend/internal/config"
	"github.com/thoff/findo/backend/internal/httpapi"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

func main() {
	cfg, err := config.Load(".env")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	idx, err := index.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open index db: %v", err)
	}
	defer idx.Close()

	smb := smbclient.New(cfg.SMBHost, cfg.SMBShare, cfg.SMBUser, cfg.SMBPass)
	if err := connectWithRetry(smb, 10, 3*time.Second); err != nil {
		log.Fatalf("connect to SMB share: %v", err)
	}
	defer smb.Close()

	srv := httpapi.NewServer(smb, idx)

	go func() {
		log.Println("running initial crawl...")
		if err := srv.Crawl(); err != nil {
			log.Printf("initial crawl failed: %v", err)
		}
	}()

	log.Printf("findo-server listening on %s", cfg.HTTPAddr)
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Routes()); err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
}

// connectWithRetry retries the initial SMB connection so the service can
// come up cleanly even if it starts before the NAS/Samba is reachable
// (e.g. both started together by docker compose).
func connectWithRetry(smb *smbclient.Client, attempts int, delay time.Duration) error {
	var err error
	for i := 1; i <= attempts; i++ {
		if err = smb.Connect(); err == nil {
			return nil
		}
		log.Printf("SMB connect attempt %d/%d failed: %v", i, attempts, err)
		if i < attempts {
			time.Sleep(delay)
		}
	}
	return err
}
