package scopes

import (
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// pkgpath.go holds a pure CST helper for package-version hover: given a cursor
// position on a `pkgs.<attr>` select chain it reconstructs the attribute path the
// position names, for lookup in the channel packages index. It is a sibling of
// optionpath.go and reuses its helpers (deepestNodeAt, segmentUnderPos,
// staticSegmentsUpTo). Anything dynamic, or a chain whose base is not exactly the
// identifier `pkgs`, yields ok=false so the caller falls through to a null hover.

// PkgPathAt returns the dotted attribute path that pos names within a select
// chain whose base is exactly the identifier `pkgs`, plus the source range of the
// hovered segment. The path runs from the segment after `pkgs` up to and
// including the hovered segment: on `pkgs.python312Packages.requests`, hovering
// `requests` yields "python312Packages.requests" and hovering `python312Packages`
// yields "python312Packages". The `pkgs` base itself is not a hover target, any
// dynamic segment in the covered span bails, and a non-`pkgs` base bails.
func PkgPathAt(tree *syntax.Tree, pos syntax.Position) (attr string, r syntax.Range, ok bool) {
	if tree == nil {
		return "", syntax.Range{}, false
	}

	node := deepestNodeAt(tree, pos)
	if node.IsZero() {
		return "", syntax.Range{}, false
	}

	// Ascend to the enclosing attrpath, if any.
	attrpath := node
	for !attrpath.IsZero() && attrpath.Kind() != "attrpath" {
		attrpath = attrpath.Parent()
	}
	if attrpath.IsZero() {
		return "", syntax.Range{}, false
	}

	idx, seg, found := segmentUnderPos(attrpath, pos)
	if !found {
		return "", syntax.Range{}, false
	}

	// The attrpath must be the selection of a select expression rooted at the
	// bare identifier `pkgs`; the base itself is reached via a different node
	// (the select's expression field), so hovering it never lands here.
	parent := attrpath.Parent()
	if parent.Kind() != "select_expression" {
		return "", syntax.Range{}, false
	}
	base := parent.ChildByFieldName("expression")
	if base.Kind() != "variable_expression" || base.Text() != "pkgs" {
		return "", syntax.Range{}, false
	}

	segs, ok := staticSegmentsUpTo(attrpath, idx)
	if !ok {
		return "", syntax.Range{}, false
	}
	return strings.Join(segs, "."), seg.Range(), true
}
