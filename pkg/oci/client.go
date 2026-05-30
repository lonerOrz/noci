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
	return &Client{
		registry: strings.ToLower(registry),
		repo:     strings.ToLower(repo),
		token:    token,
		client:   &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *Client) getOciToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.ociToken != "" && time.Since(c.tokenFetchTime) < 50*time.Minute {
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
		return "", fmt.Errorf("failed to fetch OCI token: HTTP %d, %s", resp.StatusCode, string(bodyBytes))
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
	}

	return c.client.Do(req)
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

func (c *Client) FetchIndex(ctx context.Context) (*CacheIndex, error) {
	token, err := c.getOciToken(ctx)
	if err != nil {
		return nil, err
	}

	manifestURL := fmt.Sprintf("https://%s/v2/%s/nix-cache/manifests/cache-index", c.registry, c.repo)

	req, err := http.NewRequestWithContext(ctx, "GET", manifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest not found: HTTP %d", resp.StatusCode)
	}

	var manifest struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil || len(manifest.Layers) == 0 {
		return nil, fmt.Errorf("invalid manifest structure")
	}

	blobResp, err := c.Request(ctx, "GET", "/blobs/"+manifest.Layers[0].Digest, nil, "")
	if err != nil {
		return nil, err
	}
	defer blobResp.Body.Close()

	if blobResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get blob: HTTP %d", blobResp.StatusCode)
	}

	var idx CacheIndex
	if err := json.NewDecoder(blobResp.Body).Decode(&idx); err != nil {
		return nil, err
	}

	idx.Upgrade()

	return &idx, nil
}

func (c *Client) PushIndex(ctx context.Context, idx *CacheIndex) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "noci-index-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	_, _ = tmp.Write(data)
	_ = tmp.Sync()

	hashWriter := sha256.New()
	_, _ = hashWriter.Write(data)
	sha := hex.EncodeToString(hashWriter.Sum(nil))

	digest, err := c.UploadBlob(ctx, tmp.Name(), sha)
	if err != nil {
		return err
	}

	configData := []byte("{}")
	configHashWriter := sha256.New()
	_, _ = configHashWriter.Write(configData)
	configSha := hex.EncodeToString(configHashWriter.Sum(nil))

	configTmp, _ := os.CreateTemp("", "noci-config-*.json")
	defer os.Remove(configTmp.Name())
	_, _ = configTmp.Write(configData)
	_ = configTmp.Close()

	configDigest, err := c.UploadBlob(ctx, configTmp.Name(), configSha)
	if err != nil {
		return err
	}

	manifest := map[string]interface{}{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]interface{}{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      len(configData),
		},
		"layers": []map[string]interface{}{
			{
				"mediaType": "application/vnd.nix.cache.index.v1+json",
				"digest":    digest,
				"size":      len(data),
			},
		},
	}

	manifestBytes, _ := json.Marshal(manifest)
	putResp, err := c.Request(ctx, "PUT", "/manifests/cache-index", bytes.NewReader(manifestBytes), "application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		return err
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(putResp.Body)
		return fmt.Errorf("failed to upload manifest: HTTP %d, %s", putResp.StatusCode, string(bodyBytes))
	}

	return nil
}

func (c *Client) Registry() string {
	return c.registry
}

func (c *Client) Repo() string {
	return c.repo
}
