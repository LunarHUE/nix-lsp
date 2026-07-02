# Installer-ISO example. Open examples/iso as its OWN workspace folder.
# `nix build .#iso` (on a Linux builder) produces a bootable installer image.
{
  description = "nixls example: NixOS installer ISO build";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";

  outputs = { self, nixpkgs }: {
    nixosConfigurations.iso = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        # The installer profile that turns the configuration into an ISO.
        "${nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal.nix"
        ./iso.nix
      ];
    };

    packages.x86_64-linux.iso =
      self.nixosConfigurations.iso.config.system.build.isoImage;
  };
}
