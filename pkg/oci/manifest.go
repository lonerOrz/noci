package oci

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"noci/pkg/domain/types"
	"noci/pkg/log"
	"os"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// manifestService handles OCI manifest CRUD, index management, and tag listing.
type manifestService struct {
	transport *Transport
	blobs     *blobService
	profile   *bool // shared with Client.Profile
}

func newManifestService(t *Transport, b *blobService, profile *bool) *manifestService {
	return &manifestService{transport: t, blobs: b, profile: profile}
}

// FetchManifest retrieves a manifest by tag.
func (ms *manifestService) FetchManifest(ctx context.Context, tag string) (*OCIManifest, error) {
	resp, err := ms.transport.request(ctx, "GET", "/manifests/"+tag, nil, MediaTypeImageManifest)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest %s not found: HTTP %d", tag, resp.StatusCode)
	}

	var manifest OCIManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// PushManifest uploads a manifest by tag.
func (ms *manifestService) PushManifest(ctx context.Context, tag string, manifest *OCIManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	resp, err := ms.transport.request(ctx, "PUT", "/manifests/"+tag, bytes.NewReader(data), MediaTypeImageManifest)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload manifest: HTTP %d, %s", resp.StatusCode, string(bodyBytes))
	}
	return nil
}

// ManifestExists checks if a manifest tag exists and returns the digest.
func (ms *manifestService) ManifestExists(ctx context.Context, tag string) (bool, string) {
	resp, err := ms.transport.request(ctx, "HEAD", "/manifests/"+tag, nil, MediaTypeImageManifest)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, resp.Header.Get("Docker-Content-Digest")
	}
	return false, ""
}

// DeleteManifest performs strategy-based physical deletion.
func (ms *manifestService) DeleteManifest(ctx context.Context, tag string) error {
	resp, err := ms.transport.request(ctx, "DELETE", "/manifests/"+tag, nil, "")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
			return nil
		}
		if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return ms.fallbackUntagManifest(ctx, tag)
		}
		return fmt.Errorf("delete manifest failed: HTTP %d", resp.StatusCode)
	}
	return err
}

func (ms *manifestService) fallbackUntagManifest(ctx context.Context, tag string) error {
	if ms.profile != nil && *ms.profile {
		log.Info("[profile] OCI DELETE blocked (405). Falling back to tag-overwriting (untagging) for tag: %s", tag)
	}

	dummyManifest := OCIManifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeImageManifest,
		Config:        DefaultEmptyConfigDescriptor(),
		Annotations: map[string]string{
			"org.nix.evicted": "true",
		},
	}

	return ms.PushManifest(ctx, tag, &dummyManifest)
}

// RepairIndexEntry rebuilds an index entry from the manifest annotations.
func (ms *manifestService) RepairIndexEntry(ctx context.Context, hash string, index *CacheIndex) error {
	manifest, err := ms.FetchManifest(ctx, hash)
	if err != nil {
		return fmt.Errorf("fetch manifest %s: %w", hash, err)
	}

	if manifest.Annotations == nil {
		return fmt.Errorf("manifest %s has no annotations", hash)
	}

	name := manifest.Annotations["org.nix.name"]
	narinfo := manifest.Annotations["org.nix.narinfo"]
	refsStr := manifest.Annotations["org.nix.references"]

	var refs []string
	if refsStr != "" {
		refs = strings.Split(refsStr, ",")
	}

	if len(manifest.Layers) == 0 {
		return fmt.Errorf("manifest %s has no layers", hash)
	}

	digest := manifest.Layers[0].Digest
	size := manifest.Layers[0].Size

	nixHash, hErr := types.ParseNixHash(hash)
	ociDigest, dErr := types.ParseOciDigest(digest)
	if hErr != nil {
		return fmt.Errorf("invalid nix hash %q: %w", hash, hErr)
	}
	if dErr != nil {
		return fmt.Errorf("invalid digest %q: %w", digest, dErr)
	}
	index.AddEntry(nixHash, name, narinfo, ociDigest, size, refs)
	return nil
}

// GetBlobRedirectURL gets the redirect location for a blob by digest.
func (ms *manifestService) GetBlobRedirectURL(ctx context.Context, digest string) (string, error) {
	resp, err := ms.transport.request(ctx, "GET", "/blobs/"+digest, nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusFound {
		return resp.Header.Get("Location"), nil
	}
	return "", fmt.Errorf("redirect failed: HTTP %d", resp.StatusCode)
}

// FetchIndex retrieves and decompresses the cache index.
func (ms *manifestService) FetchIndex(ctx context.Context) (*CacheIndex, error) {
	manifest, err := ms.FetchManifest(ctx, "noci-index")
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return NewIndex(ms.transport.registry, ms.transport.repo), nil
		}
		return nil, err
	}

	if len(manifest.Layers) == 0 {
		return NewIndex(ms.transport.registry, ms.transport.repo), nil
	}

	data, err := ms.blobs.downloadBlob(ctx, manifest.Layers[0].Digest)
	if err != nil {
		return nil, err
	}

	if len(data) >= 4 && data[0] == 0x28 && data[1] == 0xB5 && data[2] == 0x2F && data[3] == 0xFD {
		dec, err := zstd.NewReader(nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd reader: %w", err)
		}
		decoded, err := dec.DecodeAll(data, nil)
		dec.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to decompress zstd index: %w", err)
		}
		data = decoded
	} else if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader for index: %w", err)
		}
		decoded, err := io.ReadAll(gr)
		gr.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to decompress gzip index: %w", err)
		}
		data = decoded
	}

	var idx CacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	idx.Upgrade()
	return &idx, nil
}

// PushIndex compresses and uploads the cache index.
func (ms *manifestService) PushIndex(ctx context.Context, idx *CacheIndex) error {
	pushStart := time.Now()
	idx.Generated = time.Now()
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	compressed := enc.EncodeAll(data, nil)
	enc.Close()

	tmp, err := os.CreateTemp("", "noci-index-*.zst")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), bytes.NewReader(compressed)); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	log.Action("Uploading index...")

	digest, _, err := ms.blobs.UploadBlob(ctx, tmp.Name(), hex.EncodeToString(h.Sum(nil)), "index", nil)
	if err != nil {
		return err
	}

	indexManifest := OCIManifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeImageManifest,
		Config:        DefaultEmptyConfigDescriptor(),
		Layers: []Descriptor{
			{
				MediaType: "application/vnd.noci.index.layer.v1+zstd",
				Digest:    digest,
				Size:      int64(len(compressed)),
			},
		},
		Annotations: map[string]string{
			"org.nix.index.generated_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := ms.PushManifest(ctx, "noci-index", &indexManifest); err != nil {
		return err
	}

	if ms.profile != nil && *ms.profile {
		log.Info("[profile] PushIndex: total=%v", time.Since(pushStart))
	}
	return nil
}

// ListTags returns all tags in the repository, following pagination links.
func (ms *manifestService) ListTags(ctx context.Context) ([]string, error) {
	var allTags []string
	path := "/tags/list"

	for {
		resp, err := ms.transport.request(ctx, "GET", path, nil, "")
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to list tags: HTTP %d", resp.StatusCode)
		}

		var res struct {
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		allTags = append(allTags, res.Tags...)

		link := resp.Header.Get("Link")
		if link == "" {
			break
		}

		next := parseNextLink(link)
		if next == "" {
			break
		}
		path = next
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}

	return allTags, nil
}

func parseNextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start == -1 || end == -1 || end <= start {
			continue
		}
		rawURL := part[start+1 : end]
		u, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		path := u.Path
		q := u.RawQuery
		if idx := strings.LastIndex(path, "/tags/list"); idx != -1 {
			path = path[idx:]
		}
		if q != "" {
			return path + "?" + q
		}
		return path
	}
	return ""
}
