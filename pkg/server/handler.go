package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"noci/dist"
	"noci/pkg/log"
	"noci/pkg/oci"
	"strconv"
	"strings"
	"sync"
	"time"
)

type tokenBucket struct {
	tokens   float64
	lastTime time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64
	burst   int
}

func newRateLimiter(rate float64, burst int) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    rate,
		burst:   burst,
	}
}

func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for ip, b := range rl.buckets {
		if now.Sub(b.lastTime) > 5*time.Minute {
			delete(rl.buckets, ip)
		}
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[ip]
	if !ok {
		b = &tokenBucket{tokens: float64(rl.burst), lastTime: now}
		rl.buckets[ip] = b
	}

	elapsed := now.Sub(b.lastTime).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastTime = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	source      string
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	if !lrw.wroteHeader {
		lrw.statusCode = code
		lrw.wroteHeader = true
	}
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
	if !lrw.wroteHeader {
		lrw.WriteHeader(http.StatusOK)
	}
	return lrw.ResponseWriter.Write(b)
}

func (lrw *loggingResponseWriter) Flush() {
	if f, ok := lrw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (lrw *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return lrw.ResponseWriter
}

func setSource(w http.ResponseWriter, source string) {
	if lrw, ok := w.(*loggingResponseWriter); ok {
		lrw.source = source
	}
}

var bufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 128*1024)
	},
}

func (s *Server) checkAuth(r *http.Request) bool {
	if s.authKey == "" {
		return true
	}
	if key := r.Header.Get("X-Noci-Key"); key != "" {
		return key == s.authKey
	}
	if _, pass, ok := r.BasicAuth(); ok {
		return pass == s.authKey
	}
	return false
}

// --- Static assets ---

func (s *Server) HandleNixCacheInfo(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "text/x-nix-cache-info")
	_, _ = w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"))
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(dist.IndexHTML))
}

func (s *Server) handleAppJS(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(dist.AppJS))
}

func (s *Server) handleStyleCSS(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(dist.StyleCSS))
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(dist.FaviconSVG)
}

// --- Public key ---

func (s *Server) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	pubKey, err := s.adminSvc.FetchPublicKey(r.Context())
	if err != nil {
		log.Warning("Failed to fetch public-key: %v", err)
		http.Error(w, "Public key not found", http.StatusNotFound)
		return
	}
	setSource(w, "cache")
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(pubKey + "\n"))
}

// --- Narinfo (cache lookup + URL rewrite) ---

func (s *Server) handleNarInfoRoute(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")

	content, found := s.cacheSvc.GetNarInfo(hash)
	if !found {
		setSource(w, "upstream")
		s.proxyToUpstream(w, r, hash+".narinfo")
		return
	}

	setSource(w, "cache")
	w.Header().Set("Content-Type", "text/x-nix-narinfo")
	_, _ = w.Write([]byte(content))
}

// --- NAR blob (digest resolution + streaming) ---

func (s *Server) handleNarRoute(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")

	digest, found := s.cacheSvc.ResolveNarDigest(filename)
	if !found {
		setSource(w, "upstream")
		s.proxyToUpstream(w, r, "nar/"+filename)
		return
	}

	setSource(w, "cache")
	s.streamBlob(w, r, digest)
}

func (s *Server) streamBlob(w http.ResponseWriter, r *http.Request, digest string) {
	ctx, cancel := context.WithTimeout(r.Context(), oci.DefaultStreamTimeout)
	defer cancel()

	resp, err := s.rawClient.RawRequest(ctx, "GET", "/blobs/"+digest, nil, "")
	if err != nil {
		http.Error(w, "Failed to stream archive", http.StatusNotFound)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		w.Header().Set("Location", resp.Header.Get("Location"))
		w.WriteHeader(resp.StatusCode)
		return
	}

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Failed to stream archive", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/x-nix-nar")
	if resp.ContentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", resp.ContentLength))
	}

	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)

	_, _ = io.CopyBuffer(w, resp.Body, buf)
}

// --- Upstream proxy fallback ---

func (s *Server) proxyToUpstream(w http.ResponseWriter, r *http.Request, path string) {
	if !s.cb.Allow() {
		http.Error(w, "Upstream temporarily unavailable (circuit breaker open)", http.StatusServiceUnavailable)
		return
	}

	// Build ordered list of all upstream proxies (primary first, then extras).
	proxies := make([]*httputil.ReverseProxy, 0, 1+len(s.upstreamExtras))
	if s.upstreamProxy != nil {
		proxies = append(proxies, s.upstreamProxy)
	}
	proxies = append(proxies, s.upstreamExtras...)

	for i, proxy := range proxies {
		// Use ResponseRecorder to intercept status code before committing to client.
		// 404 responses are tiny (bytes), so buffering is negligible.
		// Non-404 responses are copied to the real writer; for large NAR blobs this
		// adds a small memory cost but keeps the fallback logic simple and correct.
		rec := httptest.NewRecorder()
		reqClone := r.Clone(r.Context())
		reqClone.URL.Path = "/" + path

		proxy.ServeHTTP(rec, reqClone)

		if rec.Code != http.StatusNotFound {
			s.cb.RecordSuccess()
			// Replay buffered response to the real client.
			for k, vv := range rec.Result().Header {
				if isHopByHopHeader(k) {
					continue
				}
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}

		if i < len(proxies)-1 {
			log.Debug("[noci-proxy] Upstream #%d returned 404 for %s, trying next...", i+1, path)
		}
	}

	// All proxies returned 404 — fall back to direct HTTP fetch.
	if s.upstream == "" {
		s.cb.RecordFailure()
		http.NotFound(w, r)
		return
	}

	upstreamURL := s.upstream + "/" + path
	req, err := http.NewRequestWithContext(r.Context(), "GET", upstreamURL, nil)
	if err != nil {
		http.Error(w, "Gateway error", http.StatusBadGateway)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.cb.RecordFailure()
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		s.cb.RecordFailure()
	} else {
		s.cb.RecordSuccess()
	}

	if resp.StatusCode != http.StatusOK {
		http.NotFound(w, r)
		return
	}

	for k, vv := range resp.Header {
		if isHopByHopHeader(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)

	_, _ = io.CopyBuffer(w, resp.Body, buf)
}

func isHopByHopHeader(name string) bool {
	return strings.EqualFold(name, "Connection") ||
		strings.EqualFold(name, "Keep-Alive") ||
		strings.EqualFold(name, "Proxy-Authenticate") ||
		strings.EqualFold(name, "Proxy-Authorization") ||
		strings.EqualFold(name, "TE") ||
		strings.EqualFold(name, "Transfer-Encoding") ||
		strings.EqualFold(name, "Trailers") ||
		strings.EqualFold(name, "Upgrade")
}

// --- Management API ---

type HealthResponse struct {
	Status      string `json:"status"`
	IndexLoaded bool   `json:"index_loaded"`
	EntryCount  int    `json:"entry_count"`
	LastDigest  string `json:"last_digest,omitempty"`
	CanDelete   bool   `json:"can_delete"`
	Upstream    string `json:"upstream"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.indexMu.RLock()
	loaded := s.index != nil
	count := 0
	if loaded {
		count = len(s.index.Entries)
	}
	digest := s.lastDigest
	s.indexMu.RUnlock()

	resp := HealthResponse{
		IndexLoaded: loaded,
		EntryCount:  count,
		LastDigest:  digest,
		CanDelete:   s.canDelete,
		Upstream:    s.upstream,
	}

	w.Header().Set("Content-Type", "application/json")
	if loaded {
		resp.Status = "ok"
		w.WriteHeader(http.StatusOK)
	} else {
		resp.Status = "degraded"
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.metrics.mu.Lock()
	counts := make(map[string]int, len(s.metrics.counts))
	for k, v := range s.metrics.counts {
		counts[k] = v
	}
	uptime := time.Since(s.metrics.startTime).Seconds()
	s.metrics.mu.Unlock()

	s.indexMu.RLock()
	indexEntryCount := 0
	var indexSize int64
	if s.index != nil {
		indexEntryCount = len(s.index.Entries)
		for _, entry := range s.index.Entries {
			indexSize += entry.NarSize
		}
	}
	s.indexMu.RUnlock()

	negCacheCount := 0
	s.negCache.Range(func(_, _ interface{}) bool {
		negCacheCount++
		return true
	})

	cbState := 0
	if s.cb != nil {
		cbState = int(s.cb.State())
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP noci_http_requests_total Total HTTP requests\n")
	fmt.Fprintf(w, "# TYPE noci_http_requests_total counter\n")
	for key, val := range counts {
		parts := strings.SplitN(key, " ", 4)
		if len(parts) == 4 {
			fmt.Fprintf(w, "noci_http_requests_total{method=%q,path=%q,status=%q,source=%q} %d\n",
				parts[0], parts[1], parts[2], parts[3], val)
		}
	}
	fmt.Fprintf(w, "\n# HELP noci_index_entries Number of index entries\n")
	fmt.Fprintf(w, "# TYPE noci_index_entries gauge\n")
	fmt.Fprintf(w, "noci_index_entries %d\n", indexEntryCount)
	fmt.Fprintf(w, "\n# HELP noci_index_size_bytes Total size of cached packages in bytes\n")
	fmt.Fprintf(w, "# TYPE noci_index_size_bytes gauge\n")
	fmt.Fprintf(w, "noci_index_size_bytes %d\n", indexSize)
	fmt.Fprintf(w, "\n# HELP noci_neg_cache_size Number of entries in negative cache\n")
	fmt.Fprintf(w, "# TYPE noci_neg_cache_size gauge\n")
	fmt.Fprintf(w, "noci_neg_cache_size %d\n", negCacheCount)
	fmt.Fprintf(w, "\n# HELP noci_upstream_circuit_breaker_state Circuit breaker state (0=closed, 1=half-open, 2=open)\n")
	fmt.Fprintf(w, "# TYPE noci_upstream_circuit_breaker_state gauge\n")
	fmt.Fprintf(w, "noci_upstream_circuit_breaker_state %d\n", cbState)
	fmt.Fprintf(w, "\n# HELP noci_uptime_seconds Time since proxy started\n")
	fmt.Fprintf(w, "# TYPE noci_uptime_seconds gauge\n")
	fmt.Fprintf(w, "noci_uptime_seconds %.1f\n", uptime)
}

func (s *Server) handleAPIDigest(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "text/plain")

	s.indexMu.RLock()
	digest := s.lastDigest
	s.indexMu.RUnlock()

	_, _ = w.Write([]byte(digest))
}

func (s *Server) handleAPIIndex(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "application/json")

	query := r.URL.Query()
	page := 1
	if pStr := query.Get("page"); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 0 {
			page = p
		}
	}
	limit := 50
	if lStr := query.Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
		}
	}
	search := query.Get("search")

	response, err := s.adminSvc.GetPaginatedIndex(page, limit, search)
	if err != nil {
		http.Error(w, "Index not ready", http.StatusServiceUnavailable)
		return
	}

	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) handleAPIDeleteRoute(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if len(hash) != 32 {
		http.Error(w, "Invalid hash length", http.StatusBadRequest)
		return
	}
	if !s.canDelete {
		http.Error(w, "Deletion is disabled (read-only proxy mode)", http.StatusForbidden)
		return
	}

	if err := s.adminSvc.DeletePackage(r.Context(), hash); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not found") {
			http.Error(w, msg, http.StatusNotFound)
		} else {
			http.Error(w, msg, http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Package deleted logically and written to OCI"))
}
