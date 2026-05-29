package oci

import "time"

type CacheIndex struct {
	Version   int                  `json:"version"`
	Repo      string               `json:"repo"`
	Registry  string               `json:"registry"`
	Image     string               `json:"image"`
	Generated time.Time            `json:"generated"`
	PublicKey string               `json:"public_key"`
	Entries   map[string]IndexItem `json:"entries"`
	GCRoots   []string             `json:"gc_roots"`
}

type IndexItem struct {
	Name      string    `json:"name"`
	NarInfo   string    `json:"narinfo"`
	NarDigest string    `json:"nar_digest"`
	NarSize   int64     `json:"nar_size"`
	Added     time.Time `json:"added"`
}

func NewIndex(registry, repo string) *CacheIndex {
	return &CacheIndex{
		Version:   1,
		Repo:      repo,
		Registry:  registry,
		Image:     registry + "/" + repo + "/nix-cache",
		Generated: time.Now(),
		Entries:   make(map[string]IndexItem),
		GCRoots:   []string{},
	}
}

func (idx *CacheIndex) AddEntry(hash, name, narinfo, digest string, size int64) {
	idx.Entries[hash] = IndexItem{
		Name:      name,
		NarInfo:   narinfo,
		NarDigest: digest,
		NarSize:   size,
		Added:     time.Now(),
	}
	idx.Generated = time.Now()
}
