# noci (Nix over OCI)

Stateless Nix binary cache over OCI container registries (e.g. GitHub Container Registry). No database, no server — just push to GHCR and pull through a proxy.

## Features

- **push:** Parallel closure analysis, filters upstream-cached packages, concurrent uploads with zstd/gzip compression and multi-registry targets.
- **proxy:** Client-side local HTTP proxy converting Nix substituter fetches to OCI layer downloads, with zero-copy stream forwarding and optional upstream fallback.
- **search:** Fuzzy-search cached packages by name, 32-char Nix hash, store path, or Flake URI.
- **gc:** Mark-sweep garbage collection with quota budgets, grace periods, TTL roots, and cascading eviction.
- **pin / unpin:** Protect critical packages or Flake targets from garbage collection.
- **index repair / clean:** Reconcile and prune OCI manifests against the cache index.

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

# 3. Push a Flake package (supports --signing-key or --key-file)
noci push .#my-package --signing-key "$(cat secret.key)"

# 4. Use the local proxy
noci proxy --repo username/repo --port 8080 &
nix build .#my-package \
  --substituters "http://127.0.0.1:8080" \
  --trusted-public-keys "$(cat public.key)"
```

Token resolution: `NOCI_TOKEN` → `GITHUB_TOKEN` → `GH_TOKEN` → `gh auth token` → `~/.docker/config.json`

## CLI Commands Reference

```bash
# Push targets with parallel zstd compression threads
noci push .#package --jobs 4 --signing-key "$NOCI_SIGNING_KEY"

# Pin critical outputs for 30 days
noci pin .#package --ttl 30d

# Search cached packages
noci search sonar
noci search /nix/store/g5jgc...-sonar-0.4

# Start proxy daemon and write dynamic port to file
noci proxy --repo username/repo --port 0 --port-file /tmp/noci-proxy.port --upstream https://cache.nixos.org

# Run quota-based garbage collection
noci gc --max-size 15GB --grace-period 360h --physical-sweep --dry-run=false

# Reconcile index entries against remote OCI manifests
noci index repair
noci index clean --delete
```

## GitHub Actions

```yaml
permissions: { packages: write, contents: read }

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
  # upstream = [ "https://cache.nixos.org" ];
};
```
