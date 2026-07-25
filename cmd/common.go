package cmd

import (
	"noci/pkg/config"

	"github.com/spf13/cobra"
)

// CommonFlags holds shared CLI flags for OCI registry configuration.
type CommonFlags struct {
	Repo     string
	Registry string
}

// Register adds common flags to the given command.
func (cf *CommonFlags) Register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&cf.Repo, "repo", "", "OCI repository (e.g. username/repo)")
	cmd.Flags().StringVar(&cf.Registry, "registry", "ghcr.io", "OCI registry endpoint")
}

// Resolve resolves flags, env vars, git remote, and tokens into a registry target.
func (cf *CommonFlags) Resolve() (config.Target, error) {
	cfg, err := config.Load(config.Options{
		Registry: cf.Registry,
		Repo:     cf.Repo,
	})
	if err != nil {
		return config.Target{}, err
	}
	return cfg.Primary, nil
}
