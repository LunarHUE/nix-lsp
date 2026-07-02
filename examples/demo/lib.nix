{
  # lib.nix is the cross-file definition target for demo.nix. Go to Definition
  # on `lib.greet` (or `lib.version`) in demo.nix jumps to the matching
  # attribute below. Keep the attribute set as the first node in the file so
  # cross-file attribute resolution can find it.

  # greet builds a greeting for a name.
  greet = name: "Hello, ${name}!";

  # farewell is the mirror of greet.
  farewell = name: "Goodbye, ${name}.";

  # version is a plain string attribute.
  version = "1.0.0";
}
