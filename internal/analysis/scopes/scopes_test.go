package scopes

import (
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// analyze parses src and runs the scope analysis, failing the test on a parse
// error.
func analyze(t *testing.T, src string) *File {
	t.Helper()
	tree, err := syntax.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	return Analyze(tree)
}

// refByName returns the single reference with the given name, failing if the
// count is not exactly one.
func refByName(t *testing.T, f *File, name string) *Reference {
	t.Helper()
	var found *Reference
	count := 0
	for _, r := range f.References {
		if r.Name == name {
			found = r
			count++
		}
	}
	if count != 1 {
		t.Fatalf("references named %q = %d, want 1", name, count)
	}
	return found
}

// bindingByName returns the first binding with the given name, or nil.
func bindingByName(f *File, name string) *Binding {
	for _, b := range f.Bindings {
		if b.Name == name {
			return b
		}
	}
	return nil
}

// bindingByKind returns the first binding with the given name and kind, or nil.
func bindingByKind(f *File, name string, kind BindingKind) *Binding {
	for _, b := range f.Bindings {
		if b.Name == name && b.Kind == kind {
			return b
		}
	}
	return nil
}

func TestResolution(t *testing.T) {
	tests := []struct {
		name string
		src  string
		// ref is the reference name to inspect (must be unique in the source).
		ref string
		// wantDef, when non-empty, is the name of the binding the reference must
		// resolve to; wantKind is that binding's kind.
		wantDef  string
		wantKind BindingKind
		// wantUnresolved requires Target == nil.
		wantUnresolved bool
		// wantUncertain is the expected WithUncertain flag.
		wantUncertain bool
	}{
		{
			name:     "let binding resolves in body",
			src:      "let x = 1; in x",
			ref:      "x",
			wantDef:  "x",
			wantKind: LetBinding,
		},
		{
			name:     "let is mutually recursive",
			src:      "let a = b; b = 1; in a",
			ref:      "b",
			wantDef:  "b",
			wantKind: LetBinding,
		},
		{
			name:     "inner let shadows outer",
			src:      "let x = 1; in let x = 2; in x",
			ref:      "x",
			wantDef:  "x",
			wantKind: LetBinding,
		},
		{
			name:     "rec attr sees sibling",
			src:      "rec { a = 1; b = a; }",
			ref:      "a",
			wantDef:  "a",
			wantKind: RecAttr,
		},
		{
			name:           "plain attr key is not a variable",
			src:            "{ a = 1; b = a; }",
			ref:            "a",
			wantUnresolved: true,
		},
		{
			name:     "simple param resolves",
			src:      "x: x",
			ref:      "x",
			wantDef:  "x",
			wantKind: Param,
		},
		{
			name:     "formal resolves",
			src:      "{ a, b }: b",
			ref:      "b",
			wantDef:  "b",
			wantKind: FormalParam,
		},
		{
			name:     "formal default references sibling formal",
			src:      "{ a, b ? a }: b",
			ref:      "a",
			wantDef:  "a",
			wantKind: FormalParam,
		},
		{
			name:     "at-pattern name resolves",
			src:      "{ a }@args: args",
			ref:      "args",
			wantDef:  "args",
			wantKind: AtPattern,
		},
		{
			name:     "at-pattern before formals",
			src:      "args@{ a }: args",
			ref:      "args",
			wantDef:  "args",
			wantKind: AtPattern,
		},
		{
			name:     "builtin resolves",
			src:      "import ./x.nix",
			ref:      "import",
			wantDef:  "import",
			wantKind: Builtin,
		},
		{
			name:     "true is a builtin",
			src:      "if true then 1 else 2",
			ref:      "true",
			wantKind: Builtin,
			wantDef:  "true",
		},
		{
			name:           "unresolved without with",
			src:            "foo",
			ref:            "foo",
			wantUnresolved: true,
			wantUncertain:  false,
		},
		{
			name:           "unresolved inside with is uncertain",
			src:            "with pkgs; foo",
			ref:            "foo",
			wantUnresolved: true,
			wantUncertain:  true,
		},
		{
			name:     "lexical binding beats with",
			src:      "let foo = 1; in with pkgs; foo",
			ref:      "foo",
			wantDef:  "foo",
			wantKind: LetBinding,
		},
		{
			name:           "with environment resolves outside the with",
			src:            "with pkgs; 1",
			ref:            "pkgs",
			wantUncertain:  false, // no enclosing with around the environment itself
			wantUnresolved: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := analyze(t, tt.src)
			ref := refByName(t, f, tt.ref)

			if tt.wantUnresolved {
				if ref.Target != nil {
					t.Fatalf("Target = %+v, want nil", ref.Target)
				}
			} else {
				if ref.Target == nil {
					t.Fatalf("Target = nil, want binding %q", tt.wantDef)
				}
				if ref.Target.Name != tt.wantDef {
					t.Fatalf("Target.Name = %q, want %q", ref.Target.Name, tt.wantDef)
				}
				if ref.Target.Kind != tt.wantKind {
					t.Fatalf("Target.Kind = %s, want %s", ref.Target.Kind, tt.wantKind)
				}
			}
			if ref.WithUncertain != tt.wantUncertain {
				t.Fatalf("WithUncertain = %v, want %v", ref.WithUncertain, tt.wantUncertain)
			}
		})
	}
}

func TestNestedLetShadowingPicksInnermost(t *testing.T) {
	// The inner `x` binding must be the resolution target, not the outer one.
	f := analyze(t, "let x = 1; in let x = 2; in x")

	ref := refByName(t, f, "x")
	if ref.Target == nil {
		t.Fatalf("Target = nil")
	}
	// The innermost binding sits later in the source, so its name range starts
	// on a greater character offset.
	inner := ref.Target
	var outer *Binding
	for _, b := range f.Bindings {
		if b.Name == "x" && b != inner {
			outer = b
		}
	}
	if outer == nil {
		t.Fatalf("expected two x bindings")
	}
	if !positionLess(outer.NameRange.Start, inner.NameRange.Start) {
		t.Fatalf("resolved to outer binding, want innermost")
	}
	if len(outer.refs) != 0 {
		t.Fatalf("outer binding refs = %d, want 0", len(outer.refs))
	}
}

func TestInherit(t *testing.T) {
	t.Run("bare inherit binds and references outer", func(t *testing.T) {
		// The outer `a` is referenced by the inherit; the inner `a` is defined and
		// then used in the body.
		f := analyze(t, "let a = 1; in let inherit a; in a")

		// Two bindings named a: the outer let and the inherit entry.
		var inheritBinding *Binding
		for _, b := range f.Bindings {
			if b.Name == "a" && b.Kind == InheritEntry {
				inheritBinding = b
			}
		}
		if inheritBinding == nil {
			t.Fatalf("no InheritEntry binding for a")
		}
		// The body reference resolves to the inherit entry (innermost).
		bodyRef := f.References[len(f.References)-1]
		if bodyRef.Name != "a" || bodyRef.Target != inheritBinding {
			t.Fatalf("body ref = %+v, want target inherit entry", bodyRef)
		}
		// The inherit itself produces a reference to the outer a.
		outer := bindingByName(f, "a") // first defined = outer let
		if len(outer.refs) != 1 {
			t.Fatalf("outer a refs = %d, want 1 (the inherit)", len(outer.refs))
		}
	})

	t.Run("inherit from expression binds without outer name reference", func(t *testing.T) {
		f := analyze(t, "let src = {}; in let inherit (src) a b; in a")

		// a and b are bound as inherit entries.
		if b := bindingByName(f, "a"); b == nil || b.Kind != InheritEntry {
			t.Fatalf("a binding = %+v, want InheritEntry", b)
		}
		// The source `src` is referenced exactly once, and no reference is created
		// for the inherited names a/b themselves (only the body use of a).
		src := bindingByName(f, "src")
		if len(src.refs) != 1 {
			t.Fatalf("src refs = %d, want 1", len(src.refs))
		}
		aRefs := 0
		for _, r := range f.References {
			if r.Name == "a" {
				aRefs++
			}
		}
		if aRefs != 1 { // only the body `a`, not the inherited name
			t.Fatalf("references to a = %d, want 1", aRefs)
		}
	})

	t.Run("inherit in plain attrset records attr binding", func(t *testing.T) {
		f := analyze(t, "let a = 1; in { inherit a; }")

		var attr *Binding
		for _, b := range f.Bindings {
			if b.Name == "a" && b.Kind == AttrBinding {
				attr = b
			}
		}
		if attr == nil {
			t.Fatalf("no AttrBinding for inherited a")
		}
		// The outer a is still referenced by the inherit.
		outer := bindingByName(f, "a")
		if len(outer.refs) != 1 {
			t.Fatalf("outer a refs = %d, want 1", len(outer.refs))
		}
	})
}

func TestUnusedBindings(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		wantUnused []string
	}{
		{
			name:       "used let binding",
			src:        "let x = 1; in x",
			wantUnused: nil,
		},
		{
			name:       "unused let binding",
			src:        "let x = 1; y = 2; in x",
			wantUnused: []string{"y"},
		},
		{
			name:       "unused function param",
			src:        "x: 1",
			wantUnused: []string{"x"},
		},
		{
			name:       "plain attr keys are never unused",
			src:        "{ a = 1; b = 2; }",
			wantUnused: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := analyze(t, tt.src)
			var got []string
			for _, b := range f.UnusedBindings() {
				got = append(got, b.Name)
			}
			if len(got) != len(tt.wantUnused) {
				t.Fatalf("unused = %v, want %v", got, tt.wantUnused)
			}
			for i := range got {
				if got[i] != tt.wantUnused[i] {
					t.Fatalf("unused = %v, want %v", got, tt.wantUnused)
				}
			}
		})
	}
}

func TestAttributePaths(t *testing.T) {
	t.Run("nested attrpath records full path and binds first segment", func(t *testing.T) {
		f := analyze(t, "rec { a.b.c = 1; d = a; }")

		a := bindingByName(f, "a")
		if a == nil {
			t.Fatalf("no binding for a")
		}
		if a.AttrPath != "a.b.c" {
			t.Fatalf("AttrPath = %q, want a.b.c", a.AttrPath)
		}
		if a.Name != "a" {
			t.Fatalf("Name = %q, want a", a.Name)
		}
		// `d = a` resolves against the rec first segment.
		ref := refByName(t, f, "a")
		if ref.Target != a {
			t.Fatalf("a reference did not resolve to the rec binding")
		}
	})

	t.Run("dynamic attr does not resolve but its key references do", func(t *testing.T) {
		// `${d}` is a computed key: the binding is Dynamic and must not be a
		// resolution target, but the interpolated `d` is a real reference.
		f := analyze(t, "let d = \"k\"; in { ${d} = 1; }")

		var dyn *Binding
		for _, b := range f.Bindings {
			if b.Dynamic {
				dyn = b
			}
		}
		if dyn == nil {
			t.Fatalf("no dynamic binding recorded")
		}
		// The interpolated d references the let binding.
		ref := refByName(t, f, "d")
		if ref.Target == nil || ref.Target.Name != "d" || ref.Target.Kind != LetBinding {
			t.Fatalf("interpolated d ref = %+v, want let binding d", ref.Target)
		}
	})
}

func TestDefaultsSeeAtPattern(t *testing.T) {
	// A formal default may reference the @-pattern name.
	f := analyze(t, "{ a ? args }@args: a")
	ref := refByName(t, f, "args")
	if ref.Target == nil || ref.Target.Kind != AtPattern {
		t.Fatalf("args ref = %+v, want AtPattern", ref.Target)
	}
}

func TestReferencesToAndBackpointers(t *testing.T) {
	f := analyze(t, "let x = 1; in [ x x x ]")

	x := bindingByName(f, "x")
	if x == nil {
		t.Fatalf("no binding x")
	}
	if got := len(f.ReferencesTo(x)); got != 3 {
		t.Fatalf("ReferencesTo = %d, want 3", got)
	}
	if got := len(x.References()); got != 3 {
		t.Fatalf("References() = %d, want 3", got)
	}
	if x.Unused() {
		t.Fatalf("x reported unused, want used")
	}
}

func TestBindingAtAndReferenceAt(t *testing.T) {
	src := "let x = 1; in x"
	f := analyze(t, src)

	// Definition site: `x` is at character offset 4.
	def := f.BindingAt(syntax.Position{Line: 0, Character: 4})
	if def == nil || def.Name != "x" || def.Kind != LetBinding {
		t.Fatalf("BindingAt(def) = %+v, want let binding x", def)
	}
	// Use site: the body `x` is the last character.
	use := f.ReferenceAt(syntax.Position{Line: 0, Character: 14})
	if use == nil || use.Name != "x" {
		t.Fatalf("ReferenceAt(use) = %+v, want reference x", use)
	}
	if use.Target != def {
		t.Fatalf("use does not point at the definition binding")
	}
	// A position in whitespace resolves to nothing.
	if b := f.BindingAt(syntax.Position{Line: 0, Character: 3}); b != nil {
		t.Fatalf("BindingAt(whitespace) = %+v, want nil", b)
	}
}

func TestScopeTree(t *testing.T) {
	// let > function > with, each nested in the previous.
	f := analyze(t, "let a = 1; in x: with pkgs; a")

	kinds := map[ScopeKind]int{}
	for _, s := range f.Scopes {
		kinds[s.Kind]++
	}
	for _, want := range []ScopeKind{ScopeRoot, ScopeLet, ScopeFunction, ScopeWith} {
		if kinds[want] == 0 {
			t.Fatalf("missing scope kind %s in %v", want, kinds)
		}
	}
	// The with scope's parent chain must reach the let scope, so `a` resolves.
	ref := refByName(t, f, "a")
	if ref.Target == nil || ref.Target.Kind != LetBinding {
		t.Fatalf("a did not resolve through the scope chain: %+v", ref.Target)
	}
}

func TestSyntaxErrorsDoNotPanic(t *testing.T) {
	tests := []string{
		"let x = in x",       // missing value
		"{ a = ; }",          // empty value
		"x: ",                // missing body
		"let inherit ; in a", // malformed inherit
		"with ; foo",         // missing environment
		"((((",               // unbalanced
		"let a = b",          // no `in`
		"",                   // empty file
		"# just a comment\n", // no expression
		"rec { ${} = 1; }",   // empty interpolation key
	}
	for _, src := range tests {
		t.Run(src, func(t *testing.T) {
			// Must not panic and must return a non-nil file.
			f := analyze(t, src)
			if f == nil {
				t.Fatalf("Analyze returned nil")
			}
			_ = f.UnusedBindings()
		})
	}
}

func TestNilTree(t *testing.T) {
	f := Analyze(nil)
	if f == nil || f.Root == nil {
		t.Fatalf("Analyze(nil) = %+v, want non-nil file with root", f)
	}
}

func TestRealisticFlake(t *testing.T) {
	src := `{
  description = "A realistic flake";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "1.0.0";
        deps = with pkgs; [ hello cowsay ];
      in
      {
        packages.default = pkgs.stdenv.mkDerivation {
          pname = "demo";
          inherit version;
          buildInputs = deps;
          src = self;
        };

        devShells.default = pkgs.mkShell {
          packages = deps;
          shellHook = ''
            echo "welcome to ${version}"
          '';
        };
      });
}`

	f := analyze(t, src)

	// The outputs function formals must be in scope.
	for _, name := range []string{"self", "nixpkgs", "flake-utils"} {
		b := bindingByKind(f, name, FormalParam)
		if b == nil {
			t.Fatalf("formal %q not found as FormalParam", name)
		}
	}

	// `system` is the lambda parameter of eachDefaultSystem's callback.
	if b := bindingByName(f, "system"); b == nil || b.Kind != Param {
		t.Fatalf("system = %+v, want Param", b)
	}

	// `pkgs` is a let binding referenced several times (mkDerivation, mkShell,
	// and inside the `with pkgs` for deps).
	pkgs := bindingByName(f, "pkgs")
	if pkgs == nil || pkgs.Kind != LetBinding {
		t.Fatalf("pkgs = %+v, want LetBinding", pkgs)
	}
	if len(pkgs.refs) < 2 {
		t.Fatalf("pkgs refs = %d, want >= 2", len(pkgs.refs))
	}

	// `nixpkgs` (the formal) is referenced by `import nixpkgs`.
	nixpkgs := bindingByKind(f, "nixpkgs", FormalParam)
	if nixpkgs == nil || len(nixpkgs.refs) == 0 {
		t.Fatalf("nixpkgs formal is never referenced, expected an import use")
	}

	// `import` resolves to a builtin.
	imp := refByName(t, f, "import")
	if imp.Target == nil || imp.Target.Kind != Builtin {
		t.Fatalf("import ref = %+v, want builtin", imp.Target)
	}

	// `deps` is referenced by buildInputs and by the devShell packages.
	deps := bindingByName(f, "deps")
	if deps == nil || len(deps.refs) != 2 {
		t.Fatalf("deps refs = %+v, want 2", deps)
	}

	// `version` is used via `inherit version;` and via ${version} interpolation.
	version := bindingByName(f, "version")
	if version == nil || len(version.refs) < 2 {
		t.Fatalf("version refs = %+v, want >= 2", version)
	}

	// Inside `with pkgs`, hello and cowsay are unresolved but uncertain.
	hello := refByName(t, f, "hello")
	if hello.Target != nil || !hello.WithUncertain {
		t.Fatalf("hello = %+v, want unresolved + uncertain", hello)
	}

	// Nothing should have panicked and the scope tree should have several scopes.
	if len(f.Scopes) < 4 {
		t.Fatalf("scopes = %d, want several", len(f.Scopes))
	}
}
