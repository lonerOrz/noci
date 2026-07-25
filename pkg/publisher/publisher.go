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
	"sort"
	"strings"
	"sync"
	"time"
)

// ExportCacheEntry holds a pre-exported compressed NAR file shared across registries.
type ExportCacheEntry struct {
	TempFile string
	FileHash string
	FileSize int64
}

type cacheProgress struct {
	done  chan struct{}
	entry *ExportCacheEntry
	err   error
}

// ExportCache ensures ExportAndCompress runs at most once per store path across registries.
type ExportCache struct {
	mu    sync.Mutex
	paths map[string]*cacheProgress
}

func NewExportCache() *ExportCache {
	return &ExportCache{paths: make(map[string]*cacheProgress)}
}

func (c *ExportCache) GetOrCreate(ctx context.Context, storePath string, exportFn func() (*ExportCacheEntry, error)) (*ExportCacheEntry, error) {
	if c == nil {
		return exportFn()
	}

	c.mu.Lock()
	if prog, exists := c.paths[storePath]; exists {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-prog.done:
			return prog.entry, prog.err
		}
	}

	prog := &cacheProgress{done: make(chan struct{})}
	c.paths[storePath] = prog
	c.mu.Unlock()

	entry, err := exportFn()
	prog.entry = entry
	prog.err = err
	close(prog.done)

	return entry, err
}

func (c *ExportCache) Cleanup() {
	if c == nil {
		return
	}
	c.mu.Lock()
	progs := make([]*cacheProgress, 0, len(c.paths))
	for _, prog := range c.paths {
		progs = append(progs, prog)
	}
	c.mu.Unlock()

	for _, prog := range progs {
		<-prog.done
		if prog.err == nil && prog.entry != nil && prog.entry.TempFile != "" {
			os.Remove(prog.entry.TempFile)
		}
	}
}

type Publisher struct {
	clients      []*oci.Client
	signer       *nix.Signer
	skipUpstream bool
	comp         string
	compLevel    int
	jobs         int
	Profile      bool
	cache        *ExportCache
}

func NewPublisher(clients []*oci.Client, signer *nix.Signer, skipUpstream bool, comp string, compLevel int, jobs int) *Publisher {
	if len(clients) == 0 {
		panic("publisher: at least one client required")
	}
	if signer == nil {
		panic("publisher: signer must not be nil")
	}
	return &Publisher{
		clients:      clients,
		signer:       signer,
		skipUpstream: skipUpstream,
		comp:         comp,
		compLevel:    compLevel,
		jobs:         jobs,
		cache:        NewExportCache(),
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

// Publish runs the full publish pipeline across all registries.
func (p *Publisher) Publish(ctx context.Context, inputPaths []string) error {
	defer p.cache.Cleanup()

	if len(p.clients) == 1 {
		return p.publishToRegistry(ctx, p.clients[0], inputPaths)
	}

	log.Info("Pushing to %d registries...", len(p.clients))

	var wg sync.WaitGroup
	errCh := make(chan error, len(p.clients))

	for _, client := range p.clients {
		wg.Add(1)
		go func(c *oci.Client) {
			defer wg.Done()
			if err := p.publishToRegistry(ctx, c, inputPaths); err != nil {
				log.Warning("Push to registry failed: %v", err)
				errCh <- err
			} else {
				log.Success("Push to registry completed.")
			}
		}(client)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for e := range errCh {
		errs = append(errs, e)
	}
	if len(errs) == len(p.clients) {
		return fmt.Errorf("all registries failed: %v", errs)
	}
	if len(errs) > 0 {
		log.Warning("%d registries failed, %d succeeded.", len(errs), len(p.clients)-len(errs))
	}
	return nil
}

// publishToRegistry orchestrates the 4-stage publish pipeline for a single registry.
func (p *Publisher) publishToRegistry(ctx context.Context, store oci.Store, inputPaths []string) error {
	totalStart := time.Now()
	var t0, t1, t2, t3, t4, t5 time.Time

	t0 = time.Now()
	if err := p.stageEnsurePublicKey(ctx, store); err != nil {
		log.Warning("Failed to push public key manifest: %v", err)
	}
	t1 = time.Now()

	uploadList, err := p.stageDiffIndex(ctx, store, inputPaths, &t2, &t3)
	if err != nil {
		return fmt.Errorf("pipeline stage diff-index failed: %w", err)
	}

	if len(uploadList) == 0 {
		if p.Profile {
			log.Info("[profile] Publish pipeline:")
			log.Info("  - Sign/PushManifest: %v", t1.Sub(t0))
			log.Info("  - FetchIndex:        %v", t2.Sub(t1))
			log.Info("  - DiffCheck:         %v", t3.Sub(t2))
			log.Info("  - Total:             %v", time.Since(totalStart))
		}
		log.Success("All packages are already cached!")
		return nil
	}

	results, err := p.stageUploadConcurrently(ctx, store, uploadList)
	if err != nil {
		return fmt.Errorf("pipeline stage upload failed: %w", err)
	}
	t4 = time.Now()

	if err := p.stageMergeIndex(ctx, store, results); err != nil {
		return fmt.Errorf("pipeline stage merge-index failed: %w", err)
	}
	t5 = time.Now()

	if p.Profile {
		log.Info("[profile] Publish pipeline:")
		log.Info("  - Sign/PushManifest: %v", t1.Sub(t0))
		log.Info("  - FetchIndex:        %v", t2.Sub(t1))
		log.Info("  - DiffCheck:         %v", t3.Sub(t2))
		log.Info("  - Upload+PushManifest: %v", t4.Sub(t3))
		log.Info("  - LateMerge:         %v", t5.Sub(t4))
		log.Info("  - Total:             %v", time.Since(totalStart))
	}

	log.Success("Cached %d packages successfully.", len(uploadList))
	return nil
}

// stageEnsurePublicKey pushes the signing public key as an OCI manifest.
func (p *Publisher) stageEnsurePublicKey(ctx context.Context, store oci.Store) error {
	if p.signer == nil {
		return nil
	}
	pubKey := p.signer.PrivateKey.Public().(ed25519.PublicKey)
	publicKeyStr := fmt.Sprintf("%s:%s",
		p.signer.KeyName,
		base64.StdEncoding.EncodeToString(pubKey),
	)
	pubManifest := oci.OCIManifest{
		SchemaVersion: 2,
		MediaType:     oci.MediaTypeImageManifest,
		Config:        oci.DefaultEmptyConfigDescriptor(),
		Annotations: map[string]string{
			"org.nix.public_key": publicKeyStr,
		},
	}
	return store.PushManifest(ctx, "public-key", &pubManifest)
}

// stageDiffIndex computes the closure, diffs against the remote index,
// repairs stale entries, filters upstream-cached paths, and returns the upload list.
// tFetch and tDiff are optional timing outputs for profiling.
func (p *Publisher) stageDiffIndex(ctx context.Context, store oci.Store, inputPaths []string, tFetch, tDiff *time.Time) ([]nix.PathInfo, error) {
	index, err := store.FetchIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load index: %w", err)
	}
	if tFetch != nil {
		*tFetch = time.Now()
	}

	log.Action("Evaluating closure for %d paths...", len(inputPaths))
	closure, err := nix.GetClosure(ctx, inputPaths)
	if err != nil {
		return nil, fmt.Errorf("failed to get closure: %w", err)
	}

	var missing []struct {
		path string
		hash string
	}
	for _, path := range closure {
		hash := nix.GetPathHash(path)
		if _, exists := index.Entries[hash]; !exists {
			missing = append(missing, struct {
				path string
				hash string
			}{path, hash})
		}
	}

	type checkResult struct {
		hash   string
		path   string
		exists bool
	}
	resultChan := make(chan checkResult, len(missing))
	checkSem := make(chan struct{}, 8)
	var checkWg sync.WaitGroup

	for _, m := range missing {
		checkSem <- struct{}{}
		checkWg.Add(1)
		go func(path, hash string) {
			defer func() {
				<-checkSem
				checkWg.Done()
			}()
			exists, _ := store.ManifestExists(ctx, hash)
			resultChan <- checkResult{hash: hash, path: path, exists: exists}
		}(m.path, m.hash)
	}
	checkWg.Wait()
	close(resultChan)

	var uncachedPaths []string
	var repairCount int
	for res := range resultChan {
		if res.exists {
			if err := store.RepairIndexEntry(ctx, res.hash, index); err != nil {
				log.Warning("Failed to repair index entry for %s: %v", res.hash, err)
				uncachedPaths = append(uncachedPaths, res.path)
			} else {
				repairCount++
			}
			continue
		}
		uncachedPaths = append(uncachedPaths, res.path)
	}

	if repairCount > 0 {
		if err := store.PushIndex(ctx, index); err != nil {
			return nil, fmt.Errorf("failed to push repaired index: %w", err)
		}
		log.Success("Repaired %d stale index entries.", repairCount)
	}

	if len(uncachedPaths) == 0 {
		return nil, nil
	}

	infos, err := nix.GetPathInfos(ctx, uncachedPaths)
	if err != nil {
		return nil, fmt.Errorf("failed to get path infos: %w", err)
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

	// Sort by size descending so large files start first
	sort.Slice(uploadList, func(i, j int) bool {
		return uploadList[i].NarSize > uploadList[j].NarSize
	})

	if tDiff != nil {
		*tDiff = time.Now()
	}

	return uploadList, nil
}

// stageUploadConcurrently exports NARs, uploads blobs, and pushes manifests in parallel.
func (p *Publisher) stageUploadConcurrently(ctx context.Context, store oci.Store, uploadList []nix.PathInfo) ([]uploadResult, error) {
	log.Info("Found %d new paths. Uploading concurrently...", len(uploadList))

	outcomeChan := make(chan uploadResult, len(uploadList))
	concurrency := p.jobs
	if concurrency <= 0 {
		concurrency = 4
	}
	sem := make(chan struct{}, concurrency)
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

			res, err := p.publishSingle(pipelineCtx, store, pathInfo)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				errMu.Unlock()
				return
			}

			layerMediaType := oci.MediaTypeNixLayerGzip
			if p.comp == "zstd" {
				layerMediaType = oci.MediaTypeNixLayerZstd
			}
			manifest := oci.OCIManifest{
				SchemaVersion: 2,
				MediaType:     oci.MediaTypeImageManifest,
				Config:        oci.DefaultEmptyConfigDescriptor(),
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
			if err := store.PushManifest(pipelineCtx, res.hash, &manifest); err != nil {
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
		return nil, firstErr
	}

	var results []uploadResult
	for res := range outcomeChan {
		results = append(results, res)
	}
	return results, nil
}

// stageMergeIndex re-fetches the index, adds all new entries, and pushes.
func (p *Publisher) stageMergeIndex(ctx context.Context, store oci.Store, results []uploadResult) error {
	freshIndex, err := store.FetchIndex(ctx)
	if err != nil {
		return fmt.Errorf("failed to re-fetch index for late merge: %w", err)
	}

	for _, res := range results {
		freshIndex.AddEntry(res.hash, res.name, res.narinfo, res.digest, res.size, res.refs)
	}

	if err := store.PushIndex(ctx, freshIndex); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}
	return nil
}

// publishSingle handles export, upload, signing, and narinfo generation for one store path.
func (p *Publisher) publishSingle(ctx context.Context, store oci.Store, info nix.PathInfo) (uploadResult, error) {
	log.Action("Processing: %s", info.Path)

	exportStart := time.Now()

	entry, cacheErr := p.cache.GetOrCreate(ctx, info.Path, func() (*ExportCacheEntry, error) {
		tFile, hash, size, e := nix.ExportAndCompress(ctx, info.Path, p.comp, p.jobs, p.compLevel)
		if e != nil {
			return nil, e
		}
		return &ExportCacheEntry{TempFile: tFile, FileHash: hash, FileSize: size}, nil
	})
	if cacheErr != nil {
		return uploadResult{}, fmt.Errorf("export failed: %w", cacheErr)
	}

	exportDuration := time.Since(exportStart)

	digest, uploadSize, err := store.UploadBlob(ctx, entry.TempFile, entry.FileHash, "NAR", &oci.NoopProgressNotifier{})
	if err != nil {
		return uploadResult{}, fmt.Errorf("upload blob failed: %w", err)
	}
	uploadDuration := time.Since(exportStart)

	if p.Profile {
		log.Info("[profile] Path: %s (%s)", nix.GetPathName(info.Path), oci.FormatSize(uploadSize))
		log.Info("  - Export+compress: %v", exportDuration)
		log.Info("  - Total (incl upload): %v", uploadDuration)
	}

	fileHash := strings.TrimPrefix(digest, "sha256:")
	if b32, err := nix.HexToNixBase32(fileHash); err == nil {
		fileHash = b32
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

	narinfoContent := nix.GenerateNarInfo(info.Path, normalizedNarHash, info.NarSize, fileHash, uploadSize, info.References, sigs, p.comp)
	hash := nix.GetPathHash(info.Path)

	return uploadResult{
		hash:    hash,
		name:    nix.GetPathName(info.Path),
		narinfo: narinfoContent,
		digest:  digest,
		size:    uploadSize,
		refs:    info.References,
	}, nil
}
