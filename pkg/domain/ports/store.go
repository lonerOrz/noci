package ports

import (
	"context"
	"io"
	"noci/pkg/domain/types"
	"noci/pkg/oci"
)

type CacheStore interface {
	FetchIndex(ctx context.Context) (*oci.CacheIndex, error)
	PushIndex(ctx context.Context, idx *oci.CacheIndex) error
	StreamBlob(ctx context.Context, digest types.OciDigest, w io.Writer) error
	DeleteManifest(ctx context.Context, tag string) error
	ManifestExists(ctx context.Context, tag string) (bool, string)
	FetchManifest(ctx context.Context, tag string) (*oci.OCIManifest, error)
	CanWrite(ctx context.Context) bool
}
