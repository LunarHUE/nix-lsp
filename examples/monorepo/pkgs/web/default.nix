# Second package directory: same callPackage pattern, no custom args.
{ writeShellScriptBin, python3 }:

writeShellScriptBin "demo-web" ''
  ${python3}/bin/python -m http.server 8080
''
