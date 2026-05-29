package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"os"
	"os/exec"
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
	Use:   "push [paths or targets...]",
	Short: "Build local paths or targets and push to OCI registry",
	RunE:  runPush,
}

func init() {
	pushCmd.Flags().StringVar(&pushRepo, "repo", "", "OCI repository (e.g. username/repo)")
	pushCmd.Flags().StringVar(&pushRegistry, "registry", "ghcr.io", "OCI registry endpoint")
	pushCmd.Flags().StringVar(&pushKeyFile, "key-file", "", "Nix private signing key file (optional)")
	pushCmd.Flags().StringVar(&pushConfigDir, "config-dir", "config", "Path to Nix flake directory")
}

func runPush(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// 1. 解析 Registry、Repository 与 Token (支持环境变量 fallback)
	registry := pushRegistry
	if registry == "" {
		registry = os.Getenv("NOCI_REGISTRY")
	}
	if registry == "" {
		registry = "ghcr.io"
	}

	repo := pushRepo
	if repo == "" {
		repo = os.Getenv("NOCI_REPO")
	}
	if repo == "" && os.Getenv("GITHUB_ACTIONS") == "true" {
		repo = os.Getenv("GITHUB_REPOSITORY")
	}
	if repo == "" {
		return fmt.Errorf("repository is required (specify via --repo or NOCI_REPO/GITHUB_REPOSITORY env)")
	}

	token := os.Getenv("NOCI_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}

	// 2. 初始化签名器并强制进行安全校验
	var signer *nix.Signer
	signingKey := os.Getenv("NOCI_SIGNING_KEY")
	keyFile := pushKeyFile
	if keyFile == "" {
		keyFile = os.Getenv("NOCI_KEY_FILE")
	}

	// 核心安全修改：强制要求必须存在签名密钥
	if signingKey == "" && keyFile == "" {
		return fmt.Errorf("signing key is required to guarantee cache integrity. " +
			"Please specify your private key via the NOCI_SIGNING_KEY environment variable " +
			"or the --key-file flag")
	}

	if signingKey != "" {
		var err error
		signer, err = nix.NewSignerFromKey(signingKey)
		if err != nil {
			return fmt.Errorf("failed to load signing key from NOCI_SIGNING_KEY: %w", err)
		}
	} else if keyFile != "" {
		var err error
		signer, err = nix.NewSigner(keyFile)
		if err != nil {
			return fmt.Errorf("failed to load signing key from file: %w", err)
		}
	}

	// 3. 初始化 OCI 客户端
	client := oci.NewClient(registry, repo, token)

	// 4. 加载或拉取现有的 index
	index, err := client.FetchIndex(ctx)
	if err != nil {
		fmt.Printf(">>> No existing index found. Starting fresh.\n")
		index = oci.NewIndex(registry, repo)
	}

	// 5. 汇集待上传的 Nix 路径
	var inputPaths []string

	// 如果有命令行参数，优先处理
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		// 若参数不以 "/nix/store" 开头，视为 Flake 属性或构建目标，调用 `nix build` 动态获取
		if !strings.HasPrefix(arg, "/nix/store") {
			fmt.Printf(">>> Target %q does not look like a store path. Running `nix build %s --no-link --json`...\n", arg, arg)
			buildPaths, err := runNixBuild(arg)
			if err != nil {
				return fmt.Errorf("failed to build target %q: %w", arg, err)
			}
			inputPaths = append(inputPaths, buildPaths...)
		} else {
			inputPaths = append(inputPaths, arg)
		}
	}

	// 若没有命令行参数，降级为从 Stdin 读取
	if len(inputPaths) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				// 支持传入 nix build --json 生成的格式
				if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "{") {
					paths, err := parseJSONBuildOutputs([]byte(line))
					if err == nil {
						inputPaths = append(inputPaths, paths...)
					}
					continue
				}
				inputPaths = append(inputPaths, line)
			}
		}
	}

	if len(inputPaths) == 0 {
		return fmt.Errorf("no paths or targets provided via arguments or stdin")
	}

	// 6. 获取完整闭包并过滤已缓存的路径 (排除 OCI 已存和官方源已存的包)
	fmt.Printf(">>> Evaluating closure for %d input path(s)...\n", len(inputPaths))
	closure, err := nix.GetClosure(inputPaths)
	if err != nil {
		return fmt.Errorf("failed to get closure: %w", err)
	}

	var uploadList []nix.PathInfo
	skippedUpstreamCount := 0

	for _, path := range closure {
		hash := nix.GetPathHash(path)
		// 优先过滤：如果本 OCI 仓库的索引中已存在该包，则跳过
		if _, exists := index.Entries[hash]; exists {
			continue
		}

		// 获取本路径的元数据
		info, err := nix.GetPathInfo(path)
		if err != nil {
			return fmt.Errorf("failed to get path info for %s: %w", path, err)
		}

		// 核心优化：检测包是否已具有官方 cache.nixos.org-1 的签名
		hasUpstreamSig := false
		for _, sig := range info.Signatures {
			if strings.HasPrefix(sig, "cache.nixos.org-1:") {
				hasUpstreamSig = true
				break
			}
		}

		// 如果官方仓库已含有此缓存，我们直接跳过，交由本地代理服务透明代理回源
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

	// 7. 对新路径进行导出、打包、哈希，并上传到 OCI 作为 Blobs
	for _, info := range uploadList {
		err := func(info nix.PathInfo) error {
			fmt.Printf(">>> Processing: %s\n", info.Path)

			// 流式打包并压缩
			narFile, fileHash, fileSize, err := nix.ExportAndCompress(info.Path)
			if err != nil {
				return fmt.Errorf("failed to export %s: %w", info.Path, err)
			}
			defer os.Remove(narFile)

			// 上传 Blob 到 OCI
			fmt.Printf("    Uploading compressed NAR blob (%d bytes)...\n", fileSize)
			digest, err := client.UploadBlob(ctx, narFile, fileHash)
			if err != nil {
				return fmt.Errorf("failed to upload blob for %s: %w", info.Path, err)
			}

			// 签名逻辑 (此时已保证 signer 不为 nil)
			sigs := info.Signatures
			if signer != nil {
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

	// 8. 推送更新后的 Index 结构
	fmt.Printf(">>> Updating remote cache-index...\n")
	if err := client.PushIndex(ctx, index); err != nil {
		return fmt.Errorf("failed to push updated index: %w", err)
	}

	fmt.Printf(">>> Success! Cached %d packages.\n", len(uploadList))
	return nil
}

// runNixBuild 调用 `nix build` 命令获取其 JSON 形式的输出路径
func runNixBuild(target string) ([]string, error) {
	cmd := exec.Command("nix", "build", target, "--no-link", "--json")
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, errOut.String())
	}
	return parseJSONBuildOutputs(out.Bytes())
}

// parseJSONBuildOutputs 解析 nix build --json 输出的包路径信息
func parseJSONBuildOutputs(data []byte) ([]string, error) {
	var buildOutputs []map[string]interface{}
	if err := json.Unmarshal(data, &buildOutputs); err != nil {
		return nil, err
	}
	var paths []string
	for _, out := range buildOutputs {
		if outs, ok := out["outputs"].(map[string]interface{}); ok {
			for _, pathVal := range outs {
				if pStr, ok := pathVal.(string); ok {
					paths = append(paths, pStr)
				}
			}
		}
	}
	return paths, nil
}
