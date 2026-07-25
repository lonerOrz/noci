# noci-action

A zero-config GitHub Action that turns any OCI registry into a Nix binary cache.

**Push mode**: provide a signing key → built paths are automatically pushed after each job.
**Fetch-only mode**: no signing key → proxy serves cached paths from the registry (read-only).

## Quick Start

```yaml
- name: Setup noci binary cache
  uses: lonerOrz/noci@dev/action
  with:
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}
```

That's it. The action:
1. Downloads or builds the `noci` binary
2. Starts a local proxy and configures Nix to use it
3. Collects all `nix build` output paths during the job
4. Pushes them to your OCI registry after the job completes

## Minimal (push + fetch)

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: cachix/install-nix-action@v30

  - name: Setup noci
    uses: lonerOrz/noci@dev/action
    with:
      signing-key: ${{ secrets.NOCI_SIGNING_KEY }}

  - run: nix build .#my-package
  # Paths are pushed automatically after this step
```

## Full Configuration

```yaml
- name: Setup noci
  uses: lonerOrz/noci@dev/action
  with:
    registry: ghcr.io                    # OCI registry (default: ghcr.io)
    repo: myorg/mycache                   # OCI repo (default: GITHUB_REPOSITORY)
    token: ${{ secrets.GITHUB_TOKEN }}   # Registry auth (default: GITHUB_TOKEN)
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}  # Nix signing key (key_name:base64)
    skip-upstream: "true"                # Skip packages with upstream sigs (default: true)
    proxy-port: "0"                      # Proxy port, 0=random (default: 0)
```

## Custom Push

For manual control over what gets pushed:

```yaml
- name: Setup noci
  uses: lonerOrz/noci@dev/action
  with:
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}

- name: Build
  run: nix build .#my-package --print-out-paths > /tmp/paths.txt

- name: Push
  run: noci push $(cat /tmp/paths.txt)
  env:
    NOCI_SIGNING_KEY: ${{ secrets.NOCI_SIGNING_KEY }}
```

## Multi-Registry Push

Push to multiple registries in one job:

```yaml
- name: Setup noci
  uses: lonerOrz/noci@dev/action
  with:
    signing-key: ${{ secrets.NOCI_SIGNING_KEY }}
    registries: |
      ghcr.io/myorg/cache
      registry.example.com/team/nix-cache
```

## Inputs

| Input           | Description                                    | Default               |
| :-------------- | :--------------------------------------------- | :-------------------- |
| `registry`      | OCI registry endpoint                          | `ghcr.io`             |
| `repo`          | OCI repository (`owner/repo`)                  | `GITHUB_REPOSITORY`   |
| `token`         | Registry auth token                            | `GITHUB_TOKEN`        |
| `signing-key`   | Nix signing key (`key_name:base64`)            | _(none — fetch-only)_ |
| `skip-upstream` | Skip packages with upstream cache.nixos.org    | `true`                |
| `proxy-port`    | Local proxy port (`0` = random)                | `0`                   |

## Outputs

| Output         | Description                        |
| :------------- | :--------------------------------- |
| `proxy-url`    | HTTP address of the local proxy    |
| `pushed-count` | Number of paths pushed to registry |

## Permissions

```yaml
permissions:
  contents: read
  packages: write   # Required for GHCR push
```

## Self-hosted Runners

Keep `proxy-port: 0` (default) to avoid port collisions on shared hosts. Each run gets a unique hook script path based on `GITHUB_RUN_ID` and `GITHUB_RUN_ATTEMPT`.

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

1. **Proxy** starts and configures Nix to use it as a substituter
2. **Build** steps pull dependencies through the proxy (cache hit) or upstream
3. **Hook** collects all built output paths to a log file
4. **Push** uploads new paths to the OCI registry after the job
