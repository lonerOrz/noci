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

	_ = proxyCmd.MarkFlagRequired("repo")
}

func runProxy(cmd *cobra.Command, args []string) error {
	token := os.Getenv("GITHUB_TOKEN")
	addr := proxyListen + ":" + strconv.Itoa(proxyPort)

	srv := server.NewServer(proxyRegistry, proxyRepo, token, addr, proxyUpstream, proxyTTL)
	fmt.Printf(">>> Starting proxy on http://%s\n", addr)
	fmt.Printf(">>> Target OCI repository: %s/%s\n", proxyRegistry, proxyRepo)
	return srv.Start()
}
