# noci (Nix over OCI)

noci is a stateless Nix binary cache toolchain that leverages standard OCI container registries (e.g., GitHub Container Registry) to store and distribute Nix build artifacts, providing a lightweight, self-hosted caching solution.

## Features

- **push**: Intelligently analyzes dependency closures, automatically filters out existing or upstream-cached packages (supports Zstd/Gzip compression, adaptive multi-threaded compression).
- **proxy**: A local HTTP proxy that transparently converts Nix fetch requests into OCI layer downloads, with support for TTL-based cache refreshing and upstream fallback. Structured request logging provides real-time visibility.
- **search**: List or fuzzy-search cached packages by name, hash, store path, or Flake URI. Uses `nix eval --raw` for zero-build Flake resolution.
- **gc**: Performs Mark-Sweep garbage collection on dependencies, supporting storage quotas (Max-Size), grace-period protection, and cascade eviction for targeted removal.
- **pin/unpin**: Manually manages cache lifelines to protect critical environments from accidental deletion by the garbage collector.

## Installation

noci is a standard Nix Flake.

**Temporary Run:**

```bash
nix run github:lonerOrz/noci -- --help
```

**NixOS Service:**
Import the module in your `flake.nix` configuration:

```nix
services.noci-proxy = {
  enable = true;
  repo = "username/repo";
  tokenFile = "/path/to/token.env";
};
```

## Getting Started

### 1. Prepare Signing Keys

```bash
nix key generate-secret --key-name "noci" > secret.key
nix key convert-secret-to-public < secret.key > public.key
```

### 2. Environment Setup

```bash
export NOCI_REPO="username/repo"
export NOCI_SIGNING_KEY=$(cat secret.key)
export GH_TOKEN="your_token"
```

Token resolution: `NOCI_TOKEN` → `GITHUB_TOKEN` → `GH_TOKEN` → `~/.docker/config.json`.

### 3. Daily Operations

```bash
# Push packages to OCI (with 4-thread zstd compression)
noci push .#package --jobs 4

# Push with default auto-threaded compression (min(4, max(1, NumCPU/2)))
noci push .#package

# Pin a package for 30 days
noci pin .#package --ttl 30d

# List cached packages
noci search

# Search by name, hash, store path, or Flake URI
noci search sonar
noci search /nix/store/g5jggck6izsllbkrjgh9hr5bwhkpwlgh-sonar-0.4.0
noci search github:owner/repo#package

# Run garbage collection (5GB quota, cascade evict specific packages)
noci gc --max-size 5GB --grace-period 6h --dry-run=false
noci gc g5jggck6... hszsl3vn...   # cascade eviction
```

## GitHub Actions Integration

Use `lonerOrz/noci-action` in your workflow for seamless, automated caching:

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    permissions: { packages: write }
    steps:
      - uses: actions/checkout@v4
      - uses: cachix/install-nix-action@v30

      - name: Setup noci cache
        uses: lonerOrz/noci-action@v1
        with:
          repo: ${{ secrets.NOCI_REPO }}
          signing-key: ${{ secrets.NOCI_SIGNING_KEY }}
          token: ${{ secrets.GH_TOKEN }}

      - run: nix build .#package
```

## Cache Consumption

1. **Start the Proxy:**
   ```bash
   noci proxy --repo username/repo --port 8080 &
   ```
   Each request is logged in real-time: `[noci-proxy] GET /xxx.narinfo - 200 (cache) (2ms)`

2. **Configure Nix:**
   ```bash
   nix build .#package \
     --substituters "http://127.0.0.1:8080" \
     --trusted-public-keys "$(cat public.key)"
   ```

---

_Note: noci features a fully stateless design and requires no external database services._
