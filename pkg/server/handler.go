package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"noci/dist"
	"noci/pkg/log"
	"noci/pkg/oci"
	"sort"
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

func (s *Server) HandleNixCacheInfo(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "text/x-nix-cache-info")
	_, _ = w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"))
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

func (s *Server) HandleRoutes(w http.ResponseWriter, r *http.Request) {
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
		// Track metrics for all requests
		status := strconv.Itoa(lrw.statusCode)
		s.metrics.inc(r.Method, r.URL.Path, status, lrw.source)
		if silentPaths[r.URL.Path] {
			return
		}
		source := ""
		if lrw.source != "" {
			source = fmt.Sprintf(" (%s)", lrw.source)
		}
		log.Info("[noci-proxy] %s %s - %d%s (%s)", r.Method, r.URL.Path, lrw.statusCode, source, time.Since(start))
	}()

	path := strings.TrimPrefix(r.URL.Path, "/")

	switch {
	case path == "":
		s.handleDashboard(lrw, r)
	case path == "app.js":
		s.handleAppJS(lrw, r)
	case path == "style.css":
		s.handleStyleCSS(lrw, r)
	case path == "favicon.svg":
		s.handleFavicon(lrw, r)
	case path == "nix-cache-info":
		s.HandleNixCacheInfo(lrw, r)
	case path == "public-key":
		s.handlePublicKey(lrw, r)
	case path == "api/digest":
		s.handleAPIDigest(lrw, r)
	case path == "api/index":
		s.handleAPIIndex(lrw, r)
	case path == "healthz":
		s.handleHealthz(lrw, r)
	case path == "metrics":
		s.handleMetrics(lrw, r)
	case strings.HasPrefix(path, "api/delete/"):
		s.handleAPIDelete(lrw, r, strings.TrimPrefix(path, "api/delete/"))
	case strings.HasSuffix(path, ".narinfo"):
		s.handleNarInfo(lrw, r, strings.TrimSuffix(path, ".narinfo"))
	case strings.HasPrefix(path, "nar/"):
		s.handleNar(lrw, r, strings.TrimPrefix(path, "nar/"))
	default:
		http.NotFound(lrw, r)
	}
}

func (s *Server) handlePublicKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	manifest, err := s.client.FetchManifest(ctx, "public-key")
	if err != nil {
		log.Warning("Failed to fetch public-key: %v", err)
	}
	if err == nil && manifest.Annotations != nil {
		pubKey := manifest.Annotations["org.nix.public_key"]
		if pubKey != "" {
			setSource(w, "cache")
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(pubKey + "\n"))
			return
		}
	}
	http.Error(w, "Public key not found", http.StatusNotFound)
}

func (s *Server) handleNarInfo(w http.ResponseWriter, r *http.Request, hash string) {
	if len(hash) != 32 {
		setSource(w, "upstream")
		s.proxyToUpstream(w, r, hash+".narinfo")
		return
	}

	if val, exists := s.negCache.Load(hash); exists {
		if time.Since(val.(time.Time)) <= 5*time.Second {
			setSource(w, "upstream")
			s.proxyToUpstream(w, r, hash+".narinfo")
			return
		}
		s.negCache.Delete(hash)
	}

	s.indexMu.RLock()
	var entry oci.IndexItem
	var found bool
	if s.index != nil && s.index.Entries != nil {
		entry, found = s.index.Entries[hash]
	}
	s.indexMu.RUnlock()

	if !found {
		s.negCache.Store(hash, time.Now())
		setSource(w, "upstream")
		s.proxyToUpstream(w, r, hash+".narinfo")
		return
	}

	setSource(w, "cache")
	s.serveNarInfo(w, &entry)
}

func (s *Server) serveNarInfo(w http.ResponseWriter, entry *oci.IndexItem) {
	narinfo := entry.NarInfo
	if narinfo == "" {
		http.Error(w, "malformed entry: no narinfo", http.StatusInternalServerError)
		return
	}
	digest := strings.TrimPrefix(entry.NarDigest, "sha256:")
	lines := strings.Split(narinfo, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "URL: ") {
			ext := ".nar.gz"
			if strings.HasSuffix(line, ".nar.zst") {
				ext = ".nar.zst"
			}
			lines[i] = "URL: nar/" + digest + ext
			break
		}
	}
	rewrittenNarInfo := strings.Join(lines, "\n")

	w.Header().Set("Content-Type", "text/x-nix-narinfo")
	_, _ = w.Write([]byte(rewrittenNarInfo))
}

func (s *Server) handleNar(w http.ResponseWriter, r *http.Request, filename string) {
	digest := filename
	if idx := strings.Index(filename, "."); idx != -1 {
		digest = filename[:idx]
	}

	if len(digest) == 64 {
		setSource(w, "cache")
		s.streamBlob(w, r, "sha256:"+digest)
		return
	}

	if len(digest) == 32 {
		s.indexMu.RLock()
		var entry oci.IndexItem
		var found bool
		if s.index != nil && s.index.Entries != nil {
			entry, found = s.index.Entries[digest]
		}
		s.indexMu.RUnlock()

		if found {
			setSource(w, "cache")
			s.streamBlob(w, r, "sha256:"+entry.NarDigest)
			return
		}
	}

	setSource(w, "upstream")
	s.proxyToUpstream(w, r, "nar/"+filename)
}

func (s *Server) streamBlob(w http.ResponseWriter, r *http.Request, digest string) {
	ctx, cancel := context.WithTimeout(r.Context(), oci.DefaultStreamTimeout)
	defer cancel()

	resp, err := s.client.RawRequest(ctx, "GET", "/blobs/"+digest, nil, "")
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

func (s *Server) proxyToUpstream(w http.ResponseWriter, r *http.Request, path string) {
	if !s.cb.Allow() {
		http.Error(w, "Upstream temporarily unavailable (circuit breaker open)", http.StatusServiceUnavailable)
		return
	}

	if s.upstreamProxy != nil {
		r.URL.Path = "/" + path
		s.upstreamProxy.ServeHTTP(w, r)
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

type PaginatedResponse struct {
	Total       int              `json:"total"`
	Page        int              `json:"page"`
	Limit       int              `json:"limit"`
	CanDelete   bool             `json:"canDelete"`
	Repo        string           `json:"repo"`
	Registry    string           `json:"registry"`
	GlobalCount int64            `json:"globalCount"`
	GlobalSize  int64            `json:"globalSize"`
	Entries     []PaginatedEntry `json:"entries"`
}

type PaginatedEntry struct {
	Hash string `json:"hash"`
	oci.IndexItem
}

func (s *Server) handleAPIIndex(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "application/json")
	s.indexMu.RLock()
	defer s.indexMu.RUnlock()

	if s.index == nil {
		http.Error(w, "Index not ready", http.StatusServiceUnavailable)
		return
	}

	var globalSize int64
	for _, entry := range s.index.Entries {
		globalSize += entry.NarSize
	}
	globalCount := int64(len(s.index.Entries))

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

	search := strings.ToLower(strings.TrimSpace(query.Get("search")))

	var filtered []PaginatedEntry
	for hash, entry := range s.index.Entries {
		if search != "" {
			nameMatch := strings.Contains(strings.ToLower(entry.Name), search)
			hashMatch := strings.Contains(strings.ToLower(hash), search)
			if !nameMatch && !hashMatch {
				continue
			}
		}
		filtered = append(filtered, PaginatedEntry{
			Hash:      hash,
			IndexItem: entry,
		})
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Added.After(filtered[j].Added)
	})

	total := len(filtered)
	startIndex := (page - 1) * limit
	endIndex := startIndex + limit

	if startIndex > total {
		startIndex = total
	}
	if endIndex > total {
		endIndex = total
	}

	pageEntries := filtered[startIndex:endIndex]

	response := PaginatedResponse{
		Total:       total,
		Page:        page,
		Limit:       limit,
		CanDelete:   s.canDelete,
		Repo:        s.index.Repo,
		Registry:    s.index.Registry,
		GlobalCount: globalCount,
		GlobalSize:  globalSize,
		Entries:     pageEntries,
	}

	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) handleAPIDelete(w http.ResponseWriter, r *http.Request, hash string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(hash) != 32 {
		http.Error(w, "Invalid hash length", http.StatusBadRequest)
		return
	}
	if !s.canDelete {
		http.Error(w, "Deletion is disabled (read-only proxy mode)", http.StatusForbidden)
		return
	}

	ctx := r.Context()
	log.Action("[noci-proxy] Received web request to delete package hash: %s", hash)

	index, err := s.client.FetchIndex(ctx)
	if err != nil {
		log.Warning("Failed to fetch index for deletion: %v", err)
		http.Error(w, "Failed to fetch index from OCI", http.StatusInternalServerError)
		return
	}

	_, exists := index.Entries[hash]
	if !exists {
		http.Error(w, "Package not found in cache", http.StatusNotFound)
		return
	}

	delete(index.Entries, hash)
	if index.Roots != nil {
		delete(index.Roots, hash)
	}

	log.Action("[noci-proxy] Saving updated index back to OCI...")
	if err := s.client.PushIndex(ctx, index); err != nil {
		log.Warning("Failed to push index after deletion: %v", err)
		http.Error(w, "Failed to update OCI index (verify write permissions)", http.StatusInternalServerError)
		return
	}

	newDigest := fmt.Sprintf("%s-dirty-%d", s.lastDigest, time.Now().UnixNano())
	s.indexMu.Lock()
	s.index = index
	s.lastDigest = newDigest
	s.indexMu.Unlock()

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		log.Action("[noci-proxy][bg] Deleting physical manifest from OCI: %s", hash)
		if err := s.client.DeleteManifest(bgCtx, hash); err != nil {
			log.Warning("[noci-proxy][bg] Optional: Failed to physically delete OCI manifest %s: %v", hash, err)
		}

		log.Action("[noci-proxy][bg] Finalizing local index refresh...")
		if err := s.RefreshIndex(bgCtx); err != nil {
			log.Warning("[noci-proxy][bg] Failed to refresh local proxy memory cache: %v", err)
		}
	}()

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Package deleted logically and written to OCI"))
}

func (s *Server) StartPreflightProbe() {
	s.canDelete = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.canDelete = s.probeWriteCapability(ctx)
	}()
}

func (s *Server) probeWriteCapability(ctx context.Context) bool {
	resp, err := s.client.RawRequest(ctx, "PUT", "/manifests/noci-probe-write", nil, "")
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "401") || strings.Contains(errStr, "403") ||
			strings.Contains(errStr, "unauthorized") || strings.Contains(errStr, "forbidden") {
			log.Warning("[noci-proxy] OCI Registry write capability: DISABLED (read-only token)")
			return false
		}
	}
	if resp != nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			log.Warning("[noci-proxy] OCI Registry write capability: DISABLED (Status %d)", resp.StatusCode)
			return false
		}
	}

	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.client.DeleteManifest(cleanupCtx, "noci-probe-write")
	}()

	log.Info("[noci-proxy] OCI Registry write capability: ENABLED")
	return true
}
