package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/nix"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type OCIConfig struct {
	Registry string
	Repo     string
	Token    string
}

var sizeRegex = regexp.MustCompile(`^(\d+)\s*(B|KB|MB|GB|TB|K|M|G|T)?$`)

var nixHashRegex = regexp.MustCompile(`^[0-9abcdfghijklmnpqrsvwxyz]{32}$`)

type CommonFlags struct {
	Repo     string
	Registry string
}

func (cf *CommonFlags) Register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&cf.Repo, "repo", "", "OCI repository (e.g. username/repo)")
	cmd.Flags().StringVar(&cf.Registry, "registry", "ghcr.io", "OCI registry endpoint")
}

func (cf *CommonFlags) Resolve() (OCIConfig, error) {
	registry := cf.Registry
	if registry == "" {
		registry = os.Getenv("NOCI_REGISTRY")
	}
	if registry == "" {
		registry = "ghcr.io"
	}

	repo := cf.Repo
	if repo == "" {
		repo = os.Getenv("NOCI_REPO")
	}
	if repo == "" && os.Getenv("GITHUB_ACTIONS") == "true" {
		repo = os.Getenv("GITHUB_REPOSITORY")
	}
	if repo == "" {
		return OCIConfig{}, fmt.Errorf("repository is required (specify via --repo or NOCI_REPO/GITHUB_REPOSITORY env)")
	}

	token := os.Getenv("NOCI_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = readDockerConfigToken(registry)
	}

	return OCIConfig{
		Registry: registry,
		Repo:     repo,
		Token:    token,
	}, nil
}

func readDockerConfigToken(registry string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(home + "/.docker/config.json")
	if err != nil {
		return ""
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	entry, ok := cfg.Auths[registry]
	if !ok {
		entry, ok = cfg.Auths["https://"+registry]
	}
	if !ok {
		entry, ok = cfg.Auths["http://"+registry]
	}
	if !ok || entry.Auth == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// resolveHashes 统一解析输入。原生兼容 32 位 Nix 纯哈希、Nix Store 绝对路径以及 Flake 构建目标。
func resolveHashes(ctx context.Context, args []string, allowBuild bool) ([]string, error) {
	var hashes []string
	for _, arg := range args {
		// 💡 健壮性优化：转换为全小写并去除空格，规避大小写不一致或环境差异导致的正则误判
		arg = strings.ToLower(strings.TrimSpace(arg))
		if arg == "" {
			continue
		}

		// 策略 A: 原生 32 位 Nix 哈希格式（最轻量，无本地 Nix 评估开销）
		if nixHashRegex.MatchString(arg) {
			hashes = append(hashes, arg)
			continue
		}

		// 策略 B: Nix Store 绝对路径形式
		if strings.HasPrefix(arg, "/nix/store") {
			hash := nix.GetPathHash(arg)
			if hash != "" {
				hashes = append(hashes, hash)
			}
			continue
		}

		// 策略 C: 只有在 pin 等明确允许的情况下，才调用 `nix build`
		if allowBuild {
			log.Action("Target %q is not a local store path or raw hash. Evaluating via `nix build`...", arg)
			buildPaths, err := nix.BuildTarget(ctx, arg)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate target %q: %w", arg, err)
			}
			for _, path := range buildPaths {
				hash := nix.GetPathHash(path)
				if hash != "" {
					hashes = append(hashes, hash)
				}
			}
		} else {
			return nil, fmt.Errorf("target %q is not a valid 32-character Nix hash or store path (building is disabled for this command)", arg)
		}
	}
	return hashes, nil
}

// parseTTL converts human-friendly TTL strings to seconds.
// Supports: "30d", "24h", "90m", "0" (permanent), and Go duration format.
func parseTTL(ttl string) (int64, error) {
	cleaned := strings.ToLower(strings.TrimSpace(ttl))
	if cleaned == "0" {
		return 0, nil
	}
	if strings.HasSuffix(cleaned, "d") {
		daysStr := strings.TrimSuffix(cleaned, "d")
		days, err := strconv.ParseInt(daysStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid day format for TTL: %s", ttl)
		}
		return days * 24 * 3600, nil
	}
	dur, err := time.ParseDuration(ttl)
	if err != nil {
		return 0, fmt.Errorf("failed to parse TTL: %w", err)
	}
	return int64(dur.Seconds()), nil
}

// parseSizeString 解析人类易读的大小限制字符串为字节数
func parseSizeString(sizeStr string) (int64, error) {
	sizeStr = strings.ToUpper(strings.TrimSpace(sizeStr))
	if sizeStr == "" {
		return 0, nil
	}
	matches := sizeRegex.FindStringSubmatch(sizeStr)
	if len(matches) < 2 {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}
	val, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 0, err
	}
	unit := "B"
	if len(matches) > 2 && matches[2] != "" {
		unit = matches[2]
	}
	switch unit {
	case "K", "KB":
		return val * 1024, nil
	case "M", "MB":
		return val * 1024 * 1024, nil
	case "G", "GB":
		return val * 1024 * 1024 * 1024, nil
	case "T", "TB":
		return val * 1024 * 1024 * 1024 * 1024, nil
	default:
		return val, nil
	}
}
