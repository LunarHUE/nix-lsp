package options

import "strings"

// Markdown renders the Doc as hover markdown: the dotted name in bold, the
// description, a Type line, optional Default and Example blocks, and the
// declaration list, separated by blank lines. Empty Default/Example blocks are
// omitted. A read-only option gets a " *(read only)*" suffix on the Type line.
// The header names the declared Loc, so wildcard docs show their <name>/*
// placeholders (escaped); prefer MarkdownFor when the concrete hovered path is
// known.
func (d *Doc) Markdown() string {
	return d.markdown(d.Loc)
}

// MarkdownFor renders the Doc like Markdown, but the header names the concrete
// path the user hovered instead of the declared Loc, so a wildcard doc shows
// the user's own instance name (systemd.services.demo-web.description rather
// than systemd.services.<name>.description). An empty path falls back to the
// declared Loc.
func (d *Doc) MarkdownFor(path []string) string {
	if len(path) == 0 {
		return d.markdown(d.Loc)
	}
	return d.markdown(path)
}

// markdown renders the Doc body under a bold header naming path.
func (d *Doc) markdown(path []string) string {
	blocks := []string{"**" + escapeHeader(strings.Join(path, ".")) + "**"}

	if desc := strings.TrimRight(d.Description, " \t\r\n"); desc != "" {
		blocks = append(blocks, desc)
	}

	typeLine := "*Type:* `" + d.Type + "`"
	if d.ReadOnly {
		typeLine += " *(read only)*"
	}
	blocks = append(blocks, typeLine)

	if d.Default != "" {
		blocks = append(blocks, renderValueBlock("Default", d.Default, d.DefaultIsMD))
	}
	if d.Example != "" {
		blocks = append(blocks, renderValueBlock("Example", d.Example, d.ExampleIsMD))
	}
	if len(d.Declarations) > 0 {
		blocks = append(blocks, renderDeclarations(d.Declarations))
	}

	return strings.Join(blocks, "\n\n")
}

// escapeHeader backslash-escapes angle brackets so a <name> or <literal>
// placeholder in the header renders as literal text; unescaped, markdown
// renderers treat it as an HTML tag and strip it, leaving a confusing "..".
func escapeHeader(s string) string {
	s = strings.ReplaceAll(s, "<", `\<`)
	return strings.ReplaceAll(s, ">", `\>`)
}

// renderValueBlock renders a labelled default/example value. Markdown text is
// emitted as prose under the label; everything else is wrapped in a nix fence,
// including single-line values (consistency over compactness).
func renderValueBlock(label, text string, isMD bool) string {
	if isMD {
		return "*" + label + ":*\n" + strings.TrimRight(text, "\n")
	}
	return "*" + label + ":*\n```nix\n" + text + "\n```"
}

// renderDeclarations renders the declaration list. A single declaration sits
// inline after the label; multiple declarations get one backticked entry per
// line under the label.
func renderDeclarations(decls []string) string {
	if len(decls) == 1 {
		return "*Declared in:* `" + decls[0] + "`"
	}
	var b strings.Builder
	b.WriteString("*Declared in:*")
	for _, decl := range decls {
		b.WriteString("\n`" + decl + "`")
	}
	return b.String()
}
