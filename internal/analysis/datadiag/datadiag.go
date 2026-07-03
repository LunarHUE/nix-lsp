// Package datadiag produces conservative dataset-backed diagnostics: a NixOS
// module option path that names no documented option (unknown-option) and a
// pkgs.<attr> select that names no channel package (unknown-package). Unlike the
// static package these depend on the loaded option/package index identity, not on
// file content alone, so they are computed in the server layer and appended to the
// published set rather than memoized. Both are the first diagnostics that can put
// a squiggle on otherwise-valid Nix, so every rule below bends toward silence: a
// path that does not clearly reach a known namespace, a wildcard instance segment,
// anything dynamic, or a package with no near-miss suggestion is left alone.
package datadiag

import (
	"sort"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// Diagnostic codes are stable, machine-readable identifiers keyed on by the code
// actions that offer the did-you-mean fixes.
const (
	// CodeUnknownOption marks a module option path segment that names no child of
	// a known, concrete option group.
	CodeUnknownOption = "unknown-option"
	// CodeUnknownPackage marks a pkgs.<attr> select that names no channel package.
	CodeUnknownPackage = "unknown-package"
)

// Diagnostic is a dataset diagnostic enriched with the ordered did-you-mean
// suggestions for its flagged range, so the server can both publish the plain
// syntax.Diagnostic and build one quick fix per suggestion (matching by code and
// range, exactly as the flake follows fix does). Suggestions may be empty for an
// unknown-option; an unknown-package is only ever emitted with at least one.
type Diagnostic struct {
	syntax.Diagnostic
	// Suggestions are the replacement texts for the flagged range, best (smallest
	// edit distance) first. For an option they are single child-segment names; for
	// a package they are full dotted attribute paths.
	Suggestions []string
}

const (
	// maxSuggestions caps the did-you-mean replacements offered for one diagnostic.
	maxSuggestions = 3
	// maxDistance is the largest Levenshtein distance a candidate name may sit from
	// the flagged text to be offered as a suggestion.
	maxDistance = 2
	// maxUnwrap bounds every structural ascent/descent so a pathological tree
	// cannot spin. It mirrors the same guard in the scopes and flake CST helpers.
	maxUnwrap = 64
)

// suggest returns up to maxSuggestions candidate names within maxDistance edits
// of target, best (smallest distance) first, ties broken by name. It mirrors
// server.suggestFollowsNames, the follows did-you-mean twin.
func suggest(target string, candidates []string) []string {
	type scored struct {
		name string
		dist int
	}
	var matches []scored
	for _, name := range candidates {
		if name == target {
			// An exact match is not a typo; never suggest replacing a name with itself.
			continue
		}
		if d := levenshtein(target, name); d <= maxDistance {
			matches = append(matches, scored{name: name, dist: d})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].dist != matches[j].dist {
			return matches[i].dist < matches[j].dist
		}
		return matches[i].name < matches[j].name
	})
	out := make([]string, 0, maxSuggestions)
	for _, m := range matches {
		out = append(out, m.name)
		if len(out) == maxSuggestions {
			break
		}
	}
	return out
}

// levenshtein returns the edit distance between a and b using a rolling row. It
// is a local copy of the twin in internal/server/flakeactions.go (the follows
// did-you-mean fix); this package cannot import server without an import cycle.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr := make([]int, len(br)+1)
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = minInt(minInt(prev[j]+1, curr[j-1]+1), prev[j-1]+cost)
		}
		prev = curr
	}
	return prev[len(br)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sortByRange orders diagnostics by their flagged range's start position for
// deterministic output.
func sortByRange(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		a, b := diagnostics[i].Range, diagnostics[j].Range
		if a.Start.Line != b.Start.Line {
			return a.Start.Line < b.Start.Line
		}
		return a.Start.Character < b.Start.Character
	})
}

// unwrap descends through parenthesized expressions and let-in bodies to the
// wrapped expression, the transparent hops the option walk sees through. A let
// binding list is opaque (never a value shape), so a let is unwrapped only to its
// body. It stops at the first non-wrapper node.
func unwrap(node syntax.Node) syntax.Node {
	for i := 0; i < maxUnwrap; i++ {
		switch node.Kind() {
		case "parenthesized_expression":
			next := node.ChildByFieldName("expression")
			if next.IsZero() {
				return node
			}
			node = next
		case "let_expression":
			body := node.ChildByFieldName("body")
			if body.IsZero() {
				return node
			}
			node = body
		default:
			return node
		}
	}
	return node
}

// attrsetBindingSet returns the binding_set of value when value, after
// unwrapping, is an attribute set (plain or rec), else a zero node.
func attrsetBindingSet(value syntax.Node) syntax.Node {
	node := unwrap(value)
	switch node.Kind() {
	case "attrset_expression", "rec_attrset_expression":
		for _, child := range node.NamedChildren() {
			if child.Kind() == "binding_set" {
				return child
			}
		}
	}
	return syntax.Node{}
}

// segmentText returns the static text of a single attrpath segment: an identifier
// yields its text, a non-interpolated string yields its unquoted content, and any
// dynamic or escaped form yields ok=false. It mirrors scopes.segmentValue so the
// option walk reads paths exactly as option hover does.
func segmentText(seg syntax.Node) (string, bool) {
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
