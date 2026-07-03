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
        # ALLOWLIST sources: only paths a derivation actually reads are copied
        # into the store, so churn in README/CHANGELOG/.github/.devcontainer/
        # CLAUDE.md/etc can never invalidate a build hash. The Go server and the
        # VS Code extension get SEPARATE allowlisted sources so that editing one
        # can never rebuild the other.
        #
        # cleanSourceWith's filter also runs on directories; returning false for
        # one prunes its whole subtree, so a directory is kept when it is (or
        # lives under) an allowed root, OR when it is an ancestor of one (e.g.
        # "editors" must be kept so the walk can descend to "editors/vscode").
        # `exclude` re-drops paths under an allowed root (built artifacts).
        mkAllowlistSrc = { allowed, exclude ? [ ] }:
          let
            lib = nixpkgs.lib;
            root = toString ./.;
          in
          lib.cleanSourceWith {
            src = ./.;
            filter = path: _type:
              let
                rel = lib.removePrefix (root + "/") (toString path);
                underAllowed = lib.any
                  (a: rel == a || lib.hasPrefix (a + "/") rel) allowed;
                ancestorOfAllowed = lib.any
                  (a: lib.hasPrefix (rel + "/") a) allowed;
              in
              rel == ""
              || ((underAllowed || ancestorOfAllowed)
                && !lib.any (e: rel == e || lib.hasPrefix (e + "/") rel) exclude);
          };

        # The Go server build reads exactly:
        #   - go.mod, go.sum   (module + vendorHash inputs)
        #   - cmd/, internal/  (Go sources; checkPhase runs `go test ./...`)
        #   - third_party/     (vendored tree-sitter grammar, cgo-compiled from
        #                       internal/syntax)
        #   - testdata/        (fixtures the internal/* tests load)
        # No package uses //go:embed, so nothing else at the repo root is read.
        serverSrc = mkAllowlistSrc {
          allowed = [ "go.mod" "go.sum" "cmd" "internal" "third_party" "testdata" ];
        };

        # The vsix derivation reads only the extension tree. vsce rebuilds out/
        # from src via the "compile" script and prunes devDependencies itself,
        # so out/ and node_modules are regenerated in the build, not shipped.
        vsixSrc = mkAllowlistSrc {
          allowed = [ "editors/vscode" ];
          exclude = [ "editors/vscode/node_modules" "editors/vscode/out" ];
        };

        # The language server. `subPackages` scopes the build/install to
        # cmd/nixls, but that alone would also scope checkPhase to cmd/nixls
        # (which has no tests). `preCheck` unsets subPackages so getGoDirs
        # falls back to `./...`, making checkPhase run the full `go test ./...`
        # suite (everything under internal/). That is what makes `nix
        # build`/`nix flake check` the real verification gate. git is in
        # nativeCheckInputs because tests in internal/{project,server,analysis}
        # exec `git init/add/ls-files/rev-parse` against throwaway repos (they
        # skip via exec.LookPath when git is absent, which is why this gap went
        # unnoticed). No test needs the network or a nix binary.
        nixls = (pkgs.buildGoModule.override { go = pkgs.go_1_26; }) {
          pname = "nixls";
          version = self.shortRev or self.dirtyShortRev or "dev";
          src = serverSrc;
          vendorHash = "sha256-cNKRQ5ArES8Ffpq1TB4VV6cvqbPSr32qzzIdQm+mcpE=";
          subPackages = [ "cmd/nixls" ];
          env.CGO_ENABLED = "1";
          ldflags = [ "-s" "-w" "-X main.version=${self.shortRev or "dev"}" ];
          nativeCheckInputs = [ pkgs.git ];
          preCheck = ''unset subPackages'';
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
          src = "${vsixSrc}/editors/vscode";
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
