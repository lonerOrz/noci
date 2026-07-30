package oci

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"noci/pkg/domain/types"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// Client is the composition root for OCI registry operations.
// It delegates to Transport (auth/retry), blobService (upload/download/delete),
// and manifestService (CRUD/index/tags) internally.
type Client struct {
	transport *Transport
	blobs     *blobService
	manifests *manifestService

	Profile bool
}

func NewClient(registry, repo, token string) *Client {
	if registry == "" || repo == "" {
		panic("oci: registry and repo must not be empty")
	}
	t := NewTransport(registry, repo, token)
	c := &Client{
		transport: t,
	}
	c.blobs = newBlobService(t, &c.Profile)
	c.manifests = newManifestService(t, c.blobs, &c.Profile)
	return c
}

func (c *Client) SetHTTPClient(hc *http.Client) {
	c.transport.SetHTTPClient(hc)
}

// --- Store interface delegation ---

func (c *Client) FetchIndex(ctx context.Context) (*CacheIndex, error) {
	return c.manifests.FetchIndex(ctx)
}

func (c *Client) PushIndex(ctx context.Context, idx *CacheIndex) error {
	return c.manifests.PushIndex(ctx, idx)
}

func (c *Client) FetchManifest(ctx context.Context, tag string) (*OCIManifest, error) {
	return c.manifests.FetchManifest(ctx, tag)
}

func (c *Client) PushManifest(ctx context.Context, tag string, manifest *OCIManifest) error {
	return c.manifests.PushManifest(ctx, tag, manifest)
}

func (c *Client) ManifestExists(ctx context.Context, tag string) (bool, string) {
	return c.manifests.ManifestExists(ctx, tag)
}

func (c *Client) UploadBlob(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error) {
	return c.blobs.UploadBlob(ctx, filePath, sha256Hex, description, notifier)
}

func (c *Client) DeleteManifest(ctx context.Context, tag string) error {
	return c.manifests.DeleteManifest(ctx, tag)
}

func (c *Client) RepairIndexEntry(ctx context.Context, hash string, index *CacheIndex) error {
	return c.manifests.RepairIndexEntry(ctx, hash, index)
}

func (c *Client) DeleteBlob(ctx context.Context, digest string) error {
	return c.blobs.DeleteBlob(ctx, digest)
}

func (c *Client) ListTags(ctx context.Context) ([]string, error) {
	return c.manifests.ListTags(ctx)
}

func (c *Client) GetBlobRedirectURL(ctx context.Context, digest string) (string, error) {
	return c.manifests.GetBlobRedirectURL(ctx, digest)
}

// --- ports.CacheStore implementation ---

func (c *Client) StreamBlob(ctx context.Context, digest types.OciDigest, w io.Writer) error {
	resp, err := c.transport.rawRequest(ctx, "GET", "/blobs/"+digest.String(), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		return &BlobRedirectError{StatusCode: resp.StatusCode, Location: loc}
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("blob streaming failed with status %d", resp.StatusCode)
	}

	_, err = io.Copy(w, resp.Body)
	return err
}

func (c *Client) CanWrite(ctx context.Context) bool {
	resp, err := c.transport.rawRequest(ctx, "PUT", "/manifests/noci-probe-write", nil, "")
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "401") || strings.Contains(errStr, "403") ||
			strings.Contains(errStr, "unauthorized") || strings.Contains(errStr, "forbidden") {
			return false
		}
		return true // non-auth error means we reached the server
	}
	if resp != nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return false
		}
	}

	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.manifests.DeleteManifest(cleanupCtx, "noci-probe-write")
	}()

	return true
}

// CheckCacheStatus checks if a manifest exists and whether it's evicted.
func (c *Client) CheckCacheStatus(ctx context.Context, tag string) (exists bool, isEvicted bool) {
	resp, err := c.transport.request(ctx, "HEAD", "/manifests/"+tag, nil, MediaTypeImageManifest)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return false, false
	}
	resp.Body.Close()

	m, err := c.FetchManifest(ctx, tag)
	if err != nil {
		return true, false
	}
	if m.Annotations != nil && m.Annotations["org.nix.evicted"] == "true" {
		return true, true
	}
	return true, false
}

// BlobRedirectError is returned by StreamBlob when the registry issues a redirect.
type BlobRedirectError struct {
	StatusCode int
	Location   string
}

func (e *BlobRedirectError) Error() string {
	return fmt.Sprintf("blob redirect (%d) to %s", e.StatusCode, e.Location)
}

// --- Progress reporting ---

// ProgressNotifier reports upload progress.
type ProgressNotifier interface {
	Update(description string, offset, total int64)
	Finish()
}

// StderrProgressNotifier writes progress to stderr with TTY-aware formatting.
type StderrProgressNotifier struct {
	isTTY      bool
	lastPrint  time.Time
	lastBucket int
	finished   bool
	mu         sync.Mutex
}

func NewStderrProgressNotifier() *StderrProgressNotifier {
	return &StderrProgressNotifier{
		isTTY:      term.IsTerminal(int(os.Stderr.Fd())),
		lastBucket: -1,
	}
}

func (n *StderrProgressNotifier) Update(description string, offset, total int64) {
	if total <= 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	pct := float64(offset) * 100 / float64(total)
	if n.isTTY {
		now := time.Now()
		if n.lastPrint.IsZero() || offset == total || now.Sub(n.lastPrint) >= 200*time.Millisecond {
			fmt.Fprintf(os.Stderr, "\r\x1b[K▶ [noci] Uploading %s... %.1f%% (%s / %s)", description, pct, FormatSize(offset), FormatSize(total))
			n.lastPrint = now
		}
	} else {
		bucket := int(pct) / 10
		for bucket > n.lastBucket {
			n.lastBucket++
			fmt.Fprintf(os.Stderr, "▶ [noci] Uploading %s... %d%% (%s / %s)\n", description, n.lastBucket*10, FormatSize(offset), FormatSize(total))
		}
	}
}

func (n *StderrProgressNotifier) Finish() {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.finished {
		return
	}
	n.finished = true
	if n.isTTY {
		fmt.Fprintln(os.Stderr)
	}
}

// NoopProgressNotifier discards all progress updates.
type NoopProgressNotifier struct{}

func (n *NoopProgressNotifier) Update(string, int64, int64) {}
func (n *NoopProgressNotifier) Finish()                     {}

// FormatSize formats bytes into human-readable size strings.
func FormatSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; exp++ {
		n /= unit
		div *= unit
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
