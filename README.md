# noci (Nix over OCI)

Stateless Nix binary cache over OCI container registries. No database, no server — just push to GHCR, pull through a proxy.

## Features

- **push** — Analyzes closure, filters upstream-cached packages, concurrent upload with zstd/gzip compression
- **proxy** — Local HTTP proxy converting Nix fetches to OCI layer downloads, cascading upstream fallback
- **search** — Fuzzy-search cached packages by name, hash, store path, or Flake URI
- **gc** — Mark-sweep garbage collection with quotas, grace periods, and cascade eviction
- **pin/unpin** — Protect critical packages from GC
- **index repair** — Reconcile OCI manifests with the index

## Install

```bash
nix run github:lonerOrz/noci -- --help
```

## Quick Start

```bash
# 1. Generate signing keys
nix key generate-secret --key-name "noci" > secret.key
nix key convert-secret-to-public < secret.key > public.key

# 2. Set environment
export NOCI_REPO="username/repo"
export NOCI_SIGNING_KEY=$(cat secret.key)
export GH_TOKEN="ghp_xxx"

# 3. Push a package
noci push .#my-package

# 4. Use the cache
noci proxy --repo username/repo --port 8080 &
nix build .#my-package --substituters "http://127.0.0.1:8080" --trusted-public-keys "$(cat public.key)"
```

Token resolution: `NOCI_TOKEN` → `GITHUB_TOKEN` → `GH_TOKEN` → `gh auth token` → `~/.docker/config.json`

## Commands

```bash
noci push .#package                        # push with auto compression
noci push .#package --jobs 4               # push with 4-thread zstd
noci pin .#package --ttl 30d               # pin for 30 days
noci search                                # list all cached packages
noci search sonar                          # search by name
noci search /nix/store/g5jgc...-sonar-0.4  # search by store path
noci gc --max-size 5GB --dry-run           # preview garbage collection
noci gc g5jgc... hszsl...                  # cascade-evict specific packages
noci index repair --dry-run                # preview index repairs
```

## GitHub Actions

```yaml
permissions: { packages: write }
steps:
  - uses: actions/checkout@v4
  - uses: cachix/install-nix-action@v30
  - uses: lonerOrz/noci/action@v1
    with:
      signing-key: ${{ secrets.NOCI_SIGNING_KEY }}
  - run: nix build .#package
```

See [action/README.md](action/README.md) for all inputs and configuration options.

## NixOS Module

```nix
services.noci-proxy = {
  enable = true;
  repo = "username/repo";
  tokenFile = "/path/to/token.env";
  # registry = "ghcr.io";
  # port = 37515;
  # upstream = "https://cache.nixos.org";
};
```
