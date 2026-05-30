package server

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"noci/pkg/log"
	"noci/pkg/oci"
	"sync"
	"time"
)

type Server struct {
	addr           string
	upstream       string
	ttl            int
	client         *oci.Client
	upstreamProxy  *httputil.ReverseProxy
	tagsMu         sync.RWMutex
	tags           map[string]struct{}
	negCache       sync.Map
	lastTagsFetch  time.Time
	fetchMu        sync.Mutex
}

func NewServer(registry, repo, token, addr, upstream string, ttl int) *Server {
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
		ttl:           ttl,
		client:        oci.NewClient(registry, repo, token),
		upstreamProxy: proxy,
	}
}

func (s *Server) Start(ctx context.Context) error {
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.RefreshTags(warmCtx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/nix-cache-info", s.HandleNixCacheInfo)
	mux.HandleFunc("/", s.HandleRoutes)

	srv := &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		log.Info("Shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Success("Proxy running on http://%s", s.addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) RefreshTags(ctx context.Context) error {
	tags, err := s.client.ListTags(ctx)
	if err != nil {
		return err
	}

	newTags := make(map[string]struct{})
	nixCount := 0
	for _, tag := range tags {
		newTags[tag] = struct{}{}
		if len(tag) == 32 {
			nixCount++
		}
	}

	s.tagsMu.Lock()
	s.tags = newTags
	s.lastTagsFetch = time.Now()
	s.tagsMu.Unlock()

	log.Success("Cache warmed. Packages: %d (Total tags: %d)", nixCount, len(tags))
	return nil
}

func (s *Server) loadTagsLazy(ctx context.Context) {
	s.tagsMu.RLock()
	needRefresh := s.tags == nil || time.Since(s.lastTagsFetch) > time.Duration(s.ttl)*time.Second
	s.tagsMu.RUnlock()

	if !needRefresh {
		return
	}

	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	s.tagsMu.RLock()
	needRefresh = s.tags == nil || time.Since(s.lastTagsFetch) > time.Duration(s.ttl)*time.Second
	s.tagsMu.RUnlock()

	if needRefresh {
		_ = s.RefreshTags(ctx)
	}
}
