// Package scopes builds a lexical scope tree and symbol table for a single
// parsed Nix file. It is a pure function of the syntax tree and forms the
// groundwork for diagnostics (unused bindings, bad inherit) and LSP features
// (go-to-definition, document highlights, document symbols).
//
// The analysis is intentionally total: malformed input and tree-sitter ERROR
// nodes must never panic. Constructs that cannot be understood are skipped, and
// whatever can be resolved is still resolved.
package scopes

import "github.com/wesleybaldwin/nix-lsp/internal/syntax"

// ScopeKind classifies a lexical scope.
type ScopeKind int

const (
	// ScopeRoot is the synthetic file-level scope. It never holds bindings; it
	// exists so every real scope has a common ancestor and lookups terminate.
	ScopeRoot ScopeKind = iota
	// ScopeLet is a `let ... in` scope. Its bindings are mutually recursive and
	// visible in every binding value and in the body.
	ScopeLet
	// ScopeRecAttrs is a `rec { ... }` scope. Attribute names are visible to one
	// another's values.
	ScopeRecAttrs
	// ScopeFunction is a function parameter scope (`x:` or `{ a, b }@args:`).
	ScopeFunction
	// ScopeWith is a `with expr;` scope. It introduces an untyped dynamic
	// environment: names not otherwise resolved inside its body are uncertain
	// rather than unresolved.
	ScopeWith
)

// String returns a stable label for a scope kind, used in diagnostics and tests.
func (k ScopeKind) String() string {
	switch k {
	case ScopeRoot:
		return "Root"
	case ScopeLet:
		return "Let"
	case ScopeRecAttrs:
		return "RecAttrs"
	case ScopeFunction:
		return "Function"
	case ScopeWith:
		return "With"
	default:
		return "Unknown"
	}
}

// BindingKind classifies how a name was introduced.
type BindingKind int

const (
	// LetBinding is a `let name = value;` binding.
	LetBinding BindingKind = iota
	// RecAttr is an attribute of a `rec { ... }` set, visible as a variable.
	RecAttr
	// Param is a simple function parameter (`x:`).
	Param
	// FormalParam is a destructured formal parameter (`{ a, b ? d }:`).
	FormalParam
	// AtPattern is the `@`-bound name of a formal set (`{ a }@args:`).
	AtPattern
	// InheritEntry is a name introduced by `inherit ...;` in a let or rec set.
	InheritEntry
	// AttrBinding is a plain attribute-set key. It is not a variable binding; it
	// is recorded only for document-symbol style consumers.
	AttrBinding
	// Builtin is a Nix global (builtins, import, true, map, ...). Builtin
	// bindings are synthesized on demand and shared by all references to a name.
	Builtin
)

// String returns a stable label for a binding kind, used in tests.
func (k BindingKind) String() string {
	switch k {
	case LetBinding:
		return "LetBinding"
	case RecAttr:
		return "RecAttr"
	case Param:
		return "Param"
	case FormalParam:
		return "FormalParam"
	case AtPattern:
		return "AtPattern"
	case InheritEntry:
		return "InheritEntry"
	case AttrBinding:
		return "AttrBinding"
	case Builtin:
		return "Builtin"
	default:
		return "Unknown"
	}
}

// Scope is one node of the lexical scope tree.
type Scope struct {
	Kind   ScopeKind
	Parent *Scope
	Range  syntax.Range
	// Bindings are the names this scope introduces into lexical scope. Plain
	// attribute-set keys are not listed here; they are recorded on File only.
	Bindings []*Binding
}

// Binding is a named definition site.
type Binding struct {
	// Name is the identifier introduced into scope. For an attribute path it is
	// the first path segment (`a` for `a.b.c = ...`).
	Name string
	// NameRange is the range of Name in the source.
	NameRange syntax.Range
	// AttrPath is the full attribute-path text for attribute bindings
	// (`a.b.c`); it equals Name for plain identifiers.
	AttrPath string
	// Kind classifies how the name was introduced.
	Kind BindingKind
	// DefScope is the scope the binding belongs to (nil for builtins' callers
	// need not care). For attribute bindings it is the enclosing lexical scope.
	DefScope *Scope
	// Dynamic reports a computed key (`${expr} = ...`). Dynamic bindings are
	// never resolved against.
	Dynamic bool

	refs []*Reference
}

// References returns every resolved reference to this binding.
func (b *Binding) References() []*Reference {
	if b == nil {
		return nil
	}
	return b.refs
}

// Unused reports whether a variable binding has no references. Attribute
// bindings and builtins are never considered unused.
func (b *Binding) Unused() bool {
	return b != nil && b.isVariable() && len(b.refs) == 0
}

// isVariable reports whether the binding participates in variable resolution
// and is therefore a candidate for unused-variable diagnostics.
func (b *Binding) isVariable() bool {
	switch b.Kind {
	case LetBinding, RecAttr, Param, FormalParam, AtPattern, InheritEntry:
		return true
	default:
		return false
	}
}

// Reference is a use of an identifier as an expression.
type Reference struct {
	Name  string
	Range syntax.Range
	// Target is the resolved binding, or nil if the name could not be resolved.
	Target *Binding
	// WithUncertain reports that the name was unresolved lexically but sits
	// inside one or more `with` scopes, so it may be provided dynamically.
	WithUncertain bool
	// FromInherit reports that this reference is the implied outer reference of a
	// bare `inherit name;` entry (shorthand for `name = name;`), rather than an
	// ordinary variable use. A bad-inherit diagnostic keys on this so it flags
	// only inherit sources, not every unresolved identifier.
	FromInherit bool
}

// File is the analysis result for one parsed file.
type File struct {
	Root       *Scope
	Scopes     []*Scope
	Bindings   []*Binding
	References []*Reference

	builtins map[string]*Binding
}

// BindingAt returns the binding whose name spans pos, or nil.
func (f *File) BindingAt(pos syntax.Position) *Binding {
	if f == nil {
		return nil
	}
	for _, b := range f.Bindings {
		if rangeContains(b.NameRange, pos) {
			return b
		}
	}
	return nil
}

// ReferenceAt returns the reference spanning pos, or nil.
func (f *File) ReferenceAt(pos syntax.Position) *Reference {
	if f == nil {
		return nil
	}
	for _, r := range f.References {
		if rangeContains(r.Range, pos) {
			return r
		}
	}
	return nil
}

// ReferencesTo returns every reference resolved to b.
func (f *File) ReferencesTo(b *Binding) []*Reference {
	if b == nil {
		return nil
	}
	return b.refs
}

// UnusedBindings returns every variable binding that has no references. It is
// the raw input for an unused-binding diagnostic; the caller decides policy
// (for example, ignoring names that begin with an underscore).
func (f *File) UnusedBindings() []*Binding {
	if f == nil {
		return nil
	}
	var unused []*Binding
	for _, b := range f.Bindings {
		if b.Unused() {
			unused = append(unused, b)
		}
	}
	return unused
}

// analyzer carries mutable state during a single Analyze call.
type analyzer struct {
	file *File
}

// Analyze builds the scope tree and symbol table for a parsed file. It returns
// a non-nil *File even for a nil or empty tree.
func Analyze(tree *syntax.Tree) *File {
	file := &File{builtins: make(map[string]*Binding)}
	root := &Scope{Kind: ScopeRoot}
	file.Root = root
	file.Scopes = append(file.Scopes, root)

	if tree == nil {
		return file
	}
	rootNode := tree.Root()
	root.Range = rootNode.Range()

	a := &analyzer{file: file}
	// The source_code node wraps a single top-level expression. Walk each of its
	// named children so ERROR wrappers at the top level degrade gracefully.
	for _, child := range rootNode.NamedChildren() {
		a.walk(child, root)
	}
	return file
}

// walk analyzes an expression node under the lexical environment scope.
func (a *analyzer) walk(node syntax.Node, scope *Scope) {
	if node.IsZero() {
		return
	}

	switch node.Kind() {
	case "variable_expression":
		a.recordReference(node, scope)
		return
	case "let_expression":
		a.walkBindingContainer(node, scope, ScopeLet)
		return
	case "rec_attrset_expression":
		a.walkBindingContainer(node, scope, ScopeRecAttrs)
		return
	case "attrset_expression":
		a.walkAttrset(node, scope)
		return
	case "function_expression":
		a.walkFunction(node, scope)
		return
	case "with_expression":
		a.walkWith(node, scope)
		return
	}

	// Default: recurse into children with the same scope. This covers ordinary
	// expressions as well as ERROR nodes, so malformed input still resolves what
	// it can.
	for _, child := range node.NamedChildren() {
		a.walk(child, scope)
	}
}

// walkWith handles `with environment; body`.
func (a *analyzer) walkWith(node syntax.Node, scope *Scope) {
	// The environment is evaluated in the outer scope, before the with takes
	// effect.
	a.walk(node.ChildByFieldName("environment"), scope)

	withScope := a.newScope(ScopeWith, scope, node.Range())
	a.walk(node.ChildByFieldName("body"), withScope)
}

// walkFunction handles `x: body`, `{ a, b ? d }: body`, and `@`-patterns.
func (a *analyzer) walkFunction(node syntax.Node, scope *Scope) {
	fnScope := a.newScope(ScopeFunction, scope, node.Range())

	universal := node.ChildByFieldName("universal")
	formals := node.ChildByFieldName("formals")

	var defaults []syntax.Node
	if !formals.IsZero() {
		// A formal set. The `universal` name, if any, is the @-pattern binding.
		for _, formal := range formals.NamedChildren() {
			if formal.Kind() != "formal" {
				continue
			}
			name := formal.ChildByFieldName("name")
			if !name.IsZero() {
				a.define(fnScope, FormalParam, name)
			}
			if def := formal.ChildByFieldName("default"); !def.IsZero() {
				defaults = append(defaults, def)
			}
		}
		if !universal.IsZero() {
			a.define(fnScope, AtPattern, universal)
		}
	} else if !universal.IsZero() {
		// A simple parameter (`x:`).
		a.define(fnScope, Param, universal)
	}

	// Defaults are evaluated in the parameter scope, so they may reference
	// sibling formals and the @-pattern name. Bindings are already in place.
	for _, def := range defaults {
		a.walk(def, fnScope)
	}
	a.walk(node.ChildByFieldName("body"), fnScope)
}

// walkBindingContainer handles let and rec attribute sets, whose names are
// visible as variables in every value.
func (a *analyzer) walkBindingContainer(node syntax.Node, scope *Scope, kind ScopeKind) {
	newScope := a.newScope(kind, scope, node.Range())
	set := childByKind(node, "binding_set")
	bindingKind := LetBinding
	if kind == ScopeRecAttrs {
		bindingKind = RecAttr
	}
	// The value scope and definition scope are the new scope: names are mutually
	// recursive. Inherit sources refer to the parent scope.
	a.walkBindingSet(set, newScope, newScope, scope, bindingKind)
	a.walk(node.ChildByFieldName("body"), newScope)
}

// walkAttrset handles a plain `{ ... }` set. Keys are attribute bindings, not
// variables, and values are evaluated in the enclosing scope.
func (a *analyzer) walkAttrset(node syntax.Node, scope *Scope) {
	set := childByKind(node, "binding_set")
	// defScope == valueScope == inheritScope == the enclosing scope: nothing new
	// enters lexical scope.
	a.walkBindingSet(set, scope, scope, scope, AttrBinding)
}

// walkBindingSet processes the entries of a binding_set.
//
//	defScope     - scope that receives variable bindings (nil-safe)
//	valueScope   - scope used to resolve binding values
//	inheritScope - scope used to resolve bare `inherit name;` sources
//	kind         - binding kind for plain `name = value;` entries
//
// It runs in two passes so that forward references within a recursive scope
// resolve: every name is defined before any value is walked.
func (a *analyzer) walkBindingSet(set syntax.Node, defScope, valueScope, inheritScope *Scope, kind BindingKind) {
	if set.IsZero() {
		return
	}
	entries := set.NamedChildren()

	// Pass 1: define every name.
	type deferred struct {
		node syntax.Node
		kind string
	}
	var later []deferred
	for _, entry := range entries {
		switch entry.Kind() {
		case "binding":
			a.definePlainBinding(entry, defScope, kind)
			later = append(later, deferred{entry, "binding"})
		case "inherit":
			a.defineInherit(entry, defScope, kind)
			later = append(later, deferred{entry, "inherit"})
		case "inherit_from":
			a.defineInherit(entry, defScope, kind)
			later = append(later, deferred{entry, "inherit_from"})
		}
	}

	// Pass 2: walk values and inherit sources now that all names exist.
	for _, d := range later {
		switch d.kind {
		case "binding":
			a.walkPlainBindingValue(d.node, valueScope)
		case "inherit":
			a.walkBareInherit(d.node, inheritScope)
		case "inherit_from":
			a.walkInheritFrom(d.node, valueScope)
		}
	}
}

// definePlainBinding records a `name = value;` (or `a.b.c = value;`) binding.
func (a *analyzer) definePlainBinding(entry syntax.Node, defScope *Scope, kind BindingKind) {
	attrpath := entry.ChildByFieldName("attrpath")
	attrs := attrpath.NamedChildren()
	if len(attrs) == 0 {
		return
	}
	first := attrs[0]
	dynamic := first.Kind() != "identifier"

	b := &Binding{
		Name:      first.Text(),
		NameRange: first.Range(),
		AttrPath:  attrpath.Text(),
		Kind:      kind,
		DefScope:  defScope,
		Dynamic:   dynamic,
	}
	a.file.Bindings = append(a.file.Bindings, b)
	// Only static names enter variable scope, and only for recursive scopes.
	if !dynamic && kind != AttrBinding && defScope != nil {
		defScope.Bindings = append(defScope.Bindings, b)
	}
}

// walkPlainBindingValue resolves the value expression and any interpolated key.
func (a *analyzer) walkPlainBindingValue(entry syntax.Node, valueScope *Scope) {
	// Dynamic keys such as `${x}` contain their own references.
	attrpath := entry.ChildByFieldName("attrpath")
	for _, attr := range attrpath.NamedChildren() {
		if attr.Kind() != "identifier" {
			a.walk(attr, valueScope)
		}
	}
	a.walk(entry.ChildByFieldName("expression"), valueScope)
}

// defineInherit records the names of `inherit a b;` or `inherit (e) a b;`.
func (a *analyzer) defineInherit(entry syntax.Node, defScope *Scope, kind BindingKind) {
	bindingKind := InheritEntry
	if kind == AttrBinding {
		bindingKind = AttrBinding
	}
	attrs := entry.ChildByFieldName("attrs")
	for _, attr := range attrs.NamedChildren() {
		if attr.Kind() != "identifier" {
			continue
		}
		b := &Binding{
			Name:      attr.Text(),
			NameRange: attr.Range(),
			AttrPath:  attr.Text(),
			Kind:      bindingKind,
			DefScope:  defScope,
		}
		a.file.Bindings = append(a.file.Bindings, b)
		if kind != AttrBinding && defScope != nil {
			defScope.Bindings = append(defScope.Bindings, b)
		}
	}
}

// walkBareInherit records the outer references implied by `inherit a b;`. Each
// inherited name is shorthand for `a = a;`, whose right-hand side resolves in
// the scope enclosing this binding set.
func (a *analyzer) walkBareInherit(entry syntax.Node, inheritScope *Scope) {
	attrs := entry.ChildByFieldName("attrs")
	for _, attr := range attrs.NamedChildren() {
		if attr.Kind() != "identifier" {
			continue
		}
		if ref := a.recordReference(attr, inheritScope); ref != nil {
			ref.FromInherit = true
		}
	}
}

// walkInheritFrom resolves only the source expression of `inherit (e) a b;`.
// The inherited names take their values from e, so they imply no outer
// references of their own.
func (a *analyzer) walkInheritFrom(entry syntax.Node, valueScope *Scope) {
	a.walk(entry.ChildByFieldName("expression"), valueScope)
}

// define records a binding for a single identifier node.
func (a *analyzer) define(scope *Scope, kind BindingKind, ident syntax.Node) {
	b := &Binding{
		Name:      ident.Text(),
		NameRange: ident.Range(),
		AttrPath:  ident.Text(),
		Kind:      kind,
		DefScope:  scope,
	}
	a.file.Bindings = append(a.file.Bindings, b)
	if scope != nil {
		scope.Bindings = append(scope.Bindings, b)
	}
}

// recordReference resolves node as a use of a name and records the reference.
// node may be a variable_expression or a bare identifier (from an inherit). It
// returns the recorded reference, or nil when node carries no usable name.
func (a *analyzer) recordReference(node syntax.Node, scope *Scope) *Reference {
	name := node.Text()
	if node.Kind() == "variable_expression" {
		if inner := node.ChildByFieldName("name"); !inner.IsZero() {
			name = inner.Text()
		}
	}
	if name == "" {
		return nil
	}

	ref := &Reference{Name: name, Range: node.Range()}
	target, uncertain := a.resolve(scope, name)
	ref.Target = target
	ref.WithUncertain = uncertain
	a.file.References = append(a.file.References, ref)
	if target != nil {
		target.refs = append(target.refs, ref)
	}
	return ref
}

// resolve looks name up lexically from scope outward. A lexical binding always
// wins (innermost first, so shadowing works). Failing that, a builtin resolves.
// Otherwise the name is unresolved, and uncertain iff any enclosing scope is a
// `with`.
func (a *analyzer) resolve(scope *Scope, name string) (target *Binding, withUncertain bool) {
	sawWith := false
	for s := scope; s != nil; s = s.Parent {
		if s.Kind == ScopeWith {
			sawWith = true
			continue
		}
		for _, b := range s.Bindings {
			if b.Name == name {
				return b, false
			}
		}
	}
	if b := a.builtin(name); b != nil {
		return b, false
	}
	return nil, sawWith
}

// builtin returns a shared binding for a Nix global, or nil if name is not one.
func (a *analyzer) builtin(name string) *Binding {
	if !builtinNames[name] {
		return nil
	}
	if b, ok := a.file.builtins[name]; ok {
		return b
	}
	b := &Binding{Name: name, AttrPath: name, Kind: Builtin, DefScope: a.file.Root}
	a.file.builtins[name] = b
	return b
}

// newScope creates a scope, links it, and registers it on the file.
func (a *analyzer) newScope(kind ScopeKind, parent *Scope, r syntax.Range) *Scope {
	s := &Scope{Kind: kind, Parent: parent, Range: r}
	a.file.Scopes = append(a.file.Scopes, s)
	return s
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

// rangeContains reports whether pos lies within the half-open range r.
func rangeContains(r syntax.Range, pos syntax.Position) bool {
	return !positionLess(pos, r.Start) && positionLess(pos, r.End)
}

// positionLess reports whether a is strictly before b.
func positionLess(a, b syntax.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

// builtinNames is the set of Nix global identifiers. These are the names Nix
// exposes without a `builtins.` prefix; keeping them here means real uses of
// `import`, `map`, `true`, and friends resolve instead of appearing unresolved.
var builtinNames = map[string]bool{
	"abort":            true,
	"baseNameOf":       true,
	"break":            true,
	"builtins":         true,
	"derivation":       true,
	"derivationStrict": true,
	"dirOf":            true,
	"false":            true,
	"fetchGit":         true,
	"fetchMercurial":   true,
	"fetchTarball":     true,
	"fetchTree":        true,
	"fetchurl":         true,
	"fromTOML":         true,
	"import":           true,
	"isNull":           true,
	"map":              true,
	"null":             true,
	"placeholder":      true,
	"removeAttrs":      true,
	"scopedImport":     true,
	"throw":            true,
	"toString":         true,
	"true":             true,
}
