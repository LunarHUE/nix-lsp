# NixOS module demonstrating option hover: nixls resolves the attribute path
# under the cursor and shows the option's documentation (description, type,
# default, example, where it is declared) from the official options.json for
# the channel named by flake.lock's nixpkgs input (here: nixos-25.05). The
# data loads in the background on startup — hover shows nothing for the first
# seconds on a cold cache, then works everywhere.
{ config, pkgs, ... }:
{
  # Flat attrpath: hover any segment. `firewall` shows the networking.firewall
  # group is not itself an option (no hover); `enable` and `allowedTCPPorts`
  # show full docs with type and default.
  networking.firewall.enable = true;
  networking.firewall.allowedTCPPorts = [ 22 80 ];

  # Nested attrsets compose the same paths: hover `PermitRootLogin` to see
  # docs for services.openssh.settings.PermitRootLogin.
  services.openssh = {
    enable = true;
    settings.PermitRootLogin = "no";
  };

  # Submodule wildcard: the docs are declared under users.users.<name>.*, and
  # hover on `home` or `shell` resolves through the <name> placeholder.
  users.users.alice = {
    home = "/home/alice";
    shell = pkgs.bashInteractive;
  };

  # Reading an option also works: hover the segments of this config.* chain.
  services.fail2ban.enable = config.networking.firewall.enable;

  time.timeZone = "UTC";

  environment.systemPackages = [
    pkgs.htop
    pkgs.claude-code
  ];
}
