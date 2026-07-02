# Shell scripts embedded in Nix strings, in the shapes the injection grammar
# recognizes: a string whose first line is a #! shebang, and well-known
# script-carrying attributes.
{ pkgs }:
{
  # Shebang-triggered: the string starts with #!, so its whole body highlights
  # as bash (note the ''${ escape — that is Nix, not shell).
  deploy = pkgs.writeScriptBin "deploy" ''
    #!${pkgs.runtimeShell}
    set -euo pipefail

    target="''${1:-staging}"
    echo "deploying to $target"
    for f in result/*; do
      scp "$f" "deploy@$target:/srv/app/"
    done
  '';

  # writeShellScriptBin adds the shebang itself; the `text`-style body still
  # highlights via the attribute-name trigger.
  healthcheck = pkgs.writeShellScriptBin "healthcheck" ''
    if ! curl -fs http://localhost:8080/health; then
      echo "unhealthy" >&2
      exit 1
    fi
  '';
}
