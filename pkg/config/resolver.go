package config

import (
	"context"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/nix"
	"regexp"
	"strings"
)

// NixHashRegex matches 32-char Nix base32 hashes.
var NixHashRegex = regexp.MustCompile(`^[0-9abcdfghijklmnpqrsvwxyz]{32}$`)

// IsNixHash returns true if s is a valid 32-char Nix base32 hash.
func IsNixHash(s string) bool {
	return NixHashRegex.MatchString(strings.ToLower(s))
}

// ResolveHashes resolves input arguments (raw hash, store path, or flake URI) to 32-char Nix hashes.
func ResolveHashes(ctx context.Context, args []string, allowBuild bool) ([]string, error) {
	var hashes []string
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}

		lowerArg := strings.ToLower(arg)
		if NixHashRegex.MatchString(lowerArg) {
			hashes = append(hashes, lowerArg)
			continue
		}

		if strings.HasPrefix(arg, "/nix/store") {
			hash := nix.GetPathHash(arg)
			if hash != "" {
				hashes = append(hashes, strings.ToLower(hash))
			}
			continue
		}

		if allowBuild {
			log.Action("Target %q is not a local store path or raw hash. Evaluating via `nix build`...", arg)
			buildPaths, err := nix.BuildTarget(ctx, arg)
			if err != nil {
				return nil, fmt.Errorf("failed to evaluate target %q: %w", arg, err)
			}
			for _, path := range buildPaths {
				if hash := nix.GetPathHash(path); hash != "" {
					hashes = append(hashes, strings.ToLower(hash))
				}
			}
		} else {
			log.Action("Target %q is not a local store path or raw hash. Evaluating via `nix eval`...", arg)
			outPath, err := nix.EvalOutPath(ctx, arg)
			if err != nil {
				return nil, fmt.Errorf("target %q is not a valid Nix hash/path, and evaluation failed: %w", arg, err)
			}
			if hash := nix.GetPathHash(outPath); hash != "" {
				hashes = append(hashes, strings.ToLower(hash))
			} else {
				return nil, fmt.Errorf("target %q evaluated to invalid store path: %s", arg, outPath)
			}
		}
	}
	return hashes, nil
}
