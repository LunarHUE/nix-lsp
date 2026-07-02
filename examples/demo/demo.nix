# demo.nix — exercises the static analyzers plus cross-file navigation.
# Open examples/demo as its own workspace folder so imports resolve relative to
# this directory. Each squiggle below carries a comment naming what it shows.
let
  # Cross-file import: `lib.greet` uses below resolve into lib.nix. Put the
  # cursor on `greet` and Go to Definition to jump there.
  lib = import ./lib.nix;

  # Missing import target: ./missing.nix does not exist (missing-import error).
  broken = import ./missing.nix;

  # Never referenced anywhere -> unused-binding warning.
  unusedValue = 42;

  # Also never referenced, but the leading underscore marks it intentional, so
  # it is deliberately NOT flagged.
  _scratch = 99;

  # Bare inherit of a name defined nowhere -> bad-inherit error. It is used in
  # `fallback` below, so it is not additionally reported as unused.
  inherit undefinedName;

  # A function, a list, and nested attribute sets give the outline, folding,
  # find-references, and highlight features some structure to work with.
  makeUser = name: {
    inherit name;
    roles = [ "reader" "writer" ];
    profile = {
      greeting = lib.greet name;
      level = 1;
    };
  };

  users = [
    (makeUser "ada")
    (makeUser "linus")
  ];
in
{
  # Duplicate key in one attribute set -> duplicate-binding error on the second
  # `port`.
  settings = {
    port = 8080;
    port = 9090;
  };

  inherit users;
  first = builtins.head users;
  fallback = undefinedName;
  greetingFor = lib.greet "world";
  release = lib.version;
  unreachable = broken;
}
