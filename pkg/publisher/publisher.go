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
	client *oci.Client
	signer *nix.Signer
}

func NewPublisher(client *oci.Client, signer *nix.Signer) *Publisher {
	return &Publisher{
		client: client,
		signer: signer,
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
	index, err := p.client.FetchIndex(ctx)
	if err != nil {
		log.Warning("No existing index found. Starting fresh.")
		index = oci.NewIndex(p.client.Registry(), p.client.Repo())
	}

	if p.signer != nil {
		pubKey := p.signer.PrivateKey.Public().(ed25519.PublicKey)
		index.PublicKey = fmt.Sprintf("%s:%s",
			p.signer.KeyName,
			base64.StdEncoding.EncodeToString(pubKey),
		)
	}

	log.Action("Evaluating closure for %d input path(s)...", len(inputPaths))
	closure, err := nix.GetClosure(ctx, inputPaths)
	if err != nil {
		return fmt.Errorf("failed to get closure: %w", err)
	}

	var uploadList []nix.PathInfo
	skippedUpstreamCount := 0

	for _, path := range closure {
		hash := nix.GetPathHash(path)
		if _, exists := index.Entries[hash]; exists {
			continue
		}

		info, err := nix.GetPathInfo(ctx, path)
		if err != nil {
			return fmt.Errorf("failed to get path info for %s: %w", path, err)
		}

		hasUpstreamSig := false
		for _, sig := range info.Signatures {
			if strings.HasPrefix(sig, "cache.nixos.org-1:") {
				hasUpstreamSig = true
				break
			}
		}

		if hasUpstreamSig {
			skippedUpstreamCount++
			continue
		}

		uploadList = append(uploadList, *info)
	}

	if skippedUpstreamCount > 0 {
		log.Success("Skipped %d path(s) that are already cached on upstream (cache.nixos.org).", skippedUpstreamCount)
	}

	if len(uploadList) == 0 {
		log.Success("Everything is already cached (either in OCI or upstream)!")
		return nil
	}

	log.Info("Found %d new store path(s) to cache. Executing concurrent upload pipeline...", len(uploadList))

	resultsChan := make(chan uploadResult, len(uploadList))
	sem := make(chan struct{}, 4) // 限制并发数为 4，防止瞬时 I/O 饱和或被 OCI 注册表限制频次
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
			resultsChan <- res
		}(info)
	}

	wg.Wait()
	close(resultsChan)

	if firstErr != nil {
		return firstErr
	}

	for res := range resultsChan {
		index.AddEntry(res.hash, res.name, res.narinfo, res.digest, res.size, res.refs)
	}

	log.Action("Updating remote cache-index...")
	if err := p.client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	log.Success("Cached %d packages successfully.", len(uploadList))
	return nil
}

func (p *Publisher) publishSingle(ctx context.Context, info nix.PathInfo) (uploadResult, error) {
	log.Action("Processing: %s", info.Path)

	narFile, fileHash, fileSize, err := nix.ExportAndCompress(ctx, info.Path)
	if err != nil {
		return uploadResult{}, fmt.Errorf("failed to export %s: %w", info.Path, err)
	}
	defer os.Remove(narFile)

	log.Action("Uploading compressed NAR blob (%d bytes)...", fileSize)
	digest, err := p.client.UploadBlob(ctx, narFile, fileHash)
	if err != nil {
		return uploadResult{}, fmt.Errorf("failed to upload blob for %s: %w", info.Path, err)
	}

	normalizedNarHash, err := nix.NormalizeNarHash(info.NarHash)
	if err != nil {
		return uploadResult{}, fmt.Errorf("failed to normalize NarHash for %s: %w", info.Path, err)
	}

	sigs := info.Signatures
	if p.signer != nil {
		sig, err := p.signer.SignPath(info.Path, normalizedNarHash, info.NarSize, info.References)
		if err != nil {
			return uploadResult{}, fmt.Errorf("failed to sign path: %w", err)
		}
		sigs = append(sigs, sig)
	}

	narinfoContent := nix.GenerateNarInfo(info.Path, normalizedNarHash, info.NarSize, fileHash, fileSize, info.References, sigs)
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
