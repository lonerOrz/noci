package gc

import (
	"noci/pkg/oci"
	"testing"
	"time"
)

func TestGetHashFromPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/nix/store/0abc1234567890abc1234567890abc12-hello", "0abc1234567890abc1234567890abc12"},
		{"/nix/store/abcdfghijklmnpqrsvwxyz0000000000-pkg", "abcdfghijklmnpqrsvwxyz0000000000"},
		{"/nix/store/short-hash", ""},
		{"/nix/store/00000000000000000000000000000000-tool", "00000000000000000000000000000000"},
		{"not-a-store-path", ""},
		{"/nix/store/", ""},
	}

	for _, tt := range tests {
		got := getHashFromPath(tt.input)
		if got != tt.want {
			t.Errorf("getHashFromPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetHashFromPath_InvalidChars(t *testing.T) {
	// Characters not in Nix base32 alphabet: e, o, t, u
	hash := "01234567890abcde0000000000000000" // contains 'e'
	got := getHashFromPath("/nix/store/" + hash + "-pkg")
	if got != "" {
		t.Errorf("getHashFromPath with invalid char 'e' = %q, want empty", got)
	}
}

func TestNewEngine_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewEngine(nil) should panic")
		}
	}()
	NewEngine(nil, time.Hour)
}

func TestSweepGracePeriod(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {
				Name:       "pkg-a",
				NarSize:    1000,
				Added:      now,
				UploadedAt: now, // just uploaded
				LastUsed:   now,
			},
			"bbb22222222222222222222222222222": {
				Name:       "pkg-b",
				NarSize:    2000,
				Added:      now.Add(-24 * time.Hour),
				UploadedAt: now.Add(-24 * time.Hour), // old
				LastUsed:   now.Add(-24 * time.Hour),
			},
		},
		Roots: map[string]oci.GCRoot{},
	}

	eng := NewEngine(idx, 1*time.Hour)
	result := eng.Sweep(now, 0) // maxSize=0 means no limit

	// pkg-a is within grace period (just uploaded), pkg-b is old but no roots → candidate
	// With no maxSize, all candidates are evicted
	if result.EvictedCount != 1 {
		t.Errorf("EvictedCount = %d, want 1", result.EvictedCount)
	}
	if len(result.EvictedKeys) != 1 || result.EvictedKeys[0] != "bbb22222222222222222222222222222" {
		t.Errorf("EvictedKeys = %v, want [bbb22222222222222222222222222222]", result.EvictedKeys)
	}
}

func TestSweepMaxSize(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {
				Name:       "pkg-a",
				NarSize:    1000,
				Added:      now.Add(-2 * time.Hour),
				UploadedAt: now.Add(-2 * time.Hour),
				LastUsed:   now.Add(-2 * time.Hour),
			},
			"bbb22222222222222222222222222222": {
				Name:       "pkg-b",
				NarSize:    2000,
				Added:      now.Add(-2 * time.Hour),
				UploadedAt: now.Add(-2 * time.Hour),
				LastUsed:   now.Add(-1 * time.Hour), // used more recently
			},
			"ccc33333333333333333333333333333": {
				Name:       "pkg-c",
				NarSize:    3000,
				Added:      now.Add(-2 * time.Hour),
				UploadedAt: now.Add(-2 * time.Hour),
				LastUsed:   now.Add(-3 * time.Hour), // oldest
			},
		},
		Roots: map[string]oci.GCRoot{},
	}

	eng := NewEngine(idx, 0) // no grace period
	result := eng.Sweep(now, 3000) // maxSize = 3000

	// Total = 6000, need to evict down to 3000
	// Sorted by LastUsed ascending: ccc (oldest), aaa, bbb
	// Evict ccc (3000) → total = 3000, fits
	if result.EvictedCount != 1 {
		t.Errorf("EvictedCount = %d, want 1", result.EvictedCount)
	}
	if result.RetainedSize != 3000 {
		t.Errorf("RetainedSize = %d, want 3000", result.RetainedSize)
	}
}

func TestSweepWithRoots(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {
				Name:       "pkg-a",
				NarSize:    1000,
				Added:      now.Add(-2 * time.Hour),
				UploadedAt: now.Add(-2 * time.Hour),
				LastUsed:   now.Add(-2 * time.Hour),
				References: []string{"/nix/store/bbb22222222222222222222222222222-dep"},
			},
			"bbb22222222222222222222222222222": {
				Name:       "dep-b",
				NarSize:    2000,
				Added:      now.Add(-2 * time.Hour),
				UploadedAt: now.Add(-2 * time.Hour),
				LastUsed:   now.Add(-2 * time.Hour),
			},
		},
		Roots: map[string]oci.GCRoot{
			"aaa11111111111111111111111111111": {PinnedAt: now, TTL: 0}, // permanent root
		},
	}

	eng := NewEngine(idx, 0)
	result := eng.Sweep(now, 0)

	// pkg-a is a root → marked. pkg-b is referenced by pkg-a → also marked.
	if result.EvictedCount != 0 {
		t.Errorf("EvictedCount = %d, want 0 (root and deps retained)", result.EvictedCount)
	}
}

func TestCascadeEvict(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {
				Name:       "pkg-a",
				NarSize:    100,
				References: []string{"/nix/store/bbb22222222222222222222222222222-dep"},
			},
			"bbb22222222222222222222222222222": {
				Name:       "pkg-b",
				NarSize:    200,
				References: []string{"/nix/store/ccc33333333333333333333333333333-deep"},
			},
			"ccc33333333333333333333333333333": {
				Name:    "pkg-c",
				NarSize: 300,
			},
		},
		Roots: map[string]oci.GCRoot{
			"aaa11111111111111111111111111111": {PinnedAt: now, TTL: 0},
		},
	}

	eng := NewEngine(idx, time.Hour)
	result := eng.CascadeEvict([]string{"ccc33333333333333333333333333333"})

	// Cascading: ccc is target, bbb references ccc, aaa references bbb
	if result.EvictedCount != 3 {
		t.Errorf("EvictedCount = %d, want 3 (cascade)", result.EvictedCount)
	}
	if result.EvictedSize != 600 {
		t.Errorf("EvictedSize = %d, want 600", result.EvictedSize)
	}
}

func TestApply(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {Name: "keep", NarSize: 100},
			"bbb22222222222222222222222222222": {Name: "evict", NarSize: 200},
		},
		Roots: map[string]oci.GCRoot{
			"aaa11111111111111111111111111111": {PinnedAt: now, TTL: 0},
			"bbb22222222222222222222222222222": {PinnedAt: now, TTL: 1}, // expired
		},
	}

	eng := NewEngine(idx, 0)
	result := &Result{
		EvictedKeys:  []string{"bbb22222222222222222222222222222"},
		ExpiredRoots: []string{"bbb22222222222222222222222222222"},
	}

	eng.Apply(result)

	if _, exists := idx.Entries["bbb22222222222222222222222222222"]; exists {
		t.Error("bbb should have been deleted from Entries")
	}
	if _, exists := idx.Roots["bbb22222222222222222222222222222"]; exists {
		t.Error("bbb should have been deleted from Roots")
	}
	if _, exists := idx.Entries["aaa11111111111111111111111111111"]; !exists {
		t.Error("aaa should still exist")
	}
}

func TestSweepExpiredRoots(t *testing.T) {
	now := time.Now()
	idx := &oci.CacheIndex{
		Version: 2,
		Entries: map[string]oci.IndexItem{
			"aaa11111111111111111111111111111": {Name: "pkg-a", NarSize: 100},
		},
		Roots: map[string]oci.GCRoot{
			"aaa11111111111111111111111111111": {
				PinnedAt: now.Add(-2 * time.Hour),
				TTL:      3600, // 1 hour, expired
			},
		},
	}

	eng := NewEngine(idx, 0)
	result := eng.Sweep(now, 0)

	if len(result.ExpiredRoots) != 1 {
		t.Errorf("ExpiredRoots = %d, want 1", len(result.ExpiredRoots))
	}
	// The expired root's package is a candidate for eviction
	if result.EvictedCount != 1 {
		t.Errorf("EvictedCount = %d, want 1", result.EvictedCount)
	}
}
