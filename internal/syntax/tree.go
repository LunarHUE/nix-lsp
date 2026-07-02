package syntax

import (
	"context"
	"fmt"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

// Severity classifies how serious a diagnostic is. Its zero value is
// SeverityError so diagnostics constructed without an explicit severity remain
// errors, matching the historical behavior of this package.
type Severity int

const (
	// SeverityError marks a problem that is almost certainly a mistake (syntax
	// errors, unresolved imports, duplicate or bad bindings).
	SeverityError Severity = iota
	// SeverityWarning marks a likely-but-not-certain problem (unused bindings,
	// flake files that exist but are not git-tracked).
	SeverityWarning
	// SeverityInformation marks an informational note.
	SeverityInformation
	// SeverityHint marks a subtle hint, typically rendered unobtrusively.
	SeverityHint
)

// Diagnostic is the syntax package's internal diagnostic shape. The LSP layer
// is responsible for converting it to protocol-specific diagnostics.
type Diagnostic struct {
	Message string
	Range   Range
	// Code is a stable, machine-readable identifier for the diagnostic kind
	// (e.g. "untracked-import"). An empty string means the diagnostic is
	// uncoded. Clients and code-action handlers key on it.
	Code     string
	Severity Severity
}

// Edit describes an edit for an incremental reparse. The current
// implementation accepts the shape but performs a full reparse.
type Edit struct {
	Range   Range
	NewText []byte
}

// Tree wraps a parsed Nix tree and the content it was parsed from.
//
// The underlying tree-sitter tree lazily populates an internal node cache as the
// tree is navigated, and that cache is not safe for concurrent access. Because a
// single parsed Tree is memoized and shared across concurrent consumers
// (background diagnostics plus synchronous LSP requests), every navigation call
// that can touch the cache is serialized through nav. Pure reads of a node's own
// data (kind, text, range) do not touch the cache and need no locking.
type Tree struct {
	tree    *sitter.Tree
	content []byte
	nav     *sync.Mutex
}

// Node is a lightweight syntax node wrapper. It carries the owning tree's nav
// mutex so navigation from any node stays serialized with the rest of the tree.
type Node struct {
	node    *sitter.Node
	content []byte
	nav     *sync.Mutex
}

// Parse parses Nix source into a syntax tree.
func Parse(content []byte) (*Tree, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(nixLanguage())

	copied := cloneBytes(content)
	tree, err := parser.ParseCtx(context.Background(), nil, copied)
	if err != nil {
		return nil, fmt.Errorf("parse nix: %w", err)
	}
	return &Tree{tree: tree, content: copied, nav: &sync.Mutex{}}, nil
}

// Reparse reparses content. Edits are accepted for API stability; this session
// intentionally keeps the implementation as a full reparse.
func Reparse(_ *Tree, _ []Edit, content []byte) (*Tree, error) {
	return Parse(content)
}

// Root returns the root syntax node.
func (t *Tree) Root() Node {
	if t == nil || t.tree == nil {
		return Node{}
	}
	t.nav.Lock()
	root := t.tree.RootNode()
	t.nav.Unlock()
	return wrapNode(root, t.content, t.nav)
}

// Content returns a copy of the parsed content.
func (t *Tree) Content() []byte {
	if t == nil {
		return nil
	}
	return cloneBytes(t.content)
}

// Diagnostics returns syntax diagnostics derived from ERROR and MISSING nodes.
func (t *Tree) Diagnostics() []Diagnostic {
	if t == nil {
		return nil
	}

	diagnostics := make([]Diagnostic, 0)
	t.Walk(func(node Node) bool {
		if node.IsMissing() {
			diagnostics = append(diagnostics, Diagnostic{
				Message: "missing syntax",
				Range:   node.Range(),
				Code:    "missing-syntax",
			})
			return true
		}
		if node.Kind() == "ERROR" {
			diagnostics = append(diagnostics, Diagnostic{
				Message: "syntax error",
				Range:   node.Range(),
				Code:    "syntax-error",
			})
		}
		return true
	})
	return diagnostics
}

// Walk calls fn for every node in depth-first order. Returning false skips the
// current node's children.
func (t *Tree) Walk(fn func(Node) bool) {
	if t == nil || t.tree == nil || fn == nil {
		return
	}
	t.nav.Lock()
	root := t.tree.RootNode()
	t.nav.Unlock()
	walkNode(wrapNode(root, t.content, t.nav), fn)
}

// Kind returns the tree-sitter node type.
func (n Node) Kind() string {
	if n.node == nil {
		return ""
	}
	return n.node.Type()
}

// Text returns the source text covered by this node.
func (n Node) Text() string {
	if n.node == nil {
		return ""
	}
	return n.node.Content(n.content)
}

// Range returns this node's LSP range.
func (n Node) Range() Range {
	if n.node == nil {
		return Range{}
	}
	return rangeForBytes(n.content, n.node.StartByte(), n.node.EndByte())
}

// ChildByFieldName returns a named field child.
func (n Node) ChildByFieldName(name string) Node {
	if n.node == nil {
		return Node{}
	}
	n.nav.Lock()
	child := n.node.ChildByFieldName(name)
	n.nav.Unlock()
	return wrapNode(child, n.content, n.nav)
}

// NamedChildren returns this node's named children.
func (n Node) NamedChildren() []Node {
	if n.node == nil {
		return nil
	}

	n.nav.Lock()
	defer n.nav.Unlock()
	count := int(n.node.NamedChildCount())
	children := make([]Node, 0, count)
	for i := 0; i < count; i++ {
		children = append(children, wrapNode(n.node.NamedChild(i), n.content, n.nav))
	}
	return children
}

// Parent returns the node's parent.
func (n Node) Parent() Node {
	if n.node == nil {
		return Node{}
	}
	n.nav.Lock()
	parent := n.node.Parent()
	n.nav.Unlock()
	return wrapNode(parent, n.content, n.nav)
}

// IsZero reports whether this wrapper has no underlying node.
func (n Node) IsZero() bool {
	return n.node == nil || n.node.IsNull()
}

// IsMissing reports whether this node is a tree-sitter missing node.
func (n Node) IsMissing() bool {
	return n.node != nil && n.node.IsMissing()
}

// HasError reports whether this node is or contains a syntax error.
func (n Node) HasError() bool {
	return n.node != nil && n.node.HasError()
}

// Typed wrappers used by early static analysis.
type SelectExpr struct{ Node }
type Apply struct{ Node }
type Binding struct{ Node }
type List struct{ Node }
type PathLiteral struct{ Node }

// AsSelectExpr returns a select-expression wrapper when node has that kind.
func AsSelectExpr(node Node) (SelectExpr, bool) {
	return SelectExpr{Node: node}, node.Kind() == "select_expression"
}

// AsApply returns an apply-expression wrapper when node has that kind.
func AsApply(node Node) (Apply, bool) {
	return Apply{Node: node}, node.Kind() == "apply_expression"
}

// AsBinding returns a binding wrapper when node has that kind.
func AsBinding(node Node) (Binding, bool) {
	return Binding{Node: node}, node.Kind() == "binding"
}

// AsList returns a list-expression wrapper when node has that kind.
func AsList(node Node) (List, bool) {
	return List{Node: node}, node.Kind() == "list_expression"
}

// AsPathLiteral returns a path-literal wrapper when node has that kind.
func AsPathLiteral(node Node) (PathLiteral, bool) {
	return PathLiteral{Node: node}, node.Kind() == "path_expression"
}

// Function returns the function expression of an apply expression.
func (a Apply) Function() Node {
	return a.ChildByFieldName("function")
}

// Argument returns the argument expression of an apply expression.
func (a Apply) Argument() Node {
	return a.ChildByFieldName("argument")
}

// AttrPath returns the attrpath node for a binding.
func (b Binding) AttrPath() Node {
	return b.ChildByFieldName("attrpath")
}

// Expression returns the value expression for a binding.
func (b Binding) Expression() Node {
	return b.ChildByFieldName("expression")
}

// Elements returns named list elements.
func (l List) Elements() []Node {
	return l.NamedChildren()
}

func walkNode(node Node, fn func(Node) bool) {
	if node.IsZero() {
		return
	}
	if !fn(node) {
		return
	}
	for _, child := range node.NamedChildren() {
		walkNode(child, fn)
	}
}

func wrapNode(node *sitter.Node, content []byte, nav *sync.Mutex) Node {
	if node == nil || node.IsNull() {
		return Node{content: content, nav: nav}
	}
	return Node{node: node, content: content, nav: nav}
}

func cloneBytes(content []byte) []byte {
	if len(content) == 0 {
		return nil
	}
	copied := make([]byte, len(content))
	copy(copied, content)
	return copied
}
