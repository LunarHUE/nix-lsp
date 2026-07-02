package imports

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestAnalyzeImportPath(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./module.nix")
	target := writeFile(t, filepath.Join(root, "module.nix"), "{}")
	tracked := map[string]bool{normalize(t, target): true}

	edges, err := Analyze(source, []byte("import ./module.nix"), tracked)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].Literal != "./module.nix" || !edges[0].Exists || !edges[0].GitTracked {
		t.Fatalf("edge = %+v", edges[0])
	}
	if edges[0].Range.Start != len("import ") {
		t.Fatalf("range start = %d, want %d", edges[0].Range.Start, len("import "))
	}
}

func TestAnalyzeImportsList(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "configuration.nix"), "imports = [ ./a.nix ./b.nix ];")
	writeFile(t, filepath.Join(root, "a.nix"), "{}")
	writeFile(t, filepath.Join(root, "b.nix"), "{}")

	edges, err := Analyze(source, []byte("imports = [ ./a.nix ./b.nix ];"), nil)
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

	edges, err := Analyze(source, []byte("pkgs.callPackage ./pkg {}"), map[string]bool{normalize(t, target): true})
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

	edges, err := Analyze(source, []byte("import ./missing.nix"), nil)
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

	edges, err := Analyze(source, content, nil)
	if err != nil {
		t.Fatalf("Analyze error = %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("edges = %+v, want none", edges)
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
