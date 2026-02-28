{ pkgs }:

pkgs.mkShellNoCC {
  name = "bifrost-nix-dev-shell";

  packages = with pkgs; [
    go
    gopls
    gofumpt
    air
    delve
    go-tools

    nodejs_24
  ];

  env = { };

  shellHook = ''
    export GOPATH="$PWD/.nix-store/go"
    export GOBIN="$GOPATH/bin"
    export GOMODCACHE="$GOPATH/pkg/mod"
    export GOCACHE="$PWD/.nix-store/gocache"

    mkdir -p "$GOBIN" "$GOMODCACHE" "$GOCACHE"
    export PATH="$PATH:$GOBIN"

    export npm_config_prefix="$PWD/.nix-store/npm-global"
    mkdir -p "$npm_config_prefix/bin"
    export PATH="$PATH:$npm_config_prefix/bin"
  '';
}
