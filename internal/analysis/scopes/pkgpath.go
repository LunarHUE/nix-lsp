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

// WithPkgsAttrAt returns the bare identifier that pos names when that identifier
// is an unresolved name in the body of an enclosing `with pkgs;` scope, plus the
// identifier's source range. It answers "which nixpkgs attribute would this bare
// name resolve to" for package-version hover, the `with pkgs;` counterpart of
// PkgPathAt's `pkgs.<attr>` selects.
//
// It bails (ok=false) unless every condition holds:
//   - pos is on an identifier in expression position (a variable_expression);
//   - the identifier does not resolve to a local binding or a builtin in file's
//     scope model — the same resolution ReferenceAt and go-to-definition use.
//     A name shadowed by a let/rec/param binding, or a builtin such as
//     `toString`, falls through to binding-value hover instead of claiming the
//     nixpkgs attribute;
//   - some enclosing `with` has as its subject exactly the bare identifier
//     `pkgs` (not `pkgs.foo`, not a call), reached from the identifier's body
//     side. The identifier that is itself a `with` subject bails, since it sits
//     in the environment rather than the body.
func WithPkgsAttrAt(file *File, tree *syntax.Tree, pos syntax.Position) (attr string, r syntax.Range, ok bool) {
	if tree == nil {
		return "", syntax.Range{}, false
	}

	node := deepestNodeAt(tree, pos)
	if node.IsZero() {
		return "", syntax.Range{}, false
	}

	// The position must be on an identifier used as an expression. The identifier
	// is the named `name` child of a variable_expression; nothing else (an
	// attrpath segment, a formal name, a select selection) is a bare variable use.
	varExpr := node
	if varExpr.Kind() == "identifier" {
		varExpr = varExpr.Parent()
	}
	if varExpr.Kind() != "variable_expression" {
		return "", syntax.Range{}, false
	}
	ident := varExpr.ChildByFieldName("name")
	if ident.IsZero() {
		ident = varExpr
	}

	// HARD RULE: a name that resolves lexically (any local binding) or to a
	// builtin is not a nixpkgs attribute. Only names left unresolved by the scope
	// model — the ones a `with` may supply dynamically — are candidates.
	if file != nil {
		if ref := file.ReferenceAt(pos); ref != nil && ref.Target != nil {
			return "", syntax.Range{}, false
		}
	}

	// Walk enclosing `with` expressions innermost-outward. The identifier matches
	// if any of them has `pkgs` as its subject and the identifier sits in that
	// with's body (not its environment, which would make the identifier itself a
	// with subject).
	for anc := varExpr.Parent(); !anc.IsZero(); anc = anc.Parent() {
		if anc.Kind() != "with_expression" {
			continue
		}
		env := anc.ChildByFieldName("environment")
		if env.IsZero() {
			continue
		}
		// Inside the subject expression, not the body: this identifier is (part of)
		// the with subject, so this with cannot supply it.
		if rangeContains(env.Range(), pos) {
			continue
		}
		if env.Kind() == "variable_expression" && env.Text() == "pkgs" {
			return ident.Text(), ident.Range(), true
		}
	}
	return "", syntax.Range{}, false
}
