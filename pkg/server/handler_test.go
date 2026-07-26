package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"noci/pkg/oci"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHealthz_IndexLoaded(t *testing.T) {
	s := &Server{
		upstream:  "https://cache.nixos.org",
		canDelete: true,
	}
	s.indexMu.Lock()
	s.index = &oci.CacheIndex{
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {},
			"bbb22222222222222222222222222222": {},
		},
	}
	s.lastDigest = "sha256:abcdef12"
	s.indexMu.Unlock()

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	s.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
	if !resp.IndexLoaded {
		t.Error("IndexLoaded should be true")
	}
	if resp.EntryCount != 2 {
		t.Errorf("EntryCount = %d, want 2", resp.EntryCount)
	}
	if resp.LastDigest != "sha256:abcdef12" {
		t.Errorf("LastDigest = %q, want sha256:abcdef12", resp.LastDigest)
	}
	if !resp.CanDelete {
		t.Error("CanDelete should be true")
	}
	if resp.Upstream != "https://cache.nixos.org" {
		t.Errorf("Upstream = %q", resp.Upstream)
	}
}

func TestHealthz_IndexNotLoaded(t *testing.T) {
	s := &Server{
		upstream: "https://cache.nixos.org",
	}

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	s.handleHealthz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}

	var resp HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Status != "degraded" {
		t.Errorf("status = %q, want degraded", resp.Status)
	}
	if resp.IndexLoaded {
		t.Error("IndexLoaded should be false")
	}
	if resp.EntryCount != 0 {
		t.Errorf("EntryCount = %d, want 0", resp.EntryCount)
	}
}

func TestStreamBlob_RedirectHandling(t *testing.T) {
	const location = "https://cdn.example.com/blobs/some-uuid"

	var redirectCode int

	mockServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/token") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"mock-token"}`)
			return
		}
		if strings.Contains(r.URL.Path, "/blobs/") {
			w.Header().Set("Location", location)
			w.WriteHeader(redirectCode)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	u := mockServer.URL
	registry := strings.TrimPrefix(u, "https://")
	client := oci.NewClient(registry, "user/repo", "test-token")
	client.SetHTTPClient(mockServer.Client())

	s := &Server{client: client, rawClient: client}

	codes := []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	}

	for _, code := range codes {
		redirectCode = code
		req := httptest.NewRequest("GET", "/nar/abc.nar", nil)
		w := httptest.NewRecorder()

		s.streamBlob(w, req, "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

		if w.Code != code {
			t.Errorf("redirect %d: got status %d", code, w.Code)
		}
		if got := w.Header().Get("Location"); got != location {
			t.Errorf("redirect %d: Location = %q, want %q", code, got, location)
		}
	}
}

func TestHandleNarInfoRoute_CacheHit(t *testing.T) {
	s := &Server{
		negCache: sync.Map{},
	}
	s.indexMu.Lock()
	s.index = &oci.CacheIndex{
		Entries: map[string]oci.IndexItem{
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {
				Name:      "test-pkg",
				NarInfo:   "StorePath: /nix/store/abc-test-pkg\nURL: nar/old-hash.nar.gz\n",
				NarDigest: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			},
		},
	}
	s.indexMu.Unlock()

	s.cacheSvc = newCacheService(s)

	req := httptest.NewRequest("GET", "/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.narinfo", nil)
	req.SetPathValue("hash", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	w := httptest.NewRecorder()

	s.handleNarInfoRoute(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	// The rewrite should replace the URL with nar/<sha256>.nar.gz
	expectedURL := "URL: nar/1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef.nar.gz"
	if !strings.Contains(body, expectedURL) {
		t.Errorf("URL was not rewritten correctly, body:\n%s", body)
	}
}

func TestHandleNarInfoRoute_CacheMiss(t *testing.T) {
	s := &Server{
		negCache:       sync.Map{},
		upstream:       "https://cache.nixos.org",
		cb:             NewCircuitBreaker(5, 30*time.Second),
		upstreamProxy:  nil,
		upstreamExtras: nil,
	}
	s.indexMu.Lock()
	s.index = &oci.CacheIndex{Entries: map[string]oci.IndexItem{}}
	s.indexMu.Unlock()

	s.cacheSvc = newCacheService(s)

	req := httptest.NewRequest("GET", "/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.narinfo", nil)
	req.SetPathValue("hash", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	w := httptest.NewRecorder()

	s.handleNarInfoRoute(w, req)

	// Should fall through to upstream — we get 404 or upstream response
	if w.Code == http.StatusOK {
		t.Error("cache miss should not return 200")
	}
}
