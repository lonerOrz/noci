package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"noci/pkg/log"
	"noci/pkg/oci"
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 32*1024)
	},
}

func (s *Server) HandleNixCacheInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-nix-cache-info")
	_, _ = w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"))
}

func (s *Server) HandleRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")

	switch {
	case path == "public-key":
		s.handlePublicKey(w, r)
	case strings.HasSuffix(path, ".narinfo"):
		s.handleNarInfo(w, r, strings.TrimSuffix(path, ".narinfo"))
	case strings.HasPrefix(path, "nar/"):
		s.handleNar(w, r, strings.TrimPrefix(path, "nar/"))
	default:
		http.NotFound(w, r)
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
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(pubKey + "\n"))
			return
		}
	}
	http.Error(w, "Public key not found", http.StatusNotFound)
}

func (s *Server) handleNarInfo(w http.ResponseWriter, r *http.Request, hash string) {
	ctx := r.Context()

	s.loadTagsLazy(ctx)

	if val, exists := s.negCache.Load(hash); exists {
		if time.Since(val.(time.Time)) <= time.Duration(s.ttl)*time.Second {
			s.proxyToUpstream(w, r, hash+".narinfo")
			return
		}
		s.negCache.Delete(hash)
	}

	s.tagsMu.RLock()
	_, inPositiveCache := s.tags[hash]
	s.tagsMu.RUnlock()

	if !inPositiveCache {
		manifest, err := s.client.FetchManifest(ctx, hash)
		if err != nil {
			if strings.Contains(err.Error(), "HTTP 404") {
				s.negCache.Store(hash, time.Now())
			} else {
				log.Warning("Failed to fetch manifest for %s: %v", hash, err)
			}
			s.proxyToUpstream(w, r, hash+".narinfo")
			return
		}

		if manifest.Annotations != nil && manifest.Annotations["org.nix.evicted"] == "true" {
			s.proxyToUpstream(w, r, hash+".narinfo")
			return
		}

		s.tagsMu.Lock()
		if s.tags != nil {
			s.tags[hash] = struct{}{}
		}
		s.tagsMu.Unlock()

		s.serveNarInfo(w, manifest)
		return
	}

	manifest, err := s.client.FetchManifest(ctx, hash)
	if err != nil {
		log.Warning("Failed to fetch manifest for cached tag %s: %v", hash, err)
		s.proxyToUpstream(w, r, hash+".narinfo")
		return
	}

	if manifest.Annotations != nil && manifest.Annotations["org.nix.evicted"] == "true" {
		s.proxyToUpstream(w, r, hash+".narinfo")
		return
	}

	s.serveNarInfo(w, manifest)
}

func (s *Server) serveNarInfo(w http.ResponseWriter, manifest *oci.OCIManifest) {
	narinfo := manifest.Annotations["org.nix.narinfo"]
	digest := strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:")
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
		s.streamBlob(w, r, "sha256:"+digest)
		return
	}

	if len(digest) == 32 {
		s.tagsMu.RLock()
		_, inPositiveCache := s.tags[digest]
		s.tagsMu.RUnlock()

		if inPositiveCache {
			manifest, err := s.client.FetchManifest(r.Context(), digest)
			if err == nil && len(manifest.Layers) > 0 {
				if manifest.Annotations != nil && manifest.Annotations["org.nix.evicted"] == "true" {
					s.proxyToUpstream(w, r, "nar/"+filename)
					return
				}
				s.streamBlob(w, r, manifest.Layers[0].Digest)
				return
			}
		}
	}

	s.proxyToUpstream(w, r, "nar/"+filename)
}

func (s *Server) streamBlob(w http.ResponseWriter, r *http.Request, digest string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	resp, err := s.client.Request(ctx, "GET", "/blobs/"+digest, nil, "")
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
	_, _ = io.Copy(w, resp.Body)
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
