self:
{
  config,
  lib,
  pkgs,
  ...
}:

with lib;

let
  cfg = config.services.noci-proxy;

  proxyUrl = "http://${cfg.listen}:${toString cfg.port}";

  # Only check the base substituters list — never read extra-substituters to
  # avoid infinite recursion during NixOS module evaluation.
  proxyInBaseSubstituters = elem proxyUrl (config.nix.settings.substituters or [ ]);

  upstreamFlags =
    if cfg.upstream == [ ] then
      "--no-upstream"
    else
      concatMapStringsSep " " (u: "--upstream ${u}") cfg.upstream;
in
{
  options.services.noci-proxy = {
    enable = mkEnableOption "noci client-side local cache proxy server";

    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.system}.default;
      defaultText = literalExpression "self.packages.\${pkgs.system}.default";
      description = "The noci package to use.";
    };

    listen = mkOption {
      type = types.str;
      default = "127.0.0.1";
      example = "0.0.0.0";
      description = ''
        Address the proxy binds to.
        Use "127.0.0.1" for local-only access (default).
        Use "0.0.0.0" to serve the cache to other machines on your LAN.
      '';
    };

    port = mkOption {
      type = types.port;
      default = 37515;
      description = "Port to listen on.";
    };

    repo = mkOption {
      type = types.str;
      description = "OCI repository (e.g., username/repo).";
    };

    registry = mkOption {
      type = types.str;
      default = "ghcr.io";
      description = "OCI registry endpoint.";
    };

    upstream = mkOption {
      type = types.listOf types.str;
      default = [ "https://cache.nixos.org" ];
      description = ''
        Fallback upstream cache URLs.
        Set to empty list `[ ]` to disable upstream fallback completely.
      '';
    };

    tokenFile = mkOption {
      type = types.nullOr types.path;
      default = null;
      description = ''
        Path to a file containing environment variables for the proxy.
        Used to supply `NOCI_TOKEN` or `GITHUB_TOKEN` securely for private registries.
      '';
    };

    publicKey = mkOption {
      type = types.str;
      default = "";
      example = "noci:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
      description = ''
        Public key for verifying cache signatures.
        Leave empty if testing without signatures or when the key is managed separately.
      '';
    };

    automaticSubstituter = mkOption {
      type = types.bool;
      default = true;
      description = ''
        Whether to automatically register the local proxy in
        `nix.settings.extra-substituters` and `extra-trusted-public-keys`.
        Set to false if you manage your substituters manually (e.g. via
        dotfiles or flakes).
      '';
    };
  };

  config = mkIf cfg.enable {
    warnings = optional (cfg.automaticSubstituter && cfg.publicKey == "") ''
      services.noci-proxy: No 'publicKey' was configured.
      Packages fetched from this cache will fail signature verification unless you manually
      configure signing or set 'nix.settings.require-sigs = false'.
    '';

    nix.settings = mkIf cfg.automaticSubstituter {
      # Use mkAfter to append after any user-declared substituters; check only
      # the base list to avoid infinite recursion against extra-substituters.
      extra-substituters = mkIf (!proxyInBaseSubstituters) [ proxyUrl ];
      extra-trusted-substituters = mkIf (!proxyInBaseSubstituters) [ proxyUrl ];
      extra-trusted-public-keys = mkIf (cfg.publicKey != "") [ cfg.publicKey ];
    };

    systemd.services.noci-proxy = {
      description = "noci local cache proxy server daemon";
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];

      serviceConfig = {
        ExecStart = "${cfg.package}/bin/noci proxy --repo ${cfg.repo} --registry ${cfg.registry} --port ${toString cfg.port} --listen ${cfg.listen} ${upstreamFlags}";
        Restart = "always";
        RestartSec = "5s";
        TimeoutStopSec = "10s";

        # Hardening
        DynamicUser = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        NoNewPrivileges = true;

        # systemd creates this directory (owned by the dynamic user) and injects
        # it as CACHE_DIRECTORY so the proxy persists its index across restarts.
        CacheDirectory = "noci";

        EnvironmentFile = lib.optional (cfg.tokenFile != null) cfg.tokenFile;
      };
    };
  };
}
