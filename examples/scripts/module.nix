# systemd service scripts: `script` and `preStart` are the classic embedded
# shell attributes in NixOS modules — both hover as options AND highlight as
# bash with the injection grammar.
{ config, pkgs, ... }:
{
  systemd.services.backup = {
    description = "nightly backup";
    startAt = "03:00";

    preStart = ''
      mkdir -p /var/backup
      find /var/backup -mtime +30 -delete
    '';

    script = ''
      tar czf "/var/backup/data-$(date +%F).tar.gz" /var/lib/app
      echo "backup done: $(du -h /var/backup | tail -1)"
    '';

    serviceConfig.Type = "oneshot";
  };
}
