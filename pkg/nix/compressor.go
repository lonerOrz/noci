package nix

import (
	"compress/gzip"
	"fmt"
	"io"
	"runtime"

	"github.com/klauspost/compress/zstd"
)

// Compressor abstracts NAR compression algorithm selection.
type Compressor interface {
	Name() string
	Extension() string
	MediaType() string
	WrapWriter(w io.Writer, concurrency, level int) (io.WriteCloser, error)
}

// ZstdCompressor implements Compressor for zstd.
type ZstdCompressor struct{}

func (z ZstdCompressor) Name() string      { return "zstd" }
func (z ZstdCompressor) Extension() string { return ".nar.zst" }
func (z ZstdCompressor) MediaType() string { return "application/vnd.nix.cache.layer.v1+tar+zstd" }
func (z ZstdCompressor) WrapWriter(w io.Writer, concurrency, level int) (io.WriteCloser, error) {
	if concurrency <= 0 {
		concurrency = runtime.NumCPU() / 2
		if concurrency < 1 {
			concurrency = 1
		} else if concurrency > 4 {
			concurrency = 4
		}
	}
	if level <= 0 {
		level = 3
	}
	return zstd.NewWriter(w,
		zstd.WithEncoderConcurrency(concurrency),
		zstd.WithEncoderLevel(zstd.EncoderLevel(level)),
	)
}

// GzipCompressor implements Compressor for gzip.
type GzipCompressor struct{}

func (g GzipCompressor) Name() string      { return "gzip" }
func (g GzipCompressor) Extension() string { return ".nar.gz" }
func (g GzipCompressor) MediaType() string { return "application/vnd.nix.cache.layer.v1+tar+gzip" }
func (g GzipCompressor) WrapWriter(w io.Writer, _, _ int) (io.WriteCloser, error) {
	return gzip.NewWriter(w), nil
}

// GetCompressor returns the Compressor for the given name.
// Empty string defaults to gzip.
func GetCompressor(name string) (Compressor, error) {
	switch name {
	case "zstd":
		return ZstdCompressor{}, nil
	case "gzip", "":
		return GzipCompressor{}, nil
	default:
		return nil, fmt.Errorf("unsupported compression algorithm: %s", name)
	}
}
