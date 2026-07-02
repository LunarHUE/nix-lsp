{
  description = "nixls demo flake — every input below is shaped to trigger one flake feature.";

  # Open examples/demo as its OWN workspace folder: root detection picks the
  # nearest flake.nix, so flake features only fire when this file is the
  # workspace-root flake.nix.
  inputs = {
    # Healthy input: has a url, is in flake.lock, and is consumed by outputs.
    # Hover over the name / url / a follows target to see its locked source.
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.05";

    # Healthy nested follows: `inputs.nixpkgs.follows = "nixpkgs"` points at a
    # declared input. Hover and Go to Definition both work on the target string.
    home-manager = {
      url = "github:nix-community/home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # Dangling follows: the target "nixpkgss" is a typo naming no declared input
    # (dangling-follows error). Open the quick fix on it for a
    # "Change follows target to 'nixpkgs'" did-you-mean.
    flake-utils = {
      url = "github:numtide/flake-utils";
      inputs.nixpkgs.follows = "nixpkgss";
    };

    # Declared but absent from flake.lock (input-not-locked warning).
    unlocked-extra.url = "github:example/unlocked-extra";

    # Declared and locked, but never consumed by outputs and never a follows
    # target (unused-input warning). Its quick fixes are "Remove input" and
    # "Add to outputs". Hover shows `flake = false`.
    demo-lib = {
      url = "github:example/demo-lib";
      flake = false;
    };
  };

  # Strict destructured formals (no `...`, no `@`-pattern) are required for the
  # unused-input check. Note demo-lib is deliberately omitted here. This formals
  # list is also a completion context: trigger completion inside the braces.
  outputs = { self, nixpkgs, home-manager, flake-utils, unlocked-extra }: {
    demo = "nixls demo outputs";
  };
}
