package cmd

import (
	"fmt"
	"os"
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
