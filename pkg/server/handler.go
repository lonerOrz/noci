package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"noci/dist"
	"noci/pkg/log"
	"noci/pkg/oci"
	"strings"
	"sync"
	"time"
)

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
		return make([]byte, 32*1024)
	},
}

func (s *Server) HandleNixCacheInfo(w http.ResponseWriter, r *http.Request) {
	setSource(w, "cache")
	w.Header().Set("Content-Type", "text/x-nix-cache-info")
	_, _ = w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"))
}

func (s *Server) HandleRoutes(w http.ResponseWriter, r *http.Request) {
	lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusNotFound}
	start := time.Now()
	defer func() {
		silentPaths := map[string]bool{
			"/api/digest":  true,
			"/app.js":      true,
			"/style.css":   true,
			"/favicon.svg": true,
		}
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	resp, err := s.client.RawRequest(ctx, "GET", "/blobs/"+digest, nil, "")
	if err != nil {
		http.Error(w, "Failed to stream archive", http.StatusNotFound)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusFound {
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
	if err != nil || resp.StatusCode != http.StatusOK {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()

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
	s.indexMu.RLock()
	defer s.indexMu.RUnlock()

	if s.index == nil {
		http.Error(w, "Index not ready", http.StatusServiceUnavailable)
		return
	}

	response := struct {
		*oci.CacheIndex
		CanDelete bool `json:"canDelete"`
	}{
		CacheIndex: s.index,
		CanDelete:  s.canDelete,
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
	log.Info("[noci-proxy] OCI Registry write capability: ENABLED")
	return true
}
