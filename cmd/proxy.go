package cmd

import (
	"fmt"
	"noci/pkg/server"
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
	cfg, err := resolveOCIConfig(proxyRegistry, proxyRepo)
	if err != nil {
		return err
	}

	addr := proxyListen + ":" + strconv.Itoa(proxyPort)
	srv := server.NewServer(cfg.Registry, cfg.Repo, cfg.Token, addr, proxyUpstream, proxyTTL)

	fmt.Printf(">>> Starting proxy on http://%s\n", addr)
	fmt.Printf(">>> Target OCI repository: %s/%s\n", cfg.Registry, cfg.Repo)
	return srv.Start()
}
