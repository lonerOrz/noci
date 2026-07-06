package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"noci/pkg/oci"
	"testing"
)

func TestHealthz_IndexLoaded(t *testing.T) {
	s := &Server{
		upstream: "https://cache.nixos.org",
		canDelete: true,
	}
	// Simulate loaded index
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
		t.Errorf("LastDigest = %q", resp.LastDigest)
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
