package oci

import "context"

// IndexReader reads the cache index and checks manifest existence.
type IndexReader interface {
	FetchIndex(ctx context.Context) (*CacheIndex, error)
	ManifestExists(ctx context.Context, tag string) (bool, string)
}

// BlobStore uploads, downloads, and deletes blobs.
type BlobStore interface {
	UploadBlob(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error)
	DeleteBlob(ctx context.Context, digest string) error
}

// ManifestStore performs manifest CRUD, index push, tag listing, and repair.
type ManifestStore interface {
	FetchManifest(ctx context.Context, tag string) (*OCIManifest, error)
	PushManifest(ctx context.Context, tag string, manifest *OCIManifest) error
	DeleteManifest(ctx context.Context, tag string) error
	PushIndex(ctx context.Context, idx *CacheIndex) error
	ListTags(ctx context.Context) ([]string, error)
	RepairIndexEntry(ctx context.Context, hash string, index *CacheIndex) error
	GetBlobRedirectURL(ctx context.Context, digest string) (string, error)
}

// Store defines the extensible storage contract for Nix caches over OCI/Object stores.
type Store interface {
	IndexReader
	BlobStore
	ManifestStore
}

var _ Store = (*Client)(nil)
