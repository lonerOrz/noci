# noci-action

Zero-config Nix binary cache over OCI for GitHub Actions.

- **Fetch-only (default):** proxy serves cached paths. No signing key needed.
- **Push mode:** add signing key → built paths auto-push after job completion.
- **Port Sync:** Uses explicit port-file signaling to avoid race conditions on ephemeral self-hosted runners.

## Quick Start

```yaml
# Fetch-only — just add this line
- uses: lonerOrz/noci/action@v1

# Push + fetch — add signing key
- uses: lonerOrz/noci/action@v1
  with:
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}
```

## Inputs

| Input               | Description                                                    | Default             |
| :------------------ | :------------------------------------------------------------- | :------------------ |
| `signing-key`       | Nix private signing key (`key_name:base64`). Omit = fetch-only | _(none)_            |
| `registry`          | OCI registry endpoint                                          | `ghcr.io`           |
| `repo`              | OCI repository (`owner/repo`)                                  | `GITHUB_REPOSITORY` |
| `token`             | Registry auth token                                            | `GITHUB_TOKEN`      |
| `compression`       | `zstd` or `gzip`                                               | `zstd`              |
| `compression-level` | Zstd compression level (1-19)                                  | `3`                 |
| `jobs`              | Compression threads (`0` = auto)                               | `0`                 |
| `skip-upstream`     | Skip paths with upstream `cache.nixos.org` signatures          | `true`              |
| `fail-on-error`     | Push failures = warning, not CI failure                        | `false`             |
| `proxy-port`        | Local proxy port (`0` = dynamic ephemeral port)                | `0`                 |

## Outputs

- **`proxy-url`** — HTTP address of the running local proxy
- **`pushed-count`** — Number of store paths pushed to OCI registry

## Permissions

```yaml
permissions:
  contents: read
  packages: write
```

## How It Works

1. **Proxy** starts, configures Nix substituter via `NIX_CONFIG`
2. **Build** steps pull dependencies through proxy (hit) or upstream (miss)
3. **Hook** collects all built output paths to an isolated log file
4. **Push** uploads new paths to OCI registry after the job (if signing key provided)

## Self-hosted Runners

- Keep `proxy-port: 0` (default) for automatic ephemeral port allocation
- Each run gets isolated hook/log paths via `GITHUB_RUN_ID` + `GITHUB_RUN_ATTEMPT`
- Proxy process is automatically killed in the post step
