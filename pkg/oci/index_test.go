package oci

import (
	"noci/pkg/domain/types"
	"testing"
	"time"
)

func TestNewIndex(t *testing.T) {
	idx := NewIndex("ghcr.io", "user/repo")
	if idx.Version != 2 {
		t.Errorf("Version = %d, want 2", idx.Version)
	}
	if idx.Registry != "ghcr.io" {
		t.Errorf("Registry = %q, want ghcr.io", idx.Registry)
	}
	if idx.Repo != "user/repo" {
		t.Errorf("Repo = %q, want user/repo", idx.Repo)
	}
	if idx.Image != "ghcr.io/user/repo/nix-cache" {
		t.Errorf("Image = %q", idx.Image)
	}
	if idx.Roots == nil {
		t.Error("Roots should be initialized")
	}
	if idx.Entries == nil {
		t.Error("Entries should be initialized")
	}
}

func TestUpgrade_V1ToV2(t *testing.T) {
	now := time.Now()
	idx := &CacheIndex{
		Version: 1,
		GCRootsV1: []string{
			"aaa11111111111111111111111111111",
			"bbb22222222222222222222222222222",
		},
		Entries: map[string]IndexItem{
			"aaa11111111111111111111111111111": {
				Name:    "pkg-a",
				Added:   now.Add(-time.Hour),
				NarInfo: "References: aaa11111111111111111111111111111-pkg\n",
			},
		},
	}

	idx.Upgrade()

	if idx.Version != 2 {
		t.Errorf("Version after upgrade = %d, want 2", idx.Version)
	}
	if idx.Roots == nil {
		t.Fatal("Roots should be initialized after upgrade")
	}
	if len(idx.Roots) != 2 {
		t.Errorf("Roots count = %d, want 2", len(idx.Roots))
	}
	if _, exists := idx.Roots["aaa11111111111111111111111111111"]; !exists {
		t.Error("aaa root should exist after upgrade")
	}
	if idx.GCRootsV1 != nil {
		t.Error("GCRootsV1 should be nil after upgrade")
	}
}

func TestUpgrade_FillsZeroFields(t *testing.T) {
	now := time.Now()
	idx := &CacheIndex{
		Version: 1,
		Entries: map[string]IndexItem{
			"aaa11111111111111111111111111111": {
				Name:  "pkg-a",
				Added: now.Add(-time.Hour),
				// LastUsed and UploadedAt are zero
			},
		},
	}

	idx.Upgrade()

	entry := idx.Entries["aaa11111111111111111111111111111"]
	if !entry.LastUsed.Equal(entry.Added) {
		t.Errorf("LastUsed should be filled from Added, got %v", entry.LastUsed)
	}
	if !entry.UploadedAt.Equal(entry.Added) {
		t.Errorf("UploadedAt should be filled from Added, got %v", entry.UploadedAt)
	}
}

func TestUpgrade_ParsesReferences(t *testing.T) {
	idx := &CacheIndex{
		Version: 1,
		Entries: map[string]IndexItem{
			"aaa11111111111111111111111111111": {
				Name:  "pkg-a",
				Added: time.Now(),
				NarInfo: "StorePath: /nix/store/aaa11111111111111111111111111111-pkg\n" +
					"References: bbb22222222222222222222222222222-dep ccc33333333333333333333333333333-deep\n",
			},
		},
	}

	idx.Upgrade()

	entry := idx.Entries["aaa11111111111111111111111111111"]
	if len(entry.References) != 2 {
		t.Errorf("References count = %d, want 2", len(entry.References))
	}
	if entry.References[0] != "/nix/store/bbb22222222222222222222222222222-dep" {
		t.Errorf("References[0] = %q", entry.References[0])
	}
}

func TestAddEntry(t *testing.T) {
	idx := NewIndex("ghcr.io", "user/repo")
	before := time.Now()
	h, _ := types.ParseNixHash("aaa11111111111111111111111111111")
	d, _ := types.ParseOciDigest("sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	idx.AddEntry(
		h,
		"pkg-a",
		"StorePath: ...",
		d,
		1024,
		[]string{"/nix/store/bbb-dep"},
	)
	after := time.Now()

	entry := idx.Entries["aaa11111111111111111111111111111"]
	if entry.Name != "pkg-a" {
		t.Errorf("Name = %q", entry.Name)
	}
	// NarDigest should have sha256: prefix stripped
	if entry.NarDigest != "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890" {
		t.Errorf("NarDigest = %q", entry.NarDigest)
	}
	if entry.NarSize != 1024 {
		t.Errorf("NarSize = %d", entry.NarSize)
	}
	if len(entry.References) != 1 {
		t.Errorf("References = %v", entry.References)
	}
	if entry.Added.Before(before) || entry.Added.After(after) {
		t.Errorf("Added time out of range: %v", entry.Added)
	}
}

func TestPinRoot(t *testing.T) {
	idx := NewIndex("ghcr.io", "user/repo")
	before := time.Now()
	h, _ := types.ParseNixHash("aaa11111111111111111111111111111")
	idx.PinRoot(h, 3600)
	after := time.Now()

	root, exists := idx.Roots["aaa11111111111111111111111111111"]
	if !exists {
		t.Fatal("PinRoot should create root entry")
	}
	if root.TTL != 3600 {
		t.Errorf("TTL = %d, want 3600", root.TTL)
	}
	if root.PinnedAt.Before(before) || root.PinnedAt.After(after) {
		t.Errorf("PinnedAt out of range: %v", root.PinnedAt)
	}
}

func TestPinRoot_NilRoots(t *testing.T) {
	idx := &CacheIndex{Version: 2}
	h, _ := types.ParseNixHash("aaa11111111111111111111111111111")
	idx.PinRoot(h, 0)
	if idx.Roots == nil {
		t.Fatal("PinRoot should initialize Roots map")
	}
}

func TestParseReferencesFromNarInfo(t *testing.T) {
	narinfo := "StorePath: /nix/store/aaa-pkg\nReferences: bbb-dep ccc-deep ddd-leaf\nSig: key:sig"
	refs := parseReferencesFromNarInfo(narinfo)
	if len(refs) != 3 {
		t.Fatalf("refs count = %d, want 3", len(refs))
	}
	if refs[0] != "/nix/store/bbb-dep" {
		t.Errorf("refs[0] = %q", refs[0])
	}
}

func TestParseReferencesFromNarInfo_Empty(t *testing.T) {
	refs := parseReferencesFromNarInfo("StorePath: /nix/store/aaa-pkg\n")
	if len(refs) != 0 {
		t.Errorf("refs should be empty, got %v", refs)
	}
}
