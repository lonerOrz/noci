package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"noci/pkg/log"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"noci/pkg/publisher"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	pushFlags            CommonFlags
	pushSigningKey       string
	pushKeyFile          string
	pushCompression      string
	pushCompressionLevel int
	pushSkipUpstream     bool
	pushJobs             int
	pushProfile          bool
	pushRegistries       []string
)

var pushCmd = &cobra.Command{
	Use:   "push [paths or targets...]",
	Short: "Build local paths or targets and push to OCI registry",
	Long: `Push one or more Nix store paths, flake targets, or derivation outputs
to an OCI registry. Paths can be provided as arguments or via stdin.

When a target is not a store path, it is built via 'nix build --no-link --json'
before pushing. Stdin accepts JSON array, newline-delimited paths, or pipe from
'--json' output.

Signing is required for cache integrity. Provide a key via NOCI_SIGNING_KEY env
var or --key-file flag.`,
	Args: cobra.ArbitraryArgs,
	RunE: runPush,
}

func init() {
	pushFlags.Register(pushCmd)
	pushCmd.Flags().StringVar(&pushSigningKey, "signing-key", "", "Nix private signing key string (key_name:base64) (env: NOCI_SIGNING_KEY)")
	pushCmd.Flags().StringVar(&pushKeyFile, "key-file", "", "Nix private signing key file path (env: NOCI_KEY_FILE)")
	pushCmd.Flags().StringVarP(&pushCompression, "compression", "c", "zstd", "Compression algorithm (zstd, gzip)")
	pushCmd.Flags().BoolVar(&pushSkipUpstream, "skip-upstream", true, "Skip pushing packages that carry an upstream cache.nixos.org signature")
	pushCmd.Flags().IntVarP(&pushJobs, "jobs", "j", 0, "Zstd compression threads (0 = auto: min(4, max(1, NumCPU/2)))")
	pushCmd.Flags().IntVar(&pushCompressionLevel, "compression-level", 3, "Zstd compression level (1-19, higher = smaller but slower)")
	pushCmd.Flags().BoolVar(&pushProfile, "profile", false, "Print detailed performance profiling of the push pipeline")
	pushCmd.Flags().StringArrayVar(&pushRegistries, "registries", nil, "Additional registries as registry/repo (can be specified multiple times)")
}

func runPush(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	pushFlags.ExtraRegistries = pushRegistries
	cfg, err := pushFlags.ResolveConfig()
	if err != nil {
		return err
	}

	signer, err := resolveSigner()
	if err != nil {
		return err
	}

	inputPaths, err := resolveInputs(ctx, args)
	if err != nil {
		return err
	}

	comp := strings.ToLower(strings.TrimSpace(pushCompression))
	if comp != "zstd" && comp != "gzip" {
		return fmt.Errorf("unsupported compression: %q (use 'zstd' or 'gzip')", pushCompression)
	}

	clients := make([]*oci.Client, 0, len(cfg.Registries))
	for _, e := range cfg.Registries {
		client := oci.NewClient(e.Registry, e.Repo, e.Token)
		client.Profile = pushProfile
		clients = append(clients, client)
	}

	pub := publisher.NewPublisher(clients, signer, pushSkipUpstream, comp, pushCompressionLevel, pushJobs)
	pub.Profile = pushProfile
	return pub.Publish(ctx, inputPaths)
}

// resolveSigner loads the signing key from flag, env, or file.
func resolveSigner() (*nix.Signer, error) {
	signingKey := pushSigningKey
	if signingKey == "" {
		signingKey = os.Getenv("NOCI_SIGNING_KEY")
	}

	keyFile := pushKeyFile
	if keyFile == "" {
		keyFile = os.Getenv("NOCI_KEY_FILE")
	}

	if signingKey == "" && keyFile == "" {
		return nil, fmt.Errorf("signing key is required to guarantee cache integrity. " +
			"Please specify via --signing-key, --key-file, or NOCI_SIGNING_KEY / NOCI_KEY_FILE environment variables")
	}

	if signingKey != "" {
		return nix.NewSignerFromKey(signingKey)
	}
	return nix.NewSigner(keyFile)
}

// resolveInputs resolves CLI args and stdin into store paths.
func resolveInputs(ctx context.Context, args []string) ([]string, error) {
	var inputPaths []string

	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}

		if !strings.HasPrefix(arg, "/nix/store") {
			log.Action("Target %q does not look like a store path. Running `nix build %s --no-link --json`...", arg, arg)
			buildPaths, err := nix.BuildTarget(ctx, arg)
			if err != nil {
				return nil, fmt.Errorf("build target %q: %w", arg, err)
			}
			inputPaths = append(inputPaths, buildPaths...)
		} else {
			inputPaths = append(inputPaths, arg)
		}
	}

	if len(inputPaths) == 0 {
		inputPaths = readStdinPaths()
	}

	if len(inputPaths) == 0 {
		return nil, fmt.Errorf("no paths or targets provided via arguments or stdin")
	}

	return inputPaths, nil
}

// readStdinPaths reads store paths from stdin (JSON array or newline-delimited).
func readStdinPaths() []string {
	stdinBytes, err := io.ReadAll(os.Stdin)
	if err != nil || len(stdinBytes) == 0 {
		return nil
	}

	trimmed := strings.TrimSpace(string(stdinBytes))
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		paths, err := nix.ParseJSONBuildOutputs([]byte(trimmed))
		if err != nil {
			return nil
		}
		return paths
	}

	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}
