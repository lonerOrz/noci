package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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
	idx := s.GetIndex()
	if idx != nil && idx.PublicKey != "" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(idx.PublicKey + "\n"))
		return
	}
	http.Error(w, "Public key not found", http.StatusNotFound)
}

func (s *Server) handleNarInfo(w http.ResponseWriter, r *http.Request, hash string) {
	idx := s.GetIndex()
	if idx != nil {
		if item, exists := idx.Entries[hash]; exists {
			w.Header().Set("Content-Type", "text/x-nix-narinfo")
			_, _ = w.Write([]byte(item.NarInfo))

			if time.Since(item.LastUsed) > 24*time.Hour {
				select {
				case s.updateChan <- hash:
				default:
				}
			}
			return
		}
	}

	s.proxyToUpstream(w, r, hash+".narinfo")
}

func (s *Server) handleNar(w http.ResponseWriter, r *http.Request, filename string) {
	hash := filename
	if idx := strings.Index(filename, "."); idx != -1 {
		hash = filename[:idx]
	}

	idx := s.GetIndex()
	if idx != nil && hash != "" {
		if item, exists := idx.Entries[hash]; exists {
			s.streamBlob(w, item.NarDigest)

			if time.Since(item.LastUsed) > 24*time.Hour {
				select {
				case s.updateChan <- hash:
				default:
				}
			}
			return
		}
	}

	s.proxyToUpstream(w, r, "nar/"+filename)
}

func (s *Server) streamBlob(w http.ResponseWriter, digest string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	resp, err := s.client.Request(ctx, "GET", "/blobs/"+digest, nil, "")
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Error(w, "Failed to stream archive", http.StatusNotFound)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/x-nix-nar")
	if resp.ContentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", resp.ContentLength))
	}

	buf := make([]byte, 64*1024)
	_, _ = io.CopyBuffer(w, resp.Body, buf)
}

func (s *Server) proxyToUpstream(w http.ResponseWriter, r *http.Request, path string) {
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

	// RFC 7230: 严格过滤 Hop-by-hop 逐跳传输头部，防止下游协议违规混乱
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
