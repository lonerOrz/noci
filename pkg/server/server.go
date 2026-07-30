package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"noci/pkg/domain/ports"
	"noci/pkg/log"
	"noci/pkg/oci"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// metrics tracks per-route request counts for /metrics endpoint.
type metrics struct {
	mu        sync.Mutex
	counts    map[string]int
	startTime time.Time
}

func (m *metrics) inc(method, path, status, source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := method + " " + path + " " + status + " " + source
	m.counts[key]++
}

type Server struct {
	addr           string
	upstream       string
	store          ports.CacheStore
	portFile       string
	upstreamProxy  *httputil.ReverseProxy
	upstreamExtras []*httputil.ReverseProxy
	indexMu        sync.RWMutex
	index          *oci.CacheIndex
	negCache       sync.Map
	lastFetch      time.Time
	lastDigest     string
	canDelete      bool
	authKey        string
	metrics        metrics
	limiter        *rateLimiter
	cb             *CircuitBreaker
}

func NewServer(registry, repo, token, addr, authKey string, rateLimit float64, upstreams []string) *Server {
	if registry == "" || repo == "" || addr == "" {
		panic("server: registry, repo, and addr must not be empty")
	}

	client := oci.NewClient(registry, repo, token)

	var primaryUpstream string
	var proxy *httputil.ReverseProxy
	var extras []*httputil.ReverseProxy

	for i, u := range upstreams {
		targetURL, err := url.Parse(u)
		if err != nil {
			log.Warning("Upstream %q parse failed: %v", u, err)
			continue
		}
		p := httputil.NewSingleHostReverseProxy(targetURL)
		origDir := p.Director
		p.Director = func(req *http.Request) {
			origDir(req)
			req.Host = targetURL.Host
		}
		if i == 0 {
			primaryUpstream = u
			proxy = p
		} else {
			extras = append(extras, p)
		}
	}

	var limiter *rateLimiter
	if rateLimit > 0 {
		burst := int(rateLimit * 2)
		if burst < 1 {
			burst = 1
		}
		limiter = newRateLimiter(rateLimit, burst)
	}

	return &Server{
		addr:           addr,
		upstream:       primaryUpstream,
		store:          client,
		upstreamProxy:  proxy,
		upstreamExtras: extras,
		authKey:        authKey,
		metrics:        metrics{counts: make(map[string]int), startTime: time.Now()},
		limiter:        limiter,
		cb:             NewCircuitBreaker(5, 30*time.Second),
	}
}

func (s *Server) SetPortFile(path string) {
	s.portFile = path
}

// withMiddleware wraps a handler with auth, rate limiting, and logging.
func (s *Server) withMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAuth(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="noci"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if s.limiter != nil {
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			if !s.limiter.allow(ip) {
				http.Error(w, "Rate Limit Exceeded", http.StatusTooManyRequests)
				return
			}
		}

		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusNotFound}
		start := time.Now()
		defer func() {
			silentPaths := map[string]bool{
				"/api/digest":  true,
				"/app.js":      true,
				"/style.css":   true,
				"/favicon.svg": true,
				"/healthz":     true,
				"/metrics":     true,
			}
			status := strconv.Itoa(lrw.statusCode)
			// Use route pattern (e.g. "/{hash}.narinfo") instead of raw path
			// to prevent high-cardinality label explosion from unique hashes.
			pattern := r.Pattern
			if pattern == "" {
				pattern = r.URL.Path
			}
			// Strip leading method prefix from pattern ("GET /foo" → "/foo")
			if idx := strings.IndexByte(pattern, ' '); idx != -1 {
				pattern = pattern[idx+1:]
			}
			s.metrics.inc(r.Method, pattern, status, lrw.source)
			if silentPaths[r.URL.Path] {
				return
			}
			source := ""
			if lrw.source != "" {
				source = fmt.Sprintf(" (%s)", lrw.source)
			}
			log.Info("[noci-proxy] %s %s - %d%s (%s)", r.Method, r.URL.Path, lrw.statusCode, source, time.Since(start))
		}()

		next(lrw, r)
	}
}

func (s *Server) setupMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Static assets
	mux.HandleFunc("GET /{$}", s.withMiddleware(s.handleDashboard))
	mux.HandleFunc("GET /app.js", s.withMiddleware(s.handleAppJS))
	mux.HandleFunc("GET /style.css", s.withMiddleware(s.handleStyleCSS))
	mux.HandleFunc("GET /favicon.svg", s.withMiddleware(s.handleFavicon))

	// Nix Cache protocol
	mux.HandleFunc("GET /nix-cache-info", s.withMiddleware(s.HandleNixCacheInfo))
	mux.HandleFunc("GET /public-key", s.withMiddleware(s.handlePublicKey))
	mux.HandleFunc("GET /nar/{filename...}", s.withMiddleware(s.handleNarRoute))
	// Catch-all for {hash}.narinfo — Go 1.26 ServeMux rejects /{hash}.narinfo
	// because the wildcard doesn't end the segment. Static routes above take priority.
	mux.HandleFunc("GET /{path}", s.withMiddleware(s.handleCatchAll))

	// Management API
	mux.HandleFunc("GET /healthz", s.withMiddleware(s.handleHealthz))
	mux.HandleFunc("GET /metrics", s.withMiddleware(s.handleMetrics))
	mux.HandleFunc("GET /api/digest", s.withMiddleware(s.handleAPIDigest))
	mux.HandleFunc("GET /api/index", s.withMiddleware(s.handleAPIIndex))
	mux.HandleFunc("DELETE /api/delete/{hash}", s.withMiddleware(s.handleAPIDeleteRoute))

	return mux
}

func (s *Server) Start(ctx context.Context) error {
	warmCtx, cancel := context.WithTimeout(ctx, oci.DefaultHTTPTimeout)
	if exists, digest := s.store.ManifestExists(warmCtx, "noci-index"); exists {
		s.lastDigest = digest
	}
	if err := s.RefreshIndex(warmCtx); err != nil {
		log.Warning("Initial cache warm failed: %v", err)
	} else {
		log.Success("Cache warmed. Package Entries: %d, Initial Digest: %s", s.indexCount(), shortDigest(s.lastDigest))
	}
	cancel()

	s.StartPreflightProbe()
	go s.startActiveSyncLoop(ctx, oci.DefaultActiveSyncPeriod)
	go s.startCleanupLoop(ctx)

	srv := &http.Server{
		Handler: s.setupMux(),
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

	boundPort := listener.Addr().(*net.TCPAddr).Port
	if s.portFile != "" {
		_ = os.WriteFile(s.portFile, []byte(strconv.Itoa(boundPort)), 0644)
	}
	log.Success("Proxy running on http://%s", listener.Addr().String())
	if err := srv.Serve(listener); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) RefreshIndex(ctx context.Context) error {
	idx, err := s.store.FetchIndex(ctx)
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

func (s *Server) StartPreflightProbe() {
	s.canDelete = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.canDelete = s.store.CanWrite(ctx)
	}()
}

func (s *Server) startActiveSyncLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			exists, remoteDigest := s.store.ManifestExists(ctx, "noci-index")
			if !exists {
				continue
			}

			s.indexMu.RLock()
			currentDigest := s.lastDigest
			s.indexMu.RUnlock()

			if remoteDigest != "" && remoteDigest != currentDigest {
				log.Info("Detected remote OCI index update (%s -> %s). Synchronizing...", shortDigest(currentDigest), shortDigest(remoteDigest))

				syncCtx, cancel := context.WithTimeout(ctx, oci.DefaultHTTPTimeout)
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

func (s *Server) startCleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.limiter != nil {
				s.limiter.cleanup()
			}
			s.negCache.Range(func(key, value interface{}) bool {
				if t, ok := value.(time.Time); ok && time.Since(t) > 30*time.Second {
					s.negCache.Delete(key)
				}
				return true
			})
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
