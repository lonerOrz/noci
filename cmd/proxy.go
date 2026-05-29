package cmd

import (
	"fmt"
	"noci/pkg/server"
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

var (
	proxyRepo     string
	proxyRegistry string
	proxyPort     int
	proxyListen   string
	proxyUpstream string
	proxyTTL      int
)

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Start client-side local cache proxy server",
	RunE:  runProxy,
}

func init() {
	proxyCmd.Flags().StringVar(&proxyRepo, "repo", "", "OCI repository (e.g. username/repo)")
	proxyCmd.Flags().StringVar(&proxyRegistry, "registry", "ghcr.io", "OCI registry endpoint")
	proxyCmd.Flags().IntVar(&proxyPort, "port", 37515, "Port to listen on")
	proxyCmd.Flags().StringVar(&proxyListen, "listen", "127.0.0.1", "Listen address")
	proxyCmd.Flags().StringVar(&proxyUpstream, "upstream", "https://cache.nixos.org", "Fallback upstream cache")
	proxyCmd.Flags().IntVar(&proxyTTL, "ttl", 300, "Index TTL in seconds")
}

func runProxy(cmd *cobra.Command, args []string) error {
	registry := proxyRegistry
	if registry == "" {
		registry = os.Getenv("NOCI_REGISTRY")
	}
	if registry == "" {
		registry = "ghcr.io"
	}

	repo := proxyRepo
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

	addr := proxyListen + ":" + strconv.Itoa(proxyPort)

	srv := server.NewServer(registry, repo, token, addr, proxyUpstream, proxyTTL)
	fmt.Printf(">>> Starting proxy on http://%s\n", addr)
	fmt.Printf(">>> Target OCI repository: %s/%s\n", registry, repo)
	return srv.Start()
}
