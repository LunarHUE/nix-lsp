package packages

// wellknown.go holds a small curated table of stable nixpkgs attributes that are
// not derivations — aliases, function libraries, builders, and fetchers — and so
// never appear in the channel packages.json artifact. The server consults it only
// after a dataset Lookup misses, so a real package entry always wins. Entries
// carry Attr and Description only: with Pname and Version empty, Markdown()
// renders the Attr as the header and omits the version backticks.

// wellknownDescriptions maps each curated attribute to its one-line description.
// Attributes in the same family (fetchers, trivial writers) share a description
// string; each attr is still its own key so lookup stays a plain map access.
var wellknownDescriptions = map[string]string{
	"runtimeShell": "Path to the default build-time shell (non-interactive bash): " +
		"the string \"${bash}/bin/bash\", not a derivation.",
	"lib":         "The nixpkgs function library (attribute sets, lists, strings, licenses, ...).",
	"callPackage": "Calls a package function, auto-wiring its arguments from the package set.",
	"mkShell":     "Builds a development-shell derivation for `nix develop` / `nix-shell`.",

	"fetchurl":        fetcherDescription,
	"fetchzip":        fetcherDescription,
	"fetchgit":        fetcherDescription,
	"fetchFromGitHub": fetcherDescription,
	"fetchFromGitLab": fetcherDescription,

	"writeText":             trivialWriterDescription,
	"writeShellScript":      trivialWriterDescription,
	"writeShellScriptBin":   trivialWriterDescription,
	"writeShellApplication": trivialWriterDescription,

	"symlinkJoin": "Builds a derivation that symlinks the contents of the given packages together.",
	"buildEnv":    "Builds an environment derivation that merges the given packages.",
}

const (
	fetcherDescription       = "Source fetcher function; produces a fixed-output derivation."
	trivialWriterDescription = "Trivial builder that writes the given content to the store."
)

// Wellknown returns the curated Doc for a stable non-derivation nixpkgs
// attribute, or ok=false. Callers should consult it only when the dataset lookup
// misses: it describes what the attribute is, never a version, and must not be
// attributed to any channel.
func Wellknown(attr string) (*Doc, bool) {
	desc, ok := wellknownDescriptions[attr]
	if !ok {
		return nil, false
	}
	return &Doc{Attr: attr, Description: desc}, true
}
