package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/imports"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/static"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

// workspaceSymbolCap bounds the number of symbols returned for one
// workspace/symbol query; workspaces can be large and the client only shows a
// handful at a time.
const workspaceSymbolCap = 128

// LSP SymbolKind values used for document symbols.
const (
	symbolKindField    = 8
	symbolKindVariable = 13
	symbolKindObject   = 19
)

// LSP DocumentHighlightKind values.
const (
	highlightKindRead  = 2
	highlightKindWrite = 3
)

type documentSymbolParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type textDocumentPositionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     protocolPosition       `json:"position"`
}

type referenceParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     protocolPosition       `json:"position"`
	Context      referenceContext       `json:"context"`
}

type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

type foldingRangeParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type workspaceSymbolParams struct {
	Query string `json:"query"`
}

type codeActionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Range        protocolRange          `json:"range"`
	Context      codeActionContext      `json:"context"`
}

type codeActionContext struct {
	Diagnostics []protocolDiagnostic `json:"diagnostics"`
	Only        []string             `json:"only"`
}

// Command is an LSP command a code action can run.
type Command struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// CodeAction is a single LSP code action. Here it is always a quick fix that
// runs a client-executed command.
type CodeAction struct {
	Title       string               `json:"title"`
	Kind        string               `json:"kind,omitempty"`
	Diagnostics []protocolDiagnostic `json:"diagnostics,omitempty"`
	IsPreferred bool                 `json:"isPreferred,omitempty"`
	Command     *Command             `json:"command,omitempty"`
}

// SymbolInformation is the flat LSP workspace-symbol shape: a name, kind, and a
// single location.
type SymbolInformation struct {
	Name     string   `json:"name"`
	Kind     int      `json:"kind"`
	Location Location `json:"location"`
}

// FoldingRange is a single LSP folding range. The optional `kind` field is
// intentionally omitted: these ranges are structural regions, not comments or
// imports.
type FoldingRange struct {
	StartLine      int `json:"startLine"`
	StartCharacter int `json:"startCharacter"`
	EndLine        int `json:"endLine"`
	EndCharacter   int `json:"endCharacter"`
}

// DocumentSymbol is the hierarchical LSP document-symbol shape.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          protocolRange    `json:"range"`
	SelectionRange protocolRange    `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// Location is an LSP location: a URI plus a range within it.
type Location struct {
	URI   string        `json:"uri"`
	Range protocolRange `json:"range"`
}

// DocumentHighlight is a single LSP document-highlight span.
type DocumentHighlight struct {
	Range protocolRange `json:"range"`
	Kind  int           `json:"kind"`
}

// documentSymbol answers textDocument/documentSymbol from the parse tree.
func (h *Handler) documentSymbol(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded documentSymbolParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	fileID, ok := h.fileInputForURI(decoded.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil {
		return nil, nil
	}
	return documentSymbols(tree), nil
}

// definition answers textDocument/definition using scope resolution.
func (h *Handler) definition(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded textDocumentPositionParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	fileID, ok := h.fileInputForURI(decoded.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	pos := syntax.Position{Line: decoded.Position.Line, Character: decoded.Position.Character}
	uri := decoded.TextDocument.URI

	// An `inherit (import ./x.nix) name;` attr resolves to its own binding under
	// scope analysis, which would shadow the cross-file jump. Try the cross-file
	// jump first, but only for the inherit-from case; bare `inherit name;` keeps
	// its existing self-definition behavior.
	if location := h.inheritSelectDefinition(ctx, fileID, uri, pos); location != nil {
		return location, nil
	}

	file, err := facts.Scopes(ctx, h.memo, fileID)
	if err != nil || file == nil {
		return nil, nil
	}
	if location := definitionAt(file, uri, pos); location != nil {
		return location, nil
	}
	// Fall back to import-path resolution: gd on `import ./foo.nix`,
	// `imports = [ ./x.nix ]`, or `callPackage ./x.nix` jumps to the target file.
	edges, err := facts.ImportEdges(ctx, h.memo, fileID)
	if err != nil {
		return nil, nil
	}
	if location := importDefinitionAt(edges, pos); location != nil {
		return location, nil
	}
	// Finally, gd on the attribute part of a select expression (`lib.foo`)
	// resolves through an import into the target file, or into a local attrset.
	if location := h.selectDefinition(ctx, fileID, uri, pos); location != nil {
		return location, nil
	}
	return nil, nil
}

// references answers textDocument/references using scope resolution.
func (h *Handler) references(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded referenceParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	fileID, ok := h.fileInputForURI(decoded.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	file, err := facts.Scopes(ctx, h.memo, fileID)
	if err != nil || file == nil {
		return nil, nil
	}
	pos := syntax.Position{Line: decoded.Position.Line, Character: decoded.Position.Character}
	locations := referencesAt(file, decoded.TextDocument.URI, pos, decoded.Context.IncludeDeclaration)
	if locations == nil {
		return nil, nil
	}
	return locations, nil
}

// foldingRange answers textDocument/foldingRange from the parse tree.
func (h *Handler) foldingRange(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded foldingRangeParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	fileID, ok := h.fileInputForURI(decoded.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil {
		return nil, nil
	}
	return foldingRanges(tree), nil
}

// codeAction answers textDocument/codeAction. The only action it offers is the
// "Run git add" quick fix for an untracked flake import target, and only where
// the untracked-import warning itself appears, so the lightbulb never shows a
// fix that would not help. Like the other feature handlers it returns nil, nil
// (LSP null) on any failure; a null result means "no actions".
func (h *Handler) codeAction(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded codeActionParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	if !quickfixRequested(decoded.Context.Only) {
		return nil, nil
	}
	fileID, ok := h.fileInputForURI(decoded.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	workspace, ok := h.Workspace()
	if !ok {
		return nil, nil
	}
	edges, err := facts.ImportEdges(ctx, h.memo, fileID)
	if err != nil {
		return nil, nil
	}

	requested := syntax.Range{
		Start: syntax.Position{Line: decoded.Range.Start.Line, Character: decoded.Range.Start.Character},
		End:   syntax.Position{Line: decoded.Range.End.Line, Character: decoded.Range.End.Character},
	}
	var actions []CodeAction
	for _, edge := range edges {
		// Guard Exists exactly as ImportDiagnostics does: a missing target is a
		// separate (non-fixable) diagnostic, and ShouldWarnUntracked alone does
		// not distinguish it. This keeps the fix strictly where the warning is.
		if !edge.Exists || !static.ShouldWarnUntracked(workspace, edge) {
			continue
		}
		if !rangesOverlap(edge.Range, requested) {
			continue
		}
		actions = append(actions, h.gitAddCodeAction(ctx, fileID, workspace, edge))
	}
	if len(actions) == 0 {
		return nil, nil
	}
	return actions, nil
}

// gitAddCodeAction builds the quick fix for one untracked import edge. The
// command argument is the absolute normalized target path; the title shows the
// workspace-relative path when it can be computed.
func (h *Handler) gitAddCodeAction(ctx context.Context, fileID string, workspace project.Workspace, edge imports.Edge) CodeAction {
	display := edge.TargetPath
	if rel, err := filepath.Rel(workspace.Root, edge.TargetPath); err == nil {
		display = rel
	}
	title := "Run git add " + display

	action := CodeAction{
		Title:       title,
		Kind:        "quickfix",
		IsPreferred: true,
		Command: &Command{
			Title:     title,
			Command:   commandGitAdd,
			Arguments: []any{edge.TargetPath},
		},
	}
	// Attach the exact untracked-import diagnostic this fix resolves, so clients
	// that pair actions with diagnostics light up the right squiggle. Omit the
	// field entirely if diagnostics cannot be recomputed.
	if diagnostics, err := facts.FileDiagnostics(ctx, h.memo, fileID); err == nil {
		action.Diagnostics = untrackedDiagnosticsForEdge(diagnostics, edge)
	}
	return action
}

// untrackedDiagnosticsForEdge returns the protocol form of exactly the
// untracked-import diagnostics anchored on edge's range.
func untrackedDiagnosticsForEdge(diagnostics []syntax.Diagnostic, edge imports.Edge) []protocolDiagnostic {
	var matched []syntax.Diagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == static.CodeUntrackedImport && diagnostic.Range == edge.Range {
			matched = append(matched, diagnostic)
		}
	}
	return toProtocolDiagnostics(matched)
}

// quickfixRequested reports whether a code-action request accepts quick fixes.
// An absent or empty `only` accepts anything; otherwise an entry matches when it
// is empty, equals "quickfix", or is an ancestor kind of "quickfix" (LSP kind
// hierarchy: the action kind starts with the requested kind + ".").
func quickfixRequested(only []string) bool {
	if len(only) == 0 {
		return true
	}
	for _, kind := range only {
		if kind == "" || kind == "quickfix" || strings.HasPrefix("quickfix", kind+".") {
			return true
		}
	}
	return false
}

// rangesOverlap reports whether two ranges intersect, counting a shared
// endpoint as an overlap. A cursor (empty) range reduces to a position-in-range
// test.
func rangesOverlap(a, b syntax.Range) bool {
	return !positionLess(b.End, a.Start) && !positionLess(a.End, b.Start)
}

// workspaceSymbol answers workspace/symbol by scanning every file in the current
// workspace through the pinned VFS snapshot and emitting one symbol per
// let/rec/attr binding. It is a synchronous request over many files; the memo
// engine caches scope analysis by (path, hash) so re-queries are cheap. The
// handler mutex is held only to read workspace state, never while evaluating
// memo queries.
func (h *Handler) workspaceSymbol(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded workspaceSymbolParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}

	h.mu.RLock()
	workspace := h.workspace
	ok := h.workspaceOK
	h.mu.RUnlock()
	if !ok {
		return []SymbolInformation{}, nil
	}

	snapshot := h.vfs.Snapshot()
	query := strings.ToLower(decoded.Query)

	symbols := make([]SymbolInformation, 0)
	for _, file := range workspace.Files {
		read, err := snapshot.ReadFile(file.Path)
		if err != nil {
			continue
		}
		fileID := facts.FileID(read.Path, read.Hash)
		facts.SetFileInput(h.memo, fileID, facts.FileInput{Path: read.Path, Content: read.Content})
		scopeFile, err := facts.Scopes(ctx, h.memo, fileID)
		if err != nil || scopeFile == nil {
			continue
		}

		symbols = append(symbols, fileWorkspaceSymbols(scopeFile, file.URI, query)...)
		// Files are visited in sorted (URI) order and each file's symbols are
		// range-sorted, so once we have a full page we can stop scanning.
		if len(symbols) >= workspaceSymbolCap {
			break
		}
	}

	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].Location.URI != symbols[j].Location.URI {
			return symbols[i].Location.URI < symbols[j].Location.URI
		}
		return protocolRangeLess(symbols[i].Location.Range, symbols[j].Location.Range)
	})
	if len(symbols) > workspaceSymbolCap {
		symbols = symbols[:workspaceSymbolCap]
	}
	return symbols, nil
}

// fileWorkspaceSymbols turns one file's scope bindings into workspace symbols,
// keeping only let/rec/attr bindings whose name substring-matches lowerQuery
// (case-insensitive; empty matches all). The result is sorted by name range.
func fileWorkspaceSymbols(file *scopes.File, uri, lowerQuery string) []SymbolInformation {
	var symbols []SymbolInformation
	for _, binding := range file.Bindings {
		if binding.Dynamic {
			continue
		}
		kind, ok := workspaceSymbolKind(binding.Kind)
		if !ok {
			continue
		}
		name := binding.AttrPath
		if name == "" {
			name = binding.Name
		}
		if name == "" {
			continue
		}
		if lowerQuery != "" && !strings.Contains(strings.ToLower(name), lowerQuery) {
			continue
		}
		symbols = append(symbols, SymbolInformation{
			Name:     name,
			Kind:     kind,
			Location: Location{URI: uri, Range: toProtocolRange(binding.NameRange)},
		})
	}
	sort.SliceStable(symbols, func(i, j int) bool {
		return protocolRangeLess(symbols[i].Location.Range, symbols[j].Location.Range)
	})
	return symbols
}

// workspaceSymbolKind maps a binding kind to an LSP SymbolKind, reporting
// ok=false for kinds that are not workspace symbols (params, inherits,
// builtins). Attribute and rec-attr keys are Fields; let bindings are Variables.
func workspaceSymbolKind(kind scopes.BindingKind) (int, bool) {
	switch kind {
	case scopes.LetBinding:
		return symbolKindVariable, true
	case scopes.RecAttr, scopes.AttrBinding:
		return symbolKindField, true
	default:
		return 0, false
	}
}

// protocolRangeLess orders protocol ranges by start then end position.
func protocolRangeLess(a, b protocolRange) bool {
	if a.Start.Line != b.Start.Line {
		return a.Start.Line < b.Start.Line
	}
	if a.Start.Character != b.Start.Character {
		return a.Start.Character < b.Start.Character
	}
	if a.End.Line != b.End.Line {
		return a.End.Line < b.End.Line
	}
	return a.End.Character < b.End.Character
}

// documentHighlight answers textDocument/documentHighlight using scope
// resolution.
func (h *Handler) documentHighlight(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded textDocumentPositionParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	fileID, ok := h.fileInputForURI(decoded.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	file, err := facts.Scopes(ctx, h.memo, fileID)
	if err != nil || file == nil {
		return nil, nil
	}
	pos := syntax.Position{Line: decoded.Position.Line, Character: decoded.Position.Character}
	return documentHighlightsAt(file, pos), nil
}

// fileInputForURI resolves uri to the current VFS content, registers the file
// input on the memo engine, and returns the fileID. It returns ok=false when the
// URI is malformed or the file cannot be read (unopened and absent from disk),
// so callers can answer with a null result rather than an error.
func (h *Handler) fileInputForURI(uri string) (string, bool) {
	path, err := vfs.URIToPath(uri)
	if err != nil {
		return "", false
	}
	file, err := h.vfs.Snapshot().ReadFile(path)
	if err != nil {
		return "", false
	}
	fileID := facts.FileID(file.Path, file.Hash)
	facts.SetFileInput(h.memo, fileID, facts.FileInput{
		Path:    file.Path,
		Content: file.Content,
	})
	return fileID, true
}

// definitionAt resolves the definition location for pos, or nil. A reference
// with a resolved, non-builtin target jumps to the target's name; a position on
// a binding name resolves to that binding's own name (double-click friendly).
func definitionAt(file *scopes.File, uri string, pos syntax.Position) *Location {
	if ref := file.ReferenceAt(pos); ref != nil {
		target := ref.Target
		if target == nil || target.Kind == scopes.Builtin {
			return nil
		}
		return &Location{URI: uri, Range: toProtocolRange(target.NameRange)}
	}
	if binding := file.BindingAt(pos); binding != nil {
		if binding.Kind == scopes.Builtin {
			return nil
		}
		return &Location{URI: uri, Range: toProtocolRange(binding.NameRange)}
	}
	return nil
}

// importDefinitionAt returns a location at the start of the file targeted by an
// import edge whose range contains pos, or nil. Only edges with an existing,
// resolved target participate; the returned range is the zero range (0:0-0:0),
// which points at the top of the target file.
func importDefinitionAt(edges []imports.Edge, pos syntax.Position) *Location {
	for _, edge := range edges {
		if !edge.Exists || edge.TargetPath == "" {
			continue
		}
		if !rangeContainsPosition(edge.Range, pos) {
			continue
		}
		uri, err := vfs.PathToURI(edge.TargetPath)
		if err != nil {
			return nil
		}
		return &Location{URI: uri}
	}
	return nil
}

// selectDefinition resolves gd on the attribute part of a select expression
// (`lib.foo`, `pkgs.a.b`). It finds the innermost select whose attrpath contains
// pos on a static identifier segment, takes the segments up to and including the
// cursor as the wanted path, and resolves that path either across an import edge
// into the target file or, when the base is a local binding whose value is an
// attribute set literal, within the current file. Anything ambiguous or dynamic
// yields nil so definition falls through to a null result.
func (h *Handler) selectDefinition(ctx context.Context, fileID, uri string, pos syntax.Position) *Location {
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil
	}
	base, wanted, ok := selectTargetAt(tree, pos)
	if !ok {
		return nil
	}
	edges, err := facts.ImportEdges(ctx, h.memo, fileID)
	if err != nil {
		return nil
	}

	if base.Kind() == "variable_expression" {
		// The base is a name: it must resolve to a local let/rec binding whose
		// value we can search. A `with`-provided or unresolved name, or a function
		// parameter (whose value is unknowable), is not followed.
		file, err := facts.Scopes(ctx, h.memo, fileID)
		if err != nil || file == nil {
			return nil
		}
		ref := file.ReferenceAt(base.Range().Start)
		if ref == nil || ref.WithUncertain || ref.Target == nil {
			return nil
		}
		switch ref.Target.Kind {
		case scopes.LetBinding, scopes.RecAttr:
		default:
			return nil
		}
		searchRange, ok := scopes.BindingValueRange(tree, ref.Target)
		if !ok {
			return nil
		}
		matched := edgesInRange(edges, searchRange)
		switch len(matched) {
		case 1:
			return h.crossFileAttr(ctx, matched[0], wanted)
		case 0:
			if r, ok := scopes.AttrsetValueResolve(tree, searchRange, wanted); ok {
				return &Location{URI: uri, Range: toProtocolRange(r)}
			}
			return nil
		default:
			return nil
		}
	}

	// An inline base such as `(import ./lib.nix)`: only a single import edge in
	// its own range is followed; there is no local binding to search.
	matched := edgesInRange(edges, base.Range())
	if len(matched) == 1 {
		return h.crossFileAttr(ctx, matched[0], wanted)
	}
	return nil
}

// inheritSelectDefinition resolves gd on an `inherit (import ./x.nix) name;`
// attribute into the target file. It runs before scope-based definition so the
// cross-file jump wins over the inherited name's self-definition. It fires only
// when the inherit source range contains exactly one import edge.
func (h *Handler) inheritSelectDefinition(ctx context.Context, fileID, uri string, pos syntax.Position) *Location {
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil
	}
	source, wanted, ok := inheritFromTargetAt(tree, pos)
	if !ok {
		return nil
	}
	edges, err := facts.ImportEdges(ctx, h.memo, fileID)
	if err != nil {
		return nil
	}
	matched := edgesInRange(edges, source.Range())
	if len(matched) != 1 {
		return nil
	}
	return h.crossFileAttr(ctx, matched[0], wanted)
}

// crossFileAttr loads the file targeted by edge and resolves wanted against its
// top-level value, returning a location in that file or nil.
func (h *Handler) crossFileAttr(ctx context.Context, edge imports.Edge, wanted []string) *Location {
	if !edge.Exists || edge.TargetURI == "" {
		return nil
	}
	tree, ok := h.parseTreeForPath(ctx, edge.TargetPath)
	if !ok {
		return nil
	}
	r, ok := scopes.ResolveAttrPath(tree, wanted)
	if !ok {
		return nil
	}
	return &Location{URI: edge.TargetURI, Range: toProtocolRange(r)}
}

// parseTreeForPath reads a (possibly unopen) file through the pinned VFS
// snapshot, registers it on the memo engine, and returns its parse tree. It
// mirrors fileInputForURI / workspaceSymbol so non-open import targets analyze
// through the same cached path.
func (h *Handler) parseTreeForPath(ctx context.Context, path string) (*syntax.Tree, bool) {
	read, err := h.vfs.Snapshot().ReadFile(path)
	if err != nil {
		return nil, false
	}
	fileID := facts.FileID(read.Path, read.Hash)
	facts.SetFileInput(h.memo, fileID, facts.FileInput{Path: read.Path, Content: read.Content})
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil, false
	}
	return tree, true
}

// selectTargetAt finds the innermost select_expression whose attrpath contains
// pos on a static identifier segment. It returns the (parenthesis-unwrapped)
// base expression and the attribute segments up to and including the cursor
// segment. ok is false when pos is not on such a segment or any segment in the
// prefix is dynamic.
func selectTargetAt(tree *syntax.Tree, pos syntax.Position) (base syntax.Node, wanted []string, ok bool) {
	var bestRange syntax.Range
	tree.Walk(func(node syntax.Node) bool {
		if node.Kind() != "select_expression" {
			return true
		}
		attrpath := node.ChildByFieldName("attrpath")
		if attrpath.IsZero() || !rangeContainsPosition(attrpath.Range(), pos) {
			return true
		}
		segs := attrpath.NamedChildren()
		idx := -1
		for i, seg := range segs {
			if rangeContainsPosition(seg.Range(), pos) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return true
		}
		path := make([]string, 0, idx+1)
		for i := 0; i <= idx; i++ {
			if segs[i].Kind() != "identifier" {
				return true
			}
			path = append(path, segs[i].Text())
		}
		r := node.Range()
		if !ok || rangeInside(r, bestRange) {
			base = unwrapParens(node.ChildByFieldName("expression"))
			wanted = path
			bestRange = r
			ok = true
		}
		return true
	})
	if base.IsZero() {
		ok = false
	}
	return base, wanted, ok
}

// inheritFromTargetAt finds an `inherit (e) a b;` entry whose attrs contain pos
// on an identifier. It returns the source expression e and the single wanted
// segment. ok is false when pos is not on such an identifier.
func inheritFromTargetAt(tree *syntax.Tree, pos syntax.Position) (source syntax.Node, wanted []string, ok bool) {
	tree.Walk(func(node syntax.Node) bool {
		if ok {
			return false
		}
		if node.Kind() != "inherit_from" {
			return true
		}
		attrs := node.ChildByFieldName("attrs")
		if attrs.IsZero() {
			return true
		}
		for _, attr := range attrs.NamedChildren() {
			if attr.Kind() != "identifier" || !rangeContainsPosition(attr.Range(), pos) {
				continue
			}
			expr := node.ChildByFieldName("expression")
			if expr.IsZero() {
				return true
			}
			source = expr
			wanted = []string{attr.Text()}
			ok = true
			return false
		}
		return true
	})
	return source, wanted, ok
}

// unwrapParens strips parenthesized_expression wrappers.
func unwrapParens(node syntax.Node) syntax.Node {
	for node.Kind() == "parenthesized_expression" {
		next := node.ChildByFieldName("expression")
		if next.IsZero() {
			return node
		}
		node = next
	}
	return node
}

// edgesInRange returns the import edges whose range lies within outer.
func edgesInRange(edges []imports.Edge, outer syntax.Range) []imports.Edge {
	var matched []imports.Edge
	for _, edge := range edges {
		if rangeInside(edge.Range, outer) {
			matched = append(matched, edge)
		}
	}
	return matched
}

// rangeInside reports whether inner lies entirely within outer.
func rangeInside(inner, outer syntax.Range) bool {
	return !positionLess(inner.Start, outer.Start) && !positionLess(outer.End, inner.End)
}

// referencesAt returns the locations of every reference to the binding under
// pos, optionally including the binding's own declaration. The cursor may sit on
// the declaration name or on any use. It returns nil when pos resolves to no
// binding. Builtins have no real declaration site, so includeDeclaration never
// adds one for them.
func referencesAt(file *scopes.File, uri string, pos syntax.Position, includeDeclaration bool) []Location {
	binding := file.BindingAt(pos)
	if binding == nil {
		if ref := file.ReferenceAt(pos); ref != nil {
			binding = ref.Target
		}
	}
	if binding == nil {
		return nil
	}

	var locations []Location
	if includeDeclaration && binding.Kind != scopes.Builtin {
		locations = append(locations, Location{URI: uri, Range: toProtocolRange(binding.NameRange)})
	}
	for _, ref := range binding.References() {
		locations = append(locations, Location{URI: uri, Range: toProtocolRange(ref.Range)})
	}
	return locations
}

// foldingRanges walks the parse tree and emits a folding range for every
// multi-line foldable construct (attribute sets, let, lists, functions). Ranges
// that share both start and end line with an already-emitted range are dropped,
// which collapses parent/child chains like `x: { ... }` into a single fold. The
// result is sorted by start line.
func foldingRanges(tree *syntax.Tree) []FoldingRange {
	if tree == nil {
		return nil
	}

	var ranges []FoldingRange
	seen := make(map[[2]int]bool)
	tree.Walk(func(node syntax.Node) bool {
		if !isFoldableKind(node.Kind()) {
			return true
		}
		r := node.Range()
		if r.End.Line <= r.Start.Line {
			return true
		}
		key := [2]int{r.Start.Line, r.End.Line}
		if seen[key] {
			return true
		}
		seen[key] = true
		ranges = append(ranges, FoldingRange{
			StartLine:      r.Start.Line,
			StartCharacter: r.Start.Character,
			EndLine:        r.End.Line,
			EndCharacter:   r.End.Character,
		})
		return true
	})
	if ranges == nil {
		return nil
	}
	sort.SliceStable(ranges, func(i, j int) bool {
		return ranges[i].StartLine < ranges[j].StartLine
	})
	return ranges
}

// isFoldableKind reports whether a node kind produces a folding range.
func isFoldableKind(kind string) bool {
	switch kind {
	case "attrset_expression", "rec_attrset_expression", "let_expression",
		"list_expression", "function_expression":
		return true
	default:
		return false
	}
}

// rangeContainsPosition reports whether pos lies within the half-open range r.
func rangeContainsPosition(r syntax.Range, pos syntax.Position) bool {
	if positionLess(pos, r.Start) {
		return false
	}
	return positionLess(pos, r.End)
}

// positionLess reports whether a is strictly before b.
func positionLess(a, b syntax.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

// documentHighlightsAt returns the write highlight for a binding's definition
// (when it has one) plus a read highlight for every reference to it. The cursor
// may sit on either the definition name or any use.
func documentHighlightsAt(file *scopes.File, pos syntax.Position) []DocumentHighlight {
	binding := file.BindingAt(pos)
	if binding == nil {
		if ref := file.ReferenceAt(pos); ref != nil {
			binding = ref.Target
		}
	}
	if binding == nil {
		return nil
	}

	var highlights []DocumentHighlight
	// Builtins have no definition site, so they get reads only.
	if binding.Kind != scopes.Builtin {
		highlights = append(highlights, DocumentHighlight{
			Range: toProtocolRange(binding.NameRange),
			Kind:  highlightKindWrite,
		})
	}
	for _, ref := range binding.References() {
		highlights = append(highlights, DocumentHighlight{
			Range: toProtocolRange(ref.Range),
			Kind:  highlightKindRead,
		})
	}
	return highlights
}

// documentSymbols builds the hierarchical outline for a parsed file. It is total:
// malformed or unexpected nodes contribute no symbols rather than panicking.
func documentSymbols(tree *syntax.Tree) []DocumentSymbol {
	if tree == nil {
		return nil
	}
	var symbols []DocumentSymbol
	for _, child := range tree.Root().NamedChildren() {
		symbols = append(symbols, exprSymbols(child)...)
	}
	return symbols
}

// exprSymbols returns the document symbols an expression contributes at its own
// level, unwrapping let/with/function bodies to reach attribute sets.
func exprSymbols(expr syntax.Node) []DocumentSymbol {
	if expr.IsZero() {
		return nil
	}
	switch expr.Kind() {
	case "attrset_expression", "rec_attrset_expression":
		return bindingSetSymbols(bindingSetChild(expr), false)
	case "let_expression":
		symbols := bindingSetSymbols(bindingSetChild(expr), true)
		return append(symbols, exprSymbols(expr.ChildByFieldName("body"))...)
	case "with_expression", "function_expression":
		return exprSymbols(expr.ChildByFieldName("body"))
	default:
		return nil
	}
}

// bindingSetSymbols turns each `name = value;` entry of a binding_set into a
// document symbol. letScope reports whether these are `let` bindings, which are
// classified as variables regardless of their value.
func bindingSetSymbols(set syntax.Node, letScope bool) []DocumentSymbol {
	if set.IsZero() {
		return nil
	}
	var symbols []DocumentSymbol
	for _, entry := range set.NamedChildren() {
		if entry.Kind() != "binding" {
			continue
		}
		if symbol, ok := bindingSymbol(entry, letScope); ok {
			symbols = append(symbols, symbol)
		}
	}
	return symbols
}

// bindingSymbol builds one document symbol for a `binding` node, nesting the
// symbols of an attribute-set value as children.
func bindingSymbol(entry syntax.Node, letScope bool) (DocumentSymbol, bool) {
	attrpath := entry.ChildByFieldName("attrpath")
	if attrpath.IsZero() || len(attrpath.NamedChildren()) == 0 {
		return DocumentSymbol{}, false
	}

	children, isAttrset := valueChildren(entry.ChildByFieldName("expression"))
	kind := symbolKindField
	switch {
	case letScope:
		kind = symbolKindVariable
	case isAttrset:
		kind = symbolKindObject
	}

	return DocumentSymbol{
		Name:           attrpath.Text(),
		Kind:           kind,
		Range:          toProtocolRange(entry.Range()),
		SelectionRange: toProtocolRange(attrpath.Range()),
		Children:       children,
	}, true
}

// valueChildren returns the child symbols of a binding value and whether that
// value is an attribute set.
func valueChildren(value syntax.Node) ([]DocumentSymbol, bool) {
	if value.IsZero() {
		return nil, false
	}
	switch value.Kind() {
	case "attrset_expression", "rec_attrset_expression":
		return bindingSetSymbols(bindingSetChild(value), false), true
	default:
		return nil, false
	}
}

// bindingSetChild returns the binding_set child of node, or a zero node.
func bindingSetChild(node syntax.Node) syntax.Node {
	for _, child := range node.NamedChildren() {
		if child.Kind() == "binding_set" {
			return child
		}
	}
	return syntax.Node{}
}

func toProtocolRange(r syntax.Range) protocolRange {
	return protocolRange{
		Start: toProtocolPosition(r.Start),
		End:   toProtocolPosition(r.End),
	}
}
