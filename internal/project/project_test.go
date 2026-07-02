package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestDetectRootPrefersNearestFlake(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "outer", "flake.nix"), "{}")
	writeFile(t, filepath.Join(root, "outer", "inner", "flake.nix"), "{}")

	got, err := DetectRoot(filepath.Join(root, "outer", "inner", "src", "default.nix"))
	if err != nil {
		t.Fatalf("DetectRoot() error = %v", err)
	}
	want := normalizeForTest(t, filepath.Join(root, "outer", "inner"))
	if got != want {
		t.Fatalf("DetectRoot() = %q, want %q", got, want)
	}
}

func TestDetectRootFallsBackToNearestGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")

	got, err := DetectRoot(filepath.Join(root, "pkg", "file.nix"))
	if err != nil {
		t.Fatalf("DetectRoot() error = %v", err)
	}
	want := normalizeForTest(t, root)
	if got != want {
		t.Fatalf("DetectRoot() = %q, want %q", got, want)
	}
}

func TestDetectRootUsesNormalizedStartingDirectoryWithoutMarkers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workspace", "src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	got, err := DetectRoot(filepath.Join(dir, "..", "src"))
	if err != nil {
		t.Fatalf("DetectRoot() error = %v", err)
	}
	want := normalizeForTest(t, dir)
	if got != want {
		t.Fatalf("DetectRoot() = %q, want %q", got, want)
	}
}

func TestDetectRootUsesParentForMissingFilePathWithoutMarkers(t *testing.T) {
	dir := t.TempDir()

	got, err := DetectRoot(filepath.Join(dir, "missing.nix"))
	if err != nil {
		t.Fatalf("DetectRoot() error = %v", err)
	}
	want := normalizeForTest(t, dir)
	if got != want {
		t.Fatalf("DetectRoot() = %q, want %q", got, want)
	}
}

func TestDetectRootAcceptsFileURI(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	start := filepath.Join(root, "nested", "default.nix")
	uri, err := PathToURI(start)
	if err != nil {
		t.Fatalf("PathToURI() error = %v", err)
	}

	got, err := DetectRoot(uri)
	if err != nil {
		t.Fatalf("DetectRoot() error = %v", err)
	}
	want := normalizeForTest(t, root)
	if got != want {
		t.Fatalf("DetectRoot() = %q, want %q", got, want)
	}
}

func TestCrawlSkipsCacheAndHiddenVCSDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.nix"), "{}")
	writeFile(t, filepath.Join(root, "pkg", "b.nix"), "{}")
	writeFile(t, filepath.Join(root, ".git", "ignored.nix"), "{}")
	writeFile(t, filepath.Join(root, ".direnv", "ignored.nix"), "{}")
	writeFile(t, filepath.Join(root, ".cache", "ignored.nix"), "{}")
	writeFile(t, filepath.Join(root, "node_modules", "ignored.nix"), "{}")
	writeFile(t, filepath.Join(root, "result", "ignored.nix"), "{}")
	writeFile(t, filepath.Join(root, "result-build", "ignored.nix"), "{}")
	writeFile(t, filepath.Join(root, ".hidden", "kept.nix"), "{}")
	writeFile(t, filepath.Join(root, "note.txt"), "not nix")

	files, err := Crawl(root)
	if err != nil {
		t.Fatalf("Crawl() error = %v", err)
	}

	got := relativePaths(t, root, files)
	want := []string{
		".hidden/kept.nix",
		"a.nix",
		"pkg/b.nix",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Crawl() paths = %v, want %v", got, want)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Crawl() paths are not sorted: %v", got)
	}
	for _, file := range files {
		if !strings.HasPrefix(file.URI, "file://") {
			t.Fatalf("File.URI = %q, want file:// prefix", file.URI)
		}
	}
}

func TestCrawlGracefullyMarksUntrackedOutsideGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")

	files, err := Crawl(root)
	if err != nil {
		t.Fatalf("Crawl() error = %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("Crawl() returned %d files, want 1", len(files))
	}
	if files[0].GitTracked {
		t.Fatal("Crawl() GitTracked = true outside git, want false")
	}
}

func TestDiscoverMarksGitTrackedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	runGit(t, root, "init")
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	writeFile(t, filepath.Join(root, "tracked.nix"), "{}")
	writeFile(t, filepath.Join(root, "untracked.nix"), "{}")
	runGit(t, root, "add", "flake.nix", "tracked.nix")

	workspace, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !workspace.HasFlake {
		t.Fatal("HasFlake = false, want true")
	}
	if !workspace.HasGit {
		t.Fatal("HasGit = false, want true")
	}

	files := make(map[string]File, len(workspace.Files))
	for _, file := range workspace.Files {
		rel, err := filepath.Rel(root, file.Path)
		if err != nil {
			t.Fatalf("Rel() error = %v", err)
		}
		files[filepath.ToSlash(rel)] = file
	}
	if !files["tracked.nix"].GitTracked {
		t.Fatal("tracked.nix GitTracked = false, want true")
	}
	if files["untracked.nix"].GitTracked {
		t.Fatal("untracked.nix GitTracked = true, want false")
	}
}

func TestURIConversionRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "with space.nix")

	uri, err := PathToURI(path)
	if err != nil {
		t.Fatalf("PathToURI() error = %v", err)
	}
	if strings.Contains(uri, " ") {
		t.Fatalf("PathToURI() = %q, want escaped spaces", uri)
	}

	got, err := URIToPath(uri)
	if err != nil {
		t.Fatalf("URIToPath() error = %v", err)
	}
	want := normalizeForTest(t, path)
	if got != want {
		t.Fatalf("URIToPath(PathToURI(path)) = %q, want %q", got, want)
	}
}

func TestURIRejectsUnsupportedSchemes(t *testing.T) {
	if _, err := URIToPath("https://example.com/file.nix"); err == nil {
		t.Fatal("URIToPath() error = nil, want unsupported scheme error")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func normalizeForTest(t *testing.T, path string) string {
	t.Helper()
	normalized, err := NormalizePath(path)
	if err != nil {
		t.Fatalf("NormalizePath() error = %v", err)
	}
	return normalized
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func relativePaths(t *testing.T, root string, files []File) []string {
	t.Helper()
	paths := make([]string, 0, len(files))
	for _, file := range files {
		rel, err := filepath.Rel(root, file.Path)
		if err != nil {
			t.Fatalf("Rel() error = %v", err)
		}
		if runtime.GOOS == "windows" {
			rel = filepath.ToSlash(rel)
		}
		paths = append(paths, rel)
	}
	return paths
}
