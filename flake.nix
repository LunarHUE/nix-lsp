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
          go_1_26
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
        # Everything the Go build needs and nothing volatile: sources, the
        # vendored tree-sitter grammar (cgo-included from internal/syntax), and
        # the extension assets the vsix derivation packages. Excludes built
        # artifacts, node_modules, and local state so the store hash stays
        # stable across dev churn.
        serverSrc = nixpkgs.lib.cleanSourceWith {
          src = ./.;
          filter = path: _type:
            let rel = nixpkgs.lib.removePrefix (toString ./. + "/") (toString path);
            in !(nixpkgs.lib.hasPrefix "editors/vscode/node_modules" rel
              || nixpkgs.lib.hasPrefix "editors/vscode/out" rel
              || nixpkgs.lib.hasPrefix "examples" rel
              || nixpkgs.lib.hasPrefix "docs" rel
              || rel == "nixls");
        };

        # The language server. buildGoModule runs the full `go test ./...`
        # suite in checkPhase, so `nix build`/`nix flake check` is also the
        # verification gate.
        nixls = (pkgs.buildGoModule.override { go = pkgs.go_1_26; }) {
          pname = "nixls";
          version = self.shortRev or self.dirtyShortRev or "dev";
          src = serverSrc;
          vendorHash = "sha256-cNKRQ5ArES8Ffpq1TB4VV6cvqbPSr32qzzIdQm+mcpE=";
          subPackages = [ "cmd/nixls" ];
          env.CGO_ENABLED = "1";
          ldflags = [ "-s" "-w" "-X main.version=${self.shortRev or "dev"}" ];
        };
        # VS Code platform target for this nix system; the vsix package exists
        # only where the mapping does (Windows has no nix — its VSIX is built
        # by a plain Go job in CI).
        vsceTarget = {
          "x86_64-linux" = "linux-x64";
          "aarch64-linux" = "linux-arm64";
          "x86_64-darwin" = "darwin-x64";
          "aarch64-darwin" = "darwin-arm64";
        }.${system} or null;

        # Platform-specific VSIX with the nix-built server bundled at bin/nixls.
        vsix = pkgs.buildNpmPackage {
          pname = "nixls-vsix";
          version = nixls.version;
          src = "${serverSrc}/editors/vscode";
          npmDepsHash = "sha256-+GNCcK8sNKbtrD2ooOxm0R32hMIXEzi327/tUq4XvKc=";
          npmBuildScript = "compile";
          dontNpmInstall = true;
          nativeBuildInputs = [ pkgs.nodejs pkgs.vsce ];
          installPhase = ''
            runHook preInstall
            mkdir -p bin $out
            cp ${nixls}/bin/nixls bin/nixls
            vsce package --target ${vsceTarget} --allow-missing-repository -o $out/nixls-${vsceTarget}.vsix
            runHook postInstall
          '';
        };
      in {
        packages = {
          inherit nixls;
          default = nixls;
        } // nixpkgs.lib.optionalAttrs (vsceTarget != null) { inherit vsix; };

        checks = {
          inherit nixls;
        } // nixpkgs.lib.optionalAttrs (vsceTarget != null) { inherit vsix; };

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
