# Starter flake for the drop-in .devcontainer — copy to your repo root as
# flake.nix, then `git add flake.nix` (flakes only see tracked files) and
# reopen the container.
{
  description = "dev shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          # claude-code (and codex) are unfree.
          config.allowUnfree = true;
        };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            # Toolchain. `go` floats to the current release — once the project
            # settles, pin it (e.g. `go_1_26`) for reproducible builds; the root
            # flake.nix of nix-lsp shows the pinned pattern.
            go
            gcc # cgo needs a C compiler
            nodejs_22
            pnpm

            # Agent CLIs.
            claude-code
            codex

            # Interactive bash + completion.
            bashInteractive
            bash-completion
          ];

          shellHook = ''
            echo "Nix devShell ready. go $(go version | cut -d' ' -f3), node $(node --version)"
          '';
        };
      }
    );
}
