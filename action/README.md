# noci-action

Zero-config Nix binary cache over OCI for GitHub Actions.

**Fetch-only** (default): proxy serves cached paths. No signing key needed.
**Push mode**: add signing key → built paths auto-push after job. Cache errors never break CI.

## Quick Start

```yaml
# Fetch-only — just add this line
- uses: lonerOrz/noci/action@v1

# Push + fetch — add one input
- uses: lonerOrz/noci/action@v1
  with:
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}
```

## Inputs

| Input               | Description                                            | Default             |
| :------------------ | :----------------------------------------------------- | :------------------ |
| `signing-key`       | Nix signing key (`key_name:base64`). Omit = fetch-only | _(none)_            |
| `registry`          | OCI registry endpoint                                  | `ghcr.io`           |
| `repo`              | OCI repository (`owner/repo`)                          | `GITHUB_REPOSITORY` |
| `token`             | Registry auth token                                    | `GITHUB_TOKEN`      |
| `compression`       | `zstd` or `gzip`                                       | `zstd`              |
| `compression-level` | 1-19 for zstd                                          | `3`                 |
| `jobs`              | Compression threads (`0` = auto)                       | `0`                 |
| `skip-upstream`     | Skip paths with upstream signatures                    | `true`              |
| `fail-on-error`     | Push failures = warning, not CI failure                | `false`             |
| `proxy-port`        | Local proxy port (`0` = random)                        | `0`                 |

## Outputs

- **`proxy-url`** — HTTP address of the local proxy
- **`pushed-count`** — Number of paths pushed to registry

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
