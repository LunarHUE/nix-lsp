# Embedded-script example. Open examples/scripts as its OWN workspace folder.
# The interesting file is scripts.nix: every indented string in it is a shell
# script, written the way real configs embed them (shebangs, systemd `script`
# blocks, shellHook). With the embedded-shell injection grammar in the nixls
# extension, those strings highlight as bash instead of flat string color.
{
  description = "nixls example: shell scripts embedded in Nix strings";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in
    {
      packages.${system} = import ./scripts.nix { inherit pkgs; };
      nixosModules.backup = ./module.nix;
    };
}
