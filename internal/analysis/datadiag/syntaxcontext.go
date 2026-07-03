package datadiag

import (
	"sort"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// syntaxErrorCode is the stable code the syntax package puts on parse-error
// diagnostics (internal/syntax tree.Diagnostics); the enrichment below keys on it.
const syntaxErrorCode = "syntax-error"

// maxHintChildren caps how many child option names an "accepts options like"
// enrichment lists; real option groups have dozens, and four is enough to orient.
const maxHintChildren = 4

// EnrichSyntaxDiagnostics appends option-schema guidance to parse-error
// diagnostics: when a syntax-error's range sits inside bindings whose composed
// attrpath resolves through the option trie (with the same wildcard tolerance as
// the unknown-option walk, so an instance name under an attrsOf option counts) to
// a node with concrete documented children, the message gains
// " — <path> accepts options like a, b, c, d". It runs behind the same module
// gate as the other option checks, changes messages only (never ranges, codes, or
// the diagnostic count), and returns the input slice untouched when nothing
// applies — otherwise a fresh slice, never mutating diags in place (the caller's
// slice may be memoized).
func EnrichSyntaxDiagnostics(tree *syntax.Tree, index *options.Index, diags []syntax.Diagnostic) []syntax.Diagnostic {
	if tree == nil || index == nil || len(diags) == 0 {
		return diags
	}
	hasSyntaxError := false
	for _, d := range diags {
		if d.Code == syntaxErrorCode {
			hasSyntaxError = true
			break
		}
	}
	if !hasSyntaxError {
		return diags
	}
	root, ok := index.Root()
	if !ok {
		return diags
	}
	if _, gated := gatherModuleBindings(tree, index); !gated {
		return diags
	}

	out := diags
	changed := false
	for i, d := range diags {
		if d.Code != syntaxErrorCode {
			continue
		}
		segs, ok := enclosingBindingPathAt(tree, d.Range.Start)
		if !ok {
			continue
		}
		hint, ok := optionChildrenHint(root, segs)
		if !ok {
			continue
		}
		if !changed {
			out = append([]syntax.Diagnostic(nil), diags...)
			changed = true
		}
		out[i].Message = d.Message + " — " + hint
	}
	return out
}

// enclosingBindingPathAt composes the attrpath segments of every binding
// enclosing pos, outermost first — the option path a value at pos is being
// written under. Containment is end-inclusive because the missing-semicolon
// diagnostic is a zero-width range at the very end of its binding. It reports
// ok=false when no binding encloses pos or any enclosing segment is dynamic.
func enclosingBindingPathAt(tree *syntax.Tree, pos syntax.Position) ([]string, bool) {
	if tree == nil {
		return nil, false
	}
	var segs []string
	node := tree.Root()
	for i := 0; i < maxUnwrap; i++ {
		binding := bindingAt(node, pos)
		if binding.IsZero() {
			break
		}
		s, _, ok := attrpathSegments(binding.ChildByFieldName("attrpath"))
		if !ok {
			return nil, false
		}
		segs = append(segs, s...)
		node = binding
	}
	if len(segs) == 0 {
		return nil, false
	}
	return segs, true
}

// bindingAt returns the shallowest binding node strictly below start that
// touches pos, descending only through nodes that themselves touch pos. When two
// siblings touch pos at a shared boundary, a node containing pos strictly inside
// wins over one merely ending at it, mirroring the hover-side descent.
func bindingAt(start syntax.Node, pos syntax.Position) syntax.Node {
	node := start
	for i := 0; i < maxUnwrap*maxUnwrap; i++ {
		var contain, endAt syntax.Node
		for _, child := range node.NamedChildren() {
			r := child.Range()
			if positionBefore(pos, r.Start) || positionBefore(r.End, pos) {
				continue
			}
			if positionBefore(pos, r.End) {
				contain = child
			} else {
				endAt = child
			}
		}
		next := contain
		if next.IsZero() {
			next = endAt
		}
		if next.IsZero() {
			return syntax.Node{}
		}
		if next.Kind() == "binding" {
			return next
		}
		node = next
	}
	return syntax.Node{}
}

// positionBefore reports whether a orders strictly before b.
func positionBefore(a, b syntax.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

// optionChildrenHint resolves a composed binding path through the option trie
// (exact child first, then the "<name>"/"*" wildcard, exactly as the
// unknown-option walk descends) and renders the guidance suffix when the reached
// node has concrete documented children. A path that leaves the trie, module
// machinery, or a node with no concrete children yields ok=false and no
// enrichment.
func optionChildrenHint(root options.Cursor, segs []string) (string, bool) {
	if len(segs) > 0 && segs[0] == "config" {
		segs = segs[1:]
	}
	if len(segs) == 0 || firstSegmentSkip[segs[0]] {
		return "", false
	}
	cur := root
	for _, seg := range segs {
		if child, ok := cur.Child(seg); ok {
			cur = child
			continue
		}
		if wc, ok := cur.Wildcard(); ok {
			cur = wc
			continue
		}
		return "", false
	}
	names := cur.ChildNames()
	if len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	if len(names) > maxHintChildren {
		names = names[:maxHintChildren]
	}
	return strings.Join(segs, ".") + " accepts options like " + strings.Join(names, ", "), true
}
