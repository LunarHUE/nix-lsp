package static

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	importedges "github.com/wesleybaldwin/nix-lsp/internal/analysis/imports"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestFileDiagnosticsMissingImport(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	workspace := project.Workspace{Root: normalize(t, root)}

	diagnostics, err := FileDiagnostics(workspace, source, parse(t, "import ./missing.nix"))
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if !strings.Contains(diagnostics[0].Message, "missing import target") {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}
	if diagnostics[0].Code != CodeMissingImport {
		t.Fatalf("code = %q, want %q", diagnostics[0].Code, CodeMissingImport)
	}
}

func TestFileDiagnosticsUntrackedFlakeImport(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./module.nix")
	target := writeFile(t, filepath.Join(root, "module.nix"), "{}")
	workspace := project.Workspace{
		Root:     normalize(t, root),
		HasFlake: true,
		HasGit:   true,
		Files: []project.File{
			{Path: normalize(t, source), GitTracked: true},
			{Path: normalize(t, target), GitTracked: false},
		},
	}

	diagnostics, err := FileDiagnostics(workspace, source, parse(t, "import ./module.nix"))
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if !strings.Contains(diagnostics[0].Message, "not git-tracked") {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}
	if diagnostics[0].Code != CodeUntrackedImport {
		t.Fatalf("code = %q, want %q", diagnostics[0].Code, CodeUntrackedImport)
	}
}

// TestBindingDiagnosticCodes asserts each binding diagnostic kind carries its
// stable machine-readable code, so the code-action handler and clients can key
// on them.
func TestBindingDiagnosticCodes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		code string
	}{
		{"unused", "let x = 1; in 2", CodeUnusedBinding},
		{"duplicate", "{ a = 1; a = 2; }", CodeDuplicateBinding},
		{"bad inherit", "{ inherit missing; }", CodeBadInherit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bindingDiagnostics(t, tc.src)
			if len(got) == 0 {
				t.Fatalf("diagnostics = none, want one with code %q", tc.code)
			}
			if got[0].Code != tc.code {
				t.Fatalf("code = %q, want %q", got[0].Code, tc.code)
			}
		})
	}
}

// TestSyntaxDiagnosticCodes asserts ERROR and MISSING nodes are coded.
func TestSyntaxDiagnosticCodes(t *testing.T) {
	// An unterminated attrset yields a missing-syntax diagnostic (missing `}`).
	diagnostics := parse(t, "{").Diagnostics()
	if len(diagnostics) == 0 {
		t.Fatal("diagnostics = none, want a syntax diagnostic")
	}
	for _, d := range diagnostics {
		if d.Code != "syntax-error" && d.Code != "missing-syntax" {
			t.Fatalf("code = %q, want syntax-error or missing-syntax", d.Code)
		}
	}
}

// TestSyntaxErrorHints asserts recognizable ERROR shapes gain an enriched
// message while unrecognized ones keep the generic "syntax error", and that the
// enrichment never invents a diagnostic where the parser reports none.
func TestSyntaxErrorHints(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string // exact message expected on at least one syntax-error diagnostic
	}{
		{
			name: "bare attribute in binding position",
			src:  "networking.wireguard.interfaces = {\n    wg0\n}\n",
			want: "syntax error: attribute 'wg0' has no value (expected 'wg0 = <value>;')",
		},
		{
			name: "lone identifier in attrset",
			src:  "{ foo }",
			want: "syntax error: attribute 'foo' has no value (expected 'foo = <value>;')",
		},
		{
			// The user's real case: a bare name in a nested `{ }` value alongside other
			// bindings, where the ERROR is an attrpath followed by a single-formal formals.
			name: "bare attribute in nested attrset value",
			src:  "{\n  services.foo.enable = true;\n  networking.wireguard.interfaces = {\n    wg0\n  }\n}\n",
			want: "syntax error: attribute 'wg0' has no value (expected 'wg0 = <value>;')",
		},
		{
			// A deleted `;` makes the parser swallow the next binding's name into
			// the first binding's value; the hint names the swallowed identifier.
			name: "missing semicolon between bindings",
			src:  "{ foo = 1 bar = 2; }",
			want: "syntax error: missing ';' before 'bar'",
		},
		{
			// A set's last binding missing its `;` leaves an anonymous zero-width
			// MISSING ";" token (invisible to a named-only walk); it must surface
			// as the classified hint.
			name: "missing semicolon before closing brace",
			src:  "{ foo = 1 }",
			want: "syntax error: missing ';' after binding",
		},
		{
			name: "unrecognized error keeps generic message",
			src:  "foo = ;",
			want: "syntax error",
		},
		{
			name: "two formals are not a bare attribute",
			src:  "{ a, b }",
			want: "syntax error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := parse(t, tc.src).Diagnostics()
			found := false
			for _, d := range diagnostics {
				if d.Code == "syntax-error" && d.Message == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("no syntax-error diagnostic with message %q; got %+v", tc.want, diagnostics)
			}
		})
	}
}

// userExactSnippet is the verbatim buffer from the misclassification report: a
// wg0 submodule binding missing the `;` after its inner closing brace.
const userExactSnippet = "networking.wireguard.interfaces = {\n    wg0 = {\n      \n    }\n  };"

// TestSyntaxErrorHintsUserExactSnippet is the regression for the report of a
// truncated name ('wg' for a buffer that says 'wg0'): the user's exact text, in
// the module wrapper it lives in, must produce exactly the classified missing-';'
// hint — and no diagnostic may ever carry a name-bearing message here, truncated
// or otherwise. The missing `;` before the inner `}` is recorded by the parser as
// an anonymous zero-width MISSING ";" token on the wg0 binding.
func TestSyntaxErrorHintsUserExactSnippet(t *testing.T) {
	src := "{ config, ... }:\n{\n  " + userExactSnippet + "\n}\n"
	diagnostics := parse(t, src).Diagnostics()
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want exactly the missing-';' hint", diagnostics)
	}
	d := diagnostics[0]
	if want := "syntax error: missing ';' after binding"; d.Message != want {
		t.Fatalf("message = %q, want %q", d.Message, want)
	}
	if strings.Contains(d.Message, "attribute '") {
		t.Fatalf("message %q names an attribute; must never name one here", d.Message)
	}
	// The zero-width range sits right after the inner `}` where the `;` belongs.
	if d.Range.Start != d.Range.End {
		t.Fatalf("range = %+v, want zero-width", d.Range)
	}
	if d.Range.Start.Line != 5 || d.Range.Start.Character != 5 {
		t.Fatalf("range start = %+v, want line 5 char 5 (after the inner '}')", d.Range.Start)
	}
}

// TestSyntaxErrorHintsUserExactSnippetTopLevel runs the same buffer as a whole
// file (no module wrapper): recovery differs (the outer `};` is swallowed, a
// stray `}` ERROR lands inside the binding, and the outer set's own `}` goes
// missing), but every message must still be a missing-';', expected-token, or
// generic one — never a name-bearing hint.
func TestSyntaxErrorHintsUserExactSnippetTopLevel(t *testing.T) {
	diagnostics := parse(t, userExactSnippet+"\n").Diagnostics()
	if len(diagnostics) == 0 {
		t.Fatal("diagnostics = none, want syntax errors (invalid at top level)")
	}
	allowed := map[string]bool{
		"syntax error": true,
		"syntax error: missing ';' after binding": true,
		"syntax error: expected '}'":              true,
	}
	for _, d := range diagnostics {
		if !allowed[d.Message] {
			t.Errorf("message = %q, want a missing-';', expected-token, or generic message", d.Message)
		}
	}
}

// userLetSnippet is the verbatim let-binding buffer from the wrong-anchor
// report: the `;` after the `pkgs = import nixpkgs { ... }` binding is deleted,
// which makes the parser swallow `corePackages` into the pkgs value.
const userLetSnippet = "let\n" +
	"  pkgs = import nixpkgs {\n" +
	"    inherit system;\n" +
	"    config.allowUnfree = true;\n" +
	"    overlays = [ claude-code.overlays.default ];\n" +
	"  }\n" +
	"  corePackages = with pkgs; [\n" +
	"    nixpkgs-fmt\n" +
	"    deadnix\n" +
	"  ];\n" +
	"in corePackages\n"

// TestSyntaxErrorHintsSwallowedLetBinding is the regression for the
// wrong-anchor report: the missing-';' diagnostic must name the swallowed
// identifier and anchor at the end of the real value (the `}` line), never on a
// later line, and exactly one missing-';' diagnostic must describe the mistake.
func TestSyntaxErrorHintsSwallowedLetBinding(t *testing.T) {
	diagnostics := parse(t, userLetSnippet).Diagnostics()
	want := "syntax error: missing ';' before 'corePackages'"
	var hits []syntax.Diagnostic
	for _, d := range diagnostics {
		if strings.Contains(d.Message, "missing ';'") {
			hits = append(hits, d)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("missing-';' diagnostics = %+v, want exactly one", hits)
	}
	d := hits[0]
	if d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
	// The `}` closing the pkgs value is on line 5; corePackages is on line 6.
	// The anchor must be zero-width at the end of the value, not on the
	// corePackages/with line or anywhere later.
	if d.Range.Start != d.Range.End {
		t.Errorf("range = %+v, want zero-width", d.Range)
	}
	if d.Range.Start.Line != 5 || d.Range.Start.Character != 3 {
		t.Errorf("anchor = %+v, want line 5 char 3 (end of the '}' value)", d.Range.Start)
	}
}

// TestSyntaxErrorHintsSwallowedBindingVariants covers the swallow recovery
// across binding sites and value shapes: let and attrset bindings, values
// ending in `}` / `]` / a function application, a swallowed multi-segment
// attrpath (named by its first token), and the mistake being independent of
// whether sibling bindings follow.
func TestSyntaxErrorHintsSwallowedBindingVariants(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"let value ends in bracket", "let\n  a = [ 1 2 ]\n  b = 3;\nin a\n",
			"syntax error: missing ';' before 'b'"},
		{"let value is application", "let\n  a = f x\n  b = 3;\nin a\n",
			"syntax error: missing ';' before 'b'"},
		{"attrset value ends in brace", "{\n  a = { x = 1; }\n  b = 3;\n}\n",
			"syntax error: missing ';' before 'b'"},
		{"swallowed multi-segment attrpath", "{\n  a = { x = 1; }\n  b.c = 3;\n}\n",
			"syntax error: missing ';' before 'b'"},
		{"no sibling after the swallowed binding", "let\n  a = [ 1 2 ]\n  b = 3;\nin a\n",
			"syntax error: missing ';' before 'b'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			for _, d := range parse(t, tc.src).Diagnostics() {
				if d.Message == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("no diagnostic with message %q; got %+v", tc.want, parse(t, tc.src).Diagnostics())
			}
		})
	}
}

// TestSyntaxExpectedTokenMessages asserts anonymous MISSING tokens surface as
// precise expected-token diagnostics, unclosed-delimiter ERROR recoveries are
// classified, and a named MISSING node reports its expected kind.
func TestSyntaxExpectedTokenMessages(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"unclosed attrset", "{ foo = 1;", "syntax error: expected '}'"},
		{"unclosed list", "[ 1 2", "syntax error: expected ']'"},
		{"unclosed parens", "( 1 + 2", "syntax error: expected ')'"},
		{"lone open brace", "{", "syntax error: unclosed '{' (expected '}')"},
		{"lone open bracket", "[", "syntax error: unclosed '[' (expected ']')"},
		{"lone open paren", "(", "syntax error: unclosed '(' (expected ')')"},
		{"unterminated string", "\"abc", "syntax error: unclosed '\"' (expected '\"')"},
		{"missing value", "{ x = ;}", "syntax error: expected identifier"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := parse(t, tc.src).Diagnostics()
			found := false
			for _, d := range diagnostics {
				if d.Message == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("no diagnostic with message %q; got %+v", tc.want, diagnostics)
			}
		})
	}
}

// TestSyntaxGenericShadowedByPrecise asserts the dedupe: when a precise
// expected-token or classified diagnostic sits inside a broad generic ERROR
// region, the generic diagnostic is dropped and only precise messages remain.
func TestSyntaxGenericShadowedByPrecise(t *testing.T) {
	// The user's let snippet wraps the swallowed-binding ERROR inside a broad
	// generic ERROR spanning the whole let head; the generic must be gone.
	for _, d := range parse(t, userLetSnippet).Diagnostics() {
		if d.Message == "syntax error" && d.Range.Start.Line == 0 {
			t.Errorf("broad generic ERROR survived dedupe: %+v", d)
		}
	}
}

// TestSyntaxErrorHintsNeverTruncatedName asserts the full-token proof: an ERROR
// wrapping a lone identifier hint always names the complete token, so on every
// probe the reported name, if any, is a full identifier of the source.
func TestSyntaxErrorHintsNeverTruncatedName(t *testing.T) {
	probes := []string{
		"{\n  networking.wireguard.interfaces = {\n    wg0\n  };\n}\n",
		"{ config, ... }:\n{\n  networking.wireguard.interfaces = {\n    wg\n  };\n}\n",
	}
	for _, src := range probes {
		for _, d := range parse(t, src).Diagnostics() {
			start := strings.Index(d.Message, "attribute '")
			if start < 0 {
				continue
			}
			rest := d.Message[start+len("attribute '"):]
			end := strings.Index(rest, "'")
			if end < 0 {
				t.Fatalf("unterminated name in %q", d.Message)
			}
			name := rest[:end]
			// The named attribute must appear in the source as a complete token
			// (not merely as a prefix of a longer identifier).
			if !containsFullToken(src, name) {
				t.Errorf("message %q names %q, which is not a full token of %q", d.Message, name, src)
			}
		}
	}
}

// containsFullToken reports whether name occurs in src with non-identifier (or
// boundary) bytes on both sides.
func containsFullToken(src, name string) bool {
	for from := 0; ; {
		i := strings.Index(src[from:], name)
		if i < 0 {
			return false
		}
		i += from
		before := i == 0 || !isIdentByte(src[i-1])
		after := i+len(name) == len(src) || !isIdentByte(src[i+len(name)])
		if before && after {
			return true
		}
		from = i + 1
	}
}

func isIdentByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '\'' || b == '-':
		return true
	default:
		return false
	}
}

// TestSyntaxErrorHintsNoFalsePositive confirms a valid function whose formals
// resemble the lone-identifier shape parses cleanly, so the enrichment adds no
// diagnostic of its own.
func TestSyntaxErrorHintsNoFalsePositive(t *testing.T) {
	for _, src := range []string{"{ pkgs }: pkgs", "{ config, ... }: { }", "{ a = 1; }"} {
		if diagnostics := parse(t, src).Diagnostics(); len(diagnostics) != 0 {
			t.Errorf("%q: got %+v, want no diagnostics", src, diagnostics)
		}
	}
}

// TestShouldWarnUntracked covers the exported predicate the code-action handler
// reuses to decide where the quick fix applies.
func TestShouldWarnUntracked(t *testing.T) {
	root := normalize(t, t.TempDir())
	target := filepath.Join(root, "lib.nix")
	base := project.Workspace{Root: root, HasFlake: true, HasGit: true}

	untracked := importedges.Edge{TargetPath: target, Exists: true, GitTracked: false}
	if !ShouldWarnUntracked(base, untracked) {
		t.Fatal("ShouldWarnUntracked(untracked) = false, want true")
	}

	tracked := untracked
	tracked.GitTracked = true
	if ShouldWarnUntracked(base, tracked) {
		t.Fatal("ShouldWarnUntracked(tracked) = true, want false")
	}

	noFlake := base
	noFlake.HasFlake = false
	if ShouldWarnUntracked(noFlake, untracked) {
		t.Fatal("ShouldWarnUntracked(no flake) = true, want false")
	}

	outside := importedges.Edge{TargetPath: normalize(t, filepath.Join(t.TempDir(), "other.nix")), Exists: true}
	if ShouldWarnUntracked(base, outside) {
		t.Fatal("ShouldWarnUntracked(outside root) = true, want false")
	}
}

func TestFileDiagnosticsNoUntrackedWarningOutsideFlakeGitWorkspace(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./module.nix")
	target := writeFile(t, filepath.Join(root, "module.nix"), "{}")
	workspace := project.Workspace{
		Root:     normalize(t, root),
		HasFlake: true,
		HasGit:   false,
		Files: []project.File{
			{Path: normalize(t, source), GitTracked: true},
			{Path: normalize(t, target), GitTracked: false},
		},
	}

	diagnostics, err := FileDiagnostics(workspace, source, parse(t, "import ./module.nix"))
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none", diagnostics)
	}
}

func TestWorkspaceDiagnosticsReadsSnapshot(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	uri, err := vfs.PathToURI(source)
	if err != nil {
		t.Fatalf("PathToURI error = %v", err)
	}
	workspace := project.Workspace{
		Root: normalize(t, root),
		Files: []project.File{
			{Path: normalize(t, source), URI: uri, GitTracked: true},
		},
	}

	diagnostics := WorkspaceDiagnostics(workspace, vfs.New().Snapshot())
	if got := len(diagnostics[uri]); got != 1 {
		t.Fatalf("diagnostics for uri = %d, want 1", got)
	}
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	return path
}

func normalize(t *testing.T, path string) string {
	t.Helper()
	normalized, err := vfs.NormalizePath(path)
	if err != nil {
		t.Fatalf("NormalizePath error = %v", err)
	}
	return normalized
}

func parse(t *testing.T, content string) *syntax.Tree {
	t.Helper()
	tree, err := syntax.Parse([]byte(content))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	return tree
}
