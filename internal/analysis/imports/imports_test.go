package imports

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestAnalyzeImportPath(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./module.nix")
	target := writeFile(t, filepath.Join(root, "module.nix"), "{}")
	tracked := map[string]bool{normalize(t, target): true}

	edges, err := analyze(t, source, "import ./module.nix", tracked)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].Literal != "./module.nix" || !edges[0].Exists || !edges[0].GitTracked {
		t.Fatalf("edge = %+v", edges[0])
	}
	if edges[0].Range.Start.Character != len("import ") {
		t.Fatalf("range start = %d, want %d", edges[0].Range.Start.Character, len("import "))
	}
}

func TestAnalyzeImportsList(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "configuration.nix"), "{ imports = [ ./a.nix ./b.nix ]; }")
	writeFile(t, filepath.Join(root, "a.nix"), "{}")
	writeFile(t, filepath.Join(root, "b.nix"), "{}")

	edges, err := analyze(t, source, "{ imports = [ ./a.nix ./b.nix ]; }", nil)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(edges))
	}
	if edges[0].Literal != "./a.nix" || edges[1].Literal != "./b.nix" {
		t.Fatalf("edges = %+v", edges)
	}
}

func TestAnalyzeCallPackageDirectoryDefault(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "pkgs.callPackage ./pkg {}")
	target := writeFile(t, filepath.Join(root, "pkg", "default.nix"), "{}")

	edges, err := analyze(t, source, "pkgs.callPackage ./pkg {}", map[string]bool{normalize(t, target): true})
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].TargetPath != normalize(t, target) {
		t.Fatalf("target = %q, want %q", edges[0].TargetPath, normalize(t, target))
	}
}

func TestAnalyzeMissingImport(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")

	edges, err := analyze(t, source, "import ./missing.nix", nil)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].Exists {
		t.Fatalf("edge exists = true, want false")
	}
	if edges[0].TargetPath != normalize(t, filepath.Join(root, "missing.nix")) {
		t.Fatalf("target = %q", edges[0].TargetPath)
	}
}

func TestAnalyzeIgnoresCommentsAndStrings(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "")
	content := []byte(`# import ./commented.nix
"import ./string.nix"
`)

	tree, err := syntax.Parse(content)
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	edges, err := Analyze(source, tree, nil)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("edges = %+v, want none", edges)
	}
}

func TestAnalyzeImportParenthesizedPath(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import (./x.nix)")
	writeFile(t, filepath.Join(root, "x.nix"), "{}")

	edges, err := analyze(t, source, "import (./x.nix)", nil)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].Literal != "./x.nix" {
		t.Fatalf("literal = %q, want ./x.nix", edges[0].Literal)
	}
}

func TestAnalyzeMultilineImportsList(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "configuration.nix"), "")
	writeFile(t, filepath.Join(root, "a.nix"), "{}")
	writeFile(t, filepath.Join(root, "b.nix"), "{}")
	content := `{
  imports = [
    ./a.nix
    (./b.nix)
  ];
}`

	edges, err := analyze(t, source, content, nil)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(edges))
	}
	if edges[0].Literal != "./a.nix" || edges[1].Literal != "./b.nix" {
		t.Fatalf("edges = %+v", edges)
	}
}

func TestAnalyzeIgnoresStringInterpolationPathText(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "")
	content := `"${import ./not-static.nix} ./also-not-import.nix"`

	edges, err := analyze(t, source, content, nil)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("edges = %+v, want none", edges)
	}
}

func TestAnalyzeAllPathsBareBindingValue(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "flake.nix"), "")
	target := writeFile(t, filepath.Join(root, "modules", "service.nix"), "{}")
	content := "{ nixosModules.web-service = ./modules/service.nix; }"

	edges := analyzeAll(t, source, content, nil)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].Literal != "./modules/service.nix" || !edges[0].Exists {
		t.Fatalf("edge = %+v", edges[0])
	}
	if edges[0].TargetPath != normalize(t, target) {
		t.Fatalf("target = %q, want %q", edges[0].TargetPath, normalize(t, target))
	}
	if edges[0].ViaDefault {
		t.Fatalf("ViaDefault = true, want false for a direct file")
	}
}

func TestAnalyzeAllPathsDirectoryDefault(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "flake.nix"), "")
	target := writeFile(t, filepath.Join(root, "modules", "default.nix"), "{}")
	content := "{ a = ./modules; }"

	edges := analyzeAll(t, source, content, nil)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if !edges[0].Exists || !edges[0].ViaDefault {
		t.Fatalf("edge = %+v, want existing directory import", edges[0])
	}
	if edges[0].TargetPath != normalize(t, target) {
		t.Fatalf("target = %q, want %q", edges[0].TargetPath, normalize(t, target))
	}
}

func TestAnalyzeAllPathsSkipsInterpolatedAndSearchPaths(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "flake.nix"), "")
	content := "{ a = ./x/${n}.nix; b = <nixpkgs>; c = ~/home.nix; }"

	edges := analyzeAll(t, source, content, nil)
	if len(edges) != 0 {
		t.Fatalf("edges = %+v, want none (interpolated / search / home paths skipped)", edges)
	}
}

func TestAnalyzeAllPathsMissingTarget(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "flake.nix"), "")
	content := "{ a = ./missing.nix; }"

	edges := analyzeAll(t, source, content, nil)
	if len(edges) != 1 || edges[0].Exists {
		t.Fatalf("edges = %+v, want one non-existent edge", edges)
	}
}

func analyzeAll(t *testing.T, source string, content string, tracked map[string]bool) []Edge {
	t.Helper()
	tree, err := syntax.Parse([]byte(content))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	edges, err := AnalyzeAllPaths(source, tree, tracked)
	if err != nil {
		t.Fatalf("AnalyzeAllPaths error = %v", err)
	}
	return edges
}

func analyze(t *testing.T, source string, content string, tracked map[string]bool) ([]Edge, error) {
	t.Helper()
	tree, err := syntax.Parse([]byte(content))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	return Analyze(source, tree, tracked)
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
