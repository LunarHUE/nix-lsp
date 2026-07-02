package server

import (
	"context"
	"encoding/json"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

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
	file, err := facts.Scopes(ctx, h.memo, fileID)
	if err != nil || file == nil {
		return nil, nil
	}
	pos := syntax.Position{Line: decoded.Position.Line, Character: decoded.Position.Character}
	if location := definitionAt(file, decoded.TextDocument.URI, pos); location != nil {
		return location, nil
	}
	return nil, nil
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
