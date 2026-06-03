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
	case path == "nix-cache-info":
		s.HandleNixCacheInfo(lrw, r)
	case path == "public-key":
		s.handlePublicKey(lrw, r)
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
	ctx := r.Context()
	s.loadIndexLazy(ctx)

	if len(hash) != 32 {
		setSource(w, "upstream")
		s.proxyToUpstream(w, r, hash+".narinfo")
		return
	}

	if val, exists := s.negCache.Load(hash); exists {
		if time.Since(val.(time.Time)) <= 300*time.Second {
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

	s.indexMu.RLock()
	idxBytes, err := json.Marshal(s.index)
	s.indexMu.RUnlock()

	if err != nil {
		http.Error(w, "Failed to marshal index", http.StatusInternalServerError)
		return
	}

	html := strings.Replace(dist.IndexHTML, "/* {{.IndexJSON}} */", string(idxBytes), 1)
	_, _ = w.Write([]byte(html))
}
