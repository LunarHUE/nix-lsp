package server

import (
	"path/filepath"
	"strings"
	"testing"
)

// valueHoverWorkspace writes src into a fresh workspace as mod.nix, initializes
// discovery (no dataset paths), opens the file, and returns its URI. Without an
// options or packages index loaded, the option and package hovers stay nil so
// binding-value hover is exercised on its own.
func valueHoverWorkspace(t *testing.T, handler *Handler, src string) string {
	t.Helper()
	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, src)
	initWorkspace(t, handler, root)
	uri := mustURI(t, modPath)
	openDocument(t, handler, uri, src)
	return uri
}

func TestHandlerValueHoverLetBinding(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	src := `let system = "x86_64-linux"; in { s = "prefix-${system}"; }`
	uri := valueHoverWorkspace(t, handler, src)

	// Hover the `system` use inside the ${system} interpolation.
	line, char := posOf(t, src, "system", 1)
	hover := requestHover(t, handler, uri, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want let-binding value hover")
	}
	value := hover.Contents.Value
	for _, want := range []string{"**system**", "let binding", `"x86_64-linux"`} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
	if hover.Contents.Kind != "markdown" {
		t.Errorf("kind = %q, want markdown", hover.Contents.Kind)
	}
	// The range spans exactly the hovered identifier.
	if hover.Range.Start.Line != line || hover.Range.Start.Character != char {
		t.Errorf("range start = %d:%d, want %d:%d", hover.Range.Start.Line, hover.Range.Start.Character, line, char)
	}
	wantEnd := char + len("system")
	if hover.Range.End.Character != wantEnd {
		t.Errorf("range end char = %d, want %d", hover.Range.End.Character, wantEnd)
	}
}

func TestHandlerValueHoverFunctionParameter(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	src := `{ pkgs, ... }: pkgs`
	uri := valueHoverWorkspace(t, handler, src)

	// Hover the formal `pkgs` at its declaration site.
	line, char := posOf(t, src, "pkgs", 0)
	hover := requestHover(t, handler, uri, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want function-parameter hover")
	}
	value := hover.Contents.Value
	for _, want := range []string{"**pkgs**", "function parameter"} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
}

func TestHandlerValueHoverFormalDefault(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	src := `{ x ? 42 }: x`
	uri := valueHoverWorkspace(t, handler, src)

	// `x` is a single character, so the cursor sits on the identifier itself.
	line, char := posOf(t, src, "x", 0)
	hover := requestHover(t, handler, uri, line, char)
	if hover == nil {
		t.Fatal("hover = null, want function-parameter hover with default")
	}
	value := hover.Contents.Value
	for _, want := range []string{"**x**", "function parameter", "```nix", "42"} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
}

func TestHandlerValueHoverUndefinedReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	src := `{ s = nope; }`
	uri := valueHoverWorkspace(t, handler, src)

	line, char := posOf(t, src, "nope", 0)
	if hover := requestHover(t, handler, uri, line, char+1); hover != nil {
		t.Fatalf("hover on undefined identifier = %+v, want null", hover)
	}
}

func TestHandlerValueHoverBuiltinReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	src := `{ s = toString 1; }`
	uri := valueHoverWorkspace(t, handler, src)

	line, char := posOf(t, src, "toString", 0)
	if hover := requestHover(t, handler, uri, line, char+1); hover != nil {
		t.Fatalf("hover on builtin = %+v, want null", hover)
	}
}

func TestHandlerValueHoverMultiLineTruncates(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// A 12-element list, one element per line: the value spans 12 lines, so the
	// fence keeps the first 10 and appends an ellipsis line.
	var b strings.Builder
	b.WriteString("let xs = [\n")
	for i := 0; i < 10; i++ {
		b.WriteString("  ")
		b.WriteByte(byte('0' + i))
		b.WriteByte('\n')
	}
	b.WriteString("]; in xs")
	src := b.String()
	uri := valueHoverWorkspace(t, handler, src)

	// Hover the `xs` use in the body.
	line, char := posOf(t, src, "xs", 1)
	hover := requestHover(t, handler, uri, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want truncated list value hover")
	}
	value := hover.Contents.Value
	if !strings.Contains(value, "…") {
		t.Errorf("truncated hover missing ellipsis:\n%s", value)
	}
	// The 12th line (`]`) must not appear; the 10th kept content line does.
	if strings.Contains(value, "\n]") {
		t.Errorf("hover kept lines beyond the limit:\n%s", value)
	}
}

func TestHandlerHoverOptionWinsOverValue(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, modFixture)
	initWithOptions(t, handler, root, optionsFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, modFixture)

	// On the option attrpath the option hover must win over the value hover,
	// which would otherwise fire on the `networking` binding name.
	line, char := posOf(t, modFixture, "allowedTCPPorts", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want option-doc hover")
	}
	if !strings.Contains(hover.Contents.Value, "**networking.firewall.allowedTCPPorts**") {
		t.Errorf("hover is not the option hover:\n%s", hover.Contents.Value)
	}
}

func TestFormatValueFence(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "trims trailing whitespace",
			src:  `"x86_64-linux"   `,
			want: `"x86_64-linux"`,
		},
		{
			name: "dedents common indentation",
			src:  "    a = 1;\n    b = 2;",
			want: "a = 1;\nb = 2;",
		},
		{
			name: "preserves relative indentation",
			src:  "  {\n    a = 1;\n  }",
			want: "{\n  a = 1;\n}",
		},
		{
			name: "ignores blank lines for indent",
			src:  "  a\n\n  b",
			want: "a\n\nb",
		},
		{
			name: "truncates past ten lines",
			src:  "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12",
			want: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatValueFence(tt.src); got != tt.want {
				t.Errorf("formatValueFence(%q) =\n%q\nwant\n%q", tt.src, got, tt.want)
			}
		})
	}
}
