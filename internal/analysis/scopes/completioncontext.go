package scopes

import (
	"sort"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// completioncontext.go holds the pure CST helper that classifies what kind of
// completion applies at a cursor position, plus the already-typed prefix. It is
// the completion-side sibling of optionpath.go / pkgpath.go and reuses their
// private helpers (segmentValue, staticSegmentsUpTo, staticAttrpathSegments,
// assembleBindingPath, enclosingBinding).
//
// Unlike the hover helpers it must cope with mid-edit, syntactically broken
// input: a trailing dot (`pkgs.`, `networking.`) leaves the buffer unparseable,
// so tree-sitter-nix produces one of a few characteristic broken shapes. The two
// that matter here:
//
//   - A select chain in value position with a trailing dot parses the good
//     prefix as a select_expression (or bare variable_expression) and drops the
//     lone `.` into a sibling ERROR node: `x = pkgs.` -> variable_expression
//     `pkgs` + ERROR ".".
//
//   - A binding attribute path being typed inside `{ ... }` parses as an ERROR
//     wrapping either the attrpath (`{ services.openssh.e }`) or a bare sequence
//     of identifiers when the trailing dot defeats the parser entirely
//     (`{ networking. }` -> ERROR > identifier "networking").
//
// Because a just-typed token or a trailing dot sits at a position that no node's
// half-open range contains, classification is driven from the raw text left of
// the cursor (the partially typed segment and whether a `.` precedes it) and then
// anchored back onto the CST to reconstruct the completed path and to bail on
// dynamic, string, or comment contexts.

// CompletionKind classifies what should be completed at a position.
type CompletionKind int

const (
	// CompletionNone is the zero value; it never accompanies ok=true.
	CompletionNone CompletionKind = iota
	// OptionPath completes a NixOS option attribute path (config stripped).
	OptionPath
	// PkgAttr completes an attribute of the nixpkgs package set.
	PkgAttr
	// WithPkgsName completes a bare nixpkgs attribute name under `with pkgs;`.
	WithPkgsName
	// LocalName completes a lexically visible binding name.
	LocalName
)

// String returns a stable label for a completion kind, used in tests.
func (k CompletionKind) String() string {
	switch k {
	case OptionPath:
		return "OptionPath"
	case PkgAttr:
		return "PkgAttr"
	case WithPkgsName:
		return "WithPkgsName"
	case LocalName:
		return "LocalName"
	default:
		return "None"
	}
}

// CompletionContext describes the completion applicable at a position.
type CompletionContext struct {
	// Kind is which completion applies.
	Kind CompletionKind
	// Prefix holds the completed segments before the one being typed. For
	// OptionPath it is the option path so far (leading "config" stripped); for
	// PkgAttr it is the segments after `pkgs`. It is nil for the bare-name kinds.
	Prefix []string
	// Partial is the partially typed segment under/before the cursor, "" right
	// after a dot.
	Partial string
	// Replace is the range the completion item should replace: the partial
	// segment, or a zero-width range at the cursor when Partial is "".
	Replace syntax.Range
}

// CompletionContextAt classifies the completion applicable at pos in a
// possibly mid-edit file. It returns ok=false in strings and comments (outside
// interpolations), on dynamic attribute paths, and anywhere no completion
// applies.
func CompletionContextAt(file *File, tree *syntax.Tree, pos syntax.Position) (CompletionContext, bool) {
	if tree == nil {
		return CompletionContext{}, false
	}
	content := tree.Content()
	off := offsetAt(content, pos)

	// Strings and comments (but not interpolations) never take completion.
	if inStringOrComment(tree, pos) {
		return CompletionContext{}, false
	}

	// The partially typed segment is the identifier run ending at the cursor.
	segStart := off
	for segStart > 0 && isIdentContinue(content[segStart-1]) {
		segStart--
	}
	partial := string(content[segStart:off])
	replace := syntax.Range{Start: syntax.PositionAt(content, segStart), End: pos}

	// A `.` immediately before the segment means an attribute-path continuation.
	if segStart > 0 && content[segStart-1] == '.' {
		return dotCompletion(tree, content, segStart-1, partial, replace)
	}
	return bareCompletion(file, tree, content, pos, partial, replace)
}

// dotCompletion classifies a path continuation whose triggering `.` begins at
// byte offset dotByte. It reconstructs the chain to the left of the dot from the
// CST and classifies it as a package or option path.
func dotCompletion(tree *syntax.Tree, content []byte, dotByte int, partial string, replace syntax.Range) (CompletionContext, bool) {
	dotPos := syntax.PositionAt(content, dotByte)
	chain := chainBeforeDot(tree, dotPos)
	if chain.IsZero() {
		return CompletionContext{}, false
	}

	switch chain.Kind() {
	case "select_expression":
		return classifySelect(chain, partial, replace)
	case "variable_expression":
		return classifyBase(baseName(chain), nil, partial, replace)
	}

	parent := chain.Parent()
	switch parent.Kind() {
	case "attrpath":
		return classifyAttrpathSegment(parent, chain, partial, replace)
	case "ERROR":
		// The broken binding path left of the dot survives inside the ERROR as a
		// run of identifier / string segments, or — two or more segments deep
		// when the enclosing attrset still parses — as a whole attrpath node
		// whose trailing dot became a separate sibling ERROR.
		switch chain.Kind() {
		case "identifier", "string_expression", "attrpath":
			return classifyFlattened(parent, chain, content, partial, replace)
		}
	}
	return CompletionContext{}, false
}

// classifySelect handles a complete select_expression sitting immediately left of
// the trailing dot (`pkgs.foo.` / `config.a.`). Its whole attrpath is the
// completed prefix.
func classifySelect(sel syntax.Node, partial string, replace syntax.Range) (CompletionContext, bool) {
	base := sel.ChildByFieldName("expression")
	if base.Kind() != "variable_expression" {
		return CompletionContext{}, false
	}
	segs, ok := staticAttrpathSegments(sel.ChildByFieldName("attrpath"))
	if !ok {
		return CompletionContext{}, false
	}
	return classifyBase(base.Text(), segs, partial, replace)
}

// classifyAttrpathSegment handles a chain whose parent is an attrpath: the cursor
// segment (chain) is one entry, and the prefix runs from the attrpath start up to
// and including it. The attrpath's own parent decides package vs option context.
func classifyAttrpathSegment(attrpath, chain syntax.Node, partial string, replace syntax.Range) (CompletionContext, bool) {
	idx := indexOfChild(attrpath, chain)
	if idx < 0 {
		return CompletionContext{}, false
	}
	segs, ok := staticSegmentsUpTo(attrpath, idx)
	if !ok {
		return CompletionContext{}, false
	}

	owner := attrpath.Parent()
	switch owner.Kind() {
	case "select_expression":
		base := owner.ChildByFieldName("expression")
		if base.Kind() != "variable_expression" {
			return CompletionContext{}, false
		}
		return classifyBase(base.Text(), segs, partial, replace)
	case "binding":
		assembled, ok := assembleBindingPath(owner, attrpath, idx)
		if !ok {
			return CompletionContext{}, false
		}
		return optionContext(assembled, partial, replace)
	case "ERROR":
		prefix, ok := prependEnclosingBindings(segs, owner.Parent())
		if !ok {
			return CompletionContext{}, false
		}
		return optionContext(prefix, partial, replace)
	}
	return CompletionContext{}, false
}

// classifyFlattened handles the broken shapes where a trailing dot defeated the
// parser and left the typed path inside an ERROR: a bare identifier sequence
// (`{ networking.firewall. }`), a surviving attrpath node (`{ config, ... }:
// { networking.firewall. }`), or a flattened mix in which enclosing bindings
// appear as earlier siblings (`{ networking = { firewall. }; }` -> ERROR
// [attrpath "networking", identifier "firewall"]). It gathers the contiguous
// run ending at chain, crossing `= {` binding hops, then prepends any bindings
// that survived outside the ERROR.
func classifyFlattened(errNode, chain syntax.Node, content []byte, partial string, replace syntax.Range) (CompletionContext, bool) {
	segs, ok := gatherFlatSegments(errNode, chain, content)
	if !ok {
		return CompletionContext{}, false
	}
	prefix, ok := prependEnclosingBindings(segs, errNode.Parent())
	if !ok {
		return CompletionContext{}, false
	}
	return optionContext(prefix, partial, replace)
}

// classifyBase turns a select/variable base name plus its completed segments into
// a package or option context. Only `pkgs` and `config` bases are recognized.
func classifyBase(name string, segs []string, partial string, replace syntax.Range) (CompletionContext, bool) {
	switch name {
	case "pkgs":
		return CompletionContext{Kind: PkgAttr, Prefix: segs, Partial: partial, Replace: replace}, true
	case "config":
		return optionContext(segs, partial, replace)
	}
	return CompletionContext{}, false
}

// optionContext builds an OptionPath context, stripping a single leading
// "config" segment as the hover side does.
func optionContext(prefix []string, partial string, replace syntax.Range) (CompletionContext, bool) {
	if len(prefix) > 0 && prefix[0] == "config" {
		prefix = prefix[1:]
	}
	return CompletionContext{Kind: OptionPath, Prefix: prefix, Partial: partial, Replace: replace}, true
}

// bareCompletion classifies a bare identifier (no preceding dot) in expression
// position: a `with pkgs;`-supplied name or an ordinary visible-binding name.
// With no identifier at all it can still classify an empty attrset body in
// option-binding position (emptyAttrsetOptionContext) or an empty with-pkgs
// list slot.
func bareCompletion(file *File, tree *syntax.Tree, content []byte, pos syntax.Position, partial string, replace syntax.Range) (CompletionContext, bool) {
	node := deepestTouching(tree, pos)
	if node.IsZero() {
		return CompletionContext{}, false
	}

	// Locate the variable_expression this identifier is a use of, if any.
	varExpr := node
	if varExpr.Kind() == "identifier" {
		varExpr = varExpr.Parent()
	}
	if varExpr.Kind() != "variable_expression" {
		// No identifier under the cursor. An empty attrset body in binding
		// position offers its option path's children; otherwise only an empty
		// expression slot inside a list may still take a (bare, empty) with-pkgs
		// completion.
		if partial == "" {
			if cctx, ok := emptyAttrsetOptionContext(node, pos, replace); ok {
				return cctx, true
			}
			if enclosingWithPkgs(node, pos) && inListPosition(node) {
				return CompletionContext{Kind: WithPkgsName, Partial: "", Replace: replace}, true
			}
		}
		return CompletionContext{}, false
	}

	// A name that resolves lexically or to a builtin is never a nixpkgs attr; it
	// is a local name. Resolve from the token start so an end-of-token cursor
	// still finds the reference (half-open ranges exclude the end).
	resolvedLocal := false
	if file != nil {
		if ref := file.ReferenceAt(varExpr.Range().Start); ref != nil && ref.Target != nil {
			resolvedLocal = true
		}
	}
	if !resolvedLocal && enclosingWithPkgs(varExpr, pos) {
		return CompletionContext{Kind: WithPkgsName, Partial: partial, Replace: replace}, true
	}
	return CompletionContext{Kind: LocalName, Partial: partial, Replace: replace}, true
}

// emptyAttrsetOptionContext classifies a cursor resting inside an EMPTY (or
// whitespace-only) attribute-set body whose enclosing bindings compose an option
// path: in `wg0 = { <cursor> }` under networking.wireguard.interfaces the
// submodule's options complete with no prefix typed. node must be the attrset
// itself (it is the deepest node touching pos exactly when the body holds no
// named node, so a cursor inside any nested expression never lands here), pos
// must sit strictly between the braces, and the composed path must survive
// config-stripping with at least one segment. Anything else declines, so data
// attrsets in non-binding positions (function bodies, list elements) and every
// previously classified position keep their existing behavior.
func emptyAttrsetOptionContext(node syntax.Node, pos syntax.Position, replace syntax.Range) (CompletionContext, bool) {
	switch node.Kind() {
	case "attrset_expression", "rec_attrset_expression":
	default:
		return CompletionContext{}, false
	}
	if len(node.NamedChildren()) != 0 {
		return CompletionContext{}, false
	}
	r := node.Range()
	if !positionLess(r.Start, pos) || !positionLess(pos, r.End) {
		return CompletionContext{}, false
	}
	binding, ok := enclosingBinding(node)
	if !ok {
		return CompletionContext{}, false
	}
	attrpath := binding.ChildByFieldName("attrpath")
	segs, ok := staticAttrpathSegments(attrpath)
	if !ok || len(segs) == 0 {
		return CompletionContext{}, false
	}
	path, ok := assembleBindingPath(binding, attrpath, len(segs)-1)
	if !ok {
		return CompletionContext{}, false
	}
	cctx, ok := optionContext(path, "", replace)
	if !ok || len(cctx.Prefix) == 0 {
		return CompletionContext{}, false
	}
	return cctx, true
}

// VisibleBindings returns the bindings lexically visible at pos, innermost scope
// first. Builtins are excluded (they are synthesized on demand and never enter a
// scope's binding list).
func VisibleBindings(file *File, pos syntax.Position) []*Binding {
	if file == nil {
		return nil
	}
	var scopes []*Scope
	for _, s := range file.Scopes {
		if s.Kind == ScopeRoot {
			continue
		}
		// Inclusive at the end so a cursor at end-of-buffer (or end of a scope)
		// still sees that scope's bindings while mid-edit.
		if !positionLess(pos, s.Range.Start) && !positionLess(s.Range.End, pos) {
			scopes = append(scopes, s)
		}
	}
	// Innermost first: a scope nested inside another has a later start or, on a
	// shared start, an earlier end.
	sort.SliceStable(scopes, func(i, j int) bool {
		a, b := scopes[i].Range, scopes[j].Range
		if a.Start != b.Start {
			return positionLess(b.Start, a.Start)
		}
		return positionLess(a.End, b.End)
	})

	var bindings []*Binding
	for _, s := range scopes {
		bindings = append(bindings, s.Bindings...)
	}
	return bindings
}

// chainBeforeDot returns the outermost attribute-path / select node whose range
// ends exactly at dotPos: the expression to the left of the trailing dot.
func chainBeforeDot(tree *syntax.Tree, dotPos syntax.Position) syntax.Node {
	node := nodeEndingAt(tree, dotPos)
	if node.IsZero() {
		return node
	}
	for i := 0; i < maxUnwrap; i++ {
		parent := node.Parent()
		if parent.IsZero() || parent.Range().End != dotPos {
			break
		}
		switch parent.Kind() {
		case "identifier", "attrpath", "select_expression", "variable_expression":
			node = parent
		default:
			return node
		}
	}
	return node
}

// nodeEndingAt returns the deepest, rightmost named node whose range ends exactly
// at pos.
func nodeEndingAt(tree *syntax.Tree, pos syntax.Position) syntax.Node {
	var best syntax.Node
	var bestStart syntax.Position
	tree.Walk(func(n syntax.Node) bool {
		r := n.Range()
		if r.End != pos {
			return true
		}
		if best.IsZero() || !positionLess(r.Start, bestStart) {
			best = n
			bestStart = r.Start
		}
		return true
	})
	return best
}

// deepestTouching returns the deepest named node touching pos, preferring a node
// that strictly contains pos over one that merely ends at it.
func deepestTouching(tree *syntax.Tree, pos syntax.Position) syntax.Node {
	node := tree.Root()
	if node.IsZero() {
		return syntax.Node{}
	}
	for i := 0; i < maxUnwrap*maxUnwrap; i++ {
		var contain, endAt syntax.Node
		for _, child := range node.NamedChildren() {
			r := child.Range()
			if positionLess(pos, r.Start) || positionLess(r.End, pos) {
				continue // pos is outside [start, end]
			}
			if positionLess(pos, r.End) {
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
			return node
		}
		node = next
	}
	return node
}

// inStringOrComment reports whether pos lies inside a string or comment but not
// inside a string interpolation (where an expression is being typed).
func inStringOrComment(tree *syntax.Tree, pos syntax.Position) bool {
	node := deepestTouching(tree, pos)
	for n := node; !n.IsZero(); n = n.Parent() {
		switch n.Kind() {
		case "interpolation":
			return false
		case "comment", "string_expression", "indented_string_expression", "string_fragment":
			return true
		}
	}
	return false
}

// enclosingWithPkgs reports whether some `with` enclosing node has the bare
// identifier `pkgs` as its subject, with pos on the body side (not the subject).
func enclosingWithPkgs(node syntax.Node, pos syntax.Position) bool {
	for anc := node.Parent(); !anc.IsZero(); anc = anc.Parent() {
		if anc.Kind() != "with_expression" {
			continue
		}
		env := anc.ChildByFieldName("environment")
		if env.IsZero() || rangeContains(env.Range(), pos) {
			continue
		}
		if env.Kind() == "variable_expression" && env.Text() == "pkgs" {
			return true
		}
	}
	return false
}

// inListPosition reports whether node is (or is inside) a list expression, the
// only empty slot where a bare, empty completion is offered.
func inListPosition(node syntax.Node) bool {
	for n := node; !n.IsZero(); n = n.Parent() {
		if n.Kind() == "list_expression" {
			return true
		}
	}
	return false
}

// prependEnclosingBindings walks outward from an attribute set (the one wrapping
// the broken binding) prepending each enclosing binding's attribute path, so a
// nested module binding yields its full option path. It stops successfully at a
// function body, list, or the top level, and bails on a let binding or dynamic
// segment.
func prependEnclosingBindings(acc []string, set syntax.Node) ([]string, bool) {
	switch set.Kind() {
	case "attrset_expression", "rec_attrset_expression":
	default:
		return acc, true
	}
	cur := set
	for i := 0; i < maxUnwrap; i++ {
		outer, found := enclosingBinding(cur)
		if !found {
			return acc, true
		}
		osegs, ok := staticAttrpathSegments(outer.ChildByFieldName("attrpath"))
		if !ok {
			return nil, false
		}
		acc = append(append([]string{}, osegs...), acc...)
		container := outer.Parent()
		if container.Kind() != "binding_set" {
			return acc, true
		}
		container = container.Parent()
		switch container.Kind() {
		case "let_expression":
			return nil, false
		case "attrset_expression", "rec_attrset_expression":
			cur = container
		default:
			return acc, true
		}
	}
	return acc, true
}

// gatherFlatSegments collects the contiguous run of static segments ending at
// chain within a flattened ERROR node. Children joined by a single dot extend
// the same attribute path; children joined by `= {` (or `= rec {`) are the
// attrpaths of enclosing bindings that collapsed into the same ERROR, so their
// segments are prepended and the run continues outward. Any other separator
// stops the run; a dynamic or otherwise non-static child inside the run bails.
func gatherFlatSegments(errNode, chain syntax.Node, content []byte) ([]string, bool) {
	children := errNode.NamedChildren()
	end := indexOfChild(errNode, chain)
	if end < 0 {
		return nil, false
	}
	segs, ok := flatChildSegments(children[end])
	if !ok {
		return nil, false
	}
	for i := end; i > 0; i-- {
		prevEnd := children[i-1].Range().End
		curStart := children[i].Range().Start
		if !onlyDotBetween(content, prevEnd, curStart) &&
			!bindingOpenBetween(content, prevEnd, curStart) {
			break
		}
		prev, ok := flatChildSegments(children[i-1])
		if !ok {
			return nil, false
		}
		segs = append(prev, segs...)
	}
	return segs, true
}

// flatChildSegments returns the static path segments contributed by one child of
// a flattened ERROR: a single identifier or string segment, or a surviving
// attrpath node's full segment list. Anything else (an interpolation, formals, a
// value expression) yields false.
func flatChildSegments(child syntax.Node) ([]string, bool) {
	if child.Kind() == "attrpath" {
		return staticAttrpathSegments(child)
	}
	v, ok := segmentValue(child)
	if !ok {
		return nil, false
	}
	return []string{v}, true
}

// onlyDotBetween reports whether the source between two positions is exactly one
// `.` surrounded only by whitespace.
func onlyDotBetween(content []byte, from, to syntax.Position) bool {
	a, b := offsetAt(content, from), offsetAt(content, to)
	if a < 0 || b > len(content) || a > b {
		return false
	}
	dots := 0
	for i := a; i < b; i++ {
		switch c := content[i]; {
		case c == '.':
			dots++
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		default:
			return false
		}
	}
	return dots == 1
}

// bindingOpenBetween reports whether the source between two positions is exactly
// `= {` or `= rec {` (with arbitrary whitespace): the separator a flattened
// enclosing binding leaves between its attrpath and its nested set's first
// segment inside a shared ERROR node.
func bindingOpenBetween(content []byte, from, to syntax.Position) bool {
	a, b := offsetAt(content, from), offsetAt(content, to)
	if a < 0 || b > len(content) || a > b {
		return false
	}
	rest := skipSpace(content[a:b])
	if len(rest) == 0 || rest[0] != '=' {
		return false
	}
	rest = skipSpace(rest[1:])
	if len(rest) >= 4 && string(rest[:3]) == "rec" && isSpaceByte(rest[3]) {
		rest = skipSpace(rest[3:])
	}
	if len(rest) == 0 || rest[0] != '{' {
		return false
	}
	return len(skipSpace(rest[1:])) == 0
}

// skipSpace returns b with leading whitespace removed.
func skipSpace(b []byte) []byte {
	for len(b) > 0 && isSpaceByte(b[0]) {
		b = b[1:]
	}
	return b
}

// isSpaceByte reports whether b is an ASCII whitespace byte.
func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// baseName returns the identifier text of a variable_expression.
func baseName(varExpr syntax.Node) string {
	if name := varExpr.ChildByFieldName("name"); !name.IsZero() {
		return name.Text()
	}
	return varExpr.Text()
}

// indexOfChild returns the index of child among parent's named children, or -1.
func indexOfChild(parent, child syntax.Node) int {
	target := child.Range()
	for i, c := range parent.NamedChildren() {
		if c.Range() == target {
			return i
		}
	}
	return -1
}

// isIdentContinue reports whether b may continue a Nix identifier segment.
func isIdentContinue(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '\'' || b == '-':
		return true
	default:
		return false
	}
}

// offsetAt converts an LSP position into a UTF-8 byte offset into content, the
// inverse of syntax.PositionAt.
func offsetAt(content []byte, pos syntax.Position) int {
	cur := syntax.Position{}
	for i := 0; i < len(content); {
		if cur == pos {
			return i
		}
		if cur.Line > pos.Line || (cur.Line == pos.Line && cur.Character > pos.Character) {
			return i
		}
		r, size := utf8.DecodeRune(content[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if r == '\n' {
			cur.Line++
			cur.Character = 0
		} else {
			cur.Character += len(utf16.Encode([]rune{r}))
		}
		i += size
	}
	return len(content)
}
