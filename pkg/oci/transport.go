package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Transport handles OCI registry HTTP communication with OAuth2 token management and retry logic.
type Transport struct {
	registry string
	repo     string
	token    string // static token for basic auth

	tokenMu       sync.Mutex
	ociTokenPull  string
	pullFetchTime time.Time
	ociTokenPush  string
	pushFetchTime time.Time

	client *http.Client
}

// NewTransport creates a Transport for the given registry and repository.
func NewTransport(registry, repo, token string) *Transport {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &Transport{
		registry: registry,
		repo:     repo,
		token:    token,
		client: &http.Client{
			Transport: transport,
		},
	}
}

// SetHTTPClient overrides the default HTTP client (used in tests).
func (t *Transport) SetHTTPClient(hc *http.Client) {
	t.client = hc
}

// getOciToken fetches or returns a cached OCI auth token for the given scope.
func (t *Transport) getOciToken(ctx context.Context, actions string) (string, error) {
	t.tokenMu.Lock()
	defer t.tokenMu.Unlock()

	const tokenCacheTTL = DefaultTokenCacheTTL
	if actions == "pull" {
		if t.ociTokenPull != "" && time.Since(t.pullFetchTime) < tokenCacheTTL {
			return t.ociTokenPull, nil
		}
	} else {
		if t.ociTokenPush != "" && time.Since(t.pushFetchTime) < tokenCacheTTL {
			return t.ociTokenPush, nil
		}
	}

	scope := fmt.Sprintf("repository:%s/nix-cache:%s", t.repo, actions)
	url := fmt.Sprintf("https://%s/token?scope=%s&service=%s", t.registry, scope, t.registry)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	if t.token != "" {
		req.SetBasicAuth("token", t.token)
	}

	resp, err := t.client.Do(req)
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
		t.ociTokenPull = res.Token
		t.pullFetchTime = time.Now()
	} else {
		t.ociTokenPush = res.Token
		t.pushFetchTime = time.Now()
	}
	return res.Token, nil
}

// request performs an authenticated OCI registry request, following redirects.
func (t *Transport) request(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	return t.doRequest(ctx, method, path, body, contentType, true)
}

// rawRequest performs an authenticated OCI registry request, NOT following redirects.
func (t *Transport) rawRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	return t.doRequest(ctx, method, path, body, contentType, false)
}

func (t *Transport) doRequest(ctx context.Context, method, path string, body io.Reader, contentType string, followRedirects bool) (*http.Response, error) {
	actions := "pull"
	if method != "GET" && method != "HEAD" {
		actions = "pull,push"
	}
	token, err := t.getOciToken(ctx, actions)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://%s/v2/%s/nix-cache%s", t.registry, t.repo, path)

	doer := t.client.Do
	if !followRedirects {
		doer = t.getHTTPTransport().RoundTrip
	}
	return t.doWithRetry(ctx, method, url, token, contentType, body, doer)
}

func (t *Transport) getHTTPTransport() http.RoundTripper {
	if t.client.Transport != nil {
		return t.client.Transport
	}
	return http.DefaultTransport
}

func (t *Transport) doWithRetry(ctx context.Context, method, url, token, contentType string, body io.Reader, doer func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	const maxRetries = 3
	var resp *http.Response
	var err error
	backoff := time.Second

	var seeker io.ReadSeeker
	if body != nil {
		if s, ok := body.(io.ReadSeeker); ok {
			seeker = s
		}
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if attempt > 0 && seeker != nil {
			if _, seekErr := seeker.Seek(0, io.SeekStart); seekErr != nil {
				return nil, fmt.Errorf("failed to seek request body for retry: %w", seekErr)
			}
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
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return resp, nil
			}

			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
				if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
					if seconds, pErr := strconv.Atoi(retryAfter); pErr == nil && seconds > 0 {
						backoff = time.Duration(seconds) * time.Second
					}
				}
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

// rawDo executes an HTTP request directly without token or retry (for streaming).
func (t *Transport) rawDo(req *http.Request) (*http.Response, error) {
	return t.client.Do(req)
}

// Do executes an HTTP request directly with the client (for upload operations).
func (t *Transport) Do(req *http.Request) (*http.Response, error) {
	return t.client.Do(req)
}
