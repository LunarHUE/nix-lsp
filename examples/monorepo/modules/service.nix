# NixOS module shipped by the monorepo. Option hover works here like in any
# module: try the systemd.services.<name> wildcard — hover `serviceConfig` or
# `wantedBy` to see docs resolved through the <name> placeholder.
{ config, lib, pkgs, ... }:
{
  systemd.services.demo-web = {
    description = "demo web service from the monorepo";
    wantedBy = [ "multi-user.target" ];
    serviceConfig = {
      ExecStart = "${pkgs.python3}/bin/python -m http.server 8080";
      DynamicUser = true;
    };
  };

  networking.firewall.allowedTCPPorts = [ 8080 ];
}
