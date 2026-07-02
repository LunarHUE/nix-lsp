package flake

import (
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// maxUnwrap bounds wrapper-unwrapping loops so a pathological tree cannot spin
// forever. Real nesting is shallow; this is only a safety valve.
const maxUnwrap = 64

// File is the modeled inputs/outputs of a single parsed flake.nix.
type File struct {
	Inputs      []*Input
	InputsRange syntax.Range
	HasInputs   bool
	Outputs     *Outputs
}

// Input is one declared flake input, merged from every binding that names it.
type Input struct {
	Name            string
	NameRange       syntax.Range
	URL             string
	URLRange        syntax.Range
	HasURL          bool
	TopFollows      string
	TopFollowsRange syntax.Range
	HasTopFollows   bool
	Flake           *bool
	Follows         []FollowsEdge
	// BindingRanges is the CST range of every binding entry that contributed to
	// this input: the whole top-level binding in the sugar form
	// (`inputs.foo.url = ...;`), and the inner `foo = ...;` binding inside the
	// inputs attrset in the nested form. Ranges include the trailing semicolon.
	BindingRanges []syntax.Range
}

// FollowsEdge is a nested `inputs.<child>.follows = "<target>"` override on an
// input, redirecting the input's own <child> to a top-level input.
type FollowsEdge struct {
	Child       string
	Target      string
	TargetRange syntax.Range
}

// Outputs models the `outputs = ...` function's parameter shape.
type Outputs struct {
	HasFormals   bool
	Formals      map[string]syntax.Range
	FormalsRange syntax.Range
	HasEllipsis  bool
	HasAtPattern bool
	// InsertAnchor is the position immediately after the last formal's range end,
	// where `, <name>` can be inserted to add an input to the outputs formals.
	// HasInsertAnchor is false when there are zero formals.
	InsertAnchor    syntax.Position
	HasInsertAnchor bool
}

// AnalyzeInputs extracts the inputs and outputs model from the top-level
// attribute set of a parsed flake.nix. It reads only static string literals and
// skips anything dynamic or unexpected, so the result is conservative.
func AnalyzeInputs(tree *syntax.Tree) *File {
	file := &File{}
	attrset := topLevelAttrset(tree)
	if attrset.IsZero() {
		return file
	}
	set := childByKind(attrset, "binding_set")
	if set.IsZero() {
		return file
	}

	byName := make(map[string]*Input)
	for _, entry := range set.NamedChildren() {
		if entry.Kind() != "binding" {
			continue
		}
		segs, segNodes, ok := staticSegments(entry.ChildByFieldName("attrpath"))
		if !ok || len(segs) == 0 {
			continue
		}
		value := entry.ChildByFieldName("expression")

		switch segs[0] {
		case "inputs":
			if !file.HasInputs {
				file.InputsRange = segNodes[0].Range()
				file.HasInputs = true
			}
			if len(segs) == 1 {
				collectNestedInputs(value, byName, file)
			} else {
				input := ensureInput(byName, file, segs[1], segNodes[1].Range(), entry)
				applyLeaves(input, segs[2:], value)
			}
		case "outputs":
			if len(segs) == 1 && file.Outputs == nil {
				file.Outputs = parseOutputs(value)
			}
		}
	}
	return file
}

// collectNestedInputs processes the entries of an `inputs = { ... }` attribute
// set, where each entry's first path segment is an input name.
func collectNestedInputs(value syntax.Node, byName map[string]*Input, file *File) {
	set := attrsetBindingSet(value)
	if set.IsZero() {
		return
	}
	for _, entry := range set.NamedChildren() {
		if entry.Kind() != "binding" {
			continue
		}
		segs, segNodes, ok := staticSegments(entry.ChildByFieldName("attrpath"))
		if !ok || len(segs) == 0 {
			continue
		}
		input := ensureInput(byName, file, segs[0], segNodes[0].Range(), entry)
		applyLeaves(input, segs[1:], entry.ChildByFieldName("expression"))
	}
}

// applyLeaves interprets a field path relative to an input. When the value is an
// attribute set it descends, extending the field path with each nested key, so
// both the nested (`hm = { url = ...; }`) and sugared (`hm.url = ...`) forms
// reduce to the same leaf fields.
func applyLeaves(input *Input, fieldPath []string, value syntax.Node) {
	set := attrsetBindingSet(value)
	if !set.IsZero() {
		for _, entry := range set.NamedChildren() {
			if entry.Kind() != "binding" {
				continue
			}
			segs, _, ok := staticSegments(entry.ChildByFieldName("attrpath"))
			if !ok || len(segs) == 0 {
				continue
			}
			child := make([]string, 0, len(fieldPath)+len(segs))
			child = append(child, fieldPath...)
			child = append(child, segs...)
			applyLeaves(input, child, entry.ChildByFieldName("expression"))
		}
		return
	}
	interpretLeaf(input, fieldPath, value)
}

// interpretLeaf records a single known leaf field onto input. Fields set more
// than once keep the first value seen.
func interpretLeaf(input *Input, fieldPath []string, value syntax.Node) {
	switch {
	case len(fieldPath) == 1 && fieldPath[0] == "url":
		if s, r, ok := staticString(value); ok && !input.HasURL {
			input.URL, input.URLRange, input.HasURL = s, r, true
		}
	case len(fieldPath) == 1 && fieldPath[0] == "follows":
		if s, r, ok := staticString(value); ok && !input.HasTopFollows {
			input.TopFollows, input.TopFollowsRange, input.HasTopFollows = s, r, true
		}
	case len(fieldPath) == 1 && fieldPath[0] == "flake":
		if b, ok := staticBool(value); ok && input.Flake == nil {
			input.Flake = b
		}
	case len(fieldPath) == 3 && fieldPath[0] == "inputs" && fieldPath[2] == "follows":
		if s, r, ok := staticString(value); ok {
			input.Follows = append(input.Follows, FollowsEdge{Child: fieldPath[1], Target: s, TargetRange: r})
		}
	}
}

// parseOutputs models the outputs function's parameters. A non-function or a
// plain single-argument outputs (`outputs = args: ...`) has no formals.
func parseOutputs(value syntax.Node) *Outputs {
	out := &Outputs{Formals: make(map[string]syntax.Range)}
	node := unwrapParen(value)
	if node.Kind() != "function_expression" {
		return out
	}
	formals := node.ChildByFieldName("formals")
	if formals.IsZero() {
		return out
	}
	out.HasFormals = true
	out.FormalsRange = formals.Range()
	out.HasAtPattern = !node.ChildByFieldName("universal").IsZero()
	for _, child := range formals.NamedChildren() {
		switch child.Kind() {
		case "formal":
			// Children iterate in source order, so the last formal seen is the
			// source-last one; its range end is where `, <name>` inserts.
			out.InsertAnchor = child.Range().End
			out.HasInsertAnchor = true
			name := child.ChildByFieldName("name")
			if !name.IsZero() && name.Kind() == "identifier" {
				if _, exists := out.Formals[name.Text()]; !exists {
					out.Formals[name.Text()] = name.Range()
				}
			}
		case "ellipses":
			out.HasEllipsis = true
		}
	}
	return out
}

// ensureInput returns the Input named name, creating it on first sight, and
// records entry as one of the bindings that contributed to it (entry is the
// whole binding node whose range covers the trailing semicolon).
func ensureInput(byName map[string]*Input, file *File, name string, nameRange syntax.Range, entry syntax.Node) *Input {
	input, ok := byName[name]
	if !ok {
		input = &Input{Name: name, NameRange: nameRange}
		byName[name] = input
		file.Inputs = append(file.Inputs, input)
	}
	if !entry.IsZero() {
		input.BindingRanges = append(input.BindingRanges, entry.Range())
	}
	return input
}

// topLevelAttrset unwraps the file's single top-level expression through
// function, let, and parenthesized wrappers until it reaches an attribute set.
func topLevelAttrset(tree *syntax.Tree) syntax.Node {
	if tree == nil {
		return syntax.Node{}
	}
	var node syntax.Node
	for _, child := range tree.Root().NamedChildren() {
		node = child
		break
	}
	for i := 0; i < maxUnwrap; i++ {
		if node.IsZero() {
			return syntax.Node{}
		}
		switch node.Kind() {
		case "attrset_expression", "rec_attrset_expression":
			return node
		case "parenthesized_expression":
			node = node.ChildByFieldName("expression")
		case "function_expression", "let_expression":
			node = node.ChildByFieldName("body")
		default:
			return syntax.Node{}
		}
	}
	return syntax.Node{}
}

// attrsetBindingSet returns the binding_set of value when value (after
// unwrapping parentheses) is an attribute set, else a zero node.
func attrsetBindingSet(value syntax.Node) syntax.Node {
	node := unwrapParen(value)
	switch node.Kind() {
	case "attrset_expression", "rec_attrset_expression":
		return childByKind(node, "binding_set")
	default:
		return syntax.Node{}
	}
}

// staticString returns the text of a plain string literal with no interpolation
// or escapes. Anything else yields ok=false so the field is treated as absent.
func staticString(node syntax.Node) (string, syntax.Range, bool) {
	n := unwrapParen(node)
	if n.Kind() != "string_expression" {
		return "", syntax.Range{}, false
	}
	var sb strings.Builder
	for _, child := range n.NamedChildren() {
		if child.Kind() != "string_fragment" {
			return "", syntax.Range{}, false
		}
		sb.WriteString(child.Text())
	}
	return sb.String(), n.Range(), true
}

// staticBool returns the value of a `true`/`false` literal, or ok=false.
func staticBool(node syntax.Node) (*bool, bool) {
	n := unwrapParen(node)
	if n.Kind() != "variable_expression" {
		return nil, false
	}
	switch n.Text() {
	case "true":
		v := true
		return &v, true
	case "false":
		v := false
		return &v, true
	}
	return nil, false
}

func unwrapParen(node syntax.Node) syntax.Node {
	for i := 0; i < maxUnwrap; i++ {
		if node.Kind() != "parenthesized_expression" {
			return node
		}
		next := node.ChildByFieldName("expression")
		if next.IsZero() {
			return node
		}
		node = next
	}
	return node
}

// staticSegments returns the identifier text and node of each attrpath segment,
// or ok=false if the attrpath is empty or holds any dynamic segment.
func staticSegments(attrpath syntax.Node) ([]string, []syntax.Node, bool) {
	if attrpath.IsZero() {
		return nil, nil, false
	}
	children := attrpath.NamedChildren()
	if len(children) == 0 {
		return nil, nil, false
	}
	segs := make([]string, 0, len(children))
	nodes := make([]syntax.Node, 0, len(children))
	for _, child := range children {
		if child.Kind() != "identifier" {
			return nil, nil, false
		}
		segs = append(segs, child.Text())
		nodes = append(nodes, child)
	}
	return segs, nodes, true
}

// childByKind returns the first named child of node with the given kind.
func childByKind(node syntax.Node, kind string) syntax.Node {
	for _, child := range node.NamedChildren() {
		if child.Kind() == kind {
			return child
		}
	}
	return syntax.Node{}
}
