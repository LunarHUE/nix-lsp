package scopes

import (
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// parse parses src, failing on error.
func parse(t *testing.T, src string) *syntax.Tree {
	t.Helper()
	tree, err := syntax.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	return tree
}

// byteOffset returns the byte offset of an LSP position in ASCII src.
func byteOffset(src string, p syntax.Position) int {
	line, col := 0, 0
	for i := 0; i < len(src); i++ {
		if line == p.Line && col == p.Character {
			return i
		}
		if src[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return len(src)
}

// textAt returns the source text covered by r (ASCII only).
func textAt(src string, r syntax.Range) string {
	return src[byteOffset(src, r.Start):byteOffset(src, r.End)]
}

func TestResolveAttrPath(t *testing.T) {
	tests := []struct {
		name string
		src  string
		path []string
		want string // covered text of the result range; "" means not found
	}{
		{"plain attrset", `{ foo = 1; bar = 2; }`, []string{"foo"}, "foo"},
		{"rec attrset", `rec { foo = 1; bar = foo; }`, []string{"bar"}, "bar"},
		{"function wrapped", `{ ... }: { hello = 1; }`, []string{"hello"}, "hello"},
		{"let-in wrapped", `let x = 1; in { y = 2; }`, []string{"y"}, "y"},
		{"parenthesized", `({ z = 1; })`, []string{"z"}, "z"},
		{"nested descend leaf", `{ a = { b = 1; }; }`, []string{"a", "b"}, "b"},
		{"nested descend interior", `{ a = { b = 1; }; }`, []string{"a"}, "a"},
		{"attrpath sugar", `{ a.b = 1; }`, []string{"a", "b"}, "a.b"},
		{"inherit entry", `{ inherit foo; }`, []string{"foo"}, "foo"},
		{"inherit-from entry", `{ inherit (src) foo; }`, []string{"foo"}, "foo"},
		{"dynamic key skipped", `{ ${p} = 1; }`, []string{"p"}, ""},
		{"non-attrset top level", `42`, []string{"foo"}, ""},
		{"function body not attrset", `x: x`, []string{"foo"}, ""},
		{"missing attr", `{ foo = 1; }`, []string{"bar"}, ""},
		{"descend into non-attrset", `{ a = 1; }`, []string{"a", "b"}, ""},
		{"empty path", `{ foo = 1; }`, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := parse(t, tc.src)
			r, ok := ResolveAttrPath(tree, tc.path)
			if tc.want == "" {
				if ok {
					t.Fatalf("ResolveAttrPath ok = true (range %q), want not found", textAt(tc.src, r))
				}
				return
			}
			if !ok {
				t.Fatalf("ResolveAttrPath ok = false, want %q", tc.want)
			}
			if got := textAt(tc.src, r); got != tc.want {
				t.Fatalf("ResolveAttrPath range text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBindingValueRange(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		binding string
		kind    BindingKind
		want    string // covered value text; "" means not found
	}{
		{"let binding", `let a = 42; in a`, "a", LetBinding, "42"},
		{"rec attr", `rec { a = 42; }`, "a", RecAttr, "42"},
		{"plain attr", `{ a = 42; }`, "a", AttrBinding, "42"},
		{"nested attr value", `{ a = { b = 1; }; }`, "a", AttrBinding, "{ b = 1; }"},
		{"inherit entry not found", `let inherit (x) a; in a`, "a", InheritEntry, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := parse(t, tc.src)
			file := Analyze(tree)
			b := bindingByKind(file, tc.binding, tc.kind)
			if b == nil {
				t.Fatalf("binding %q kind %v not found", tc.binding, tc.kind)
			}
			r, ok := BindingValueRange(tree, b)
			if tc.want == "" {
				if ok {
					t.Fatalf("BindingValueRange ok = true (range %q), want not found", textAt(tc.src, r))
				}
				return
			}
			if !ok {
				t.Fatalf("BindingValueRange ok = false, want %q", tc.want)
			}
			if got := textAt(tc.src, r); got != tc.want {
				t.Fatalf("BindingValueRange text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAttrsetValueResolve(t *testing.T) {
	src := `let cfg = { port = 80; }; in cfg.port`
	tree := parse(t, src)
	file := Analyze(tree)
	b := bindingByKind(file, "cfg", LetBinding)
	if b == nil {
		t.Fatal("cfg binding not found")
	}
	valueRange, ok := BindingValueRange(tree, b)
	if !ok {
		t.Fatal("BindingValueRange(cfg) not found")
	}
	r, ok := AttrsetValueResolve(tree, valueRange, []string{"port"})
	if !ok {
		t.Fatal("AttrsetValueResolve ok = false")
	}
	if got := textAt(src, r); got != "port" {
		t.Fatalf("AttrsetValueResolve text = %q, want %q", got, "port")
	}

	// Missing attr under a valid attrset value yields not found.
	if _, ok := AttrsetValueResolve(tree, valueRange, []string{"nope"}); ok {
		t.Fatal("AttrsetValueResolve for missing attr ok = true, want false")
	}
}
