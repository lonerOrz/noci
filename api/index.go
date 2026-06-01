package handler

import (
	"context"
	"net/http"
	"noci/pkg/log"
	"noci/pkg/server"
	"os"
	"sync"
	"time"
)

var (
	srv       *server.Server
	initMu    sync.Mutex
	lastError error
)

func Handler(w http.ResponseWriter, r *http.Request) {
	if srv == nil {
		initMu.Lock()
		if srv == nil {
			registry := os.Getenv("NOCI_REGISTRY")
			if registry == "" {
				registry = "ghcr.io"
			}
			repo := os.Getenv("NOCI_REPO")
			token := os.Getenv("NOCI_TOKEN")

			upstream := os.Getenv("NOCI_UPSTREAM")
			if upstream == "" {
				upstream = "https://cache.nixos.org"
			}

			if repo == "" {
				log.Warning("Vercel Deployment Error: NOCI_REPO environment variable is missing")
				http.Error(w, "Gateway Configuration Error: NOCI_REPO is missing", http.StatusBadGateway)
				initMu.Unlock()
				return
			}

			srv = server.NewServer(registry, repo, token, "127.0.0.1:37515", upstream)

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			lastError = srv.RefreshIndex(ctx)
			cancel()
		}
		initMu.Unlock()
	}

	if lastError != nil {
		log.Warning("Lazy warm-up failed, retrying on next request: %v", lastError)

		initMu.Lock()
		srv = nil
		initMu.Unlock()

		http.Error(w, "Registry Connection Timeout, please retry", http.StatusBadGateway)
		return
	}

	path := r.URL.Path
	if path == "/nix-cache-info" {
		srv.HandleNixCacheInfo(w, r)
		return
	}

	srv.HandleRoutes(w, r)
}
