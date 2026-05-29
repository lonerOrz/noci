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
	"time"
)

type Client struct {
	registry string
	repo     string
	token    string
	ociToken string
	client   *http.Client
}

func NewClient(registry, repo, token string) *Client {
	return &Client{
		// OCI 规范要求所有仓库和域名路径必须为全小写，在此进行强制转换规避 403 隐患
		registry: strings.ToLower(registry),
		repo:     strings.ToLower(repo),
		token:    token,
		client:   &http.Client{Timeout: 5 * time.Minute},
	}
}

// getOciToken 使用 GitHub Token 换取 OCI 的 Scoped 临时 Token
func (c *Client) getOciToken(ctx context.Context) (string, error) {
	if c.ociToken != "" {
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
	return c.ociToken, nil
}

// Request 发送带 Bearer Token 头的标准 OCI HTTP 请求
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

// UploadBlob 推送本地临时文件作为 OCI Blob，返回其 SHA256 摘要
func (c *Client) UploadBlob(ctx context.Context, filePath, sha256Hex string) (string, error) {
	digest := "sha256:" + sha256Hex

	// 1. 验证 Blob 是否已存在 (免去重复上传)
	headResp, err := c.Request(ctx, "HEAD", "/blobs/"+digest, nil, "")
	if err == nil && headResp.StatusCode == http.StatusOK {
		return digest, nil
	}

	// 2. 发起上传请求获取 Location
	initResp, err := c.Request(ctx, "POST", "/blobs/uploads/", nil, "")
	if err != nil {
		return "", err
	}
	defer initResp.Body.Close()

	// 检查 POST 状态码，如果是 401/403 会直接向用户报错并打印 GHCR 原始返回
	if initResp.StatusCode != http.StatusAccepted && initResp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(initResp.Body)
		return "", fmt.Errorf("failed to initiate blob upload (HTTP %d): %s", initResp.StatusCode, string(bodyBytes))
	}

	uploadURL := initResp.Header.Get("Location")
	if uploadURL == "" {
		return "", fmt.Errorf("registry didn't return upload location")
	}

	// 解析 Location URL
	u, err := url.Parse(uploadURL)
	if err != nil {
		return "", fmt.Errorf("invalid upload location URL: %w", err)
	}

	// 如果是相对路径（如 GHCR 返回的 /v2/...），以当前注册表域名为基准自动补全 Scheme 和 Host
	if !u.IsAbs() {
		base, _ := url.Parse(fmt.Sprintf("https://%s", c.registry))
		u = base.ResolveReference(u)
	}

	// 使用标准库 url.Values 设置 Query 参数，由标准库自动健壮地处理 ? 和 & 的逻辑
	q := u.Query()
	q.Set("digest", digest)
	u.RawQuery = q.Encode()
	uploadURL = u.String() // 此时得到的 URL 结构绝对合法（包含正确的 ?digest=）

	// 3. 上传大文件
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

// FetchIndex 从 GHCR 获取最新的 cache-index JSON
func (c *Client) FetchIndex(ctx context.Context) (*CacheIndex, error) {
	token, err := c.getOciToken(ctx)
	if err != nil {
		return nil, err
	}

	// 获取 Manifest 清单
	manifestURL := fmt.Sprintf("https://%s/v2/%s/nix-cache/manifests/cache-index", c.registry, c.repo)

	// 核心修复：移除之前的冗余占位 request，并正确绑定 ctx 到本次实际请求上
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

	// 获取真实的 index 数据
	blobResp, err := c.Request(ctx, "GET", "/blobs/"+manifest.Layers[0].Digest, nil, "")
	if err != nil {
		return nil, err
	}
	defer blobResp.Body.Close()

	// 核心修复：严格校验底层 Blob 响应状态码，防止远端非 200 报文导致解析崩溃
	if blobResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get blob: HTTP %d", blobResp.StatusCode)
	}

	var idx CacheIndex
	if err := json.NewDecoder(blobResp.Body).Decode(&idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// PushIndex 将更新后的索引和 OCI Manifest 上传回 GHCR
func (c *Client) PushIndex(ctx context.Context, idx *CacheIndex) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}

	// 保存为本地临时文件上传为 Blob
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

	// 空配置 (OCI 规范必须)
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

	// 构造 Manifest 清单
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
