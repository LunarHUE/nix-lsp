# NixOS option-hover example. Open examples/nixos as its OWN workspace folder
# so this flake.nix is the workspace root: option hover picks its dataset
# channel from flake.lock's nixpkgs pin (here nixos-25.05).
{
  description = "nixls example: NixOS option hover in a real configuration";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";

  outputs = { self, nixpkgs }: {
    nixosConfigurations.demo = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [ ./configuration.nix ];
    };
  };
}
