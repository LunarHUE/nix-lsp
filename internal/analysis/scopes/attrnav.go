package scopes

import "github.com/wesleybaldwin/nix-lsp/internal/syntax"

// attrnav.go holds pure CST helpers for resolving attribute selection into a
// concrete definition site (a binding attrpath, or an inherited name). Like the
// rest of this package these functions never touch the memo engine or the
// filesystem; they answer only from a single parsed tree. Anything ambiguous,
// dynamic, or unexpected yields ok=false so callers can fall through to a null
// result rather than risk a wrong jump.

// maxUnwrap bounds the wrapper-unwrapping loop so a pathological tree cannot
// spin forever. Real nesting is shallow; this is only a safety valve.
const maxUnwrap = 64

// BindingValueRange returns the range of the value expression of the binding
// that introduced b. Only plain-key bindings (let, rec attr, attrset key) have a
// value to point at; inherited names and function parameters return false. It
// locates the CST `binding` node whose attrpath contains b's name and returns
// that binding's expression range.
func BindingValueRange(tree *syntax.Tree, b *Binding) (syntax.Range, bool) {
	if tree == nil || b == nil {
		return syntax.Range{}, false
	}
	switch b.Kind {
	case LetBinding, RecAttr, AttrBinding:
	default:
		return syntax.Range{}, false
	}

	var result syntax.Range
	found := false
	tree.Walk(func(node syntax.Node) bool {
		if found {
			return false
		}
		if node.Kind() != "binding" {
			return true
		}
		attrpath := node.ChildByFieldName("attrpath")
		if attrpath.IsZero() || !rangeContains(attrpath.Range(), b.NameRange.Start) {
			return true
		}
		expr := node.ChildByFieldName("expression")
		if expr.IsZero() {
			return true
		}
		result = expr.Range()
		found = true
		return false
	})
	return result, found
}

// BindingValueSource returns the source text of the value expression of the
// binding that introduced b, located the same way as BindingValueRange. It is
// the building block for binding-value hover, which shows the bound expression
// verbatim: this is the source text of the expression, never an evaluated value.
func BindingValueSource(tree *syntax.Tree, b *Binding) (string, bool) {
	r, ok := BindingValueRange(tree, b)
	if !ok {
		return "", false
	}
	node := nodeWithRange(tree, r)
	if node.IsZero() {
		return "", false
	}
	return node.Text(), true
}

// FormalDefaultSource returns the source text of the default expression of a
// formal parameter binding (`{ x ? 42 }:` -> "42"), or false when b is not a
// formal, the formal has no default, or the formal cannot be located. It locates
// the CST `formal` node whose name spans b's name.
func FormalDefaultSource(tree *syntax.Tree, b *Binding) (string, bool) {
	if tree == nil || b == nil || b.Kind != FormalParam {
		return "", false
	}
	var result string
	found := false
	tree.Walk(func(node syntax.Node) bool {
		if found {
			return false
		}
		if node.Kind() != "formal" {
			return true
		}
		name := node.ChildByFieldName("name")
		if name.IsZero() || !rangeContains(name.Range(), b.NameRange.Start) {
			return true
		}
		found = true
		def := node.ChildByFieldName("default")
		if def.IsZero() {
			return false
		}
		result = def.Text()
		return false
	})
	return result, found && result != ""
}

// ResolveAttrPath resolves a static attribute path against the file's top-level
// value, unwrapping through function bodies, parentheses, and let-in bodies
// until an attribute set is reached. It returns the range of the definition the
// path names, or false if any segment is dynamic, the path is not found, or the
// top-level value is not (after unwrapping) an attribute set.
func ResolveAttrPath(tree *syntax.Tree, path []string) (syntax.Range, bool) {
	if tree == nil || len(path) == 0 {
		return syntax.Range{}, false
	}
	top := topLevelExpr(tree)
	attrset, ok := unwrapToAttrset(top)
	if !ok {
		return syntax.Range{}, false
	}
	return resolveInAttrset(attrset, path)
}

// AttrsetValueResolve resolves a static attribute path against the expression at
// valueRange within tree (the same-file case: the base identifier resolved to a
// local binding whose value is an attribute set literal). It shares the descent
// logic with ResolveAttrPath.
func AttrsetValueResolve(tree *syntax.Tree, valueRange syntax.Range, path []string) (syntax.Range, bool) {
	if tree == nil || len(path) == 0 {
		return syntax.Range{}, false
	}
	node := nodeWithRange(tree, valueRange)
	if node.IsZero() {
		return syntax.Range{}, false
	}
	attrset, ok := unwrapToAttrset(node)
	if !ok {
		return syntax.Range{}, false
	}
	return resolveInAttrset(attrset, path)
}

// resolveInAttrset resolves the remaining path against an attribute-set node. It
// prefers an exact static attrpath match (returning that attrpath's range),
// otherwise descends through the longest static attrpath that is a proper prefix
// of the path, whose value must itself unwrap to an attribute set. A single
// remaining segment may also match an `inherit` / `inherit (e)` entry, returning
// the inherited identifier's range.
func resolveInAttrset(attrset syntax.Node, path []string) (syntax.Range, bool) {
	set := bindingSet(attrset)
	if set.IsZero() {
		return syntax.Range{}, false
	}

	var prefixValue syntax.Node
	prefixLen := 0
	for _, entry := range set.NamedChildren() {
		switch entry.Kind() {
		case "binding":
			attrpath := entry.ChildByFieldName("attrpath")
			segs, ok := staticAttrSegments(attrpath)
			if !ok {
				continue
			}
			if len(segs) == len(path) && segmentsEqual(segs, path) {
				return attrpath.Range(), true
			}
			if len(segs) < len(path) && segmentsEqual(segs, path[:len(segs)]) && len(segs) > prefixLen {
				prefixLen = len(segs)
				prefixValue = entry.ChildByFieldName("expression")
			}
		case "inherit", "inherit_from":
			if len(path) != 1 {
				continue
			}
			attrs := entry.ChildByFieldName("attrs")
			for _, attr := range attrs.NamedChildren() {
				if attr.Kind() == "identifier" && attr.Text() == path[0] {
					return attr.Range(), true
				}
			}
		}
	}

	if prefixLen > 0 {
		inner, ok := unwrapToAttrset(prefixValue)
		if !ok {
			return syntax.Range{}, false
		}
		return resolveInAttrset(inner, path[prefixLen:])
	}
	return syntax.Range{}, false
}

// unwrapToAttrset strips parentheses, function bodies, and let-in bodies until it
// reaches an attribute set (plain or rec). Any other expression yields false.
func unwrapToAttrset(node syntax.Node) (syntax.Node, bool) {
	for i := 0; i < maxUnwrap; i++ {
		if node.IsZero() {
			return syntax.Node{}, false
		}
		switch node.Kind() {
		case "attrset_expression", "rec_attrset_expression":
			return node, true
		case "parenthesized_expression":
			node = node.ChildByFieldName("expression")
		case "function_expression", "let_expression":
			node = node.ChildByFieldName("body")
		default:
			return syntax.Node{}, false
		}
	}
	return syntax.Node{}, false
}

// topLevelExpr returns the file's single top-level expression node.
func topLevelExpr(tree *syntax.Tree) syntax.Node {
	for _, child := range tree.Root().NamedChildren() {
		return child
	}
	return syntax.Node{}
}

// nodeWithRange returns the outermost node whose range exactly equals r.
func nodeWithRange(tree *syntax.Tree, r syntax.Range) syntax.Node {
	var found syntax.Node
	tree.Walk(func(node syntax.Node) bool {
		if !found.IsZero() {
			return false
		}
		if node.Range() == r {
			found = node
			return false
		}
		return true
	})
	return found
}

// bindingSet returns the binding_set child of an attribute-set node.
func bindingSet(node syntax.Node) syntax.Node {
	return childByKind(node, "binding_set")
}

// staticAttrSegments returns the identifier text of each attrpath segment, or
// false if the attrpath is empty or holds any dynamic (string/interpolation)
// segment.
func staticAttrSegments(attrpath syntax.Node) ([]string, bool) {
	if attrpath.IsZero() {
		return nil, false
	}
	children := attrpath.NamedChildren()
	if len(children) == 0 {
		return nil, false
	}
	segs := make([]string, 0, len(children))
	for _, child := range children {
		if child.Kind() != "identifier" {
			return nil, false
		}
		segs = append(segs, child.Text())
	}
	return segs, true
}

// segmentsEqual reports whether two segment slices are element-wise equal.
func segmentsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
