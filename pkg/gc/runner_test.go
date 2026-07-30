package gc

import (
	"context"
	"noci/pkg/domain/types"
	"noci/pkg/oci"
	"testing"
	"time"
)

type mockGCStore struct {
	index       *oci.CacheIndex
	pushedIndex *oci.CacheIndex
	deletedTags []string
}

func (m *mockGCStore) FetchIndex(_ context.Context) (*oci.CacheIndex, error) {
	return m.index, nil
}

func (m *mockGCStore) PushIndex(_ context.Context, idx *oci.CacheIndex) error {
	m.pushedIndex = idx
	return nil
}

func (m *mockGCStore) DeleteManifest(_ context.Context, tag string) error {
	m.deletedTags = append(m.deletedTags, tag)
	return nil
}

func (m *mockGCStore) ManifestExists(_ context.Context, _ string) (bool, string) { return false, "" }
func (m *mockGCStore) FetchManifest(_ context.Context, _ string) (*oci.OCIManifest, error) {
	return nil, nil
}
func (m *mockGCStore) PushManifest(_ context.Context, _ string, _ *oci.OCIManifest) error { return nil }
func (m *mockGCStore) UploadBlob(_ context.Context, _, _, _ string, _ oci.ProgressNotifier) (string, int64, error) {
	return "", 0, nil
}
func (m *mockGCStore) DeleteBlob(_ context.Context, _ string) error { return nil }
func (m *mockGCStore) ListTags(_ context.Context) ([]string, error) { return nil, nil }
func (m *mockGCStore) GetBlobRedirectURL(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (m *mockGCStore) RepairIndexEntry(_ context.Context, _ string, _ *oci.CacheIndex) error {
	return nil
}

func TestRunner_DryRun_NoPhysicalDelete(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {
				Name:       "old-pkg",
				NarSize:    100,
				UploadedAt: now.Add(-24 * time.Hour),
				LastUsed:   now.Add(-24 * time.Hour),
			},
		},
		Roots: map[string]oci.GCRoot{},
	}

	store := &mockGCStore{index: idx}
	runner := NewRunner(store, 1*time.Hour, 0, false, true) // dryRun=true

	res, err := runner.Run(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if res.EvictedCount != 1 {
		t.Errorf("EvictedCount = %d, want 1", res.EvictedCount)
	}
	if store.pushedIndex != nil {
		t.Error("dry run should not push index")
	}
	if len(store.deletedTags) != 0 {
		t.Error("dry run should not delete tags")
	}
}

func TestRunner_PhysicalSweep(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {
				Name:       "old-pkg",
				NarSize:    100,
				UploadedAt: now.Add(-24 * time.Hour),
				LastUsed:   now.Add(-24 * time.Hour),
			},
			"bbb22222222222222222222222222222": {
				Name:       "another-old",
				NarSize:    200,
				UploadedAt: now.Add(-24 * time.Hour),
				LastUsed:   now.Add(-24 * time.Hour),
			},
		},
		Roots: map[string]oci.GCRoot{},
	}

	store := &mockGCStore{index: idx}
	runner := NewRunner(store, 1*time.Hour, 0, true, false) // physicalSweep=true, dryRun=false

	res, err := runner.Run(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if res.EvictedCount != 2 {
		t.Errorf("EvictedCount = %d, want 2", res.EvictedCount)
	}
	if store.pushedIndex == nil {
		t.Error("expected index to be pushed")
	}
	if len(store.deletedTags) != 2 {
		t.Errorf("deletedTags = %d, want 2", len(store.deletedTags))
	}
}

func TestRunner_CascadeEvict(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {
				Name:       "pkg-a",
				NarSize:    100,
				UploadedAt: now.Add(-24 * time.Hour),
				References: []string{"/nix/store/bbb22222222222222222222222222222-dep"},
			},
			"bbb22222222222222222222222222222": {
				Name:       "dep-b",
				NarSize:    200,
				UploadedAt: now.Add(-24 * time.Hour),
			},
		},
		Roots: map[string]oci.GCRoot{},
	}

	store := &mockGCStore{index: idx}
	runner := NewRunner(store, 1*time.Hour, 0, false, false)

	h, _ := types.ParseNixHash("bbb22222222222222222222222222222")
	res, err := runner.Run(context.Background(), 0, []types.NixHash{h})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if res.EvictedCount != 2 {
		t.Errorf("EvictedCount = %d, want 2 (cascade: bbb + aaa)", res.EvictedCount)
	}
}

func TestRunner_NoEviction(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {
				Name:       "fresh-pkg",
				NarSize:    100,
				UploadedAt: now, // just uploaded, within grace period
			},
		},
		Roots: map[string]oci.GCRoot{
			"aaa11111111111111111111111111111": {PinnedAt: now, TTL: 0},
		},
	}

	store := &mockGCStore{index: idx}
	runner := NewRunner(store, 1*time.Hour, 0, false, false)

	res, err := runner.Run(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if res.EvictedCount != 0 {
		t.Errorf("EvictedCount = %d, want 0", res.EvictedCount)
	}
	if store.pushedIndex != nil {
		t.Error("no eviction means no index push")
	}
}
