# noci-action

Zero-config Nix binary cache over OCI for GitHub Actions.

**Fetch-only** (default): proxy serves cached paths from any OCI registry. No signing key needed.
**Push mode**: add a signing key → built paths auto-push after each job. Cache errors never break your CI.

## 1-Line Setup (Fetch-Only)

```yaml
steps:
  - uses: cachix/install-nix-action@v30
  - uses: lonerOrz/noci@v1/action
  - run: nix build .#my-package   # pulls from OCI cache automatically
```

## 1-Line Setup (Push + Fetch)

```yaml
steps:
  - uses: cachix/install-nix-action@v30
  - uses: lonerOrz/noci@v1/action
    with:
      signing-key: ${{ secrets.NOCI_SIGNING_KEY }}
  - run: nix build .#my-package   # cache hit reads, new paths auto-push after job
```

## Full Configuration

```yaml
- uses: lonerOrz/noci@v1/action
  with:
    # ── Registry ──
    registry: ghcr.io                          # OCI endpoint (default: ghcr.io)
    repo: myorg/mycache                        # OCI repo (default: GITHUB_REPOSITORY)
    token: ${{ secrets.GITHUB_TOKEN }}         # Auth token (default: GITHUB_TOKEN)
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }} # Nix key (key_name:base64, omit = fetch-only)

    # ── Performance ──
    compression: zstd                          # zstd or gzip (default: zstd)
    compression-level: 6                       # 1-19 for zstd (default: 3)
    jobs: 4                                    # Compression threads, 0=auto (default: 0)
    skip-upstream: "true"                      # Skip cache.nixos.org-signed paths (default: true)

    # ── Reliability ──
    fail-on-error: "false"                     # Push errors = warning, not failure (default: false)
    proxy-port: "0"                            # Local proxy port, 0=random (default: 0)
```

## Outputs

| Output         | Description                        |
| :------------- | :--------------------------------- |
| `proxy-url`    | HTTP address of the local proxy    |
| `pushed-count` | Number of paths pushed to registry |

Use `proxy-url` as an `extra-substituters` in downstream jobs:

```yaml
# Job 1: build + push
- uses: lonerOrz/noci@v1/action
  id: noci
  with:
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}

# Job 2: fetch-only from the same cache
- uses: lonerOrz/noci@v1/action
  with:
    proxy-port: "0"
```

## Manual Push

For explicit control over what gets pushed:

```yaml
- uses: lonerOrz/noci@v1/action
  with:
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}

- run: |
    paths=$(nix build .#my-package --print-out-paths)
    noci push $paths
  env:
    NOCI_SIGNING_KEY: ${{ secrets.NOCI_SIGNING_KEY }}
```

## Permissions

```yaml
permissions:
  contents: read
  packages: write   # Required for GHCR push
```

## Self-hosted Runners

- Keep `proxy-port: 0` (default) for automatic ephemeral port allocation
- Each run gets isolated hook/log paths via `GITHUB_RUN_ID` + `GITHUB_RUN_ATTEMPT`
- Proxy process is automatically killed in the post step

## How It Works

```
┌─────────────┐     ┌──────────┐     ┌──────────┐
│  nix build  │────▶│  proxy   │────▶│  OCI     │
│  (main job) │     │ (fetch)  │     │ registry │
└─────────────┘     └──────────┘     └──────────┘
       │                                     ▲
       │ post-build-hook                     │
       ▼                                     │
┌─────────────┐     ┌──────────┐             │
│  collect    │────▶│  push    │─────────────┘
│  paths      │     │ (upload) │
└─────────────┘     └──────────┘
```

1. **Proxy** starts, configures Nix substituter via `NIX_CONFIG`
2. **Build** steps pull dependencies through proxy (hit) or upstream (miss)
3. **Hook** collects all built output paths to an isolated log file
4. **Push** uploads new paths to OCI registry after the job (if signing key provided)

## Architecture

```
action/src/
├── config.js    — loadConfig(): resolve 10 inputs with smart defaults
├── binary.js    — ensureBinary(): prebuilt → download → nix build fallback
├── proxy.js     — startProxy(): daemon, port detection, NIX_CONFIG injection
├── index.js     — main entry (15 lines): loadConfig → ensureBinary → startProxy
├── post.js      — post entry: collectPaths → push (with compression args) → cleanup
└── utils.js     — GitHub Actions helpers (state, env, output)
```

Key design decisions:
- **`fail-on-error: false`** — cache failures are warnings, never break CI
- **Compression passthrough** — post.js passes `--compression`, `--compression-level`, `--jobs` to `noci push`
- **Fetch-only mode** — no signing key = silent skip, no errors, no warnings
