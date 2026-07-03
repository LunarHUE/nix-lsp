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

// Parse parses Nix source into a syntax tree with a background context, so the
// parse always runs to completion. Call sites on a cancellable compute path use
// ParseCtx instead, so a parse superseded by a newer edit can be abandoned
// mid-flight rather than burning CPU to completion.
func Parse(content []byte) (*Tree, error) {
	return ParseCtx(context.Background(), content)
}

// ParseCtx parses Nix source into a syntax tree, honoring ctx. If ctx is
// cancelled while the parse is in progress, tree-sitter halts and returns a nil
// tree; ParseCtx maps that to a wrapped context error (errors.Is(err,
// context.Canceled) holds) and never returns a partial or nil tree presented as
// success — a non-nil *Tree always came from a completed parse. Callers route
// the cancellation error to a quiet early-out, not a broken-file diagnostic.
func ParseCtx(ctx context.Context, content []byte) (*Tree, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(nixLanguage())

	copied := cloneBytes(content)
	tree, err := parser.ParseCtx(ctx, nil, copied)
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
	var preciseAnchors []Position
	t.Walk(func(node Node) bool {
		if node.IsMissing() {
			// A named MISSING node (an identifier the grammar demanded, as in
			// `{ x = ;}`): its kind is exactly the expected token.
			diagnostics = append(diagnostics, Diagnostic{
				Message: "syntax error: expected " + node.Kind(),
				Range:   node.Range(),
				Code:    "missing-syntax",
			})
			preciseAnchors = append(preciseAnchors, node.Range().Start)
			return true
		}
		if node.Kind() == "ERROR" {
			d := classifyError(node)
			diagnostics = append(diagnostics, d)
			if d.Message != genericSyntaxError {
				preciseAnchors = append(preciseAnchors, d.Range.Start)
			}
		}
		// Walk visits only named nodes, but the parser records a demanded token
		// that was never typed as an anonymous zero-width MISSING token — the `;`
		// of a set's last binding (`{ x = 1 }`), the `}` of an unclosed attrset
		// (`{ x = 1;`), the `]` of an unclosed list, the `)` of unclosed parens.
		// A MISSING token's kind IS the expected token, so each renders as a
		// precise expected-token diagnostic with zero guesswork.
		for _, missing := range missingAnonymousTokens(node) {
			message := "syntax error: expected '" + missing.Kind() + "'"
			if missing.Kind() == ";" && isBindingEntry(node.Kind()) {
				message = missingSemicolonMessage
			}
			diagnostics = append(diagnostics, Diagnostic{
				Message: message,
				Range:   missing.Range(),
				Code:    "syntax-error",
			})
			preciseAnchors = append(preciseAnchors, missing.Range().Start)
		}
		return true
	})
	return dropShadowedGenerics(diagnostics, preciseAnchors)
}

// genericSyntaxError is the unclassified parse-error message; every classifier
// below falls back to it, and dedupe only ever drops diagnostics carrying it.
const genericSyntaxError = "syntax error"

// missingSemicolonMessage is the classified hint for a binding (or inherit)
// whose terminating semicolon is absent, shared by the MISSING-token and the
// ERROR-recovery detections so both spellings of the mistake read identically.
const missingSemicolonMessage = "syntax error: missing ';' after binding"

// isBindingEntry reports whether kind is a binding-set entry terminated by `;`,
// where a MISSING ";" reads better as the classified missing-semicolon hint.
func isBindingEntry(kind string) bool {
	switch kind {
	case "binding", "inherit", "inherit_from":
		return true
	default:
		return false
	}
}

// missingAnonymousTokens returns node's anonymous MISSING token children,
// scanning all children because unnamed punctuation is invisible to the
// named-only Walk.
func missingAnonymousTokens(node Node) []Node {
	var missing []Node
	for _, child := range rawChildren(node) {
		if child.node != nil && child.node.IsMissing() && !child.node.IsNamed() {
			missing = append(missing, child)
		}
	}
	return missing
}

// rawChildren returns all children of node, named and anonymous alike.
func rawChildren(node Node) []Node {
	if node.node == nil {
		return nil
	}
	node.nav.Lock()
	count := int(node.node.ChildCount())
	children := make([]Node, 0, count)
	for i := 0; i < count; i++ {
		children = append(children, wrapNode(node.node.Child(i), node.content, node.nav))
	}
	node.nav.Unlock()
	return children
}

// dropShadowedGenerics removes generic (unclassified) syntax-error diagnostics
// whose range covers the anchor of a precise diagnostic: a broad ERROR region
// and the exact expected-token (or classified) diagnostic inside it report the
// same problem, and the precise one says what is actually wrong. Classified
// messages are never dropped.
func dropShadowedGenerics(diagnostics []Diagnostic, preciseAnchors []Position) []Diagnostic {
	if len(preciseAnchors) == 0 {
		return diagnostics
	}
	kept := diagnostics[:0]
	for _, d := range diagnostics {
		if d.Message == genericSyntaxError && rangeCoversAny(d.Range, preciseAnchors) {
			continue
		}
		kept = append(kept, d)
	}
	return kept
}

// rangeCoversAny reports whether r contains any of the positions, inclusive at
// both ends since precise anchors are zero-width and may sit on a region's edge.
func rangeCoversAny(r Range, positions []Position) bool {
	for _, p := range positions {
		afterStart := p.Line > r.Start.Line || (p.Line == r.Start.Line && p.Character >= r.Start.Character)
		beforeEnd := p.Line < r.End.Line || (p.Line == r.End.Line && p.Character <= r.End.Character)
		if afterStart && beforeEnd {
			return true
		}
	}
	return false
}

// classifyError renders a diagnostic for an ERROR node, enriching the message
// (and, for the swallowed-binding recovery, re-anchoring the range to where the
// missing token belongs) when the shape is a recognizable mid-edit mistake, and
// falling back to the generic message at the ERROR's own range otherwise. It
// only rewords and re-anchors an ERROR the parser already reported; it never
// changes whether a diagnostic exists.
func classifyError(node Node) Diagnostic {
	d := Diagnostic{Message: genericSyntaxError, Range: node.Range(), Code: "syntax-error"}
	if name, ok := loneAttributeName(node); ok {
		d.Message = "syntax error: attribute '" + name + "' has no value (expected '" + name + " = <value>;')"
		return d
	}
	if swallowed, ok := swallowedBindingDiagnostic(node); ok {
		return swallowed
	}
	if isMissingSeparator(node) || isStrayCloseAfterValue(node) {
		d.Message = missingSemicolonMessage
		return d
	}
	if open, close, ok := unclosedDelimiter(node); ok {
		d.Message = "syntax error: unclosed '" + open + "' (expected '" + close + "')"
		return d
	}
	return d
}

// swallowedBindingDiagnostic classifies the recovery for a `;` deleted between
// two bindings when the parser swallows the next binding's name into the first
// binding's value: `pkgs = import nixpkgs { ... } corePackages = with pkgs; ...`
// becomes an apply chain `(value) corePackages` followed by an ERROR starting
// with the orphaned `=`. The diagnostic re-anchors zero-width at the end of the
// real value — where the `;` belongs — and names the swallowed identifier when
// it is provably complete: "missing ';' before 'corePackages'". An unprovable
// name keeps the unnamed missing-semicolon message at the same corrected anchor.
func swallowedBindingDiagnostic(errNode Node) (Diagnostic, bool) {
	raw := rawChildren(errNode)
	if len(raw) == 0 || raw[0].node == nil || raw[0].node.IsNamed() || raw[0].Kind() != "=" {
		return Diagnostic{}, false
	}
	parent := errNode.Parent()
	if parent.Kind() != "apply_expression" {
		return Diagnostic{}, false
	}
	// The apply chain left of the orphaned `=` holds the previous binding's real
	// value and, as its final application argument, the swallowed name.
	siblings := parent.NamedChildren()
	idx := -1
	errRange := errNode.Range()
	for i, sib := range siblings {
		if sib.Range() == errRange && sib.Kind() == "ERROR" {
			idx = i
			break
		}
	}
	if idx < 1 {
		return Diagnostic{}, false
	}
	chain := siblings[idx-1]
	if chain.Kind() != "apply_expression" {
		return Diagnostic{}, false
	}
	inner := chain.NamedChildren()
	if len(inner) < 2 {
		return Diagnostic{}, false
	}
	value, swallowed := inner[len(inner)-2], inner[len(inner)-1]

	message := missingSemicolonMessage
	if name, ok := swallowedName(swallowed); ok {
		message = "syntax error: missing ';' before '" + name + "'"
	}
	anchor := value.Range().End
	return Diagnostic{
		Message: message,
		Range:   Range{Start: anchor, End: anchor},
		Code:    "syntax-error",
	}, true
}

// swallowedName returns the provably complete first identifier of a swallowed
// binding name: the identifier of a bare variable, or the base identifier of a
// multi-segment path (`b` of `b.c`, the token the missing `;` sits before).
func swallowedName(node Node) (string, bool) {
	switch node.Kind() {
	case "variable_expression":
		return fullIdentifierText(node.ChildByFieldName("name"))
	case "select_expression":
		base := node.ChildByFieldName("expression")
		if base.Kind() != "variable_expression" {
			return "", false
		}
		return fullIdentifierText(base.ChildByFieldName("name"))
	default:
		return "", false
	}
}

// unclosedDelimiter reports whether an ERROR node is the recovery for an opening
// delimiter with nothing usable after it (`{`, `[`, `(`, or `"` at the end of
// input): the ERROR wraps exactly that one anonymous token. It returns the
// opening token and its expected closer.
func unclosedDelimiter(errNode Node) (open, close string, ok bool) {
	raw := rawChildren(errNode)
	if len(raw) != 1 || raw[0].node == nil || raw[0].node.IsNamed() || raw[0].IsMissing() {
		return "", "", false
	}
	switch raw[0].Kind() {
	case "{":
		return "{", "}", true
	case "[":
		return "[", "]", true
	case "(":
		return "(", ")", true
	case "\"":
		return "\"", "\"", true
	default:
		return "", "", false
	}
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
