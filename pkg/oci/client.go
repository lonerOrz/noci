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
	"noci/pkg/log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/term"
)

type OCIManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type Descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type Client struct {
	registry      string
	repo          string
	token         string
	tokenMu       sync.Mutex
	ociTokenPull  string
	pullFetchTime time.Time
	ociTokenPush  string
	pushFetchTime time.Time
	client        *http.Client
	Profile       bool
}

func NewClient(registry, repo, token string) *Client {
	if registry == "" || repo == "" {
		panic("oci: registry and repo must not be empty")
	}
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &Client{
		registry: strings.ToLower(registry),
		repo:     strings.ToLower(repo),
		token:    token,
		client: &http.Client{
			Transport: transport,
		},
	}
}

func (c *Client) SetHTTPClient(hc *http.Client) {
	c.client = hc
}

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
func (n *NoopProgressNotifier) Finish()                      {}

const chunkedThreshold = 64 * 1024 * 1024 // 64MB

func (c *Client) getOciToken(ctx context.Context, actions string) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	// GHCR tokens expire in ~5 min; cache for 4 min to ensure fresh token on retry
	const tokenCacheTTL = 4 * time.Minute
	if actions == "pull" {
		if c.ociTokenPull != "" && time.Since(c.pullFetchTime) < tokenCacheTTL {
			return c.ociTokenPull, nil
		}
	} else {
		if c.ociTokenPush != "" && time.Since(c.pushFetchTime) < tokenCacheTTL {
			return c.ociTokenPush, nil
		}
	}

	scope := fmt.Sprintf("repository:%s/nix-cache:%s", c.repo, actions)
	url := fmt.Sprintf("https://%s/token?scope=%s&service=%s", c.registry, scope, c.registry)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	if c.token != "" {
		req.SetBasicAuth("token", c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("fetch token for %s failed: HTTP %d, %s", actions, resp.StatusCode, string(bodyBytes))
	}

	var res struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if actions == "pull" {
		c.ociTokenPull = res.Token
		c.pullFetchTime = time.Now()
	} else {
		c.ociTokenPush = res.Token
		c.pushFetchTime = time.Now()
	}
	return res.Token, nil
}

func (c *Client) Request(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	return c.doRequest(ctx, method, path, body, contentType, true)
}

func (c *Client) RawRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	return c.doRequest(ctx, method, path, body, contentType, false)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader, contentType string, followRedirects bool) (*http.Response, error) {
	actions := "pull"
	if method != "GET" && method != "HEAD" {
		actions = "pull,push"
	}
	token, err := c.getOciToken(ctx, actions)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://%s/v2/%s/nix-cache%s", c.registry, c.repo, path)

	doer := c.client.Do
	if !followRedirects {
		doer = c.getTransport().RoundTrip
	}
	return c.doWithRetry(ctx, method, url, token, contentType, body, doer)
}

func (c *Client) getTransport() http.RoundTripper {
	if c.client.Transport != nil {
		return c.client.Transport
	}
	return http.DefaultTransport
}

func (c *Client) doWithRetry(ctx context.Context, method, url, token, contentType string, body io.Reader, doer func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	const maxRetries = 3
	var resp *http.Response
	var err error
	backoff := time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Authorization", "Bearer "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Accept", contentType)
		}

		resp, err = doer(req)
		if err == nil {
			if resp.StatusCode < 500 {
				return resp, nil
			}
			resp.Body.Close()
		}

		if attempt < maxRetries {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return resp, err
}

func (c *Client) FetchManifest(ctx context.Context, tag string) (*OCIManifest, error) {
	resp, err := c.Request(ctx, "GET", "/manifests/"+tag, nil, "application/vnd.oci.image.manifest.v1+json")
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

func (c *Client) RepairIndexEntry(ctx context.Context, hash string, index *CacheIndex) error {
	manifest, err := c.FetchManifest(ctx, hash)
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

	index.AddEntry(hash, name, narinfo, digest, size, refs)
	return nil
}

func (c *Client) PushManifest(ctx context.Context, tag string, manifest *OCIManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	resp, err := c.Request(ctx, "PUT", "/manifests/"+tag, bytes.NewReader(data), "application/vnd.oci.image.manifest.v1+json")
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

func (c *Client) CheckCacheStatus(ctx context.Context, tag string) (exists bool, isEvicted bool) {
	resp, err := c.Request(ctx, "HEAD", "/manifests/"+tag, nil, "application/vnd.oci.image.manifest.v1+json")
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

func (c *Client) ManifestExists(ctx context.Context, tag string) (bool, string) {
	resp, err := c.Request(ctx, "HEAD", "/manifests/"+tag, nil, "application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, resp.Header.Get("Docker-Content-Digest")
	}
	return false, ""
}

func (c *Client) GetBlobRedirectURL(ctx context.Context, digest string) (string, error) {
	resp, err := c.Request(ctx, "GET", "/blobs/"+digest, nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusFound {
		return resp.Header.Get("Location"), nil
	}
	return "", fmt.Errorf("redirect failed: HTTP %d", resp.StatusCode)
}

func (c *Client) ListTags(ctx context.Context) ([]string, error) {
	var allTags []string
	path := "/tags/list"

	for {
		resp, err := c.Request(ctx, "GET", path, nil, "")
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
		// Only keep the relative endpoint path, stripping /v2/<repo>/nix-cache prefix.
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

// DeleteManifest performs strategy-based physical deletion.
func (c *Client) DeleteManifest(ctx context.Context, tag string) error {
	resp, err := c.Request(ctx, "DELETE", "/manifests/"+tag, nil, "")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
			return nil
		}
		// Registry may disable DELETE: 405 Method Not Allowed (GHCR),
		// or 401/403 token lacks delete permission (ECR, Harbor)
		if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return c.fallbackUntagManifest(ctx, tag)
		}
		return fmt.Errorf("delete manifest failed: HTTP %d", resp.StatusCode)
	}
	return err
}

// fallbackUntagManifest overwrites the tag pointer, making the original manifest untagged.
func (c *Client) fallbackUntagManifest(ctx context.Context, tag string) error {
	if c.Profile {
		log.Info("[profile] OCI DELETE blocked (405). Falling back to tag-overwriting (untagging) for tag: %s", tag)
	}

	// Push a tiny dummy empty manifest to the original tag,
	// immediately unbinding the NAR Manifest and leaving it untagged.
	dummyManifest := OCIManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: Descriptor{
			MediaType: "application/vnd.noci.dummy.config.v1+json",
			Size:      0,
			Digest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		Annotations: map[string]string{
			"org.nix.evicted": "true",
		},
	}

	return c.PushManifest(ctx, tag, &dummyManifest)
}

func (c *Client) FetchIndex(ctx context.Context) (*CacheIndex, error) {
	manifest, err := c.FetchManifest(ctx, "noci-index")
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return NewIndex(c.registry, c.repo), nil
		}
		return nil, err
	}

	if len(manifest.Layers) == 0 {
		return NewIndex(c.registry, c.repo), nil
	}

	data, err := c.downloadBlob(ctx, manifest.Layers[0].Digest)
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

func (c *Client) PushIndex(ctx context.Context, idx *CacheIndex) error {
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

	digest, _, err := c.UploadBlob(ctx, tmp.Name(), hex.EncodeToString(h.Sum(nil)), "index", nil)
	if err != nil {
		return err
	}

	indexManifest := OCIManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: Descriptor{
			MediaType: "application/vnd.noci.index.config.v1+json",
			Size:      0,
			Digest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
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
	if err := c.PushManifest(ctx, "noci-index", &indexManifest); err != nil {
		return err
	}

	if c.Profile {
		log.Info("[profile] PushIndex: total=%v", time.Since(pushStart))
	}
	return nil
}

// UploadBlob uploads a blob, auto-selecting monolithic or chunked strategy.
func (c *Client) UploadBlob(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error) {
	if notifier == nil {
		notifier = &NoopProgressNotifier{}
	}

	digest = "sha256:" + sha256Hex

	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, fmt.Errorf("failed to open blob file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("failed to stat blob file: %w", err)
	}
	size = stat.Size()

	headCtx, headCancel := context.WithTimeout(ctx, 30*time.Second)
	defer headCancel()
	if headResp, headErr := c.Request(headCtx, "HEAD", "/blobs/"+digest, nil, ""); headErr == nil {
		headResp.Body.Close()
		if headResp.StatusCode == http.StatusOK {
			if c.Profile {
				log.Info("[profile] Blob %s already exists (HEAD), skipped", sha256Hex[:12])
			}
			return digest, size, nil
		}
	} else if headResp != nil {
		headResp.Body.Close()
	}

	if size > chunkedThreshold {
		return c.uploadBlobChunked(ctx, filePath, sha256Hex, description, notifier)
	}
	return c.uploadBlobMonolithic(ctx, filePath, sha256Hex, description, notifier)
}

func (c *Client) uploadBlobMonolithic(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error) {
	uploadStart := time.Now()

	digest = "sha256:" + sha256Hex

	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, fmt.Errorf("failed to open blob file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("failed to stat blob file: %w", err)
	}
	size = stat.Size()

	token, err := c.getOciToken(ctx, "pull,push")
	if err != nil {
		return "", 0, err
	}

	initResp, err := c.Request(ctx, "POST", "/blobs/uploads/", nil, "")
	if err != nil {
		return "", 0, err
	}
	defer initResp.Body.Close()

	if initResp.StatusCode != http.StatusAccepted && initResp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(initResp.Body)
		return "", 0, fmt.Errorf("failed to initiate blob upload (HTTP %d): %s", initResp.StatusCode, string(bodyBytes))
	}

	uploadLocation := initResp.Header.Get("Location")
	if uploadLocation == "" {
		return "", 0, fmt.Errorf("registry didn't return upload location")
	}

	u, err := url.Parse(uploadLocation)
	if err != nil {
		return "", 0, fmt.Errorf("invalid upload location URL: %w", err)
	}
	if !u.IsAbs() {
		base, _ := url.Parse(fmt.Sprintf("https://%s", c.registry))
		u = base.ResolveReference(u)
	}
	uploadURL := u.String()

	uploadURL += "?digest=" + digest

	var putResp *http.Response
	var netTime time.Duration
	const maxRetries = 3
	backoff := time.Second
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Re-open file for retry
		if attempt > 0 {
			file.Seek(0, io.SeekStart)
			freshToken, tokenErr := c.getOciToken(ctx, "pull,push")
			if tokenErr != nil {
				return "", 0, fmt.Errorf("refresh token for monolithic upload: %w", tokenErr)
			}
			token = freshToken
		}

		body := file

		putReq, err := http.NewRequestWithContext(ctx, "PUT", uploadURL, body)
		if err != nil {
			return "", 0, fmt.Errorf("failed to create PUT request: %w", err)
		}
		putReq.Header.Set("Authorization", "Bearer "+token)
		putReq.Header.Set("Content-Type", "application/octet-stream")
		putReq.ContentLength = size

		startNet := time.Now()
		putResp, err = c.client.Do(putReq)
		netTime = time.Since(startNet)

		if err == nil && putResp.StatusCode < 500 {
			break
		}
		if putResp != nil {
			putResp.Body.Close()
		}
		if attempt < maxRetries {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	if err != nil {
		return "", 0, fmt.Errorf("monolithic upload failed: %w", err)
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusCreated && putResp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(putResp.Body)
		return "", 0, fmt.Errorf("monolithic upload failed: HTTP %d, %s", putResp.StatusCode, string(bodyBytes))
	}

	notifier.Update(description, size, size)
	notifier.Finish()

	if c.Profile {
		totalElapsed := time.Since(uploadStart)
		log.Info("[profile] Upload %s: total=%v net=%v (net %.1f%%) size=%s",
			description, totalElapsed, netTime,
			float64(netTime)/float64(totalElapsed)*100,
			FormatSize(size))
	}

	return digest, size, nil
}

func (c *Client) uploadBlobChunked(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error) {
	digest = "sha256:" + sha256Hex

	initResp, err := c.Request(ctx, "POST", "/blobs/uploads/", nil, "")
	if err != nil {
		return "", 0, err
	}
	defer initResp.Body.Close()

	if initResp.StatusCode != http.StatusAccepted && initResp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(initResp.Body)
		return "", 0, fmt.Errorf("failed to initiate chunked upload (HTTP %d): %s", initResp.StatusCode, string(bodyBytes))
	}

	location := initResp.Header.Get("Location")
	if location == "" {
		return "", 0, fmt.Errorf("registry didn't return upload location")
	}

	u, err := url.Parse(location)
	if err != nil {
		return "", 0, fmt.Errorf("invalid upload location URL: %w", err)
	}
	if !u.IsAbs() {
		base, _ := url.Parse(fmt.Sprintf("https://%s", c.registry))
		u = base.ResolveReference(u)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	size = stat.Size()

	var offset int64
	const chunkSize = 32 * 1024 * 1024
	buf := make([]byte, chunkSize)
	for offset < size {
		n, readErr := file.Read(buf)
		if n > 0 {
			chunkStart := offset
			chunkEnd := offset + int64(n) - 1

			var resp *http.Response
			const maxRetries = 3
			backoff := time.Second
			var patchErr error

			for attempt := 0; attempt <= maxRetries; attempt++ {
				select {
				case <-ctx.Done():
					return "", 0, ctx.Err()
				default:
				}

				if attempt > 0 {
					if _, seekErr := file.Seek(chunkStart, io.SeekStart); seekErr != nil {
						return "", 0, fmt.Errorf("seek for chunk retry: %w", seekErr)
					}
					if _, rErr := io.ReadFull(file, buf[:n]); rErr != nil {
						return "", 0, fmt.Errorf("re-read chunk: %w", rErr)
					}
				}

				freshToken, tokenErr := c.getOciToken(ctx, "pull,push")
				if tokenErr != nil {
					return "", 0, fmt.Errorf("get token: %w", tokenErr)
				}

				req, err := http.NewRequestWithContext(ctx, "PATCH", u.String(), bytes.NewReader(buf[:n]))
				if err != nil {
					return "", 0, err
				}
				req.Header.Set("Authorization", "Bearer "+freshToken)
				req.Header.Set("Content-Type", "application/octet-stream")
				req.Header.Set("Content-Length", strconv.Itoa(n))
				req.Header.Set("Content-Range", fmt.Sprintf("%d-%d", chunkStart, chunkEnd))
				req.ContentLength = int64(n)

				resp, err = c.client.Do(req)
				if err == nil {
					if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
						patchErr = nil
						break
					}
					if resp.StatusCode < 500 {
						bodyBytes, _ := io.ReadAll(resp.Body)
						resp.Body.Close()
						return "", 0, fmt.Errorf("PATCH failed at offset %d: HTTP %d, %s", chunkStart, resp.StatusCode, string(bodyBytes))
					}
					resp.Body.Close()
					patchErr = fmt.Errorf("server error HTTP %d at offset %d", resp.StatusCode, chunkStart)
				} else {
					patchErr = err
				}

				if attempt < maxRetries {
					time.Sleep(backoff)
					backoff *= 2
				}
			}
			if patchErr != nil {
				return "", 0, fmt.Errorf("PATCH failed after retries: %w", patchErr)
			}

			newLoc := resp.Header.Get("Location")
			resp.Body.Close()
			if newLoc != "" {
				newU, err := url.Parse(newLoc)
				if err == nil {
					if !newU.IsAbs() {
						base, _ := url.Parse(fmt.Sprintf("https://%s", c.registry))
						newU = base.ResolveReference(newU)
					}
					u = newU
				}
			}

			offset += int64(n)
			notifier.Update(description, offset, size)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return "", 0, readErr
			}
			break
		}
	}

	notifier.Finish()

	// Final PUT: commit the upload with digest.
	finalURL := u.String()
	if strings.Contains(finalURL, "?") {
		finalURL += "&digest=" + digest
	} else {
		finalURL += "?digest=" + digest
	}

	var putResp *http.Response
	const maxRetries = 3
	backoff := time.Second
	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		default:
		}

		freshToken, tokenErr := c.getOciToken(ctx, "pull,push")
		if tokenErr != nil {
			return "", 0, fmt.Errorf("refresh token for final PUT: %w", tokenErr)
		}

		putReq, err := http.NewRequestWithContext(ctx, "PUT", finalURL, nil)
		if err != nil {
			return "", 0, err
		}
		putReq.Header.Set("Authorization", "Bearer "+freshToken)
		putReq.Header.Set("Content-Type", "application/octet-stream")
		putReq.Header.Set("Content-Length", "0")

		putResp, err = c.client.Do(putReq)
		if err == nil && putResp.StatusCode < 500 {
			break
		}
		if putResp != nil {
			putResp.Body.Close()
		}
		if attempt < maxRetries {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	if err != nil {
		return "", 0, fmt.Errorf("final PUT failed: %w", err)
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusCreated && putResp.StatusCode != http.StatusAccepted && putResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(putResp.Body)
		return "", 0, fmt.Errorf("final PUT failed: HTTP %d, %s", putResp.StatusCode, string(bodyBytes))
	}

	return digest, size, nil
}

func (c *Client) downloadBlob(ctx context.Context, digest string) ([]byte, error) {
	resp, err := c.Request(ctx, "GET", "/blobs/"+digest, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blob %s not found: HTTP %d", digest, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) DeleteBlob(ctx context.Context, digest string) error {
	resp, err := c.Request(ctx, "DELETE", "/blobs/"+digest, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNotFound {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete blob %s (HTTP %d): %s", digest, resp.StatusCode, string(bodyBytes))
	}
	return nil
}

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
