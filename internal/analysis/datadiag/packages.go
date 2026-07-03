package datadiag

import (
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// minPackageAttrLen is the shortest dotted attr the unknown-package check will
// consider. A one- or two-character attr is too ambiguous to correct safely, so
// it stays silent.
const minPackageAttrLen = 3

// PackageDiagnostics reports `pkgs.<attr>` selects that name no channel package.
// It scans every static select chain whose base is exactly the identifier `pkgs`
// (single-segment `pkgs.foo` and nested `pkgs.ns.foo`), and flags the attr when it
// is absent from both the packages index and the curated well-known table. To keep
// the check conservative it emits only when a near-miss suggestion exists (a real
// typo is by definition one or two edits from a real package), so an unknown-but-
// plausible attr like `pkgs.lib.mkIf` stays silent rather than squiggling. A nil
// tree, a nil index, or an empty index yields none.
//
// Bare names supplied by an enclosing `with pkgs;` are deliberately NOT diagnosed
// in v1: such a name may resolve to any in-scope binding, so flagging it would be
// far too noisy. Only the explicit `pkgs.` select form is checked here.
func PackageDiagnostics(tree *syntax.Tree, index *packages.Index) []Diagnostic {
	if tree == nil || index == nil || index.Len() == 0 {
		return nil
	}

	var out []Diagnostic
	tree.Walk(func(node syntax.Node) bool {
		if node.Kind() != "select_expression" {
			return true
		}
		base := node.ChildByFieldName("expression")
		if base.Kind() != "variable_expression" || base.Text() != "pkgs" {
			return true
		}
		attrpath := selectAttrpath(node)
		if attrpath.IsZero() {
			return true
		}
		segs, _, ok := attrpathSegments(attrpath)
		if !ok {
			// A dynamic segment (pkgs.${name}): not a static attr, skip.
			return true
		}
		attr := strings.Join(segs, ".")
		if len(attr) < minPackageAttrLen {
			return true
		}
		if _, known := index.Lookup(attr); known {
			return true
		}
		if _, wellknown := packages.Wellknown(attr); wellknown {
			return true
		}
		suggestions := packageSuggestions(index, attr)
		if len(suggestions) == 0 {
			// No near-miss package: an unknown attr this far from anything real is more
			// likely a package we simply do not know than a typo, so stay silent.
			return true
		}
		out = append(out, Diagnostic{
			Diagnostic: syntax.Diagnostic{
				Message:  "unknown package: pkgs." + attr + " (did you mean " + suggestions[0] + "?)",
				Range:    attrpath.Range(),
				Code:     CodeUnknownPackage,
				Severity: syntax.SeverityWarning,
			},
			Suggestions: suggestions,
		})
		return true
	})
	sortByRange(out)
	return out
}

// packageSuggestions returns up to maxSuggestions channel-package attrs within
// maxDistance edits of attr, drawn from the index completions for attr's first two
// characters. The full dotted attr key is both the candidate and the replacement.
func packageSuggestions(index *packages.Index, attr string) []string {
	prefix := attr
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	docs := index.Complete(prefix, 500)
	names := make([]string, 0, len(docs))
	for _, d := range docs {
		names = append(names, d.Attr)
	}
	return suggest(attr, names)
}

// selectAttrpath returns the attrpath (the selection) of a select_expression,
// which sits alongside the base expression as a named child.
func selectAttrpath(node syntax.Node) syntax.Node {
	for _, child := range node.NamedChildren() {
		if child.Kind() == "attrpath" {
			return child
		}
	}
	return syntax.Node{}
}
