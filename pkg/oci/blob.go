package oci

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"noci/pkg/log"
	"os"
	"strconv"
	"strings"
	"time"
)

// blobService handles OCI blob upload, download, and deletion.
type blobService struct {
	transport *Transport
	profile   *bool // shared with Client.Profile
}

func newBlobService(t *Transport, profile *bool) *blobService {
	return &blobService{transport: t, profile: profile}
}

// UploadBlob uploads a blob, auto-selecting monolithic or chunked strategy.
func (bs *blobService) UploadBlob(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error) {
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

	headCtx, headCancel := context.WithTimeout(ctx, DefaultHTTPTimeout)
	defer headCancel()
	if headResp, headErr := bs.transport.request(headCtx, "HEAD", "/blobs/"+digest, nil, ""); headErr == nil {
		headResp.Body.Close()
		if headResp.StatusCode == http.StatusOK {
			if bs.profile != nil && *bs.profile {
				log.Info("[profile] Blob %s already exists (HEAD), skipped", sha256Hex[:12])
			}
			return digest, size, nil
		}
	} else if headResp != nil {
		headResp.Body.Close()
	}

	// Retry the entire upload on session expiration (GHCR returns 404 "blob upload invalid"
	// when the upload session expires during long chunked uploads).
	const maxUploadRetries = 2
	for attempt := 0; attempt <= maxUploadRetries; attempt++ {
		if attempt > 0 {
			log.Warning("[noci] Upload session expired for %s, retrying (attempt %d)...", description, attempt+1)
			time.Sleep(time.Duration(attempt) * 10 * time.Second)
			notifier = &NoopProgressNotifier{} // reset notifier for retry
		}

		if size > chunkedThreshold {
			digest, size, err = bs.uploadBlobChunked(ctx, filePath, sha256Hex, description, notifier)
		} else {
			digest, size, err = bs.uploadBlobMonolithic(ctx, filePath, sha256Hex, description, notifier)
		}

		if err == nil {
			return digest, size, nil
		}

		if !isUploadSessionExpired(err) {
			return digest, size, err
		}
	}
	return digest, size, fmt.Errorf("upload failed after %d retries: %w", maxUploadRetries, err)
}

func isUploadSessionExpired(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "blob upload invalid") ||
		strings.Contains(msg, "BLOB_UPLOAD_INVALID") ||
		strings.Contains(msg, "HTTP 404")
}

const chunkedThreshold = 64 * 1024 * 1024 // 64MB

func (bs *blobService) uploadBlobMonolithic(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error) {
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

	token, err := bs.transport.getOciToken(ctx, "pull,push")
	if err != nil {
		return "", 0, err
	}

	initResp, err := bs.transport.request(ctx, "POST", "/blobs/uploads/", nil, "")
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
		base, _ := url.Parse(fmt.Sprintf("https://%s", bs.transport.registry))
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
			freshToken, tokenErr := bs.transport.getOciToken(ctx, "pull,push")
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
		putResp, err = bs.transport.Do(putReq)
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

	if bs.profile != nil && *bs.profile {
		totalElapsed := time.Since(uploadStart)
		log.Info("[profile] Upload %s: total=%v net=%v (net %.1f%%) size=%s",
			description, totalElapsed, netTime,
			float64(netTime)/float64(totalElapsed)*100,
			FormatSize(size))
	}

	return digest, size, nil
}

func (bs *blobService) uploadBlobChunked(ctx context.Context, filePath, sha256Hex, description string, notifier ProgressNotifier) (digest string, size int64, err error) {
	digest = "sha256:" + sha256Hex

	initResp, err := bs.transport.request(ctx, "POST", "/blobs/uploads/", nil, "")
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
		base, _ := url.Parse(fmt.Sprintf("https://%s", bs.transport.registry))
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
	const chunkSize = 4 * 1024 * 1024 // 4MB — GHCR limit per PATCH request
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

				freshToken, tokenErr := bs.transport.getOciToken(ctx, "pull,push")
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

				resp, err = bs.transport.Do(req)
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
						base, _ := url.Parse(fmt.Sprintf("https://%s", bs.transport.registry))
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

		freshToken, tokenErr := bs.transport.getOciToken(ctx, "pull,push")
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

		putResp, err = bs.transport.Do(putReq)
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

// downloadBlob fetches raw blob content by digest.
func (bs *blobService) downloadBlob(ctx context.Context, digest string) ([]byte, error) {
	resp, err := bs.transport.request(ctx, "GET", "/blobs/"+digest, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blob %s not found: HTTP %d", digest, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// DeleteBlob removes a blob by digest.
func (bs *blobService) DeleteBlob(ctx context.Context, digest string) error {
	resp, err := bs.transport.request(ctx, "DELETE", "/blobs/"+digest, nil, "")
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
