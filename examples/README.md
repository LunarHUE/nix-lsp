# nixls examples

Each subfolder is a self-contained workspace demonstrating one use case. Root
detection picks the nearest `flake.nix` above the file you open, so always open
the **subfolder** (not this directory or the repo root) as the workspace
folder; flake features fire only for the workspace-root `flake.nix`.

| Folder | Shows off |
| --- | --- |
| `demo/` | Flake-input intelligence: hover, follows navigation, dangling/unlocked/unused-input diagnostics and quick fixes, static analyzers. Has its own README with a what-to-try table. |
| `nixos/` | NixOS option hover in a real configuration: flat and nested attrpaths, `users.users.<name>` wildcards, `config.*` reads. Dataset auto-downloads for the channel pinned in `flake.lock`. |
| `monorepo/` | One flake fanning out to per-package directories: Ctrl-click path literals to jump across files, workspace symbols, callPackage wiring, a shipped NixOS module with option hover. |
| `iso/` | Building a NixOS installer ISO. Standard options hover with docs; the `isoImage.*` installer-profile options are documented as a known hover gap (not in the channel dataset). |
| `scripts/` | Bash embedded in Nix strings (shebang scripts, systemd `script`/`preStart`, writeShellScriptBin) with embedded-shell syntax highlighting via the extension's injection grammar. |
