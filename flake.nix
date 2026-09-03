{
  description = "Bifrost's Nix Flake";

  # Flake inputs
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/staging-next";
  };

  # Flake outputs
  outputs =
    { self, ... }@inputs:
    let
      # The systems supported for this flake's outputs
      supportedSystems = [
        "x86_64-linux" # 64-bit Intel/AMD Linux
        "aarch64-linux" # 64-bit ARM Linux
        "aarch64-darwin" # 64-bit ARM macOS
      ];

      # Temporary workaround until nixpkgs includes Go 1.27.0 (pinned rev has 1.27rc2).
      go_1_27_0_overlay = final: prev: {
        go_1_27 = prev.go_1_27.overrideAttrs (oldAttrs: rec {
          version = "1.27.0";
          src = final.fetchurl {
            url = "https://go.dev/dl/go${version}.src.tar.gz";
            sha256 = "7002403d7cc44529ef6d26f69a44818263395ead7c16c05a5808ae047ebeb0e5";
          };
        });
        go = final.go_1_27;
      };

      # Helper for providing system-specific attributes
      forEachSupportedSystem =
        f:
        inputs.nixpkgs.lib.genAttrs supportedSystems (
          system:
          f {
            inherit system;
            # Provides a system-specific, configured Nixpkgs
            pkgs = import inputs.nixpkgs {
              inherit system;
              overlays = [ go_1_27_0_overlay ];
              # Enable using unfree packages
              config.allowUnfree = true;
            };
          }
        );
    in
    {
      nixosModules = {
        bifrost =
          { pkgs, lib, ... }:
          {
            imports = [ ./nix/modules/bifrost.nix ];
            services.bifrost.package = lib.mkDefault self.packages.${pkgs.system}.bifrost-http;
          };
      };

      packages = forEachSupportedSystem (
        {
          pkgs,
          system,
        }:
        let
          version = "1.4.9";

          bifrost-ui = pkgs.callPackage ./nix/packages/bifrost-ui.nix {
            src = self;
            inherit version;
          };
        in
        {
          bifrost-ui = bifrost-ui;

          bifrost-http = pkgs.callPackage ./nix/packages/bifrost-http.nix {
            inherit inputs;
            src = self;
            inherit version;
            inherit bifrost-ui;
          };

          default = self.packages.${system}.bifrost-http;
        }
      );

      apps = forEachSupportedSystem (
        { system, ... }:
        {
          bifrost-http = {
            type = "app";
            program = "${self.packages.${system}.bifrost-http}/bin/bifrost-http";
          };

          default = self.apps.${system}.bifrost-http;
        }
      );

      # To activate the default environment:
      # nix develop
      # Or if you use direnv:
      # direnv allow
      devShells = forEachSupportedSystem (
        { pkgs, ... }:
        {
          # Run `nix develop` to activate this environment or `direnv allow` if you have direnv installed
          default = import ./nix/devshells/default.nix { inherit pkgs; };
        }
      );
    };
}
