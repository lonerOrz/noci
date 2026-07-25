package publisher

import (
	"context"
	"noci/pkg/nix"
	"noci/pkg/oci"
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
