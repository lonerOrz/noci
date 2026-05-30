package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"noci/pkg/log"
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
	registry       string
	repo           string
	token          string
	tokenMu        sync.Mutex
	ociToken       string
	tokenFetchTime time.Time
	client         *http.Client
}

func NewClient(registry, repo, token string) *Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
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
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *Client) getOciToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.ociToken != "" && time.Since(c.tokenFetchTime) < 45*time.Minute {
		return c.ociToken, nil
	}

	scope := fmt.Sprintf("repository:%s/nix-cache:pull,push", c.repo)
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
		return "", fmt.Errorf("fetch token failed: HTTP %d, %s", resp.StatusCode, string(bodyBytes))
	}

	var res struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	c.ociToken = res.Token
	c.tokenFetchTime = time.Now()
	return c.ociToken, nil
}

func (c *Client) Request(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	token, err := c.getOciToken(ctx)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://%s/v2/%s/nix-cache%s", c.registry, c.repo, path)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", contentType)
	}

	return c.client.Do(req)
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
	resp, err := c.Request(ctx, "GET", "/tags/list", nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list tags: HTTP %d", resp.StatusCode)
	}

	var res struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Tags, nil
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
	tags, err := c.ListTags(ctx)
	if err != nil {
		return nil, err
	}

	idx := NewIndex(c.registry, c.repo)
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, tag := range tags {
		if len(tag) != 32 {
			continue
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(t string) {
			defer func() {
				<-sem
				wg.Done()
			}()

			manifest, err := c.FetchManifest(ctx, t)
			if err != nil || manifest.Annotations == nil {
				return
			}
			if manifest.Annotations["org.nix.evicted"] == "true" {
				return
			}

			narinfo := manifest.Annotations["org.nix.narinfo"]
			if narinfo == "" {
				return
			}

			mu.Lock()
			idx.Entries[t] = IndexItem{
				Name:       manifest.Annotations["org.nix.name"],
				NarInfo:    narinfo,
				NarDigest:  manifest.Layers[0].Digest,
				NarSize:    manifest.Layers[0].Size,
				Added:      time.Now(),
				LastUsed:   time.Now(),
				UploadedAt: time.Now(),
				References: parseReferencesFromNarInfo(narinfo),
			}

			if pinnedStr := manifest.Annotations["org.nix.pinned_until"]; pinnedStr != "" {
				var ttl int64
				fmt.Sscanf(pinnedStr, "%d", &ttl)
				idx.Roots[t] = GCRoot{
					PinnedAt: time.Now(),
					TTL:      ttl,
				}
			}
			mu.Unlock()
		}(tag)
	}
	wg.Wait()

	return idx, nil
}

func (c *Client) PushIndex(ctx context.Context, idx *CacheIndex) error {
	tags, err := c.ListTags(ctx)
	if err != nil {
		return err
	}

	for _, tag := range tags {
		if len(tag) != 32 {
			continue
		}
		if _, exists := idx.Entries[tag]; !exists {
			manifest, err := c.FetchManifest(ctx, tag)
			if err != nil {
				continue
			}
			if manifest.Annotations == nil {
				manifest.Annotations = make(map[string]string)
			}
			if manifest.Annotations["org.nix.evicted"] != "true" {
				log.Action("Marking evicted: %s", tag)
				manifest.Annotations["org.nix.evicted"] = "true"
				_ = c.PushManifest(ctx, tag, manifest)
			}
			continue
		}
	}

	for hash, root := range idx.Roots {
		manifest, err := c.FetchManifest(ctx, hash)
		if err != nil {
			continue
		}
		if manifest.Annotations == nil {
			manifest.Annotations = make(map[string]string)
		}

		pinnedStr := fmt.Sprintf("%d", root.TTL)
		if manifest.Annotations["org.nix.pinned_until"] != pinnedStr {
			manifest.Annotations["org.nix.pinned_until"] = pinnedStr
			log.Action("Pinning tag: %s", hash)
			_ = c.PushManifest(ctx, hash, manifest)
		}
	}

	for _, tag := range tags {
		if len(tag) != 32 {
			continue
		}
		if _, pinned := idx.Roots[tag]; !pinned {
			manifest, err := c.FetchManifest(ctx, tag)
			if err != nil {
				continue
			}
			if manifest.Annotations != nil && manifest.Annotations["org.nix.pinned_until"] != "" {
				delete(manifest.Annotations, "org.nix.pinned_until")
				log.Action("Unpinning tag: %s", tag)
				_ = c.PushManifest(ctx, tag, manifest)
			}
		}
	}

	return nil
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

	req, err := http.NewRequestWithContext(ctx, "PUT", uploadURL, file)
	if err != nil {
		return "", err
	}

	token, _ := c.getOciToken(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")

	stat, _ := file.Stat()
	req.ContentLength = stat.Size()

	putResp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusCreated && putResp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(putResp.Body)
		return "", fmt.Errorf("failed to complete upload: HTTP %d, %s", putResp.StatusCode, string(bodyBytes))
	}

	return digest, nil
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
