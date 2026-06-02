# noci-action

A zero-config, high-performance GitHub Action to use **noci** as a Nix binary cache over OCI registries.

## Usage

Add this step to your GitHub workflow to enable automated caching:

```yaml
- name: Setup noci binary cache
  uses: lonerOrz/noci-action@v1
  with:
    repo: ${{ secrets.NOCI_REPO }} # OCI repository (e.g., owner/repo)
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }} # Required for pushing
    token: ${{ secrets.GH_TOKEN }} # Required for authentication
```

### Inputs

| Input           | Description                                    | Default                           |
| :-------------- | :--------------------------------------------- | :-------------------------------- |
| `repo`          | OCI repository (e.g., `owner/repo`)            | Inferred from `GITHUB_REPOSITORY` |
| `signing-key`   | Raw Nix signing key (`key_name:base64`)        | None (Fetch-only mode)            |
| `token`         | OCI registry token                             | `GITHUB_TOKEN`                    |
| `registry`      | OCI registry endpoint                          | `ghcr.io`                         |
| `skip-upstream` | Skip pushing packages with upstream signatures | `true`                            |
| `proxy-port`    | Port for the local proxy (`0` for random)      | `0`                               |
| `version`       | Version of noci to download                    | `v1.0.0`                          |

## Outputs

- `proxy-url`: The HTTP address of the local noci proxy.
- `pushed-count`: Number of store paths successfully pushed to the OCI registry.

## How it works

1. **Bootstrap**: Downloads or builds the `noci` binary.
2. **Proxy**: Starts a local proxy server and configures Nix to use it as a substituter via `NIX_CONFIG`.
3. **Build**: Your subsequent `nix build` or `nix develop` steps pull dependencies from the OCI cache.
4. **Push**: After the job completes, the action scans newly built paths and pushes them to your OCI registry (if a `signing-key` is provided).

## Notes for Self-hosted Runners

- **Port Management**: It is recommended to keep `proxy-port: 0` (default) to allow the action to automatically negotiate an ephemeral port, preventing collisions on shared host environments.
- **Permissions**: Ensure your job has `packages: write` permission to allow pushing artifacts to the OCI registry.
