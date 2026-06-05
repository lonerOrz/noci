package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"noci/pkg/log"
	"noci/pkg/oci"
	"strings"
	"sync"
	"time"
)

type Server struct {
	addr          string
	upstream      string
	client        *oci.Client
	upstreamProxy *httputil.ReverseProxy
	indexMu       sync.RWMutex
	index         *oci.CacheIndex
	negCache      sync.Map
	lastFetch     time.Time
	lastDigest    string
	canDelete     bool
}

func NewServer(registry, repo, token, addr, upstream string) *Server {
	if registry == "" || repo == "" || addr == "" {
		panic("server: registry, repo, and addr must not be empty")
	}
	targetURL, err := url.Parse(upstream)
	var proxy *httputil.ReverseProxy
	if err == nil {
		proxy = httputil.NewSingleHostReverseProxy(targetURL)
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Host = targetURL.Host
		}
	} else {
		log.Warning("Upstream proxy init failed: %v", err)
	}

	return &Server{
		addr:          addr,
		upstream:      upstream,
		client:        oci.NewClient(registry, repo, token),
		upstreamProxy: proxy,
	}
}

func (s *Server) Start(ctx context.Context) error {
	warmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if exists, digest := s.client.ManifestExists(warmCtx, "noci-index"); exists {
		s.lastDigest = digest
	}
	if err := s.RefreshIndex(warmCtx); err != nil {
		log.Warning("Initial cache warm failed: %v", err)
	} else {
		log.Success("Cache warmed. Package Entries: %d, Initial Digest: %s", s.indexCount(), shortDigest(s.lastDigest))
	}
	cancel()

	s.StartPreflightProbe()
	go s.startActiveSyncLoop(ctx, 5*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.HandleRoutes)

	srv := &http.Server{
		Handler: mux,
	}

	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		log.Info("Shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Success("Proxy running on http://%s", listener.Addr().String())
	if err := srv.Serve(listener); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) RefreshIndex(ctx context.Context) error {
	idx, err := s.client.FetchIndex(ctx)
	if err != nil {
		return err
	}

	s.indexMu.Lock()
	s.index = idx
	s.lastFetch = time.Now()
	s.indexMu.Unlock()

	return nil
}

func (s *Server) indexCount() int {
	s.indexMu.RLock()
	defer s.indexMu.RUnlock()
	if s.index == nil {
		return 0
	}
	return len(s.index.Entries)
}

func (s *Server) startActiveSyncLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			exists, remoteDigest := s.client.ManifestExists(ctx, "noci-index")
			if !exists {
				continue
			}

			s.indexMu.RLock()
			currentDigest := s.lastDigest
			s.indexMu.RUnlock()

			if remoteDigest != "" && remoteDigest != currentDigest {
				log.Info("Detected remote OCI index update (%s -> %s). Synchronizing...", shortDigest(currentDigest), shortDigest(remoteDigest))

				syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := s.RefreshIndex(syncCtx); err != nil {
					log.Warning("Background sync index failed: %v", err)
				} else {
					s.indexMu.Lock()
					s.lastDigest = remoteDigest
					s.indexMu.Unlock()
					log.Success("OCI index auto-synced successfully. Entries: %d", s.indexCount())
				}
				cancel()
			}
		}
	}
}

func shortDigest(digest string) string {
	parts := strings.Split(digest, ":")
	if len(parts) == 2 && len(parts[1]) > 8 {
		return parts[0] + ":" + parts[1][:8]
	}
	if len(digest) > 8 {
		return digest[:8]
	}
	return digest
}
