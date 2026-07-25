package oci

import "context"

// Store defines the extensible storage contract for Nix caches over OCI/Object stores.
type Store interface {
	FetchIndex(ctx context.Context) (*CacheIndex, error)
	PushIndex(ctx context.Context, idx *CacheIndex) error
	FetchManifest(ctx context.Context, tag string) (*OCIManifest, error)
	PushManifest(ctx context.Context, tag string, manifest *OCIManifest) error
	ManifestExists(ctx context.Context, tag string) (bool, string)
	UploadBlob(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error)
	DeleteManifest(ctx context.Context, tag string) error
	RepairIndexEntry(ctx context.Context, hash string, index *CacheIndex) error
	DeleteBlob(ctx context.Context, digest string) error
	ListTags(ctx context.Context) ([]string, error)
	GetBlobRedirectURL(ctx context.Context, digest string) (string, error)
}

var _ Store = (*Client)(nil)
