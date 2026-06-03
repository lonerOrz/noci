package publisher

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"os"
	"strings"
	"sync"
)

type Publisher struct {
	client       *oci.Client
	signer       *nix.Signer
	skipUpstream bool
	comp         string
	jobs         int
}

func NewPublisher(client *oci.Client, signer *nix.Signer, skipUpstream bool, comp string, jobs int) *Publisher {
	if client == nil || signer == nil {
		panic("publisher: client and signer must not be nil")
	}
	return &Publisher{
		client:       client,
		signer:       signer,
		skipUpstream: skipUpstream,
		comp:         comp,
		jobs:         jobs,
	}
}

type uploadResult struct {
	hash    string
	name    string
	narinfo string
	digest  string
	size    int64
	refs    []string
}

func (p *Publisher) Publish(ctx context.Context, inputPaths []string) error {
	if p.signer != nil {
		pubKey := p.signer.PrivateKey.Public().(ed25519.PublicKey)
		publicKeyStr := fmt.Sprintf("%s:%s",
			p.signer.KeyName,
			base64.StdEncoding.EncodeToString(pubKey),
		)
		pubManifest := oci.OCIManifest{
			SchemaVersion: 2,
			MediaType:     "application/vnd.oci.image.manifest.v1+json",
			Config: oci.Descriptor{
				MediaType: "application/vnd.oci.image.config.v1+json",
				Size:      2,
				Digest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
			Annotations: map[string]string{
				"org.nix.public_key": publicKeyStr,
			},
		}
		_ = p.client.PushManifest(ctx, "public-key", &pubManifest)
	}

	index, err := p.client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to load index: %w", err)
	}

	log.Action("Evaluating closure for %d paths...", len(inputPaths))
	closure, err := nix.GetClosure(ctx, inputPaths)
	if err != nil {
		return fmt.Errorf("failed to get closure: %w", err)
	}

	var uncachedPaths []string
	for _, path := range closure {
		hash := nix.GetPathHash(path)
		if _, exists := index.Entries[hash]; exists {
			continue
		}
		uncachedPaths = append(uncachedPaths, path)
	}

	if len(uncachedPaths) == 0 {
		log.Success("All packages are already cached!")
		return nil
	}

	infos, err := nix.GetPathInfos(ctx, uncachedPaths)
	if err != nil {
		return fmt.Errorf("failed to get path infos: %w", err)
	}

	var uploadList []nix.PathInfo
	skippedUpstreamCount := 0

	for _, path := range uncachedPaths {
		info, ok := infos[path]
		if !ok {
			continue
		}

		if p.skipUpstream {
			skip := false
			for _, sig := range info.Signatures {
				if strings.HasPrefix(sig, "cache.nixos.org-1:") {
					skippedUpstreamCount++
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		}
		uploadList = append(uploadList, info)
	}

	if skippedUpstreamCount > 0 {
		log.Success("Skipped %d upstream-cached paths.", skippedUpstreamCount)
	}

	if len(uploadList) == 0 {
		log.Success("All packages are already cached!")
		return nil
	}

	log.Info("Found %d new paths. Uploading concurrently...", len(uploadList))

	outcomeChan := make(chan uploadResult, len(uploadList))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup

	pipelineCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var firstErr error
	var errMu sync.Mutex

	for _, info := range uploadList {
		errMu.Lock()
		if firstErr != nil {
			errMu.Unlock()
			break
		}
		errMu.Unlock()

		sem <- struct{}{}
		wg.Add(1)

		go func(pathInfo nix.PathInfo) {
			defer func() {
				<-sem
				wg.Done()
			}()

			res, err := p.publishSingle(pipelineCtx, pathInfo)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				errMu.Unlock()
				return
			}

			layerMediaType := "application/vnd.nix.cache.layer.v1+tar+gzip"
			if p.comp == "zstd" {
				layerMediaType = "application/vnd.nix.cache.layer.v1+tar+zstd"
			}
			manifest := oci.OCIManifest{
				SchemaVersion: 2,
				MediaType:     "application/vnd.oci.image.manifest.v1+json",
				Config: oci.Descriptor{
					MediaType: "application/vnd.oci.image.config.v1+json",
					Size:      2,
					Digest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				},
				Layers: []oci.Descriptor{
					{
						MediaType: layerMediaType,
						Digest:    res.digest,
						Size:      res.size,
					},
				},
				Annotations: map[string]string{
					"org.nix.narinfo":    res.narinfo,
					"org.nix.name":       res.name,
					"org.nix.references": strings.Join(res.refs, ","),
				},
			}
			if err := p.client.PushManifest(pipelineCtx, res.hash, &manifest); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("push manifest %s failed: %w", res.hash, err)
					cancel()
				}
				errMu.Unlock()
				return
			}

			outcomeChan <- res
		}(info)
	}

	wg.Wait()
	close(outcomeChan)

	if firstErr != nil {
		return firstErr
	}

	// Late Merge: upload 完成后重新拉取最新索引，消除并发冲突风险
	freshIndex, err := p.client.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to re-fetch index for late merge: %w", err)
	}

	for res := range outcomeChan {
		freshIndex.AddEntry(res.hash, res.name, res.narinfo, res.digest, res.size, res.refs)
	}

	if err := p.client.PushIndex(ctx, freshIndex); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	log.Success("Cached %d packages successfully.", len(uploadList))
	return nil
}

func (p *Publisher) publishSingle(ctx context.Context, info nix.PathInfo) (uploadResult, error) {
	log.Action("Processing: %s", info.Path)

	narFile, fileHash, fileSize, err := nix.ExportAndCompress(ctx, info.Path, p.comp, p.jobs)
	if err != nil {
		return uploadResult{}, fmt.Errorf("export failed: %w", err)
	}
	defer os.Remove(narFile)

	log.Action("Uploading NAR (%d bytes)...", fileSize)
	digest, err := p.client.UploadBlob(ctx, narFile, fileHash)
	if err != nil {
		return uploadResult{}, fmt.Errorf("upload blob failed: %w", err)
	}

	normalizedNarHash, err := nix.NormalizeNarHash(info.NarHash)
	if err != nil {
		return uploadResult{}, fmt.Errorf("normalize NarHash failed: %w", err)
	}

	sigs := info.Signatures
	if p.signer != nil {
		sig, err := p.signer.SignPath(info.Path, normalizedNarHash, info.NarSize, info.References)
		if err != nil {
			return uploadResult{}, fmt.Errorf("sign path failed: %w", err)
		}
		sigs = append(sigs, sig)
	}

	narinfoContent := nix.GenerateNarInfo(info.Path, normalizedNarHash, info.NarSize, fileHash, fileSize, info.References, sigs, p.comp)
	hash := nix.GetPathHash(info.Path)

	return uploadResult{
		hash:    hash,
		name:    nix.GetPathName(info.Path),
		narinfo: narinfoContent,
		digest:  digest,
		size:    fileSize,
		refs:    info.References,
	}, nil
}
