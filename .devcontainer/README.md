# Nix Dev Container

Drop this `.devcontainer/` folder into any repo that has a `flake.nix` and get a
ready-to-use Nix development environment. Reopen the repo in a container and — BAM.

## What you get

- **Pinned Determinate Nix** in a slim Debian (`bookworm-slim`) image, installed
  single-user (no daemon).
- **A shared `/nix` store.** The named docker volume `lunarhue-containers-nix-store`
  is mounted at `/nix`, so every container that uses this folder reuses one store.
  Your second repo starts warm — packages built once are already there.
- **direnv auto-loading** of `devShells.default` via `.envrc` (`use flake`). Entering
  the workspace loads the dev tooling automatically.
- **A pre-built devshell.** `create.sh` warms `nix develop` at container-creation
  time, so the first terminal opens ready instead of building on demand.
- **Nix-version drift warning.** `start.sh` warns loudly when the Nix pinned by the
  image disagrees with the Nix already living in the persistent `/nix` volume.
- **Pressure-triggered store GC.** `create.sh` sets `min-free = 5G` / `max-free = 20G`
  in `nix.conf`, so a build that pushes free disk below 5G garbage-collects up to 20G
  free before continuing. This fires only *during* nix operations — it does not shrink
  the `/nix` volume while idle, so `nix store gc` is still the tool for reclaiming space
  on demand.

If the repo has no committed `.envrc`, `create.sh` synthesizes one (`use flake`)
automatically. If the repo already commits `.envrc`, it is left untouched.

## Requirements

- The repo exports a dev shell: `devShells.<system>.default` in `flake.nix`.
- Docker (the container uses docker-outside-of-docker).

## What to customize when copying

- **`devcontainer.json` VS Code extensions/settings.** The `golang.go` extension and
  the `go.*` settings are specific to this Go repo — swap them for your stack's
  language server and settings. Keep `mkhl.direnv` and `jnoortheen.nix-ide`.
- **The pinned Nix version.** `NIX_EXPECTED_VERSION` and the installer tag in the URL
  in `Dockerfile` must move together — bump both when upgrading Nix.
- **The volume name.** `lunarhue-containers-nix-store` (in `devcontainer.json`) is a
  shared store across all your containers. Rename it if you want an isolated,
  per-project store instead.
