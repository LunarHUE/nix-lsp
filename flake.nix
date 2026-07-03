{
  description = "nix-lsp";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;

          config.allowUnfree = true;
        };

        # Build essentials shared by every devShell.
        buildPackages = with pkgs; [
          go_1_26
          gcc
          nodejs_22
        ];

        # Interactive bash + completion, shared by every devShell.
        bashPackages = with pkgs; [
          bashInteractive
          bash-completion
          nix-bash-completions
        ];

        # Extras layered on top of the lean shell for `full` only: the agent
        # CLIs plus the heavy service/tooling stack. claude-code and codex now
        # come straight from nixpkgs (no third-party sadjow flake inputs).
        fullPackages = with pkgs; [
          claude-code
          codex

          pnpm
          docker
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

        # Single source of truth for the build's version so the store-path
        # `version` attribute and the binary's embedded `main.version` can never
        # diverge (a dirty tree yields `<rev>-dirty` for both).
        version = self.shortRev or self.dirtyShortRev or "dev";

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
          inherit version;
          src = serverSrc;
          vendorHash = "sha256-cNKRQ5ArES8Ffpq1TB4VV6cvqbPSr32qzzIdQm+mcpE=";
          subPackages = [ "cmd/nixls" ];
          env.CGO_ENABLED = "1";
          ldflags = [ "-s" "-w" "-X main.version=${version}" ];
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

        # Lean shell: build essentials only. This is what CI's `nix develop`
        # gates, so a breakage in the heavy `full` extras can never brick the
        # `go`/`gcc`/`node` toolchain that all work depends on.
        devShells.default = pkgs.mkShell {
          packages = buildPackages ++ bashPackages;

          BASH_COMPLETION_PATH =
            "${pkgs.bash-completion}/etc/profile.d/bash_completion.sh";

          shellHook = ''
            echo "Nix default devShell ready. node $(node --version 2>/dev/null)"
          '';
        };

        # Full shell: everything in default plus the agent CLIs and the heavy
        # service/tooling extras. `.envrc` uses this so interactive terminals
        # keep the same environment as before.
        devShells.full = pkgs.mkShell {
          packages = buildPackages ++ bashPackages ++ fullPackages;

          BASH_COMPLETION_PATH =
            "${pkgs.bash-completion}/etc/profile.d/bash_completion.sh";

          shellHook = ''
            echo "Nix full devShell ready. node $(node --version 2>/dev/null), pnpm $(pnpm --version 2>/dev/null)"
          '';
        };
      });
}
