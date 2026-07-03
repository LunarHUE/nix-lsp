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
				Message: syntaxErrorMessage(node),
				Range:   node.Range(),
				Code:    "syntax-error",
			})
			return true
		}
		// Walk visits only named nodes, but the parser records a binding whose
		// terminating `;` was never typed as an anonymous zero-width MISSING ";"
		// token — the recovery for `{ x = 1 }` and for a nested `wg0 = { ... }`
		// missing its semicolon before the closing brace. Surface exactly that
		// token as a classified diagnostic; other anonymous MISSING tokens
		// (a delimiter the parser closed on its own) stay unreported, as before.
		switch node.Kind() {
		case "binding", "inherit", "inherit_from":
			if missing, ok := missingSemicolon(node); ok {
				diagnostics = append(diagnostics, Diagnostic{
					Message: missingSemicolonMessage,
					Range:   missing.Range(),
					Code:    "syntax-error",
				})
			}
		}
		return true
	})
	return diagnostics
}

// missingSemicolonMessage is the classified hint for a binding (or inherit)
// whose terminating semicolon is absent, shared by the MISSING-token and the
// ERROR-recovery detections so both spellings of the mistake read identically.
const missingSemicolonMessage = "syntax error: missing ';' after binding"

// missingSemicolon returns the anonymous MISSING ";" token of a binding-like
// node, scanning all children because MISSING punctuation is unnamed and thus
// invisible to the named-only Walk.
func missingSemicolon(node Node) (Node, bool) {
	if node.node == nil {
		return Node{}, false
	}
	node.nav.Lock()
	count := int(node.node.ChildCount())
	children := make([]Node, 0, count)
	for i := 0; i < count; i++ {
		children = append(children, wrapNode(node.node.Child(i), node.content, node.nav))
	}
	node.nav.Unlock()
	for _, child := range children {
		if child.IsMissing() && child.Kind() == ";" {
			return child, true
		}
	}
	return Node{}, false
}

// syntaxErrorMessage returns a hint-enriched message for an ERROR node whose
// shape is a recognizable mid-edit mistake, and the plain "syntax error" for
// anything it cannot classify with certainty. It only rewords an ERROR the
// parser already reported; it never changes whether a diagnostic exists.
func syntaxErrorMessage(node Node) string {
	if name, ok := loneAttributeName(node); ok {
		return "syntax error: attribute '" + name + "' has no value (expected '" + name + " = <value>;')"
	}
	if isMissingSeparator(node) || isStrayCloseAfterValue(node) {
		return missingSemicolonMessage
	}
	return "syntax error"
}

// loneAttributeName reports the single bare attribute name an ERROR node wraps,
// the shape produced by a name written in binding position with no `= value;`.
// Two recovery shapes are recognized: a `{ name }` value, which tree-sitter turns
// into a `formals` node holding one plain formal (seen both for a whole-file
// `{ wg0 }` and for a binding value `interfaces = { wg0 }`, whose ERROR is an
// attrpath followed by that formals); and a bare name with nothing after it, whose
// ERROR has the identifier as its only child. Requiring a single plain formal
// keeps a partial function like `{ a, b }` or `{ pkgs, ... }` from matching, and
// the hint is only ever emitted with a provably complete identifier (see
// fullIdentifierText), so no recovery can put a truncated name in the message.
func loneAttributeName(errNode Node) (string, bool) {
	children := errNode.NamedChildren()
	for _, child := range children {
		if child.Kind() == "formals" {
			if name, ok := singleFormalName(child); ok {
				return name, true
			}
		}
	}
	if len(children) == 1 {
		return fullIdentifierText(children[0])
	}
	return "", false
}

// singleFormalName returns the name of a formals node holding exactly one plain
// formal: a single identifier with no default and no `...` ellipsis, which is
// indistinguishable from a lone attribute typed into an attribute set.
func singleFormalName(formals Node) (string, bool) {
	if !formals.ChildByFieldName("ellipses").IsZero() {
		return "", false
	}
	kids := formals.NamedChildren()
	if len(kids) != 1 || kids[0].Kind() != "formal" {
		return "", false
	}
	formal := kids[0]
	if !formal.ChildByFieldName("default").IsZero() {
		return "", false
	}
	return fullIdentifierText(formal.ChildByFieldName("name"))
}

// fullIdentifierText returns node's text only when node is a real (non-missing)
// identifier whose text is provably the complete token in the source: non-empty
// and not abutting further identifier characters on either side. Error recovery
// must never let a diagnostic name a truncated identifier (reporting `wg` for a
// buffer that says `wg0`), so any node that fails this proof disqualifies the
// name-bearing hint entirely.
func fullIdentifierText(node Node) (string, bool) {
	if node.node == nil || node.IsMissing() || node.Kind() != "identifier" {
		return "", false
	}
	start, end := int(node.node.StartByte()), int(node.node.EndByte())
	if start >= end || end > len(node.content) {
		return "", false
	}
	if start > 0 && isIdentifierByte(node.content[start-1]) {
		return "", false
	}
	if end < len(node.content) && isIdentifierByte(node.content[end]) {
		return "", false
	}
	return string(node.content[start:end]), true
}

// isIdentifierByte reports whether b can appear in a Nix identifier
// ([a-zA-Z_][a-zA-Z0-9_'-]*).
func isIdentifierByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '\'' || b == '-':
		return true
	default:
		return false
	}
}

// isMissingSeparator reports whether an ERROR node is the lone `=` tree-sitter
// leaves behind when a `;` is missing between two bindings (`a = 1 b = 2;`
// recovers as an apply of `1 b` followed by an orphan `=`). The `=` text plus an
// apply_expression parent pins this to the missing-separator recovery.
func isMissingSeparator(errNode Node) bool {
	if errNode.Text() != "=" {
		return false
	}
	return errNode.Parent().Kind() == "apply_expression"
}

// isStrayCloseAfterValue reports whether an ERROR node is the lone `}` recovery
// left when a binding's `;` is missing and the enclosing set's closing brace
// arrives in its place (`wg0 = { ... } };` recovers as the binding swallowing
// `};` with the stray `}` wrapped in an ERROR child of the binding).
func isStrayCloseAfterValue(errNode Node) bool {
	if errNode.Text() != "}" {
		return false
	}
	return errNode.Parent().Kind() == "binding"
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
