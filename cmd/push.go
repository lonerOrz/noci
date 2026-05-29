package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	pushRepo      string
	pushRegistry  string
	pushKeyFile   string
	pushConfigDir string
)

var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "Build local paths and push to OCI registry",
	RunE:  runPush,
}

func init() {
	pushCmd.Flags().StringVar(&pushRepo, "repo", "", "OCI repository (e.g. username/repo)")
	pushCmd.Flags().StringVar(&pushRegistry, "registry", "ghcr.io", "OCI registry endpoint")
	pushCmd.Flags().StringVar(&pushKeyFile, "key-file", "", "Nix private signing key file (optional)")
	pushCmd.Flags().StringVar(&pushConfigDir, "config-dir", "config", "Path to Nix flake directory")

	_ = pushCmd.MarkFlagRequired("repo")
}

func runPush(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	token := os.Getenv("GITHUB_TOKEN")

	// 1. 初始化 OCI 客户端
	client := oci.NewClient(pushRegistry, pushRepo, token)

	// 2. 加载或拉取现有的 index
	index, err := client.FetchIndex(ctx)
	if err != nil {
		fmt.Printf(">>> No existing index found. Starting fresh.\n")
		index = oci.NewIndex(pushRegistry, pushRepo)
	}

	// 3. 读取 Stdin 传入的要推送的 Nix 路径
	var inputPaths []string
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			// 支持传入 nix build --json 生成的格式
			if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "{") {
				var buildOutputs []map[string]interface{}
				if err := json.Unmarshal([]byte(line), &buildOutputs); err == nil {
					for _, out := range buildOutputs {
						if outs, ok := out["outputs"].(map[string]interface{}); ok {
							for _, pathVal := range outs {
								if pStr, ok := pathVal.(string); ok {
									inputPaths = append(inputPaths, pStr)
								}
							}
						}
					}
				}
				continue
			}
			inputPaths = append(inputPaths, line)
		}
	}

	if len(inputPaths) == 0 {
		return fmt.Errorf("no paths provided via stdin")
	}

	// 4. 获取完整闭包并过滤已缓存的路径
	fmt.Printf(">>> Evaluating closure for %d input path(s)...\n", len(inputPaths))
	closure, err := nix.GetClosure(inputPaths)
	if err != nil {
		return fmt.Errorf("failed to get closure: %w", err)
	}

	var uploadList []nix.PathInfo
	for _, path := range closure {
		hash := nix.GetPathHash(path)
		if _, exists := index.Entries[hash]; exists {
			continue
		}
		// 获取本路径的元数据
		info, err := nix.GetPathInfo(path)
		if err != nil {
			return fmt.Errorf("failed to get path info for %s: %w", path, err)
		}
		uploadList = append(uploadList, *info)
	}

	if len(uploadList) == 0 {
		fmt.Printf(">>> Everything is already cached!\n")
		return nil
	}

	fmt.Printf(">>> Found %d new store path(s) to cache.\n", len(uploadList))

	// 5. 对新路径进行导出、打包、哈希，并上传到 OCI 作为 Blobs
	for _, info := range uploadList {
		// 核心修复：通过匿名函数包裹单次循环，确保 defer 能在每次迭代结束时立即释放磁盘空间，避免大量打包文件导致磁盘满
		err := func(info nix.PathInfo) error {
			fmt.Printf(">>> Processing: %s\n", info.Path)

			// 流式打包并压缩
			narFile, fileHash, fileSize, err := nix.ExportAndCompress(info.Path)
			if err != nil {
				return fmt.Errorf("failed to export %s: %w", info.Path, err)
			}
			defer os.Remove(narFile) // 此时 defer 会在当前匿名函数块结束时立即执行

			// 上传 Blob 到 OCI
			fmt.Printf("    Uploading compressed NAR blob (%d bytes)...\n", fileSize)
			digest, err := client.UploadBlob(ctx, narFile, fileHash)
			if err != nil {
				return fmt.Errorf("failed to upload blob for %s: %w", info.Path, err)
			}

			// 签名逻辑 (如果配置了密钥文件)
			sigs := info.Signatures
			if pushKeyFile != "" {
				signer, err := nix.NewSigner(pushKeyFile)
				if err != nil {
					return fmt.Errorf("failed to load signer: %w", err)
				}
				sig, err := signer.SignPath(info.Path, info.NarHash, info.NarSize, info.References)
				if err != nil {
					return fmt.Errorf("failed to sign path: %w", err)
				}
				sigs = append(sigs, sig)
			}

			// 生成并格式化 .narinfo 文本
			narinfoContent := nix.GenerateNarInfo(info.Path, info.NarHash, info.NarSize, fileHash, fileSize, info.References, sigs)

			// 合并写入 Index
			hash := nix.GetPathHash(info.Path)
			index.AddEntry(hash, nix.GetPathName(info.Path), narinfoContent, digest, fileSize)
			return nil
		}(info)

		if err != nil {
			return err
		}
	}

	// 6. 推送更新后的 Index 结构
	fmt.Printf(">>> Updating remote cache-index...\n")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	fmt.Printf(">>> Success! Cached %d packages.\n", len(uploadList))
	return nil
}
