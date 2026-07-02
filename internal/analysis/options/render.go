package options

import "strings"

// Markdown renders the Doc as hover markdown: the dotted name in bold, the
// description, a Type line, optional Default and Example blocks, and the
// declaration list, separated by blank lines. Empty Default/Example blocks are
// omitted. A read-only option gets a " *(read only)*" suffix on the Type line.
func (d *Doc) Markdown() string {
	blocks := []string{"**" + strings.Join(d.Loc, ".") + "**"}

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
