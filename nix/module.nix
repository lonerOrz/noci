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
      description = "Listen address for the proxy server.";
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
      type = types.str;
      default = "https://cache.nixos.org";
      description = "Fallback upstream cache.";
    };

    tokenFile = mkOption {
      type = types.nullOr types.path;
      default = null;
      description = ''
        Path to a file containing environment variables for the proxy.
        Used to supply `NOCI_TOKEN` or `GITHUB_TOKEN` securely
        without exposing secrets in the world-readable Nix store.
      '';
    };
  };

  config = mkIf cfg.enable {
    systemd.services.noci-proxy = {
      description = "noci local cache proxy server daemon";
      after = [ "network.target" ];
      wantedBy = [ "multi-user.target" ];

      serviceConfig = {
        ExecStart = "${cfg.package}/bin/noci proxy --repo ${cfg.repo} --registry ${cfg.registry} --port ${toString cfg.port} --listen ${cfg.listen} --upstream ${cfg.upstream}";
        Restart = "always";
        RestartSec = "5s";

        DynamicUser = true;
        PrivateTmp = true;
        ProtectSystem = "full";
        ProtectHome = true;
        NoNewPrivileges = true;

        EnvironmentFile = lib.optional (cfg.tokenFile != null) cfg.tokenFile;
      };
    };
  };
}
