package static

import (
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// bindingDiagnostics parses src, runs the scope analysis, and returns the
// binding diagnostics for it.
func bindingDiagnostics(t *testing.T, src string) []syntax.Diagnostic {
	t.Helper()
	tree, err := syntax.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	return BindingDiagnostics(scopes.Analyze(tree), tree)
}

func TestBindingDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		src  string
		// want lists the diagnostics expected, in position order, as
		// (message, severity) pairs.
		want []wantDiagnostic
	}{
		{
			name: "unused let binding flagged",
			src:  "let x = 1; in 2",
			want: []wantDiagnostic{{`unused binding "x"`, syntax.SeverityWarning}},
		},
		{
			name: "used let binding not flagged",
			src:  "let x = 1; in x",
			want: nil,
		},
		{
			name: "underscore let binding skipped",
			src:  "let _x = 1; in 2",
			want: nil,
		},
		{
			name: "unused inherit entry in let flagged",
			src:  "let inherit (builtins) toString; in 2",
			want: []wantDiagnostic{{`unused binding "toString"`, syntax.SeverityWarning}},
		},
		{
			name: "used inherit entry in let not flagged",
			src:  "let inherit (builtins) toString; in toString 1",
			want: nil,
		},
		{
			name: "function param never flagged",
			src:  "x: 1",
			want: nil,
		},
		{
			name: "formal params never flagged",
			src:  "{ a, b }: 1",
			want: nil,
		},
		{
			name: "rec attr never flagged",
			src:  "rec { a = 1; }",
			want: nil,
		},
		{
			name: "unused rec inherit not flagged",
			src:  "rec { inherit (builtins) toString; }",
			want: nil,
		},
		{
			name: "plain attr key never flagged",
			src:  "{ a = 1; }",
			want: nil,
		},
		{
			name: "duplicate in let flagged as error",
			src:  "let a = 1; a = 2; in a",
			want: []wantDiagnostic{{`duplicate binding "a"`, syntax.SeverityError}},
		},
		{
			name: "duplicate in attrset flagged as error",
			src:  "{ a = 1; a = 2; }",
			want: []wantDiagnostic{{`duplicate binding "a"`, syntax.SeverityError}},
		},
		{
			name: "duplicate in rec flagged as error",
			src:  "rec { a = 1; a = 2; }",
			want: []wantDiagnostic{{`duplicate binding "a"`, syntax.SeverityError}},
		},
		{
			name: "inherit then binding collision flagged",
			// a is in scope (from the let) so the inherit resolves; the only
			// problem is that inherit and binding introduce a into the same set.
			src:  "let a = 1; in { inherit a; a = 2; }",
			want: []wantDiagnostic{{`duplicate binding "a"`, syntax.SeverityError}},
		},
		{
			name: "triplicate flags second and third",
			src:  "{ a = 1; a = 2; a = 3; }",
			want: []wantDiagnostic{
				{`duplicate binding "a"`, syntax.SeverityError},
				{`duplicate binding "a"`, syntax.SeverityError},
			},
		},
		{
			name: "sibling attrsets with same key not flagged",
			src:  "{ a = 1; } // { a = 1; }",
			want: nil,
		},
		{
			name: "merging attrpaths not flagged",
			src:  "{ a.b = 1; a.c = 2; }",
			want: nil,
		},
		{
			name: "identical attrpath twice flagged",
			src:  "{ a.b = 1; a.b = 2; }",
			want: []wantDiagnostic{{`duplicate binding "a.b"`, syntax.SeverityError}},
		},
		{
			name: "dynamic keys skipped",
			src:  "let x = 1; in { ${x} = 1; ${x} = 2; }",
			want: nil,
		},
		{
			name: "bad inherit flagged as error",
			src:  "{ inherit missing; }",
			want: []wantDiagnostic{{`inherit of undefined variable "missing"`, syntax.SeverityError}},
		},
		{
			name: "inherit from unknown target not flagged",
			// The `inherit (expr) name` form implies no outer reference for the
			// name, so an unknown `missing` is never flagged. The source `e` is an
			// ordinary reference, deliberately out of scope for this check.
			src:  "{ inherit (e) missing; }",
			want: nil,
		},
		{
			name: "inherit under with not flagged",
			src:  "with pkgs; { inherit missing; }",
			want: nil,
		},
		{
			name: "resolved inherit not flagged",
			src:  "let a = 1; in { inherit a; }",
			want: nil,
		},
		{
			name: "syntactically broken binding set skipped",
			src:  "{ a = ; a = ; }",
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bindingDiagnostics(t, tc.src)
			assertDiagnostics(t, got, tc.want)
		})
	}
}

type wantDiagnostic struct {
	message  string
	severity syntax.Severity
}

func assertDiagnostics(t *testing.T, got []syntax.Diagnostic, want []wantDiagnostic) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("diagnostics = %+v, want %d (%+v)", got, len(want), want)
	}
	for i, w := range want {
		if got[i].Message != w.message {
			t.Errorf("diagnostic[%d] message = %q, want %q", i, got[i].Message, w.message)
		}
		if got[i].Severity != w.severity {
			t.Errorf("diagnostic[%d] severity = %v, want %v", i, got[i].Severity, w.severity)
		}
	}
}

// TestBindingDiagnosticsUnusedRangeIsName asserts the unused-binding diagnostic
// anchors on the binding name, not the whole binding.
func TestBindingDiagnosticsUnusedRangeIsName(t *testing.T) {
	got := bindingDiagnostics(t, "let unusedName = 1; in 2")
	if len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want 1", got)
	}
	// "unusedName" starts at column 4 ("let ") and is 10 characters long.
	r := got[0].Range
	if r.Start.Line != 0 || r.Start.Character != 4 {
		t.Fatalf("start = %+v, want 0:4", r.Start)
	}
	if r.End.Character != 14 {
		t.Fatalf("end character = %d, want 14", r.End.Character)
	}
}

// TestBindingDiagnosticsNilFile guards the nil-file path.
func TestBindingDiagnosticsNilFile(t *testing.T) {
	if got := BindingDiagnostics(nil, nil); got != nil {
		t.Fatalf("BindingDiagnostics(nil, nil) = %+v, want nil", got)
	}
}
