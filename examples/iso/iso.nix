# ISO configuration. Option hover works for everything declared by the
# standard NixOS module set (networking.*, services.*, users.*, boot.*, ...).
# Honest limitation: isoImage.* options are declared by the installer profile,
# which is NOT part of the channel's published options.json — hover on those
# stays empty rather than guessing.
{ config, pkgs, lib, ... }:
{
  # These all hover with full docs:
  networking.hostName = "nixls-live";
  networking.wireless.enable = false;
  services.openssh.enable = true;
  users.users.nixos.initialPassword = "nixos";
  boot.kernelParams = [ "copytoram" ];
  environment.systemPackages = [
    pkgs.htop
    pkgs.git
  ];

  # Installer-profile options (declared outside the standard module set — no
  # hover, by design):
  isoImage.isoName = lib.mkForce "nixls-demo.iso";
  isoImage.squashfsCompression = "zstd";
}
