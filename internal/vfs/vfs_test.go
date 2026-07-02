package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlayTakesPrecedenceOverDisk(t *testing.T) {
	path := writeTempFile(t, "disk")
	store := New()
	if _, err := store.OpenBuffer(path, []byte("overlay")); err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}

	file, err := store.Snapshot().ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(file.Content); got != "overlay" {
		t.Fatalf("ReadFile() content = %q, want %q", got, "overlay")
	}
	if !file.Overlay {
		t.Fatal("ReadFile() Overlay = false, want true")
	}
}

func TestSnapshotImmutability(t *testing.T) {
	path := writeTempFile(t, "disk")
	store := New()
	if _, err := store.OpenBuffer(path, []byte("one")); err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	snapshot := store.Snapshot()

	if _, err := store.UpdateBuffer(path, []byte("two")); err != nil {
		t.Fatalf("UpdateBuffer() error = %v", err)
	}

	file, err := snapshot.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(file.Content); got != "one" {
		t.Fatalf("snapshot content = %q, want %q", got, "one")
	}

	file.Content[0] = 'x'
	fileAgain, err := snapshot.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() second call error = %v", err)
	}
	if got := string(fileAgain.Content); got != "one" {
		t.Fatalf("snapshot content after caller mutation = %q, want %q", got, "one")
	}
}

func TestHashChangesAndGenerationIncrements(t *testing.T) {
	path := writeTempFile(t, "disk")
	store := New()

	first, err := store.OpenBuffer(path, []byte("one"))
	if err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	second, err := store.UpdateBuffer(path, []byte("two"))
	if err != nil {
		t.Fatalf("UpdateBuffer() error = %v", err)
	}

	if first.Hash == second.Hash {
		t.Fatalf("hash did not change: %q", first.Hash)
	}
	if first.Generation != 1 {
		t.Fatalf("first generation = %d, want 1", first.Generation)
	}
	if second.Generation != 2 {
		t.Fatalf("second generation = %d, want 2", second.Generation)
	}
	if store.Snapshot().Generation() != 2 {
		t.Fatalf("snapshot generation = %d, want 2", store.Snapshot().Generation())
	}
}

func TestCloseBufferFallsBackToDisk(t *testing.T) {
	path := writeTempFile(t, "disk")
	store := New()
	if _, err := store.OpenBuffer(path, []byte("overlay")); err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	if err := store.CloseBuffer(path); err != nil {
		t.Fatalf("CloseBuffer() error = %v", err)
	}

	file, err := store.Snapshot().ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(file.Content); got != "disk" {
		t.Fatalf("ReadFile() content = %q, want %q", got, "disk")
	}
	if file.Overlay {
		t.Fatal("ReadFile() Overlay = true, want false")
	}
}

func TestURIConversionRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "with space.nix")

	uri, err := PathToURI(path)
	if err != nil {
		t.Fatalf("PathToURI() error = %v", err)
	}
	if !strings.HasPrefix(uri, "file://") {
		t.Fatalf("PathToURI() = %q, want file:// prefix", uri)
	}
	if strings.Contains(uri, " ") {
		t.Fatalf("PathToURI() = %q, want escaped spaces", uri)
	}

	roundTrip, err := URIToPath(uri)
	if err != nil {
		t.Fatalf("URIToPath() error = %v", err)
	}
	normalized, err := NormalizePath(path)
	if err != nil {
		t.Fatalf("NormalizePath() error = %v", err)
	}
	if roundTrip != normalized {
		t.Fatalf("URIToPath(PathToURI(path)) = %q, want %q", roundTrip, normalized)
	}
}

func TestUpdateBufferRequiresOpenBuffer(t *testing.T) {
	store := New()
	_, err := store.UpdateBuffer(filepath.Join(t.TempDir(), "missing.nix"), []byte("content"))
	if !errors.Is(err, ErrBufferNotOpen) {
		t.Fatalf("UpdateBuffer() error = %v, want ErrBufferNotOpen", err)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.nix")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
