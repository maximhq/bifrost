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
        { pkgs, system }:
        let
          lib = pkgs.lib;
          version = lib.strings.fileContents (self + /core/version);
          vendorHash = "sha256-Ck1cwv/DYI9EXmp7U2ZSNXlU+Qok8BFn5bcN1Pv7Nmc=";
          npmDepsHash = "sha256-+tI2NUJtpHwvI9sAYMXO7r00Y3Pb1E62ms1ZSd3O0hM=";

          bifrost-ui = pkgs.callPackage ./nix/packages/bifrost-ui.nix {
            src = self;
            inherit version;
            inherit npmDepsHash;
          };
        in
        {
          bifrost-ui = bifrost-ui;

          bifrost-http = pkgs.callPackage ./nix/packages/bifrost-http.nix {
            inherit inputs;
            src = self;
            inherit version;
            inherit vendorHash;
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
