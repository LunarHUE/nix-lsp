#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ -f "$WORKSPACE_DIR/.envrc" ]; then
  direnv allow "$WORKSPACE_DIR" >/dev/null 2>&1 || true
fi

# Nix version drift check.
# /nix is a persistent named volume, so the Nix in the volume shadows whatever
# the image installed. Rebuilding the image with a newer pinned installer does
# NOT upgrade Nix in an existing volume — warn loudly if they disagree.
# This check must never fail the script (set -euo pipefail is active).
if [ -n "${NIX_EXPECTED_VERSION:-}" ]; then
  nix_version_line=""
  if command -v nix >/dev/null 2>&1; then
    nix_version_line="$(nix --version 2>/dev/null || true)"
  fi
  # Parse the Determinate token: `nix (Determinate Nix 3.21.1) 2.34.7`.
  # Plain upstream nix reports `nix (Nix) 2.x`, which won't match and is treated
  # as "can't be parsed" -> warn.
  active_nix_version=""
  case "$nix_version_line" in
    *"Determinate Nix "*)
      active_nix_version="${nix_version_line#*Determinate Nix }"
      active_nix_version="${active_nix_version%%)*}"
      ;;
  esac
  if [ "${active_nix_version:-}" != "$NIX_EXPECTED_VERSION" ]; then
    {
      echo ""
      echo "========================================================================"
      echo "WARNING: Nix version drift detected"
      echo "  expected (Determinate Nix): $NIX_EXPECTED_VERSION"
      echo "  active:                     ${active_nix_version:-<unknown / could not parse>}"
      echo ""
      echo "The /nix store is a persistent docker volume, so it can shadow the Nix"
      echo "pinned by the image. To resolve this drift, either:"
      echo "  - recreate the nix-store volume so the image's pinned Nix is used, or"
      echo "  - run 'sudo -i nix upgrade-nix' inside the container, or"
      echo "  - bump NIX_EXPECTED_VERSION (in the Dockerfile) if the drift is intentional."
      echo "========================================================================"
      echo ""
    } >&2
  fi
fi
