# noci-action

Zero-config, high-performance Nix binary cache over OCI registry.

## Usage

Add this step to your GitHub workflow:

```yaml
- name: Setup noci binary cache
  uses: lonerOrz/noci-action@v1
  with:
    # Optional: OCI registry endpoint (default: ghcr.io)
    registry: ghcr.io
    # Optional: OCI repository (e.g. owner/repo). If empty, inferred from GITHUB_REPOSITORY.
    repo: 
    # Optional: Registry token (required for push). If empty, uses GITHUB_TOKEN.
    token: 
    # Optional: Raw Nix signing key content (key_name:base64). If empty, switches to Fetch-only mode.
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}
    # Optional: Skip pushing packages that carry an upstream signature.
    skip-upstream: true
    # Optional: Port for the local noci proxy (0 = bind to random free port).
    proxy-port: 0
    # Optional: Version of noci to download from GitHub Releases if not found locally.
    version: v1.0.0
```

After this step, subsequent `nix build`, `nix develop`, etc. will automatically use the local proxy as a substituter.

If `signing-key` is provided, the action will push newly built store paths to the OCI registry at the end of the job.

## Outputs

- `proxy-url`: HTTP address of the local noci proxy
- `pushed-count`: Number of store paths pushed to the OCI registry (only in push mode)

## How it works

1. The action bootstraps the `noci` binary if needed.
2. It starts a local proxy server that serves as a Nix substituter.
3. The proxy address and public key are injected via `NIX_CONFIG` environment variable.
4. Your build steps run normally, pulling from the proxy cache when possible.
5. At the end of the job, if a signing key was provided, the action scans the local Nix database for newly built paths (using `registrationTime`) and pushes them to the OCI registry.

## Notes for Self-hosted Runners

To avoid port collisions on shared host environments, it is highly recommended to leave `proxy-port` at its default value `0`. This allows the action to automatically negotiate an ephemeral free port.