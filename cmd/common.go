package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type OCIConfig struct {
	Registry string
	Repo     string
	Token    string
}

// resolveOCIConfig 解析 OCI 基础参数，提供一致的环境变量后备支持
func resolveOCIConfig(flagRegistry, flagRepo string) (OCIConfig, error) {
	registry := flagRegistry
	if registry == "" {
		registry = os.Getenv("NOCI_REGISTRY")
	}
	if registry == "" {
		registry = "ghcr.io"
	}

	repo := flagRepo
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

	return OCIConfig{
		Registry: registry,
		Repo:     repo,
		Token:    token,
	}, nil
}

// parseSizeString 解析人类易读的大小限制字符串（如 "500MB", "10GB"）为字节数
func parseSizeString(sizeStr string) (int64, error) {
	sizeStr = strings.ToUpper(strings.TrimSpace(sizeStr))
	if sizeStr == "" {
		return 0, nil
	}
	re := regexp.MustCompile(`^(\d+)\s*(B|KB|MB|GB|TB)?$`)
	matches := re.FindStringSubmatch(sizeStr)
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
	case "KB":
		return val * 1024, nil
	case "MB":
		return val * 1024 * 1024, nil
	case "GB":
		return val * 1024 * 1024 * 1024, nil
	case "TB":
		return val * 1024 * 1024 * 1024 * 1024, nil
	default:
		return val, nil
	}
}
