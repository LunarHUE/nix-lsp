package scopes

import "github.com/wesleybaldwin/nix-lsp/internal/syntax"

// optionpath.go holds a pure CST helper for NixOS option-hover: given a cursor
// position it reconstructs the full static option attribute path that the
// position names. Like the rest of this package it never touches the memo engine
// or the filesystem; it answers only from a single parsed tree. Anything
// dynamic, ambiguous, or off an attrpath yields ok=false so the caller can fall
// through to a null hover rather than look up a wrong option.

// OptionPathAt returns the full static attribute path that the position names,
// for option-hover lookup, plus the source range of the specific path segment
// under pos.
//
// Two shapes are supported:
//
//   - Binding attrpaths. pos on a segment of a binding's attrpath inside an
//     attribute set (plain or rec). The path is assembled by walking outward:
//     each enclosing (attrset -> binding) hop prepends that binding's full
//     attrpath, ascending through parentheses and let-in bodies. The ascent
//     stops successfully at any other enclosing context (function body, top of
//     file, list element, call argument). A binding inside a let..in binding
//     list is not an option path and yields ok=false. A single leading "config"
//     segment is stripped after assembly.
//
//   - Select expressions rooted at config. pos on a segment of a select chain
//     whose base identifier is exactly "config" (e.g. config.networking.enable).
//     The config base is stripped; the base identifier itself is not a hover
//     target. Other bases (options, lib, ...) yield ok=false.
//
// The returned path covers segments from the outermost enclosing binding down to
// and including the hovered segment, never segments to its right. If any segment
// in that assembled path is dynamic (interpolation), ok=false. r is exactly the
// hovered segment's own range (for string segments this includes the quotes).
func OptionPathAt(tree *syntax.Tree, pos syntax.Position) (path []string, r syntax.Range, ok bool) {
	if tree == nil {
		return nil, syntax.Range{}, false
	}

	node := deepestNodeAt(tree, pos)
	if node.IsZero() {
		return nil, syntax.Range{}, false
	}

	// Ascend to the enclosing attrpath, if any.
	attrpath := node
	for !attrpath.IsZero() && attrpath.Kind() != "attrpath" {
		attrpath = attrpath.Parent()
	}
	if attrpath.IsZero() {
		return nil, syntax.Range{}, false
	}

	idx, seg, found := segmentUnderPos(attrpath, pos)
	if !found {
		return nil, syntax.Range{}, false
	}
	r = seg.Range()

	parent := attrpath.Parent()
	switch parent.Kind() {
	case "binding":
		assembled, ok := assembleBindingPath(parent, attrpath, idx)
		if !ok {
			return nil, syntax.Range{}, false
		}
		// Strip at most one leading "config"; if that empties the path (the
		// hovered segment WAS the leading config), there is nothing to look up.
		if len(assembled) > 0 && assembled[0] == "config" {
			assembled = assembled[1:]
		}
		if len(assembled) == 0 {
			return nil, syntax.Range{}, false
		}
		return assembled, r, true

	case "select_expression":
		base := parent.ChildByFieldName("expression")
		if base.Kind() != "variable_expression" || base.Text() != "config" {
			return nil, syntax.Range{}, false
		}
		segs, ok := staticSegmentsUpTo(attrpath, idx)
		if !ok {
			return nil, syntax.Range{}, false
		}
		// The config base is not part of the attrpath, so segs is already the
		// stripped path.
		return segs, r, true

	default:
		return nil, syntax.Range{}, false
	}
}

// assembleBindingPath builds the full option path for a binding-attrpath hover.
// binding is the CST binding node, attrpath its attrpath, and idx the hovered
// segment's index. It seeds the accumulator with this binding's segments up to
// and including idx, then walks outward prepending each enclosing binding's full
// attrpath. It returns ok=false if any segment on the path is dynamic or if any
// hop is a let binding rather than an attrset binding.
func assembleBindingPath(binding, attrpath syntax.Node, idx int) ([]string, bool) {
	acc, ok := staticSegmentsUpTo(attrpath, idx)
	if !ok {
		return nil, false
	}

	cur := binding
	for i := 0; i < maxUnwrap; i++ {
		set := cur.Parent()
		if set.Kind() != "binding_set" {
			return nil, false
		}
		container := set.Parent()
		switch container.Kind() {
		case "let_expression":
			// cur lives in a let..in binding list, not an option path.
			return nil, false
		case "attrset_expression", "rec_attrset_expression":
			outer, found := enclosingBinding(container)
			if !found {
				// The attrset is top-level, a function body, a list element, a
				// call argument, etc.: stop the ascent successfully.
				return acc, true
			}
			osegs, ok := staticAttrpathSegments(outer.ChildByFieldName("attrpath"))
			if !ok {
				return nil, false
			}
			combined := make([]string, 0, len(osegs)+len(acc))
			combined = append(combined, osegs...)
			combined = append(combined, acc...)
			acc = combined
			cur = outer
		default:
			return nil, false
		}
	}
	return nil, false
}

// enclosingBinding reports whether the attribute-set expression node is the
// value of an enclosing binding, ascending through parentheses and let-in
// bodies. It returns that binding node when so. It returns false (stop the
// ascent successfully) when the attrset is instead a function body, a list
// element, a call argument, the top-level expression, or similar.
func enclosingBinding(node syntax.Node) (syntax.Node, bool) {
	cur := node
	for i := 0; i < maxUnwrap; i++ {
		parent := cur.Parent()
		if parent.IsZero() {
			return syntax.Node{}, false
		}
		switch parent.Kind() {
		case "parenthesized_expression":
			cur = parent
		case "let_expression":
			// Only a let BODY is transparent; a let binding list is handled by
			// the caller (assembleBindingPath) as a bail.
			body := parent.ChildByFieldName("body")
			if !body.IsZero() && body.Range() == cur.Range() {
				cur = parent
				continue
			}
			return syntax.Node{}, false
		case "binding":
			// An attrset in binding position is the binding's value.
			return parent, true
		default:
			return syntax.Node{}, false
		}
	}
	return syntax.Node{}, false
}

// deepestNodeAt returns the deepest named node whose range contains pos,
// descending into the single child that contains pos at each level.
func deepestNodeAt(tree *syntax.Tree, pos syntax.Position) syntax.Node {
	node := tree.Root()
	if node.IsZero() {
		return syntax.Node{}
	}
	for i := 0; i < maxUnwrap*maxUnwrap; i++ {
		var next syntax.Node
		for _, child := range node.NamedChildren() {
			if rangeContains(child.Range(), pos) {
				next = child
				break
			}
		}
		if next.IsZero() {
			return node
		}
		node = next
	}
	return node
}

// segmentUnderPos returns the index and node of the attrpath segment whose range
// contains pos. It reports false when pos lies between segments (on a dot) or
// outside every segment.
func segmentUnderPos(attrpath syntax.Node, pos syntax.Position) (int, syntax.Node, bool) {
	for i, child := range attrpath.NamedChildren() {
		if rangeContains(child.Range(), pos) {
			return i, child, true
		}
	}
	return 0, syntax.Node{}, false
}

// staticSegmentsUpTo returns the static text of attrpath segments [0..idx]. It
// reports false when idx is out of range or any segment in that span is dynamic.
func staticSegmentsUpTo(attrpath syntax.Node, idx int) ([]string, bool) {
	children := attrpath.NamedChildren()
	if idx < 0 || idx >= len(children) {
		return nil, false
	}
	segs := make([]string, 0, idx+1)
	for i := 0; i <= idx; i++ {
		v, ok := segmentValue(children[i])
		if !ok {
			return nil, false
		}
		segs = append(segs, v)
	}
	return segs, true
}

// staticAttrpathSegments returns the static text of every attrpath segment, or
// false if the attrpath is empty or holds any dynamic segment.
func staticAttrpathSegments(attrpath syntax.Node) ([]string, bool) {
	if attrpath.IsZero() {
		return nil, false
	}
	children := attrpath.NamedChildren()
	if len(children) == 0 {
		return nil, false
	}
	return staticSegmentsUpTo(attrpath, len(children)-1)
}

// segmentValue returns the static text of a single attrpath segment. An
// identifier yields its text; a non-interpolated string yields its unquoted
// content (its string_fragment text). Interpolations, strings with escapes or
// interpolations, and any other node yield false.
func segmentValue(seg syntax.Node) (string, bool) {
	switch seg.Kind() {
	case "identifier":
		return seg.Text(), true
	case "string_expression":
		value := ""
		for _, child := range seg.NamedChildren() {
			if child.Kind() != "string_fragment" {
				return "", false
			}
			value += child.Text()
		}
		return value, true
	default:
		return "", false
	}
}
