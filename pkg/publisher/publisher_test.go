package publisher

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"noci/pkg/domain/types"
	"noci/pkg/log"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"strings"
	"testing"
)

func TestNewPublisher_PanicsOnEmptyClients(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewPublisher with empty clients should panic")
		}
	}()
	signer := &nix.Signer{KeyName: "test"}
	NewPublisher([]*oci.Client{}, signer, false, "zstd", 3, 0)
}

func TestNewPublisher_PanicsOnNilSigner(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewPublisher with nil signer should panic")
		}
	}()
	client := oci.NewClient("registry.example.com", "repo", "token")
	NewPublisher([]*oci.Client{client}, nil, false, "zstd", 3, 0)
}

func TestNewPublisher_SingleClientCreatesCache(t *testing.T) {
	signer := &nix.Signer{KeyName: "test"}
	client := oci.NewClient("registry.example.com", "repo", "token")
	pub := NewPublisher([]*oci.Client{client}, signer, false, "zstd", 3, 0)
	if pub.cache == nil {
		t.Error("single client should also create ExportCache")
	}
}

func TestNewPublisher_MultiClientCreatesCache(t *testing.T) {
	signer := &nix.Signer{KeyName: "test"}
	c1 := oci.NewClient("r1.example.com", "repo1", "token1")
	c2 := oci.NewClient("r2.example.com", "repo2", "token2")
	pub := NewPublisher([]*oci.Client{c1, c2}, signer, false, "zstd", 3, 0)
	if pub.cache == nil {
		t.Error("multi-client should create ExportCache")
	}
	if len(pub.clients) != 2 {
		t.Errorf("clients = %d, want 2", len(pub.clients))
	}
}

func TestExportCache_Dedup(t *testing.T) {
	cache := NewExportCache()
	var callCount int
	exportFn := func() (*ExportCacheEntry, error) {
		callCount++
		return &ExportCacheEntry{TempFile: "/tmp/test", FileHash: "abc"}, nil
	}

	// Two goroutines request the same path
	done := make(chan struct{})
	go func() {
		entry, err := cache.GetOrCreate(context.Background(), "/nix/store/test-pkg", exportFn)
		if err != nil {
			t.Errorf("first caller: %v", err)
		}
		if entry.FileHash != "abc" {
			t.Errorf("FileHash = %q, want abc", entry.FileHash)
		}
		done <- struct{}{}
	}()

	go func() {
		entry, err := cache.GetOrCreate(context.Background(), "/nix/store/test-pkg", exportFn)
		if err != nil {
			t.Errorf("second caller: %v", err)
		}
		if entry.FileHash != "abc" {
			t.Errorf("FileHash = %q, want abc", entry.FileHash)
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	if callCount != 1 {
		t.Errorf("exportFn called %d times, want 1 (dedup failed)", callCount)
	}
}

func TestExportCache_NilReceiver(t *testing.T) {
	// nil ExportCache should just call exportFn directly
	var cache *ExportCache
	entry, err := cache.GetOrCreate(context.Background(), "/nix/store/test-pkg", func() (*ExportCacheEntry, error) {
		return &ExportCacheEntry{TempFile: "/tmp/test", FileHash: "xyz"}, nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if entry.FileHash != "xyz" {
		t.Errorf("FileHash = %q, want xyz", entry.FileHash)
	}
}

func TestExportCache_Cleanup(t *testing.T) {
	cache := NewExportCache()
	exportFn := func() (*ExportCacheEntry, error) {
		return &ExportCacheEntry{TempFile: "/tmp/does-not-exist", FileHash: "abc"}, nil
	}
	cache.GetOrCreate(context.Background(), "/nix/store/test-pkg", exportFn)
	// Cleanup should not panic even if temp file doesn't exist
	cache.Cleanup()
}

// mockStore implements oci.Store for testing pipeline stages in isolation.
type mockStore struct {
	pushedManifests map[string]*oci.OCIManifest
	index           *oci.CacheIndex
	pushedIndex     *oci.CacheIndex
	// manifestDigests controls what ManifestExists returns for each tag.
	// Key is tag name, value is the Docker-Content-Digest header value.
	manifestDigests map[string]string
}

func newMockStore() *mockStore {
	return &mockStore{
		pushedManifests: make(map[string]*oci.OCIManifest),
		index:           oci.NewIndex("ghcr.io", "test/repo"),
		manifestDigests: make(map[string]string),
	}
}

func (m *mockStore) FetchIndex(_ context.Context) (*oci.CacheIndex, error) {
	return m.index, nil
}

func (m *mockStore) ManifestExists(_ context.Context, tag string) (bool, string) {
	if digest, ok := m.manifestDigests[tag]; ok {
		return true, digest
	}
	return false, ""
}

func (m *mockStore) PushManifest(_ context.Context, tag string, manifest *oci.OCIManifest) error {
	m.pushedManifests[tag] = manifest
	return nil
}

func (m *mockStore) PushIndex(_ context.Context, idx *oci.CacheIndex) error {
	m.pushedIndex = idx
	return nil
}

func (m *mockStore) RepairIndexEntry(_ context.Context, hash string, index *oci.CacheIndex) error {
	return nil
}

func (m *mockStore) UploadBlob(_ context.Context, _, _, _ string, _ oci.ProgressNotifier) (string, int64, error) {
	return "sha256:1111111111111111111111111111111111111111111111111111111111111111", 1024, nil
}

func (m *mockStore) DeleteBlob(_ context.Context, _ string) error     { return nil }
func (m *mockStore) DeleteManifest(_ context.Context, _ string) error { return nil }
func (m *mockStore) ListTags(_ context.Context) ([]string, error)     { return nil, nil }
func (m *mockStore) GetBlobRedirectURL(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (m *mockStore) FetchManifest(_ context.Context, _ string) (*oci.OCIManifest, error) {
	return nil, nil
}

func TestStageEnsurePublicKey(t *testing.T) {
	mock := newMockStore()
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := &Publisher{signer: &nix.Signer{KeyName: "test-key", PrivateKey: priv}}

	err := pub.stageEnsurePublicKey(context.Background(), mock)
	if err != nil {
		t.Fatalf("stageEnsurePublicKey failed: %v", err)
	}

	manifest, ok := mock.pushedManifests["public-key"]
	if !ok {
		t.Fatal("expected public-key manifest to be pushed")
	}
	key, ok := manifest.Annotations["org.nix.public_key"]
	if !ok || key == "" {
		t.Error("expected org.nix.public_key annotation")
	}
}

func TestStageEnsurePublicKey_NilSigner(t *testing.T) {
	mock := newMockStore()
	pub := &Publisher{signer: nil}

	err := pub.stageEnsurePublicKey(context.Background(), mock)
	if err != nil {
		t.Fatalf("nil signer should return nil error, got: %v", err)
	}
	if _, ok := mock.pushedManifests["public-key"]; ok {
		t.Error("should not push manifest when signer is nil")
	}
}

func TestStageMergeIndex(t *testing.T) {
	mock := newMockStore()
	// No index digest set → slow path, FetchIndex returns the existing index
	pub := &Publisher{logger: log.DefaultLogger{}}

	h1, _ := types.ParseNixHash("0abc1234567890abc1234567890abc12")
	d1, _ := types.ParseOciDigest("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	h2, _ := types.ParseNixHash("1d8f2345678901d8f2345678901d8f23")
	d2, _ := types.ParseOciDigest("sha256:2222222222222222222222222222222222222222222222222222222222222222")
	results := []uploadResult{
		{
			hash:    h1,
			name:    "test-package",
			narinfo: "StorePath: /nix/store/test-package\n",
			digest:  d1,
			size:    2048,
			refs:    []string{"/nix/store/dep1"},
		},
		{
			hash:    h2,
			name:    "another-package",
			narinfo: "StorePath: /nix/store/another-package\n",
			digest:  d2,
			size:    4096,
			refs:    []string{"/nix/store/dep1", "/nix/store/dep2"},
		},
	}

	err := pub.stageMergeIndex(context.Background(), mock, mock.index, "sha256:initial", results)
	if err != nil {
		t.Fatalf("stageMergeIndex failed: %v", err)
	}

	if mock.pushedIndex == nil {
		t.Fatal("expected index to be pushed")
	}

	for _, res := range results {
		entry, ok := mock.pushedIndex.Entries[res.hash.String()]
		if !ok {
			t.Errorf("expected entry for hash %s", res.hash)
			continue
		}
		if entry.Name != res.name {
			t.Errorf("entry %s name = %q, want %q", res.hash, entry.Name, res.name)
		}
	}
}

func TestStageMergeIndex_EmptyResults(t *testing.T) {
	mock := newMockStore()
	pub := &Publisher{logger: log.DefaultLogger{}}

	err := pub.stageMergeIndex(context.Background(), mock, mock.index, "sha256:initial", nil)
	if err != nil {
		t.Fatalf("stageMergeIndex with empty results failed: %v", err)
	}

	if mock.pushedIndex != nil {
		t.Fatal("expected index NOT to be pushed with empty results")
	}
}

func TestStageDiffIndex_WithFakeRunner(t *testing.T) {
	fakeNix := nix.NewFakeRunner()
	fakeNix.Closures["/nix/store/0abc1234567890abc1234567890abc12-pkg"] = []string{
		"/nix/store/0abc1234567890abc1234567890abc12-pkg",
		"/nix/store/1def2345678901def2345678901def23-dep",
	}
	fakeNix.PathInfos["/nix/store/0abc1234567890abc1234567890abc12-pkg"] = nix.PathInfo{
		Path:    "/nix/store/0abc1234567890abc1234567890abc12-pkg",
		NarHash: "sha256-BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		NarSize: 4096,
	}
	fakeNix.PathInfos["/nix/store/1def2345678901def2345678901def23-dep"] = nix.PathInfo{
		Path:    "/nix/store/1def2345678901def2345678901def23-dep",
		NarHash: "sha256-CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=",
		NarSize: 2048,
	}

	mock := newMockStore()
	pub := &Publisher{runner: fakeNix, skipUpstream: false, logger: log.DefaultLogger{}}

	result, err := pub.stageDiffIndex(context.Background(), mock, []string{"/nix/store/0abc1234567890abc1234567890abc12-pkg"}, nil, nil)
	if err != nil {
		t.Fatalf("stageDiffIndex failed: %v", err)
	}

	if len(result.uploadList) != 2 {
		t.Fatalf("expected 2 paths in upload list, got %d", len(result.uploadList))
	}

	if result.index == nil {
		t.Error("expected index to be returned")
	}
}

func TestPublishSingle_WithFakeRunner(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	signerKey := "test-key:" + base64.StdEncoding.EncodeToString(priv)
	signer, err := nix.NewSignerFromKey(signerKey)
	if err != nil {
		t.Fatalf("NewSignerFromKey failed: %v", err)
	}

	fakeNix := nix.NewFakeRunner()
	mock := newMockStore()
	pub := &Publisher{
		signer:  signer,
		cache:   NewExportCache(),
		runner:  fakeNix,
		comp:    "zstd",
		Profile: false,
		logger:  log.DefaultLogger{},
	}

	info := nix.PathInfo{
		Path:       "/nix/store/0abc1234567890abc1234567890abc12-pkg",
		NarHash:    "sha256-BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		NarSize:    4096,
		References: []string{"/nix/store/1def2345678901def2345678901def23-dep"},
	}

	result, err := pub.publishSingle(context.Background(), mock, info)
	if err != nil {
		t.Fatalf("publishSingle failed: %v", err)
	}

	if result.hash != "0abc1234567890abc1234567890abc12" {
		t.Errorf("hash = %q, want 0abc1234567890abc1234567890abc12", result.hash)
	}
	if result.name != "pkg" {
		t.Errorf("name = %q, want pkg", result.name)
	}
	if !strings.Contains(result.narinfo, "StorePath: /nix/store/0abc1234567890abc1234567890abc12-pkg") {
		t.Errorf("narinfo missing StorePath, got: %s", result.narinfo)
	}
	if !strings.Contains(result.narinfo, "test-key:") {
		t.Errorf("narinfo missing signature, got: %s", result.narinfo)
	}
}

func TestStageEnsurePublicKey_SkipsIfExists(t *testing.T) {
	mock := newMockStore()
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := &Publisher{signer: &nix.Signer{KeyName: "test-key", PrivateKey: priv}, Profile: true, logger: log.DefaultLogger{}}

	// Pre-populate: public-key manifest already exists
	mock.manifestDigests["public-key"] = "sha256:existing"

	err := pub.stageEnsurePublicKey(context.Background(), mock)
	if err != nil {
		t.Fatalf("stageEnsurePublicKey failed: %v", err)
	}

	// Should NOT have pushed a new manifest
	if _, ok := mock.pushedManifests["public-key"]; ok {
		t.Error("should not push manifest when it already exists")
	}
}

func TestStageMergeIndex_FastPath(t *testing.T) {
	mock := newMockStore()
	pub := &Publisher{Profile: true, logger: log.DefaultLogger{}}

	// Simulate: remote index has same digest as initial → fast-path
	initialDigest := "sha256:abc123"
	mock.manifestDigests["noci-index"] = initialDigest

	initialIndex := oci.NewIndex("ghcr.io", "test/repo")
	results := []uploadResult{
		{hash: "0abc1234567890abc1234567890abc12", name: "pkg", narinfo: "test", digest: "sha256:1111", size: 100},
	}

	err := pub.stageMergeIndex(context.Background(), mock, initialIndex, initialDigest, results)
	if err != nil {
		t.Fatalf("stageMergeIndex failed: %v", err)
	}

	// Should have pushed the SAME index object (fast-path reused it)
	if mock.pushedIndex != initialIndex {
		t.Error("fast-path should reuse the initial index object")
	}

	// Entry should be added
	if _, ok := mock.pushedIndex.Entries["0abc1234567890abc1234567890abc12"]; !ok {
		t.Error("expected entry to be added to fast-path index")
	}
}

func TestStageMergeIndex_SlowPath(t *testing.T) {
	mock := newMockStore()
	pub := &Publisher{logger: log.DefaultLogger{}}

	// Simulate: remote index has DIFFERENT digest → slow-path re-fetches
	initialDigest := "sha256:old"
	mock.manifestDigests["noci-index"] = "sha256:new"

	initialIndex := oci.NewIndex("ghcr.io", "test/repo")
	results := []uploadResult{
		{hash: "0abc1234567890abc1234567890abc12", name: "pkg", narinfo: "test", digest: "sha256:1111", size: 100},
	}

	err := pub.stageMergeIndex(context.Background(), mock, initialIndex, initialDigest, results)
	if err != nil {
		t.Fatalf("stageMergeIndex failed: %v", err)
	}

	// Should have pushed a DIFFERENT index object (slow-path re-fetched)
	if mock.pushedIndex == initialIndex {
		t.Error("slow-path should NOT reuse the initial index object")
	}

	// Entry should still be added
	if _, ok := mock.pushedIndex.Entries["0abc1234567890abc1234567890abc12"]; !ok {
		t.Error("expected entry to be added to slow-path index")
	}
}
