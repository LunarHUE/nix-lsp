package server

import (
	"context"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// maxValueFenceLines bounds how many lines of a bound expression a value hover
// renders before it appends an ellipsis marker.
const maxValueFenceLines = 10

// valueHover answers a binding-value hover for an identifier in expression
// position in any workspace .nix file. Hovering a variable use (including inside
// a `${...}` interpolation) shows what the name is bound to, when that can be
// answered from the same file's lexical scope: the source text of the bound
// expression, never an evaluated value. It resolves the identifier with the same
// scope machinery go-to-definition uses; a name that does not resolve locally (a
// builtin, or a `with`-provided or undefined name) yields nil. valueHover runs
// last in the hover chain, so flake-input, package, and option hovers always win.
func (h *Handler) valueHover(ctx context.Context, uri string, pos syntax.Position) *Hover {
	fileID, ok := h.optionFileInputForURI(uri)
	if !ok {
		return nil
	}
	file, err := facts.Scopes(ctx, h.memo, fileID)
	if err != nil || file == nil {
		return nil
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil
	}
	return valueHoverAt(file, tree, pos)
}

// valueHoverAt renders the binding-value hover for the identifier under pos, or
// nil. It mirrors definitionAt's resolution: a reference resolves to its target
// binding; a position on a binding name resolves to that binding itself. Only a
// locally introduced name renders — a nil or builtin target yields nil. The
// hover range is always the hovered identifier's own range.
func valueHoverAt(file *scopes.File, tree *syntax.Tree, pos syntax.Position) *Hover {
	if ref := file.ReferenceAt(pos); ref != nil {
		target := ref.Target
		if target == nil || target.Kind == scopes.Builtin {
			return nil
		}
		return wrapValueHover(renderBindingHover(tree, target), ref.Range)
	}
	if b := file.BindingAt(pos); b != nil {
		if b.Kind == scopes.Builtin {
			return nil
		}
		return wrapValueHover(renderBindingHover(tree, b), b.NameRange)
	}
	return nil
}

// wrapValueHover wraps rendered markdown in a *Hover anchored on r, or nil when the
// markdown is empty (a binding kind with nothing to say).
func wrapValueHover(markdown string, r syntax.Range) *Hover {
	if markdown == "" {
		return nil
	}
	return &Hover{
		Contents: MarkupContent{Kind: "markdown", Value: markdown},
		Range:    toProtocolRange(r),
	}
}

// renderBindingHover renders the markdown for a resolved binding by its kind: a
// let/attribute binding fences its value expression, a function parameter fences
// its default when it has one, and an inherited name shows only its label.
func renderBindingHover(tree *syntax.Tree, b *scopes.Binding) string {
	switch b.Kind {
	case scopes.LetBinding:
		return renderBindingValue(b.Name, "let binding", tree, b)
	case scopes.RecAttr, scopes.AttrBinding:
		return renderBindingValue(b.Name, "attribute", tree, b)
	case scopes.Param, scopes.AtPattern:
		return valueHoverHeader(b.Name, "function parameter")
	case scopes.FormalParam:
		header := valueHoverHeader(b.Name, "function parameter")
		if def, ok := scopes.FormalDefaultSource(tree, b); ok {
			return header + "\n\n" + nixFence(def)
		}
		return header
	case scopes.InheritEntry:
		return valueHoverHeader(b.Name, "inherited")
	default:
		return ""
	}
}

// renderBindingValue renders a labelled header followed by the binding's value
// expression in a nix fence. When the value cannot be located it degrades to the
// header alone.
func renderBindingValue(name, label string, tree *syntax.Tree, b *scopes.Binding) string {
	header := valueHoverHeader(name, label)
	src, ok := scopes.BindingValueSource(tree, b)
	if !ok {
		return header
	}
	return header + "\n\n" + nixFence(src)
}

// valueHoverHeader renders the bold name and em-dash label line.
func valueHoverHeader(name, label string) string {
	return "**" + name + "** — " + label
}

// nixFence wraps src in a ```nix fenced code block after formatting it for
// display.
func nixFence(src string) string {
	return "```nix\n" + formatValueFence(src) + "\n```"
}

// formatValueFence normalizes a bound expression's source text for display in a
// code fence: it trims trailing whitespace, truncates to the first
// maxValueFenceLines lines (appending an ellipsis line when longer), and dedents
// the kept lines by their common leading whitespace so the fence reads naturally
// while preserving relative indentation.
func formatValueFence(src string) string {
	src = strings.TrimRight(src, " \t\r\n")
	lines := strings.Split(src, "\n")

	truncated := false
	if len(lines) > maxValueFenceLines {
		lines = lines[:maxValueFenceLines]
		truncated = true
	}

	indent := commonIndent(lines)
	for i, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if len(line) >= len(indent) {
			line = line[len(indent):]
		} else {
			line = strings.TrimLeft(line, " \t")
		}
		lines[i] = line
	}
	if truncated {
		lines = append(lines, "…")
	}
	return strings.Join(lines, "\n")
}

// commonIndent returns the longest leading-whitespace prefix shared by every
// non-blank line. Blank lines are ignored so they never force the indent to "".
func commonIndent(lines []string) string {
	indent := ""
	first := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lead := leadingWhitespace(line)
		if first {
			indent = lead
			first = false
			continue
		}
		indent = commonPrefix(indent, lead)
	}
	return indent
}

// leadingWhitespace returns the run of spaces and tabs at the start of line.
func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// commonPrefix returns the longest common prefix of a and b.
func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}
