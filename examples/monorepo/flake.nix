# Flake monorepo example: one flake fanning out to per-package directories, a
# shared lib, and a NixOS module. Open examples/monorepo as its OWN workspace
# folder. Things to try are in README.md; the short version: Ctrl-click any
# ./pkgs/... path to jump into it, use Go to Symbol in Workspace to find
# definitions across files, and hover options inside modules/service.nix.
{
  description = "nixls example: flake monorepo with per-directory packages";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      # Cross-file navigation: Ctrl-click the path literals to open the files.
      helpers = import ./lib/helpers.nix;
    in
    {
      packages.${system} = {
        cli = pkgs.callPackage ./pkgs/cli { inherit helpers; };
        web = pkgs.callPackage ./pkgs/web { };
      };

      # A module shipped by the monorepo; option hover works inside it, and
      # Ctrl+click or hover on the ./modules/service.nix path itself now follows
      # to the file.
      nixosModules.web-service = ./modules/service.nix;

      lib = helpers;

      devShells.${system}.default = pkgs.mkShell {
        packages = [ pkgs.htop ];
      };
    };
}
