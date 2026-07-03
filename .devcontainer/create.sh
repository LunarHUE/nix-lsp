#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT_NAME="$(basename "$WORKSPACE_DIR")"

append_once() {
  local file="$1"
  local line="$2"

  mkdir -p "$(dirname "$file")"
  touch "$file"

  grep -qxF "$line" "$file" 2>/dev/null || echo "$line" >> "$file"
}

append_once "$HOME/.bashrc" "export PS1='\\[\\e[1;32m\\]\\u@${PROJECT_NAME}\\[\\e[0m\\]:\\[\\e[1;34m\\]\\w\\[\\e[0m\\]\\$ '"
append_once "$HOME/.bashrc" 'eval "$(direnv hook bash)"'

# Configure nix. This user nix.conf is the single explicit place experimental-features
# are set (Determinate's system /etc/nix/nix.conf already enables them via
# extra-experimental-features; these lines are the belt-and-suspenders source of truth
# now that the Dockerfile NIX_CONFIG env and devcontainer.json containerEnv are gone).
mkdir -p "$HOME/.config/nix"
append_once "$HOME/.config/nix/nix.conf" "experimental-features = nix-command flakes"
append_once "$HOME/.config/nix/nix.conf" "warn-dirty = false"

# Pressure-triggered GC for the unbounded shared /nix volume. During a nix build,
# if free disk on the store's filesystem drops below min-free, Nix garbage-collects
# until at least max-free is available, then continues. G suffixes are verified to
# parse on Determinate Nix 3.21.1 (5G = 5368709120 B, 20G = 21474836480 B).
# NOTE: this only fires *during* nix operations; it does NOT shrink the volume while
# idle. Reclaiming space on demand is still a manual `nix store gc`.
append_once "$HOME/.config/nix/nix.conf" "min-free = 5G"
append_once "$HOME/.config/nix/nix.conf" "max-free = 20G"

# Drop-in fallback: repos that don't commit a .envrc get one synthesized so
# direnv auto-loads the flake devshell. Dormant in this repo (.envrc is committed).
if [ ! -f "$WORKSPACE_DIR/.envrc" ] && [ -f "$WORKSPACE_DIR/flake.nix" ]; then
  echo 'use flake' > "$WORKSPACE_DIR/.envrc" \
    || echo "warning: could not write $WORKSPACE_DIR/.envrc; skipping direnv setup" >&2
fi

if [ -f "$WORKSPACE_DIR/.envrc" ]; then
  direnv allow "$WORKSPACE_DIR" \
    || echo "warning: 'direnv allow' failed; env will load once the flake is fixed" >&2
fi

# Single-user Nix store is root-owned from the image build; hand it to the dev user.
# Runs against the mounted /nix volume, so it fixes the persistent volume too.
if [ ! -w /nix/var/nix/db/big-lock ]; then
  echo "Claiming /nix for $(id -un)..."
  sudo chown -R "$(id -u):$(id -g)" /nix \
    || echo "warning: could not chown /nix; nix will fail until this is fixed" >&2
fi

git config --global --get-all safe.directory | grep -qxF "$WORKSPACE_DIR" \
  || git config --global --add safe.directory "$WORKSPACE_DIR"
git lfs install --skip-repo

# Surface the active Nix version in creation logs so a drift from the pinned
# installer version (see .devcontainer/Dockerfile) is immediately visible.
echo "nix version: $(nix --version)"

# Pre-build the devshell so the first terminal opens warm. Non-fatal: a broken
# flake or a missing devShells.default must not abort container creation.
if [ ! -f "$WORKSPACE_DIR/flake.nix" ]; then
  echo "No flake.nix in $WORKSPACE_DIR; skipping devshell warm step."
else
  echo "Warming the nix devshell (first run on a fresh /nix volume can take several minutes)..."
  if ! nix develop "$WORKSPACE_DIR" --command true; then
    {
      echo ""
      echo "========================================================================"
      echo "WARNING: the nix devshell failed to build."
      echo ""
      echo "Container creation will continue, but terminals will lack the dev"
      echo "tooling until 'nix develop' succeeds. Check that flake.nix is valid and"
      echo "exports devShells.<system>.default, then re-run 'nix develop'."
      echo "========================================================================"
      echo ""
    } >&2
  fi
fi
