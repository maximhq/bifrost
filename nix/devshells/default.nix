{pkgs}:
pkgs.mkShellNoCC {
  name = "bifrost-nix-dev-shell";

  packages = with pkgs; [
    go_1_26
    gopls
    gofumpt
    air
    delve
    go-tools

    nodejs_24
  ];

  env = {};

  shellHook = ''
    CACHE_ROOT="''${XDG_CACHE_HOME:-$HOME/.cache}/bifrost"
    mkdir -p "$CACHE_ROOT"

    export GOPATH="$CACHE_ROOT/go"
    export GOBIN="$GOPATH/bin"
    export GOMODCACHE="$GOPATH/pkg/mod"
    export GOCACHE="$CACHE_ROOT/gocache"

    mkdir -p "$GOBIN" "$GOMODCACHE" "$GOCACHE"
    export PATH="$PATH:$GOBIN"

    export npm_config_prefix="$CACHE_ROOT/npm-global"
    mkdir -p "$npm_config_prefix/bin"
    export PATH="$PATH:$npm_config_prefix/bin"
  '';
}
