{
  description = "nix-lsp";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";

    claude-code = {
      url = "github:sadjow/claude-code-nix";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-utils.follows = "flake-utils";
    };

    codex-cli-nix = {
      url = "github:sadjow/codex-cli-nix";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-utils.follows = "flake-utils";
    };
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
    codex-cli-nix,
    claude-code,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;

          config.allowUnfree = true;

          overlays = [
            claude-code.overlays.default
          ];
        };

        corePackages = with pkgs; [
          nodejs_22
          pnpm
        ];

        devOnlyPackages = with pkgs; [
          bashInteractive
          bash-completion
          nix-bash-completions

          # Comes from the claude-code overlay now:
          pkgs.claude-code

          # Direct flake package, no overlay needed:
          codex-cli-nix.packages.${system}.default

          docker
          gcc
          postgresql
          opentofu
        ];
      in {
        devShells.default = pkgs.mkShell {
          packages = corePackages ++ devOnlyPackages;

          BASH_COMPLETION_PATH =
            "${pkgs.bash-completion}/etc/profile.d/bash_completion.sh";

          shellHook = ''
            echo "Nix devShell ready. node $(node --version 2>/dev/null), pnpm $(pnpm --version 2>/dev/null)"
          '';
        };
      });
}
