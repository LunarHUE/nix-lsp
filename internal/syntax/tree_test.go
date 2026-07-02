package syntax

import "testing"

func TestParseReturnsTree(t *testing.T) {
	tree, err := Parse([]byte(`{ foo = import ./bar.nix; }`))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	if got := tree.Root().Kind(); got != "source_code" {
		t.Fatalf("root kind = %q, want source_code", got)
	}
}

func TestWalkAndTypedWrappers(t *testing.T) {
	tree, err := Parse([]byte(`import ./bar.nix`))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}

	var sawApply bool
	var sawPath bool
	tree.Walk(func(node Node) bool {
		if apply, ok := AsApply(node); ok {
			sawApply = true
			if got := apply.Function().Text(); got != "import" {
				t.Fatalf("apply function = %q, want import", got)
			}
			if got := apply.Argument().Text(); got != "./bar.nix" {
				t.Fatalf("apply argument = %q, want ./bar.nix", got)
			}
		}
		if path, ok := AsPathLiteral(node); ok {
			sawPath = true
			if got := path.Text(); got != "./bar.nix" {
				t.Fatalf("path text = %q, want ./bar.nix", got)
			}
		}
		return true
	})

	if !sawApply {
		t.Fatal("did not see apply_expression")
	}
	if !sawPath {
		t.Fatal("did not see path_expression")
	}
}

func TestDiagnosticsFromTreeSitterErrors(t *testing.T) {
	tree, err := Parse([]byte(`{ foo = ; }`))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}

	diagnostics := tree.Diagnostics()
	if len(diagnostics) == 0 {
		t.Fatal("expected syntax diagnostics")
	}
	if diagnostics[0].Range.Start.Line != 0 {
		t.Fatalf("diagnostic line = %d, want 0", diagnostics[0].Range.Start.Line)
	}
}

func TestReparseUsesNewContent(t *testing.T) {
	tree, err := Parse([]byte(`import ./one.nix`))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}

	reparsed, err := Reparse(tree, []Edit{{NewText: []byte(`import ./two.nix`)}}, []byte(`import ./two.nix`))
	if err != nil {
		t.Fatalf("Reparse error = %v", err)
	}
	if got := reparsed.Root().Text(); got != "import ./two.nix" {
		t.Fatalf("root text = %q, want updated content", got)
	}
}

func TestPositionAtUsesUTF16Characters(t *testing.T) {
	content := []byte("a😀b")
	position := PositionAt(content, len("a😀"))
	if position.Line != 0 || position.Character != 3 {
		t.Fatalf("position = %+v, want line 0 character 3", position)
	}
}
