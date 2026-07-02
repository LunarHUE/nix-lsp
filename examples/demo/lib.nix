# lib.nix — the cross-file definition target for demo.nix.
# Go to Definition on `lib.greet` (or `lib.version`) in demo.nix jumps here, to
# the matching attribute below.
{
  # greet builds a greeting for a name.
  greet = name: "Hello, ${name}!";

  # farewell is the mirror of greet.
  farewell = name: "Goodbye, ${name}.";

  # version is a plain string attribute.
  version = "1.0.0";
}
