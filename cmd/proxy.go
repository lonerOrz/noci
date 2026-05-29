package cmd

import (
	"noci/pkg/log"
	"noci/pkg/server"
	"strconv"

	"github.com/spf13/cobra"
)

var (
	proxyFlags    CommonFlags
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
	proxyFlags.Register(proxyCmd)
	proxyCmd.Flags().IntVar(&proxyPort, "port", 37515, "Port to listen on")
	proxyCmd.Flags().StringVar(&proxyListen, "listen", "127.0.0.1", "Listen address")
	proxyCmd.Flags().StringVar(&proxyUpstream, "upstream", "https://cache.nixos.org", "Fallback upstream cache")
	proxyCmd.Flags().IntVar(&proxyTTL, "ttl", 300, "Index TTL in seconds")
}

func runProxy(cmd *cobra.Command, args []string) error {
	cfg, err := proxyFlags.Resolve()
	if err != nil {
		return err
	}

	addr := proxyListen + ":" + strconv.Itoa(proxyPort)
	srv := server.NewServer(cfg.Registry, cfg.Repo, cfg.Token, addr, proxyUpstream, proxyTTL)

	log.Action("Starting proxy on http://%s", addr)
	log.Action("Target OCI repository: %s/%s", cfg.Registry, cfg.Repo)
	return srv.Start()
}
