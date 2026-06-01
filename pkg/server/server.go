package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"noci/pkg/log"
	"noci/pkg/oci"
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
	fetchMu       sync.Mutex
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
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.RefreshIndex(warmCtx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/nix-cache-info", s.HandleNixCacheInfo)
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

	log.Success("Cache warmed. Package Entries: %d", len(idx.Entries))
	return nil
}

func (s *Server) loadIndexLazy(ctx context.Context) {
	s.indexMu.RLock()
	needRefresh := s.index == nil || time.Since(s.lastFetch) > 300*time.Second
	s.indexMu.RUnlock()

	if !needRefresh {
		return
	}

	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	s.indexMu.RLock()
	needRefresh = s.index == nil || time.Since(s.lastFetch) > 300*time.Second
	s.indexMu.RUnlock()

	if needRefresh {
		if err := s.RefreshIndex(ctx); err != nil {
			log.Warning("Failed to lazy load index: %v", err)
		}
	}
}
