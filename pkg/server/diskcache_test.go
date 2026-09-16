package server

import (
	"context"
	"errors"
	"io"
	"noci/pkg/domain/types"
	"noci/pkg/oci"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failStore struct {
	oci.Store
}

func (f *failStore) FetchIndex(_ context.Context) (*oci.CacheIndex, error) {
	return nil, errors.New("network unreachable / 429 rate limit")
}
func (f *failStore) ManifestExists(_ context.Context, _ string) (bool, string) {
	return false, ""
}
func (f *failStore) CanWrite(_ context.Context) bool { return false }
func (f *failStore) StreamBlob(_ context.Context, _ types.OciDigest, _ io.Writer) error {
	return errors.New("not implemented")
}
func (f *failStore) UploadBlob(_ context.Context, _, _, _ string, _ oci.ProgressNotifier) (string, int64, error) {
	return "", 0, errors.New("not implemented")
}
func (f *failStore) DeleteBlob(_ context.Context, _ string) error     { return nil }
func (f *failStore) DeleteManifest(_ context.Context, _ string) error { return nil }
func (f *failStore) ListTags(_ context.Context) ([]string, error)     { return nil, nil }
func (f *failStore) GetBlobRedirectURL(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (f *failStore) FetchManifest(_ context.Context, _ string) (*oci.OCIManifest, error) {
	return nil, nil
}
func (f *failStore) PushManifest(_ context.Context, _ string, _ *oci.OCIManifest) error {
	return nil
}
func (f *failStore) PushIndex(_ context.Context, _ *oci.CacheIndex) error { return nil }
func (f *failStore) RepairIndexEntry(_ context.Context, _ string, _ *oci.CacheIndex) error {
	return nil
}

func TestDiskIndexCache_ColdStartFallback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "noci-cache-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dc := NewDiskIndexCache(tmpDir, "ghcr.io", "test/repo")

	h, _ := types.ParseNixHash("0abc1234567890abc1234567890abc12")
	d, _ := types.ParseOciDigest("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	initialIdx := oci.NewIndex("ghcr.io", "test/repo")
	initialIdx.AddEntry(h, "cached-tool", "StorePath: /nix/store/abc\n", d, 1024, nil)

	if err := dc.Save(initialIdx, "sha256:digest123"); err != nil {
		t.Fatalf("dc.Save failed: %v", err)
	}

	diskIdx, diskDigest, err := dc.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if diskIdx == nil {
		t.Fatal("expected non-nil diskIdx")
	}
	if len(diskIdx.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(diskIdx.Entries))
	}
	if diskDigest != "sha256:digest123" {
		t.Errorf("digest = %q, want sha256:digest123", diskDigest)
	}
}

func TestDiskIndexCache_AtomicWrite_NoCorruptionOnKill(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "noci-cache-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dc := NewDiskIndexCache(tmpDir, "ghcr.io", "test/repo")
	idx := oci.NewIndex("ghcr.io", "test/repo")

	if err := dc.Save(idx, "sha256:abc"); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Temp files must have been cleaned up
	_, err = os.Stat(dc.filePath + ".tmp")
	if err == nil {
		t.Error("expected .tmp file to be removed after rename")
	}
	_, err = os.Stat(dc.digestPath + ".tmp")
	if err == nil {
		t.Error("expected digest .tmp file to be removed after rename")
	}

	// Valid file must exist
	if _, err := os.Stat(dc.filePath); err != nil {
		t.Errorf("main cache file missing: %v", err)
	}
	if _, err := os.Stat(dc.digestPath); err != nil {
		t.Errorf("digest file missing: %v", err)
	}
}

func TestDiskIndexCache_NilSafe(t *testing.T) {
	var dc *DiskIndexCache
	if _, _, err := dc.Load(); err != nil {
		t.Errorf("Load on nil should not error, got: %v", err)
	}
	if err := dc.Save(nil, "sha256:abc"); err != nil {
		t.Errorf("Save on nil should not error, got: %v", err)
	}
}

func TestDiskIndexCache_ResolveCacheDir_Precedence(t *testing.T) {
	origExplicit := os.Getenv("NOCI_CACHE_DIR")
	origSystemd := os.Getenv("CACHE_DIRECTORY")
	defer func() {
		os.Setenv("NOCI_CACHE_DIR", origExplicit)
		os.Setenv("CACHE_DIRECTORY", origSystemd)
	}()

	// Explicit wins over env vars
	os.Setenv("NOCI_CACHE_DIR", "/env-cache")
	os.Setenv("CACHE_DIRECTORY", "/systemd-cache")
	if got := resolveCacheDir("/explicit-cache"); got != "/explicit-cache" {
		t.Errorf("explicit = %q, want /explicit-cache", got)
	}

	// NOCI_CACHE_DIR wins over CACHE_DIRECTORY
	if got := resolveCacheDir(""); got != "/env-cache" {
		t.Errorf("NOCI_CACHE_DIR = %q, want /env-cache", got)
	}

	os.Unsetenv("NOCI_CACHE_DIR")
	if got := resolveCacheDir(""); got != "/systemd-cache" {
		t.Errorf("CACHE_DIRECTORY = %q, want /systemd-cache", got)
	}
}

func TestDiskIndexCache_CorruptedFile_ReturnsError(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "noci-cache-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dc := NewDiskIndexCache(tmpDir, "ghcr.io", "test/repo")
	// Write corruption directly to the path dc.filePath expects.
	if err := os.MkdirAll(filepath.Dir(dc.filePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dc.filePath, []byte("not valid json!!!"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, err = dc.Load()
	if err == nil {
		t.Fatal("expected error for corrupted file")
	}
}

func TestDiskIndexCache_PathTraversalBlocked(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "noci-cache-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// A genuinely malicious repo that survives sanitization must still resolve
	// inside tmpDir — the boundary check must reject anything that escapes.
	dc := NewDiskIndexCache(tmpDir, "ghcr.io", "../../etc")
	if dc == nil {
		// Sanitizer may have converted this to a safe subdir; either outcome is fine.
		t.Log("path-traversal repo returned nil (sanitized)")
		return
	}
	absPath, _ := filepath.Abs(dc.filePath)
	if !strings.HasPrefix(absPath, tmpDir+string(filepath.Separator)) {
		t.Errorf("filePath %q escaped cache root %q", absPath, tmpDir)
	}

	// Normal repo should produce a valid cache inside tmpDir.
	dc2 := NewDiskIndexCache(tmpDir, "ghcr.io", "user/repo")
	if dc2 == nil {
		t.Fatal("expected non-nil cache for normal repo")
	}
	absPath2, _ := filepath.Abs(dc2.filePath)
	if !strings.HasPrefix(absPath2, tmpDir+string(filepath.Separator)) {
		t.Errorf("filePath %q escaped cache root %q", absPath2, tmpDir)
	}
	if dc2.filePath == "" {
		t.Error("expected non-empty filePath")
	}
}

// startServerForTest is a minimal replacement for server.NewServer that accepts
// an already-configured store, useful when tests need full Start() wiring.
type testServerBuilder struct {
	s *Server
}

func newTestServer(t *testing.T, tmpDir, registry, repo string) (*Server, *oci.CacheIndex) {
	t.Helper()
	idx := oci.NewIndex(registry, repo)
	h, _ := types.ParseNixHash("0abc1234567890abc1234567890abc12")
	d, _ := types.ParseOciDigest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	idx.AddEntry(h, "pkg", "StorePath: /nix/store/abc\n", d, 1024, nil)

	s := &Server{
		addr:      "127.0.0.1:0",
		store:     &failStore{},
		diskCache: NewDiskIndexCache(tmpDir, registry, repo),
	}
	// Seed the disk so Start() can cold-restore from it.
	if err := s.diskCache.Save(idx, "sha256:testdigest"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s, idx
}

func TestServer_Start_ColdStartFromDisk(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "noci-start-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	s, seedIdx := newTestServer(t, tmpDir, "ghcr.io", "test/repo")

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done <- s.Start(ctx)
	}()

	select {
	case err := <-done:
		if err != nil && err != context.DeadlineExceeded {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start() timed out waiting for Serve to finish")
	}

	s.indexMu.RLock()
	got := s.index
	s.indexMu.RUnlock()

	if got == nil {
		t.Fatal("index should be loaded from disk")
	}
	if len(got.Entries) != len(seedIdx.Entries) {
		t.Errorf("entries = %d, want %d", len(got.Entries), len(seedIdx.Entries))
	}
}
