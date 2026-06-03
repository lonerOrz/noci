package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
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
			Timeout:   5 * time.Minute,
			Transport: transport,
		},
	}
}

func (c *Client) getOciToken(ctx context.Context, actions string) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if actions == "pull" {
		if c.ociTokenPull != "" && time.Since(c.pullFetchTime) < 45*time.Minute {
			return c.ociTokenPull, nil
		}
	} else {
		if c.ociTokenPush != "" && time.Since(c.pushFetchTime) < 45*time.Minute {
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
	actions := "pull"
	if method != "GET" && method != "HEAD" {
		actions = "pull,push"
	}
	token, err := c.getOciToken(ctx, actions)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://%s/v2/%s/nix-cache%s", c.registry, c.repo, path)

	var bodyBytes []byte
	if body != nil {
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}

	return c.doWithRetry(ctx, method, url, token, contentType, bodyBytes, c.client.Do)
}

func (c *Client) getTransport() http.RoundTripper {
	if c.client.Transport != nil {
		return c.client.Transport
	}
	return http.DefaultTransport
}

func (c *Client) RawRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	actions := "pull"
	if method != "GET" && method != "HEAD" {
		actions = "pull,push"
	}
	token, err := c.getOciToken(ctx, actions)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://%s/v2/%s/nix-cache%s", c.registry, c.repo, path)

	var bodyBytes []byte
	if body != nil {
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}

	return c.doWithRetry(ctx, method, url, token, contentType, bodyBytes, c.getTransport().RoundTrip)
}

func (c *Client) doWithRetry(ctx context.Context, method, url, token, contentType string, body []byte, doer func(*http.Request) (*http.Response, error)) (*http.Response, error) {
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
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err = http.NewRequestWithContext(ctx, method, url, reader)
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

func (c *Client) DeleteManifest(ctx context.Context, digest string) error {
	resp, err := c.Request(ctx, "DELETE", "/manifests/"+digest, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete manifest failed: HTTP %d", resp.StatusCode)
	}
	return nil
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

	var idx CacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	idx.Upgrade()
	return &idx, nil
}

func (c *Client) PushIndex(ctx context.Context, idx *CacheIndex) error {
	idx.Generated = time.Now()
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "noci-index-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), bytes.NewReader(data)); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	digest, err := c.UploadBlob(ctx, tmp.Name(), hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return err
	}

	indexManifest := OCIManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: Descriptor{
			MediaType: "application/vnd.noci.index.config.v1+json",
			Size:      2,
			Digest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		Layers: []Descriptor{
			{
				MediaType: "application/vnd.noci.index.layer.v1+json",
				Digest:    digest,
				Size:      int64(len(data)),
			},
		},
		Annotations: map[string]string{
			"org.nix.index.generated_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	return c.PushManifest(ctx, "noci-index", &indexManifest)
}

func (c *Client) UploadBlob(ctx context.Context, filePath, sha256Hex string) (string, error) {
	digest := "sha256:" + sha256Hex

	headResp, err := c.Request(ctx, "HEAD", "/blobs/"+digest, nil, "")
	if err == nil && headResp.StatusCode == http.StatusOK {
		return digest, nil
	}

	initResp, err := c.Request(ctx, "POST", "/blobs/uploads/", nil, "")
	if err != nil {
		return "", err
	}
	defer initResp.Body.Close()

	if initResp.StatusCode != http.StatusAccepted && initResp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(initResp.Body)
		return "", fmt.Errorf("failed to initiate blob upload (HTTP %d): %s", initResp.StatusCode, string(bodyBytes))
	}

	uploadURL := initResp.Header.Get("Location")
	if uploadURL == "" {
		return "", fmt.Errorf("registry didn't return upload location")
	}

	u, err := url.Parse(uploadURL)
	if err != nil {
		return "", fmt.Errorf("invalid upload location URL: %w", err)
	}

	if !u.IsAbs() {
		base, _ := url.Parse(fmt.Sprintf("https://%s", c.registry))
		u = base.ResolveReference(u)
	}

	q := u.Query()
	q.Set("digest", digest)
	u.RawQuery = q.Encode()
	uploadURL = u.String()

	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	stat, _ := file.Stat()

	pr := &progressReader{r: file, total: stat.Size()}
	req, err := http.NewRequestWithContext(ctx, "PUT", uploadURL, pr)
	if err != nil {
		return "", err
	}

	token, _ := c.getOciToken(ctx, "pull,push")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")

	req.ContentLength = stat.Size()

	putResp, err := c.client.Do(req)
	if err != nil {
		fmt.Println()
		return "", err
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusCreated && putResp.StatusCode != http.StatusAccepted {
		fmt.Println()
		bodyBytes, _ := io.ReadAll(putResp.Body)
		return "", fmt.Errorf("failed to complete upload: HTTP %d, %s", putResp.StatusCode, string(bodyBytes))
	}

	fmt.Print("\n  Done.\n")
	return digest, nil
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

type progressReader struct {
	r     io.Reader
	total int64
	done  int64
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	pr.done += int64(n)
	if pr.total > 0 {
		pct := float64(pr.done) * 100 / float64(pr.total)
		fmt.Printf("\r  Uploading... %.1f%% (%s / %s)", pct, formatSize(pr.done), formatSize(pr.total))
	}
	return n, err
}

func formatSize(b int64) string {
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
