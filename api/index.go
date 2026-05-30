package handler

import (
	"net/http"
	"noci/pkg/server"
	"os"
	"strconv"
)

var srv *server.Server

func init() {
	registry := os.Getenv("NOCI_REGISTRY")
	if registry == "" {
		registry = "ghcr.io"
	}

	upstream := os.Getenv("NOCI_UPSTREAM")
	if upstream == "" {
		upstream = "https://cache.nixos.org"
	}

	ttl, _ := strconv.Atoi(os.Getenv("NOCI_TTL"))
	if ttl == 0 {
		ttl = 300
	}

	srv = server.NewServer(
		registry,
		os.Getenv("NOCI_REPO"),
		os.Getenv("NOCI_TOKEN"),
		"",
		upstream,
		ttl,
	)
}

func Handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/nix-cache-info" {
		srv.HandleNixCacheInfo(w, r)
		return
	}
	srv.HandleRoutes(w, r)
}
