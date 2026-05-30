{
  description = "noci: highly modular Nix binary cache over OCI registry";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];

      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system);
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          noci = pkgs.callPackage ./nix/package.nix { };
          default = self.packages.${system}.noci;
        }
      );

      apps = forAllSystems (system: {
        noci = {
          type = "app";
          program = "${self.packages.${system}.noci}/bin/noci";
          meta = {
            description = "A highly modular Nix binary cache over OCI registry";
          };
        };
        default = self.apps.${system}.noci;
      });

      devShells = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go
              gopls
              gotools
            ];
          };
        }
      );

      nixosModules = {
        default = import ./nix/module.nix self;
        noci = self.nixosModules.default;
      };
    };
}
