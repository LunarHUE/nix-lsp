# Per-package directory, wired via callPackage from the root flake. Ctrl-click
# `helpers` uses to navigate; hover pkgs.hello for package docs once package
# hover lands.
{ writeShellScriptBin, hello, helpers }:

writeShellScriptBin "demo-cli" ''
  ${hello}/bin/hello
  echo "${helpers.greet "cli"}"
''
