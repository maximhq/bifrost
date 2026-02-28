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
        {
          pkgs,
        }:
        {
          # Run `nix develop` to activate this environment or `direnv allow` if you have direnv installed
          default = pkgs.mkShellNoCC {
            # The name of the environment
            name = "bifrost-nix-dev-shell";

            # The Nix packages provided in the environment
            packages = with pkgs; [
              go
              gopls
              gofumpt
              air
              delve # provides dlv
              go-tools # provides staticcheck

              nodejs_24
            ];

            # Set any environment variables for your development environment
            env = { };

            # Add any shell logic you want executed when the environment is activated
            shellHook = ''
              ##
              ## Go: project-local GOPATH/GOBIN
              ##
              export GOPATH="$PWD/.nix-store/go"
              export GOBIN="$GOPATH/bin"
              export GOMODCACHE="$GOPATH/pkg/mod"
              export GOCACHE="$PWD/.nix-store/gocache"

              mkdir -p "$GOBIN" "$GOMODCACHE" "$GOCACHE"

              export PATH="$PATH:$GOBIN"

              ##
              ## Node: project-local "global" npm prefix
              ##
              # npm reads npm_config_prefix (or NPM_CONFIG_PREFIX) as the "prefix" config,
              # which is where `npm i -g` installs to.
              export npm_config_prefix="$PWD/.nix-store/npm-global"

              mkdir -p "$npm_config_prefix/bin"

              # Ensure "global" npm bin is on PATH for this shell only
              export PATH="$PATH:$npm_config_prefix/bin"
            '';
          };
        }
      );
    };
}