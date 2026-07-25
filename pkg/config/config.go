package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrRepoRequired is returned when no repository can be resolved.
var ErrRepoRequired = fmt.Errorf("repository is required (auto-detection failed; specify via --repo or NOCI_REPO)")

// Target represents a resolved OCI registry target.
type Target struct {
	Registry string
	Repo     string
	Token    string
}

// Options represents raw input from CLI flags or caller.
type Options struct {
	Registry        string
	Repo            string
	Token           string
	ExtraRegistries []string
}

// Config represents fully resolved configuration.
type Config struct {
	Primary    Target
	Registries []Target
}

// nociConfig is the YAML configuration file structure.
type nociConfig struct {
	Registry   string           `yaml:"registry"`
	Repo       string           `yaml:"repo"`
	Token      string           `yaml:"token"`
	Registries []registryTarget `yaml:"registries"`
}

// registryTarget is a single registry entry in YAML config.
type registryTarget struct {
	Name     string `yaml:"name"`
	Registry string `yaml:"registry"`
	Repo     string `yaml:"repo"`
	Token    string `yaml:"token"`
	Mode     string `yaml:"mode"` // "push-pull" (default), "push-only", "pull-only"
}

// Load resolves configuration by merging flags, env vars, config files, git remote, and tokens.
func Load(opts Options) (*Config, error) {
	registry := opts.Registry
	if registry == "" {
		registry = os.Getenv("NOCI_REGISTRY")
	}
	if registry == "" {
		registry = "ghcr.io"
	}

	repo := opts.Repo
	if repo == "" {
		repo = os.Getenv("NOCI_REPO")
	}
	if repo == "" && os.Getenv("GITHUB_ACTIONS") == "true" {
		repo = os.Getenv("GITHUB_REPOSITORY")
	}
	if repo == "" {
		repo = autoDetectGitRepo()
	}
	if repo == "" {
		return nil, ErrRepoRequired
	}

	token := opts.Token
	if token == "" {
		token = os.Getenv("NOCI_TOKEN")
	}
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		token = readGhCLIToken()
	}
	if token == "" {
		token = readDockerConfigToken(registry)
	}

	primary := Target{Registry: registry, Repo: repo, Token: token}

	registries, err := ResolveRegistries(opts.ExtraRegistries, primary)
	if err != nil {
		return nil, err
	}

	return &Config{Primary: primary, Registries: registries}, nil
}

// ResolveRegistries merges extra registry values with YAML config and falls back to single target.
func ResolveRegistries(values []string, base Target) ([]Target, error) {
	if len(values) > 0 {
		return ParseRegistries(values, base)
	}

	cfg, err := loadNociConfig()
	if err != nil {
		return []Target{base}, nil
	}

	if len(cfg.Registries) > 0 {
		var entries []Target
		for _, rt := range cfg.Registries {
			if rt.Mode == "pull-only" {
				continue
			}
			token := rt.Token
			if token == "" {
				token = os.Getenv("NOCI_TOKEN")
			}
			entries = append(entries, Target{
				Registry: rt.Registry,
				Repo:     rt.Repo,
				Token:    token,
			})
		}
		if len(entries) > 0 {
			return entries, nil
		}
	}

	return []Target{base}, nil
}

// ParseRegistries parses registry/repo values into Target list.
func ParseRegistries(values []string, base Target) ([]Target, error) {
	if len(values) == 0 {
		return []Target{base}, nil
	}
	var entries []Target
	for _, v := range values {
		parts := strings.SplitN(v, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid registry format %q (expected registry/repo)", v)
		}
		token := os.Getenv("NOCI_TOKEN")
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}
		entries = append(entries, Target{Registry: parts[0], Repo: parts[1], Token: token})
	}
	return entries, nil
}

func configFilePaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".config", "noci", "config.yaml"),
		filepath.Join(home, ".config", "noci", "config.yml"),
		"noci.yaml",
		"noci.yml",
	}
}

func loadNociConfig() (*nociConfig, error) {
	for _, path := range configFilePaths() {
		data, err := os.ReadFile(path)
		if err == nil {
			var cfg nociConfig
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return nil, fmt.Errorf("failed to parse %s: %w", path, err)
			}
			return &cfg, nil
		}
	}
	return nil, fmt.Errorf("no config file found")
}

// autoDetectGitRepo extracts owner/repo from local Git origin remote.
func autoDetectGitRepo() string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(out))
	return parseGitRemote(raw)
}

// parseGitRemote extracts owner/repo from a git remote URL string.
func parseGitRemote(raw string) string {
	if strings.HasSuffix(raw, ".git") {
		raw = strings.TrimSuffix(raw, ".git")
	}
	// URL-style: https://host/owner/repo or ssh://git@host/owner/repo
	if idx := strings.Index(raw, "://"); idx != -1 {
		raw = raw[idx+3:]
		if atIdx := strings.Index(raw, "@"); atIdx != -1 {
			raw = raw[atIdx+1:]
		}
		if slashIdx := strings.Index(raw, "/"); slashIdx != -1 {
			raw = raw[slashIdx+1:]
		}
		if strings.Count(raw, "/") == 1 && raw != "" && !strings.HasPrefix(raw, "/") {
			return raw
		}
		return ""
	}
	// SSH SCP-style: git@host:owner/repo
	if idx := strings.LastIndex(raw, ":"); idx != -1 {
		suffix := raw[idx+1:]
		if strings.Contains(suffix, "/") {
			return suffix
		}
	}
	return ""
}

// readGhCLIToken reads authentication token from GitHub CLI hosts config.
func readGhCLIToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "gh", "hosts.yml"))
	if err != nil {
		return ""
	}
	var cfg map[string]struct {
		OauthToken string `yaml:"oauth_token"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	if gh, ok := cfg["github.com"]; ok && gh.OauthToken != "" {
		return gh.OauthToken
	}
	return ""
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
