package cmd

import (
	"noci/pkg/config"

	"github.com/spf13/cobra"
)

// CommonFlags holds shared CLI flags for OCI registry configuration.
type CommonFlags struct {
	Repo           string
	Registry       string
	ExtraRegistries []string
}

// Register adds common flags to the given command.
func (cf *CommonFlags) Register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&cf.Repo, "repo", "", "OCI repository (e.g. username/repo)")
	cmd.Flags().StringVar(&cf.Registry, "registry", "ghcr.io", "OCI registry endpoint")
}

// ResolveConfig resolves flags, env vars, git remote, and tokens into a full config.
func (cf *CommonFlags) ResolveConfig() (*config.Config, error) {
	return config.Load(config.Options{
		Registry:        cf.Registry,
		Repo:            cf.Repo,
		ExtraRegistries: cf.ExtraRegistries,
	})
}

// Resolve resolves flags, env vars, git remote, and tokens into the primary registry target.
func (cf *CommonFlags) Resolve() (config.Target, error) {
	cfg, err := cf.ResolveConfig()
	if err != nil {
		return config.Target{}, err
	}
	return cfg.Primary, nil
}
