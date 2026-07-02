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
	return d.markdown(d.Loc, "")
}

// MarkdownFor renders the Doc like Markdown, but the header names the concrete
// path the user hovered instead of the declared Loc, so a wildcard doc shows
// the user's own instance name (systemd.services.demo-web.description rather
// than systemd.services.<name>.description). An empty path falls back to the
// declared Loc. Declaration paths stay backticked text; use MarkdownForChannel
// to link them to their nixpkgs source.
func (d *Doc) MarkdownFor(path []string) string {
	return d.MarkdownForChannel(path, "")
}

// MarkdownForChannel renders the Doc like MarkdownFor, additionally linking each
// nixpkgs-relative declaration path to its source on the given channel branch of
// nixpkgs on GitHub. An empty channel leaves declarations backticked (the
// MarkdownFor behavior); a non-path declaration stays backticked even with a
// channel set.
func (d *Doc) MarkdownForChannel(path []string, channel string) string {
	if len(path) == 0 {
		return d.markdown(d.Loc, channel)
	}
	return d.markdown(path, channel)
}

// markdown renders the Doc body under a bold header naming path, linking
// declaration paths to nixpkgs on channel when channel is non-empty.
func (d *Doc) markdown(path []string, channel string) string {
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
		blocks = append(blocks, renderDeclarations(d.Declarations, channel))
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
// inline after the label; multiple declarations get one entry per line under the
// label. Each entry is independently linked-or-backticked by renderDeclaration.
func renderDeclarations(decls []string, channel string) string {
	if len(decls) == 1 {
		return "*Declared in:* " + renderDeclaration(decls[0], channel)
	}
	var b strings.Builder
	b.WriteString("*Declared in:*")
	for _, decl := range decls {
		b.WriteString("\n" + renderDeclaration(decl, channel))
	}
	return b.String()
}

// renderDeclaration renders one declaration entry. When channel is set and decl
// looks like a nixpkgs-relative source path, it becomes a markdown link to that
// file on the channel branch of nixpkgs on GitHub — the channel branch tip, not
// the locked rev, since the dataset tracks the channel and may rarely 404 for a
// since-moved file. Anything else (an absolute store path, a URL from another
// dataset, an odd shape, or any decl when channel is "") stays backticked text.
func renderDeclaration(decl, channel string) string {
	if channel != "" && isNixpkgsRelPath(decl) {
		return "[" + decl + "](https://github.com/NixOS/nixpkgs/blob/" + channel + "/" + decl + ")"
	}
	return "`" + decl + "`"
}

// isNixpkgsRelPath reports whether decl looks like a nixpkgs-relative source
// path safe to turn into a GitHub blob link: it starts with "nixos/" or "pkgs/"
// (which already rules out a scheme and a leading slash) and contains no
// whitespace. Absolute store paths and foreign-dataset URLs fail the prefix test
// and stay backticked.
func isNixpkgsRelPath(decl string) bool {
	if strings.ContainsAny(decl, " \t\r\n") {
		return false
	}
	return strings.HasPrefix(decl, "nixos/") || strings.HasPrefix(decl, "pkgs/")
}
