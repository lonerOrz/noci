package cmd

import (
	"bufio"
	"fmt"
	"noci/pkg/log"
	"noci/pkg/nix"
	"noci/pkg/oci"
	"noci/pkg/publisher"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	pushFlags     CommonFlags
	pushKeyFile   string
	pushConfigDir string
)

var pushCmd = &cobra.Command{
	Use:   "push [paths or targets...]",
	Short: "Build local paths or targets and push to OCI registry",
	RunE:  runPush,
}

func init() {
	pushFlags.Register(pushCmd)
	pushCmd.Flags().StringVar(&pushKeyFile, "key-file", "", "Nix private signing key file (optional)")
	pushCmd.Flags().StringVar(&pushConfigDir, "config-dir", "config", "Path to Nix flake directory")
}

func runPush(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	cfg, err := pushFlags.Resolve()
	if err != nil {
		return err
	}

	var signer *nix.Signer
	signingKey := os.Getenv("NOCI_SIGNING_KEY")
	keyFile := pushKeyFile
	if keyFile == "" {
		keyFile = os.Getenv("NOCI_KEY_FILE")
	}

	if signingKey == "" && keyFile == "" {
		return fmt.Errorf("signing key is required to guarantee cache integrity. " +
			"Please specify your private key via the NOCI_SIGNING_KEY environment variable " +
			"or the --key-file flag")
	}

	if signingKey != "" {
		var err error
		signer, err = nix.NewSignerFromKey(signingKey)
		if err != nil {
			return fmt.Errorf("failed to load signing key from NOCI_SIGNING_KEY: %w", err)
		}
	} else {
		var err error
		signer, err = nix.NewSigner(keyFile)
		if err != nil {
			return fmt.Errorf("failed to load signing key from file: %w", err)
		}
	}

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
				return fmt.Errorf("failed to build target %q: %w", arg, err)
			}
			inputPaths = append(inputPaths, buildPaths...)
		} else {
			inputPaths = append(inputPaths, arg)
		}
	}

	if len(inputPaths) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				if strings.HasPrefix(line, "[") || strings.HasPrefix(line, "{") {
					paths, err := nix.ParseJSONBuildOutputs([]byte(line))
					if err == nil {
						inputPaths = append(inputPaths, paths...)
					}
					continue
				}
				inputPaths = append(inputPaths, line)
			}
		}
	}

	if len(inputPaths) == 0 {
		return fmt.Errorf("no paths or targets provided via arguments or stdin")
	}

	client := oci.NewClient(cfg.Registry, cfg.Repo, cfg.Token)
	pub := publisher.NewPublisher(client, signer)

	return pub.Publish(ctx, inputPaths)
}
