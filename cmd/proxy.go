package cmd

import (
	"noci/pkg/log"
	"noci/pkg/server"
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

var (
	proxyFlags     CommonFlags
	proxyPort      int
	proxyListen    string
	proxyUpstream  string
	proxyUpstreams []string
	proxyAuthKey   string
	proxyRateLimit float64
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
	proxyCmd.Flags().StringArrayVar(&proxyUpstreams, "upstreams", nil, "Additional upstream caches (can be specified multiple times)")
	proxyCmd.Flags().StringVar(&proxyAuthKey, "auth-key", "", "Authentication key for proxy access (env: NOCI_AUTH_KEY)")
	proxyCmd.Flags().Float64Var(&proxyRateLimit, "rate-limit", 0, "Max requests per second per IP (0 = unlimited)")
}

func runProxy(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	cfg, err := proxyFlags.Resolve()
	if err != nil {
		return err
	}

	authKey := proxyAuthKey
	if authKey == "" {
		authKey = os.Getenv("NOCI_AUTH_KEY")
	}

	addr := proxyListen + ":" + strconv.Itoa(proxyPort)
	srv := server.NewServer(cfg.Registry, cfg.Repo, cfg.Token, addr, proxyUpstream, authKey, proxyRateLimit, proxyUpstreams)

	log.Info("Target OCI repository: %s/%s", cfg.Registry, cfg.Repo)
	return srv.Start(ctx)
}
