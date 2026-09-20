// Command findo-server crawls one or more NAS SMB shares into a local
// SQLite index and serves it over HTTP: volume configuration, directory
// listing, search, Range-aware file streaming, health/stats, manual crawl
// triggers, and a small dashboard.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/thoff/findo/backend/internal/config"
	"github.com/thoff/findo/backend/internal/httpapi"
	"github.com/thoff/findo/backend/internal/index"
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

	srv := httpapi.NewServer(idx, cfg.MasterKey)
	ctx := context.Background()

	volumes, err := idx.ListVolumes()
	if err != nil {
		log.Fatalf("list volumes: %v", err)
	}
	if len(volumes) == 0 {
		log.Println("no volumes configured yet — add one via the dashboard or POST /volumes")
	}
	for _, vol := range volumes {
		if !vol.Enabled {
			continue
		}
		go startVolumeWithRetry(ctx, srv, vol, 10, 3*time.Second)
	}

	log.Printf("findo-server listening on %s", cfg.HTTPAddr)
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}

// startVolumeWithRetry retries a volume's initial connection so the service
// can come up cleanly even if it starts before the NAS is reachable (e.g.
// both started together by docker compose), without blocking the HTTP
// server — or any other volume's startup — while it does.
func startVolumeWithRetry(ctx context.Context, srv *httpapi.Server, vol index.Volume, attempts int, delay time.Duration) {
	var err error
	for i := 1; i <= attempts; i++ {
		if err = srv.StartVolume(ctx, vol); err == nil {
			return
		}
		log.Printf("volume %d (%s): connect attempt %d/%d failed: %v", vol.ID, vol.Name, i, attempts, err)
		if i < attempts {
			time.Sleep(delay)
		}
	}
	log.Printf("volume %d (%s): giving up after %d attempts: %v", vol.ID, vol.Name, attempts, err)
}
