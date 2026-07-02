// Package options parses the official NixOS options.json artifact into a
// segment trie and renders per-option documentation for hover. Like the rest of
// the analysis code it is a pure function of its inputs: it performs no I/O,
// no network access, and no nix execution. Data acquisition (download, brotli
// decompression, caching) is the responsibility of a separate slice.
package options

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// Doc is the documentation for one option, rendered ready for display. Default
// and Example hold Nix source text (empty when the field was absent); the
// matching IsMD flag is true when the value originated from a literalMD block
// and should be rendered as prose rather than a nix code fence.
type Doc struct {
	Loc          []string // path segments as declared, wildcards kept as "<name>" / "*"
	Description  string
	Type         string
	Default      string // Nix source text, "" if absent
	DefaultIsMD  bool   // true when default came from a literalMD (render as prose, not code)
	Example      string
	ExampleIsMD  bool
	Declarations []string
	ReadOnly     bool
}

// Index is a lookup structure over parsed options, keyed by path segment. It is
// built by Parse and queried with Lookup.
type Index struct {
	root *node
	n    int
}

// node is one level of the segment trie. A node holds a Doc only when it is the
// terminal segment of a declared option; interior nodes (option groups) do not.
type node struct {
	children map[string]*node
	doc      *Doc
}

// rawEntry mirrors the on-disk shape of a single options.json value. Fields
// whose interpretation varies (default, example, declarations) are decoded
// lazily so that malformed or unusual shapes can be tolerated per field.
type rawEntry struct {
	Description  string            `json:"description"`
	Type         string            `json:"type"`
	Default      json.RawMessage   `json:"default"`
	Example      json.RawMessage   `json:"example"`
	Declarations []json.RawMessage `json:"declarations"`
	Loc          []string          `json:"loc"`
	ReadOnly     bool              `json:"readOnly"`
}

// Parse decodes the official options.json (a JSON object keyed by dotted path)
// into an Index. Unknown fields are ignored and a malformed individual entry is
// skipped rather than failing the whole parse. It returns an error only when the
// top level is not a JSON object.
func Parse(data []byte) (*Index, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	ix := &Index{root: &node{}}
	for key, rawMsg := range raw {
		var e rawEntry
		if err := json.Unmarshal(rawMsg, &e); err != nil {
			continue
		}

		loc := e.Loc
		if len(loc) == 0 {
			loc = strings.Split(key, ".")
		}
		if len(loc) == 0 {
			continue
		}

		doc := &Doc{
			Loc:          loc,
			Description:  e.Description,
			Type:         e.Type,
			Declarations: parseDeclarations(e.Declarations),
			ReadOnly:     e.ReadOnly,
		}
		doc.Default, doc.DefaultIsMD = renderRawValue(e.Default)
		doc.Example, doc.ExampleIsMD = renderRawValue(e.Example)
		ix.insert(loc, doc)
	}
	return ix, nil
}

// insert places doc at the terminal node for path, creating interior nodes as
// needed. A path that already holds a doc is overwritten without inflating Len.
func (ix *Index) insert(path []string, doc *Doc) {
	n := ix.root
	for _, seg := range path {
		if n.children == nil {
			n.children = make(map[string]*node)
		}
		child, ok := n.children[seg]
		if !ok {
			child = &node{}
			n.children[seg] = child
		}
		n = child
	}
	if n.doc == nil {
		ix.n++
	}
	n.doc = doc
}

// Len reports the number of options held in the index.
func (ix *Index) Len() int {
	return ix.n
}

// Lookup resolves an attribute path to its Doc. At each level it tries, in
// priority order, an exact child, then a "<name>" wildcard child, then a "*"
// wildcard child, backtracking when a greedy branch dead-ends. It succeeds only
// when the final node holds a Doc, so an option group (interior node) reports
// found=false.
func (ix *Index) Lookup(path []string) (*Doc, bool) {
	if ix == nil || ix.root == nil {
		return nil, false
	}
	return lookup(ix.root, path)
}

// LookupNearest resolves an attribute path to its Doc, falling back to the
// longest strict prefix that is a documented option when the full path misses.
// This serves hovers on wildcard instance segments: [systemd services demo-web]
// matches into the trie but the <name> node holds no doc, so the fallback finds
// the systemd.services attrsOf doc instead. matched is the (sub)path that
// actually resolved, so a renderer can honestly name what is documented. It
// reports ok=false when no prefix (length >= 1) resolves.
func (ix *Index) LookupNearest(path []string) (*Doc, []string, bool) {
	for end := len(path); end >= 1; end-- {
		if doc, ok := ix.Lookup(path[:end]); ok {
			return doc, path[:end], true
		}
	}
	return nil, nil, false
}

// Child describes one completable child segment of an option group: one entry a
// dot-triggered completion offers under a resolved option path.
type Child struct {
	Name        string // concrete segment name ("firewall", "enable")
	Doc         *Doc   // non-nil when this child is itself a documented option (leaf)
	HasChildren bool   // true when deeper segments exist under it
}

// Children lists the completable child segments of the option group reached by
// descending path with the same wildcard tolerance as Lookup (exact, then
// "<name>", then "*" at each level, backtracking on a dead end). An empty path
// returns the top-level groups. The children are sorted by Name; a "<name>" or
// "*" placeholder child is never listed itself, since a placeholder is not a
// concrete completion. It returns nil when path does not reach a node or the
// reached node has no concrete children. nil-receiver safe.
func (ix *Index) Children(path []string) []Child {
	if ix == nil || ix.root == nil {
		return nil
	}
	n := descend(ix.root, path)
	if n == nil || len(n.children) == 0 {
		return nil
	}
	var out []Child
	for name, child := range n.children {
		if name == "<name>" || name == "*" {
			continue
		}
		out = append(out, Child{
			Name:        name,
			Doc:         child.doc,
			HasChildren: len(child.children) > 0,
		})
	}
	slices.SortFunc(out, func(a, b Child) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// descend walks the trie to the node at the end of path, using the same
// exact/"<name>"/"*" priority and backtracking as lookup, but succeeding on any
// reached node rather than only one that holds a Doc. It returns nil when no
// branch consumes the whole path.
func descend(n *node, path []string) *node {
	if len(path) == 0 {
		return n
	}
	seg := path[0]
	keys := []string{seg}
	if seg != "<name>" {
		keys = append(keys, "<name>")
	}
	if seg != "*" {
		keys = append(keys, "*")
	}
	for _, key := range keys {
		child, ok := n.children[key]
		if !ok {
			continue
		}
		if reached := descend(child, path[1:]); reached != nil {
			return reached
		}
	}
	return nil
}

func lookup(n *node, path []string) (*Doc, bool) {
	if len(path) == 0 {
		if n.doc != nil {
			return n.doc, true
		}
		return nil, false
	}
	seg := path[0]
	keys := []string{seg}
	if seg != "<name>" {
		keys = append(keys, "<name>")
	}
	if seg != "*" {
		keys = append(keys, "*")
	}
	for _, key := range keys {
		child, ok := n.children[key]
		if !ok {
			continue
		}
		if doc, ok := lookup(child, path[1:]); ok {
			return doc, true
		}
	}
	return nil, false
}

// renderRawValue turns a raw default/example value into display text and a flag
// marking markdown prose. A literalExpression yields its text verbatim; a
// literalMD yields its text with isMD=true; any other value (a plain JSON
// scalar, list, or object from older datasets) is rendered as compact JSON. An
// absent value yields "".
func renderRawValue(raw json.RawMessage) (text string, isMD bool) {
	if len(raw) == 0 {
		return "", false
	}
	var lit struct {
		Type string `json:"_type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &lit); err == nil {
		switch lit.Type {
		case "literalExpression":
			return lit.Text, false
		case "literalMD":
			return lit.Text, true
		}
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw), false
	}
	return buf.String(), false
}

// parseDeclarations normalizes the declarations array to plain strings. String
// entries are kept as-is; object entries contribute their "name", falling back
// to "url"; anything else is skipped.
func parseDeclarations(raw []json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	for _, r := range raw {
		var s string
		if err := json.Unmarshal(r, &s); err == nil {
			out = append(out, s)
			continue
		}
		var obj struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if err := json.Unmarshal(r, &obj); err != nil {
			continue
		}
		switch {
		case obj.Name != "":
			out = append(out, obj.Name)
		case obj.URL != "":
			out = append(out, obj.URL)
		}
	}
	return out
}
