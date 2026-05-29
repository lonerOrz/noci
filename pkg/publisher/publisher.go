package publisher

import (
	"context"
	"fmt"
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

// Publish 封装了获取/创建索引、依赖闭包分析、上游缓存去重、流式压缩、上传和签名的完整事务
func (p *Publisher) Publish(ctx context.Context, inputPaths []string) error {
	// 1. 获取最新索引
	index, err := p.client.FetchIndex(ctx)
	if err != nil {
		fmt.Printf(">>> No existing index found. Starting fresh.\n")
		index = oci.NewIndex(p.client.Registry(), p.client.Repo())
	}

	// 2. 闭包分析与过滤
	fmt.Printf(">>> Evaluating closure for %d input path(s)...\n", len(inputPaths))
	closure, err := nix.GetClosure(inputPaths)
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

		info, err := nix.GetPathInfo(path)
		if err != nil {
			return fmt.Errorf("failed to get path info for %s: %w", path, err)
		}

		// 通过密码学官方签名判断是否能跳过（无需网络开销）
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
		fmt.Printf(">>> Skipped %d path(s) that are already cached on upstream (cache.nixos.org).\n", skippedUpstreamCount)
	}

	if len(uploadList) == 0 {
		fmt.Printf(">>> Everything is already cached (either in OCI or upstream)!\n")
		return nil
	}

	fmt.Printf(">>> Found %d new store path(s) to cache.\n", len(uploadList))

	// 3. 执行物理上传
	for _, info := range uploadList {
		err := func(info nix.PathInfo) error {
			fmt.Printf(">>> Processing: %s\n", info.Path)

			// 物理导出并压缩成临时归档
			narFile, fileHash, fileSize, err := nix.ExportAndCompress(info.Path)
			if err != nil {
				return fmt.Errorf("failed to export %s: %w", info.Path, err)
			}
			defer os.Remove(narFile)

			// 上传 Blob 归档到 OCI 存储
			fmt.Printf("    Uploading compressed NAR blob (%d bytes)...\n", fileSize)
			digest, err := p.client.UploadBlob(ctx, narFile, fileHash)
			if err != nil {
				return fmt.Errorf("failed to upload blob for %s: %w", info.Path, err)
			}

			// 进行签名生成
			sigs := info.Signatures
			if p.signer != nil {
				sig, err := p.signer.SignPath(info.Path, info.NarHash, info.NarSize, info.References)
				if err != nil {
					return fmt.Errorf("failed to sign path: %w", err)
				}
				sigs = append(sigs, sig)
			}

			// 转换 .narinfo 元数据并合并写入本地索引
			narinfoContent := nix.GenerateNarInfo(info.Path, info.NarHash, info.NarSize, fileHash, fileSize, info.References, sigs)
			hash := nix.GetPathHash(info.Path)
			index.AddEntry(hash, nix.GetPathName(info.Path), narinfoContent, digest, fileSize)
			return nil
		}(info)

		if err != nil {
			return err
		}
	}

	// 4. 保存提交索引
	fmt.Printf(">>> Updating remote cache-index...\n")
	if err := p.client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	fmt.Printf(">>> Success! Cached %d packages.\n", len(uploadList))
	return nil
}
