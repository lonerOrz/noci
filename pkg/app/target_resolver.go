package app

import (
	"context"
	"fmt"
	"noci/pkg/domain/types"
	"noci/pkg/log"
	"noci/pkg/nix"
	"strings"
)

type TargetResolver struct {
	runner nix.Runner
}

func NewTargetResolver(r nix.Runner) *TargetResolver {
	if r == nil {
		r = nix.DefaultRunner
	}
	return &TargetResolver{runner: r}
}

func (r *TargetResolver) ResolveHashes(ctx context.Context, args []string, allowBuild bool) ([]types.NixHash, error) {
	var hashes []types.NixHash
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}

		if h, err := types.ParseNixHash(arg); err == nil {
			hashes = append(hashes, h)
			continue
		}

		if strings.HasPrefix(arg, "/nix/store") {
			if sp, err := types.ParseStorePath(arg); err == nil {
				hashes = append(hashes, sp.Hash())
			}
			continue
		}

		if allowBuild {
			log.Action("Target %q is not a local store path or raw hash. Evaluating via `nix build`...", arg)
			buildPaths, err := r.runner.BuildTarget(ctx, arg)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate target %q: %w", arg, err)
			}
			for _, path := range buildPaths {
				if sp, err := types.ParseStorePath(path); err == nil {
					hashes = append(hashes, sp.Hash())
				}
			}
		} else {
			log.Action("Target %q is not a local store path or raw hash. Evaluating via `nix eval`...", arg)
			outPath, err := r.runner.EvalOutPath(ctx, arg)
			if err != nil {
				return nil, fmt.Errorf("target %q failed evaluation: %w", arg, err)
			}
			if sp, err := types.ParseStorePath(outPath); err == nil {
				hashes = append(hashes, sp.Hash())
			}
		}
	}
	return hashes, nil
}
