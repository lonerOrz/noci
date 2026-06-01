# noci

noci (Nix over OCI) is a stateless Nix binary cache toolchain. It utilizes standard OCI container registries (such as GitHub Container Registry) to store and distribute Nix build artifacts, providing a lightweight, self-hosted caching solution for individuals and teams.

## Features

- **push**: Parses Nix Flake or Store paths, calculates dependency closures, automatically filters out packages that already exist in the OCI cache or carry an upstream `cache.nixos.org-1:` signature (indicating they are public upstream packages), and compresses, signs, and pushes remaining private packages to OCI. Use `--skip-upstream` to disable upstream-signature filtering and mirror all packages including public ones.
- **proxy**: Provides a local HTTP proxy service compliant with the Nix Substituter protocol, transparently converting Nix fetch requests into OCI layer downloads. Features an in-memory tag cache with lazy refresh and TTL, a negative cache for 404 suppression, and a fallback upstream proxy to the official Nix cache.
- **gc**: In-memory dependency directed graph coloring (Mark-Sweep) for garbage collection. Supports evicting cold data based on storage limits (Max-Size) and Least Recently Used (LRU) algorithms, featuring grace period protection against race conditions.
- **pin/unpin**: Manually manages cache lifelines (GC Roots) with Time-To-Live (TTL) support, protecting critical environment data from accidental deletion by the garbage collector.

## Architecture

noci adopts a completely stateless design, eliminating the need for external databases like Postgres or Redis:

1. **Unified Index Blob**: The entire `CacheIndex` (entries, roots, registry info) is serialized to JSON and stored as a single OCI blob with media type `application/vnd.noci.index.v1+json`, referenced by a dedicated `noci-index` manifest — avoiding OCI annotation size limits. Unlike tag-scanning approaches, fetching the index is a single O(1) blob download. Push/Pin/GC operations update this single source of truth atomically. The proxy server, GC engine, and publisher all consume the same unified index, eliminating the stale-tag-walking problem entirely.

2. **GC Concurrency Safety**: The garbage collector follows a read-only analysis + apply pattern: it fetches the full index from OCI, performs Mark-Sweep coloring entirely in memory (`Sweep`), and only mutates the index in a separate `Apply` phase before pushing the updated state back. This avoids in-place mutation of remote state.

3. **Process Management**: Deeply integrates with the Go Context lifecycle, supporting graceful shutdown by catching signals like SIGINT to ensure I/O safety.

## Advantages

- **Low Maintenance Cost**: Reuses existing OCI registry infrastructure (e.g., GHCR free tier) without the need to deploy and maintain extra storage services.
- **High Transfer Efficiency**: Accurately excludes basic system dependencies from uploading via cryptographic signature comparison, significantly reducing network and storage overhead.
- **Secure Isolation**: OCI credentials and signing private keys can be injected entirely via environment variables or files, avoiding exposure in the Nix Store.

## Areas for Improvement

- Make the upload worker pool size configurable (currently hardcoded to 4 goroutines for blob+manifest concurrent push).
- Introduce Zstd compression support (currently uses Gzip only).
- Abstract the storage layer into interfaces for future extension to native AWS S3 or other storage backends.

## Installation

noci is a standard Nix Flake.

**Temporary Run:**

```bash
nix run github:lonerOrz/noci -- help
```

**NixOS Daemon:**
Import the module in your `flake.nix` and enable it in your system configuration:

```nix
{
  imports = [ inputs.noci.nixosModules.default ];

  services.noci-proxy = {
    enable = true;
    port = 8080;
    repo = "lonerOrz/noci-cache";
    tokenFile = "/path/to/secure/noci-token.env"; # Store sensitive information like NOCI_TOKEN here
  };
}
```

## Usage

**Basic Setup:**
Generate a Nix signing key pair:

```bash
nix key generate-secret --key-name "noci" > secret.key
nix key convert-secret-to-public < secret.key > public.key
```

Configure environment variables:

```bash
export NOCI_REPO="your-username/your-cache-repo"
export NOCI_REGISTRY="ghcr.io"
export NOCI_SIGNING_KEY=$(cat secret.key)
export GITHUB_TOKEN="ghp_your_token"
```

**Daily Operations:**

```bash
# Push packages to the cache
noci push .#package

# Pin a version for 30 days, exempt from GC
noci pin .#package --ttl 30d

# Execute cleanup (5GB budget limit, retaining new packages uploaded within the last 6 hours)
noci gc --max-size 5GB --grace-period 6h --dry-run=false
```

## GitHub Actions Integration

You can easily integrate noci into your CI workflow. Create `.github/workflows/nix-cache.yml`:

```yaml
name: "Nix Build & Cache"
on: [push]

permissions:
  contents: read
  packages: write

jobs:
  build-and-cache:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: determinateSystems/nix-installer-action@main

      - run: nix profile install github:lonerOrz/noci

      - name: Build and Push
        env:
          NOCI_SIGNING_KEY: ${{ secrets.NIX_SIGNING_KEY }}
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: noci push .#package
```

## Cache Consumption

On the machine where you want to use the cache:

1. Start the local proxy service:

```bash
noci proxy --port 8080 --repo "your-username/your-cache-repo" --listen "127.0.0.1" --upstream "https://cache.nixos.org" --ttl 300 &
```

2. Use with Nix commands (can also be permanently configured in `/etc/nix/nix.conf`):

```bash
nix build .#package \
  --substituters "http://127.0.0.1:8080" \
  --trusted-public-keys "$(cat public.key)"
```

## Acknowledgments

- The Nix / NixOS community for the declarative package management ecosystem.
- The OCI (Open Container Initiative) standards for image distribution specifications.
