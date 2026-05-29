package publisher

import (
	"context"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"os"
	"strings"
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

func (p *Publisher) Publish(ctx context.Context, inputPaths []string) error {
	index, err := p.client.FetchIndex(ctx)
	if err != nil {
		log.Warning("No existing index found. Starting fresh.")
		index = oci.NewIndex(p.client.Registry(), p.client.Repo())
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

	log.Info("Found %d new store path(s) to cache.", len(uploadList))

	for _, info := range uploadList {
		err := func(info nix.PathInfo) error {
			log.Action("Processing: %s", info.Path)

			narFile, fileHash, fileSize, err := nix.ExportAndCompress(ctx, info.Path)
			if err != nil {
				return fmt.Errorf("failed to export %s: %w", info.Path, err)
			}
			defer os.Remove(narFile)

			log.Action("Uploading compressed NAR blob (%d bytes)...", fileSize)
			digest, err := p.client.UploadBlob(ctx, narFile, fileHash)
			if err != nil {
				return fmt.Errorf("failed to upload blob for %s: %w", info.Path, err)
			}

			sigs := info.Signatures
			if p.signer != nil {
				sig, err := p.signer.SignPath(info.Path, info.NarHash, info.NarSize, info.References)
				if err != nil {
					return fmt.Errorf("failed to sign path: %w", err)
				}
				sigs = append(sigs, sig)
			}

			narinfoContent := nix.GenerateNarInfo(info.Path, info.NarHash, info.NarSize, fileHash, fileSize, info.References, sigs)
			hash := nix.GetPathHash(info.Path)

			index.AddEntry(hash, nix.GetPathName(info.Path), narinfoContent, digest, fileSize, info.References)
			return nil
		}(info)

		if err != nil {
			return err
		}
	}

	log.Action("Updating remote cache-index...")
	if err := p.client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	log.Success("Cached %d packages successfully.", len(uploadList))
	return nil
}
