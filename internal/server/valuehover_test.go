package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
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
			// The extracted text's first line starts at the expression itself, so
			// it stays put; the continuation lines dedent by their common indent.
			name: "dedents continuation lines",
			src:  "{\n    a = 1;\n    b = 2;\n  }",
			want: "{\n  a = 1;\n  b = 2;\n}",
		},
		{
			name: "preserves relative indentation",
			src:  "{\n    a = {\n      b = 1;\n    };\n  }",
			want: "{\n  a = {\n    b = 1;\n  };\n}",
		},
		{
			name: "ignores blank lines for indent",
			src:  "[\n    1\n\n    2\n  ]",
			want: "[\n  1\n\n  2\n]",
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

func TestIndentedStringContent(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "single content line",
			src:  "''\n    echo hi\n  ''",
			want: "echo hi",
		},
		{
			name: "multi line keeps relative indent",
			src:  "''\n    if true; then\n      echo hi\n    fi\n  ''",
			want: "if true; then\n  echo hi\nfi",
		},
		{
			name: "interior blank line survives",
			src:  "''\n    a\n\n    b\n  ''",
			want: "a\n\nb",
		},
		{
			name: "inline content",
			src:  "''echo hi''",
			want: "echo hi",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := indentedStringContent(tt.src); got != tt.want {
				t.Errorf("indentedStringContent(%q) =\n%q\nwant\n%q", tt.src, got, tt.want)
			}
		})
	}
}

// renderFor parses src, analyzes its scopes, and renders the hover markdown for
// the binding named name, exercising the pure render path directly.
func renderFor(t *testing.T, src, name string) string {
	t.Helper()
	tree, err := syntax.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	file := scopes.Analyze(tree)
	for _, b := range file.Bindings {
		if b.Name == name {
			return renderBindingHover(tree, b)
		}
	}
	t.Fatalf("binding %q not found in %q", name, src)
	return ""
}

func TestRenderBindingHoverIndentedStrings(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		binding string
		want    []string
		wantNot []string
	}{
		{
			// A single-content-line indented string collapses to one line.
			name:    "single line indented string collapses",
			src:     "{ motd = ''\n    hello\n  ''; }",
			binding: "motd",
			want:    []string{"```nix", "''hello''"},
			wantNot: []string{"```bash"},
		},
		{
			// A script-carrying attribute renders content only, as bash.
			name:    "shellHook renders bash content",
			src:     "{ shellHook = ''\n    echo \"ready\"\n  ''; }",
			binding: "shellHook",
			want:    []string{"**shellHook** — attribute", "```bash\necho \"ready\"\n```"},
			wantNot: []string{"''", "```nix"},
		},
		{
			name:    "script multi line keeps relative indent",
			src:     "{ script = ''\n    if ok; then\n      run\n    fi\n  ''; }",
			binding: "script",
			want:    []string{"```bash\nif ok; then\n  run\nfi\n```"},
			wantNot: []string{"''"},
		},
		{
			// A non-script attribute keeps the nix fence with the delimiters.
			name:    "non-script multi line keeps nix fence",
			src:     "{ motd = ''\n    line one\n    line two\n  ''; }",
			binding: "motd",
			want:    []string{"```nix", "''"},
			wantNot: []string{"```bash"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderFor(t, tt.src, tt.binding)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("render missing %q:\n%s", want, got)
				}
			}
			for _, not := range tt.wantNot {
				if strings.Contains(got, not) {
					t.Errorf("render unexpectedly contains %q:\n%s", not, got)
				}
			}
		})
	}
}

func TestRenderBindingHoverBashFenceTruncates(t *testing.T) {
	// A 12-content-line script: the bash fence keeps 10 lines plus an ellipsis.
	var b strings.Builder
	b.WriteString("{ preStart = ''\n")
	for i := 0; i < 12; i++ {
		b.WriteString("    echo ")
		b.WriteByte(byte('a' + i))
		b.WriteByte('\n')
	}
	b.WriteString("  ''; }")

	got := renderFor(t, b.String(), "preStart")
	if !strings.Contains(got, "```bash") {
		t.Fatalf("render missing bash fence:\n%s", got)
	}
	if !strings.Contains(got, "echo j\n…") {
		t.Errorf("render not truncated after ten lines:\n%s", got)
	}
	if strings.Contains(got, "echo k") {
		t.Errorf("render kept lines beyond the limit:\n%s", got)
	}
}

func TestHandlerValueHoverShellHookBashFence(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// Mirrors the root flake's mkShell shape: shellHook bound to an indented
	// string inside a nested attrset.
	src := "{ pkgs }:\npkgs.mkShell {\n  shellHook = ''\n    echo \"Nix devShell ready. node $(node --version 2>/dev/null)\"\n  '';\n}\n"
	uri := valueHoverWorkspace(t, handler, src)

	line, char := posOf(t, src, "shellHook", 0)
	hover := requestHover(t, handler, uri, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want shellHook bash-fence hover")
	}
	value := hover.Contents.Value
	for _, want := range []string{
		"**shellHook** — attribute",
		"```bash",
		"echo \"Nix devShell ready. node $(node --version 2>/dev/null)\"",
	} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
	if strings.Contains(value, "''") {
		t.Errorf("hover value still contains indented-string delimiters:\n%s", value)
	}
}
