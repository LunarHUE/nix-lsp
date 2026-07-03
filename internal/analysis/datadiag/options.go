package datadiag

import (
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// firstSegmentSkip is the set of leading attrpath segments a module binding uses
// for module machinery rather than option values. A path under any of these is
// never a candidate for the unknown-option check.
var firstSegmentSkip = map[string]bool{
	"options":         true,
	"imports":         true,
	"_module":         true,
	"disabledModules": true,
}

// OptionDiagnostics reports module option paths that name no documented option.
// It runs only for a file that passes the module gate (a `config`-formal function
// module, or a plain attrset with at least two binding paths that resolve exactly
// to a documented option), so an arbitrary data attrset is never flagged. Within
// a module it enumerates every static binding attrpath (nested attrsets compose
// full paths, exactly like option hover's assembly but file-wide), strips one
// leading `config`, skips module-machinery and dynamic paths, and walks each into
// the option trie, emitting at most one diagnostic per path on the first segment
// that names no child of a known option group — and only when a near-miss
// sibling exists to suggest, so a real-but-undocumented option (declared
// internal/invisible, hence absent from options.json) is never flagged. A nil
// tree or index yields none.
func OptionDiagnostics(tree *syntax.Tree, index *options.Index) []Diagnostic {
	if tree == nil || index == nil {
		return nil
	}
	root, ok := index.Root()
	if !ok {
		return nil
	}
	bindings, gated := gatherModuleBindings(tree, index)
	if !gated {
		return nil
	}

	var out []Diagnostic
	for _, b := range bindings {
		if d, ok := descendOption(root, b.segs, b.ranges); ok {
			out = append(out, d)
		}
	}
	sortByRange(out)
	return out
}

// moduleBinding is one enumerated, normalized module option binding: the full
// composed attrpath segments (one leading `config` stripped, module-machinery
// namespaces excluded), the matching per-segment source ranges, and the bound
// value expression.
type moduleBinding struct {
	segs   []string
	ranges []syntax.Range
	value  syntax.Node
}

// gatherModuleBindings applies the module gate and, when it passes, returns every
// normalized option binding in the file. gated is false when the top level is not
// an attribute set, or the file is not a module (no `config` formal and fewer than
// two exact documented-option hits); a caller must emit nothing in that case. It
// is the shared front half of the option-name and option-type checks, so both see
// exactly the same files and the same paths.
func gatherModuleBindings(tree *syntax.Tree, index *options.Index) (bindings []moduleBinding, gated bool) {
	body, configModule := moduleShape(tree)
	if body.IsZero() {
		return nil, false
	}
	bindingSet := childBindingSet(body)
	if bindingSet.IsZero() {
		return nil, false
	}

	var candidates []candidate
	enumerate(bindingSet, nil, nil, 0, &candidates)

	// Normalize each candidate (strip one leading `config`, drop module-machinery
	// namespaces) and count how many resolve exactly to a documented option; that
	// count is the plain-attrset half of the module gate.
	exactHits := 0
	for _, cand := range candidates {
		segs, ranges := cand.segs, cand.ranges
		if len(segs) > 0 && segs[0] == "config" {
			segs, ranges = segs[1:], ranges[1:]
		}
		if len(segs) == 0 || firstSegmentSkip[segs[0]] {
			continue
		}
		bindings = append(bindings, moduleBinding{segs: segs, ranges: ranges, value: cand.value})
		if _, ok := index.Lookup(segs); ok {
			exactHits++
		}
	}

	// Module gate: never flag an arbitrary data attrset. A `config`-formal function
	// is a module by shape; otherwise demand at least two exact documented-option
	// hits before trusting the file to be a module.
	if !configModule && exactHits < 2 {
		return nil, false
	}
	return bindings, true
}

// candidate is one enumerated binding attrpath: the full composed segments, their
// matching source ranges, and the bound value expression.
type candidate struct {
	segs   []string
	ranges []syntax.Range
	value  syntax.Node
}

// enumerate walks a binding_set, recording each binding's full attrpath (composed
// with prefix) and recursing into a nested attribute-set value with that path as
// the new prefix. It records a candidate for every binding (a group prefix like
// `[networking]` yields no diagnostic on its own, so this is harmless) and never
// descends into a let binding list, an inherit, or a dynamic path.
func enumerate(bindingSet syntax.Node, prefixSegs []string, prefixRanges []syntax.Range, depth int, out *[]candidate) {
	if depth > maxUnwrap || bindingSet.IsZero() {
		return
	}
	for _, entry := range bindingSet.NamedChildren() {
		// Only plain bindings carry an option path; inherit/inherit_from do not.
		if entry.Kind() != "binding" {
			continue
		}
		segs, ranges, ok := attrpathSegments(entry.ChildByFieldName("attrpath"))
		if !ok {
			// A dynamic (${...}) or otherwise non-static segment: bail this binding.
			continue
		}
		fullSegs := concatStrings(prefixSegs, segs)
		fullRanges := concatRanges(prefixRanges, ranges)
		value := entry.ChildByFieldName("expression")
		*out = append(*out, candidate{segs: fullSegs, ranges: fullRanges, value: value})

		if inner := attrsetBindingSet(value); !inner.IsZero() {
			enumerate(inner, fullSegs, fullRanges, depth+1, out)
		}
	}
}

// descendOption walks segs into the option trie from root, per the conservative
// rules: stop silently at or beyond a documented option; accept an arbitrary
// instance segment through a "<name>"/"*" wildcard; descend a matching concrete
// child; and, only at an interior group node past the first segment, flag the
// first segment that matches no concrete child. It returns at most one diagnostic.
func descendOption(root options.Cursor, segs []string, ranges []syntax.Range) (Diagnostic, bool) {
	cur := root
	for i, seg := range segs {
		if cur.HasDoc() {
			// A real option leaf (or a freeform/attrsOf option): deeper segments are
			// normal values, never unknown options.
			return Diagnostic{}, false
		}
		if wc, ok := cur.Wildcard(); ok {
			// This node names instances (services.<name>, users.users.<name>): seg is
			// an arbitrary instance name, so accept it and descend through the wildcard.
			cur = wc
			continue
		}
		if child, ok := cur.Child(seg); ok {
			cur = child
			continue
		}
		// seg is not a concrete child here.
		if i == 0 {
			// A first segment that reaches no top-level namespace is not an option path
			// at all (e.g. isoImage.* from the installer profile): stay silent.
			return Diagnostic{}, false
		}
		names := cur.ChildNames()
		if len(names) == 0 {
			// Not an interior group with concrete children: nothing to compare against.
			return Diagnostic{}, false
		}
		suggestions := suggest(seg, names)
		if len(suggestions) == 0 {
			// No near-miss sibling: the channel options.json only carries DOCUMENTED
			// options, and NixOS declares some real ones with internal/invisible
			// flags (system.disableInstallerTools is absent while its siblings are
			// present). A genuine typo is by definition within a couple of edits of
			// a real documented sibling; an unknown segment this far from every
			// child is more likely a dataset-hidden real option than a typo, so
			// stay silent — exactly the gate the package check applies.
			return Diagnostic{}, false
		}
		return buildOptionDiagnostic(segs[:i+1], ranges[i], suggestions), true
	}
	// The whole path landed on a node (a group or a leaf): not an unknown option.
	return Diagnostic{}, false
}

// buildOptionDiagnostic assembles the warning for an unknown option segment: the
// message names the path so far and the best suggestion (the near-miss gate in
// descendOption guarantees at least one); Suggestions carries all offered
// replacements for the quick fix.
func buildOptionDiagnostic(pathSoFar []string, r syntax.Range, suggestions []string) Diagnostic {
	message := "unknown option: " + strings.Join(pathSoFar, ".") +
		" (did you mean " + suggestions[0] + "?)"
	return Diagnostic{
		Diagnostic: syntax.Diagnostic{
			Message:  message,
			Range:    r,
			Code:     CodeUnknownOption,
			Severity: syntax.SeverityWarning,
		},
		Suggestions: suggestions,
	}
}

// moduleShape unwraps the file's single top-level expression through parens, let
// bodies, and function bodies to the attribute set it defines, reporting whether a
// function whose formals include `config` was passed through (the strong module
// signal). A top level that is not ultimately an attribute set yields a zero node.
func moduleShape(tree *syntax.Tree) (body syntax.Node, configModule bool) {
	var node syntax.Node
	for _, child := range tree.Root().NamedChildren() {
		node = child
		break
	}
	for i := 0; i < maxUnwrap; i++ {
		if node.IsZero() {
			return syntax.Node{}, configModule
		}
		switch node.Kind() {
		case "attrset_expression", "rec_attrset_expression":
			return node, configModule
		case "parenthesized_expression":
			node = node.ChildByFieldName("expression")
		case "let_expression":
			node = node.ChildByFieldName("body")
		case "function_expression":
			if functionHasConfigFormal(node) {
				configModule = true
			}
			node = node.ChildByFieldName("body")
		default:
			return syntax.Node{}, configModule
		}
	}
	return syntax.Node{}, configModule
}

// functionHasConfigFormal reports whether fn is a destructured function one of
// whose formals is named `config`, the parameter every NixOS module receives.
func functionHasConfigFormal(fn syntax.Node) bool {
	formals := fn.ChildByFieldName("formals")
	if formals.IsZero() {
		return false
	}
	for _, child := range formals.NamedChildren() {
		if child.Kind() != "formal" {
			continue
		}
		name := child.ChildByFieldName("name")
		if !name.IsZero() && name.Kind() == "identifier" && name.Text() == "config" {
			return true
		}
	}
	return false
}

// childBindingSet returns the binding_set child of an attribute-set node.
func childBindingSet(attrset syntax.Node) syntax.Node {
	for _, child := range attrset.NamedChildren() {
		if child.Kind() == "binding_set" {
			return child
		}
	}
	return syntax.Node{}
}

// attrpathSegments returns the static text and source range of each attrpath
// segment, or ok=false when the attrpath is empty or holds any dynamic segment.
func attrpathSegments(attrpath syntax.Node) ([]string, []syntax.Range, bool) {
	if attrpath.IsZero() {
		return nil, nil, false
	}
	children := attrpath.NamedChildren()
	if len(children) == 0 {
		return nil, nil, false
	}
	segs := make([]string, 0, len(children))
	ranges := make([]syntax.Range, 0, len(children))
	for _, child := range children {
		v, ok := segmentText(child)
		if !ok {
			return nil, nil, false
		}
		segs = append(segs, v)
		ranges = append(ranges, child.Range())
	}
	return segs, ranges, true
}

// concatStrings returns a fresh slice of a followed by b, never aliasing either.
func concatStrings(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

// concatRanges returns a fresh slice of a followed by b, never aliasing either.
func concatRanges(a, b []syntax.Range) []syntax.Range {
	out := make([]syntax.Range, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}
