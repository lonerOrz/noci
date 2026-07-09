package oci

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestFormatSize(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tt := range tests {
		got := FormatSize(tt.input)
		if got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseNextLink(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple next link",
			input: `</v2/user/repo/nix-cache/tags/list?n=100&last=abc>; rel="next"`,
			want:  "/tags/list?n=100&last=abc",
		},
		{
			name:  "multiple links",
			input: `</first>; rel="prev", </v2/user/repo/nix-cache/tags/list?n=100&last=abc>; rel="next"`,
			want:  "/tags/list?n=100&last=abc",
		},
		{
			name:  "no next link",
			input: `</first>; rel="prev"`,
			want:  "",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNextLink(tt.input)
			if got != tt.want {
				t.Errorf("parseNextLink(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDeleteManifest_FallbackOnMethodNotAllowed(t *testing.T) {
	var deleteCalled, putCalled bool

	mockServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/token") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"mock-token"}`)
			return
		}
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/manifests/tag-to-delete") {
			deleteCalled = true
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == "PUT" && strings.Contains(r.URL.Path, "/manifests/tag-to-delete") {
			putCalled = true
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	u, _ := url.Parse(mockServer.URL)
	client := NewClient(u.Host, "user/repo", "token")
	client.SetHTTPClient(mockServer.Client())

	err := client.DeleteManifest(context.Background(), "tag-to-delete")
	if err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if !deleteCalled {
		t.Error("DELETE was never called")
	}
	if !putCalled {
		t.Error("fallback PUT was never called")
	}
}

func TestDeleteManifest_FallbackOnForbidden(t *testing.T) {
	var putCalled bool

	mockServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/token") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"mock-token"}`)
			return
		}
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/manifests/tag-to-delete") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == "PUT" && strings.Contains(r.URL.Path, "/manifests/tag-to-delete") {
			putCalled = true
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	u, _ := url.Parse(mockServer.URL)
	client := NewClient(u.Host, "user/repo", "token")
	client.SetHTTPClient(mockServer.Client())

	err := client.DeleteManifest(context.Background(), "tag-to-delete")
	if err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if !putCalled {
		t.Error("fallback PUT was not triggered on 403")
	}
}

func TestDeleteManifest_FallbackOnUnauthorized(t *testing.T) {
	var putCalled bool

	mockServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/token") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"mock-token"}`)
			return
		}
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/manifests/tag-to-delete") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == "PUT" && strings.Contains(r.URL.Path, "/manifests/tag-to-delete") {
			putCalled = true
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	u, _ := url.Parse(mockServer.URL)
	client := NewClient(u.Host, "user/repo", "token")
	client.SetHTTPClient(mockServer.Client())

	err := client.DeleteManifest(context.Background(), "tag-to-delete")
	if err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if !putCalled {
		t.Error("fallback PUT was not triggered on 401")
	}
}

func TestUploadBlobChunked_RetryAndSeek(t *testing.T) {
	// sha256("test") = 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
	const expectedSHA = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	var patchAttempts int
	const sessionPath = "/v2/user/repo/nix-cache/blobs/uploads/session-12345"

	mockServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Token endpoint
		if strings.HasPrefix(r.URL.Path, "/token") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"mock-token"}`)
			return
		}

		// HEAD: blob doesn't exist
		if r.Method == "HEAD" && strings.Contains(r.URL.Path, "/blobs/sha256:") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// POST: initiate upload session
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/blobs/uploads/") {
			u, _ := url.Parse(r.URL.String())
			u.Path = sessionPath
			w.Header().Set("Location", u.String())
			w.WriteHeader(http.StatusAccepted)
			return
		}

		// PATCH: chunk upload
		if r.Method == "PATCH" && strings.Contains(r.URL.Path, "/session-") {
			patchAttempts++

			cr := r.Header.Get("Content-Range")
			if cr != "0-3" {
				t.Errorf("Content-Range = %q, want %q", cr, "0-3")
			}

			// First attempt: simulate 502
			if patchAttempts == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}

			// Retry: verify body re-read
			body, _ := io.ReadAll(r.Body)
			if string(body) != "test" {
				t.Errorf("retry body = %q, want %q", string(body), "test")
			}

			w.Header().Set("Location", r.URL.String()+"_final")
			w.WriteHeader(http.StatusAccepted)
			return
		}

		// PUT: finalize
		if r.Method == "PUT" && strings.Contains(r.URL.Path, "/session-") {
			digest := r.URL.Query().Get("digest")
			if digest != "sha256:"+expectedSHA {
				t.Errorf("PUT digest = %q, want %q", digest, "sha256:"+expectedSHA)
			}
			w.WriteHeader(http.StatusCreated)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	u, _ := url.Parse(mockServer.URL)
	client := NewClient(u.Host, "user/repo", "test-token")
	client.SetHTTPClient(mockServer.Client())

	tmp, err := os.CreateTemp("", "noci-chunk-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write([]byte("test"))
	tmp.Close()

	digest, err := client.UploadBlobChunked(context.Background(), tmp.Name(), expectedSHA, "test-nar", 10)
	if err != nil {
		t.Fatalf("UploadBlobChunked: %v", err)
	}
	if digest != "sha256:"+expectedSHA {
		t.Errorf("digest = %q, want %q", digest, "sha256:"+expectedSHA)
	}
	if patchAttempts != 2 {
		t.Errorf("PATCH attempts = %d, want 2 (1 fail + 1 retry)", patchAttempts)
	}
}
