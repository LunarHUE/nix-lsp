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
	if _, err := store.OpenBuffer(path, []byte("overlay"), 1); err != nil {
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
	if _, err := store.OpenBuffer(path, []byte("one"), 1); err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	snapshot := store.Snapshot()

	if _, err := store.UpdateBuffer(path, []byte("two"), 2); err != nil {
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

func TestHashChangesOnBufferUpdate(t *testing.T) {
	path := writeTempFile(t, "disk")
	store := New()

	first, err := store.OpenBuffer(path, []byte("one"), 1)
	if err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	second, err := store.UpdateBuffer(path, []byte("two"), 2)
	if err != nil {
		t.Fatalf("UpdateBuffer() error = %v", err)
	}

	if first.Hash == second.Hash {
		t.Fatalf("hash did not change: %q", first.Hash)
	}
}

func TestCloseBufferFallsBackToDisk(t *testing.T) {
	path := writeTempFile(t, "disk")
	store := New()
	if _, err := store.OpenBuffer(path, []byte("overlay"), 1); err != nil {
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
	_, err := store.UpdateBuffer(filepath.Join(t.TempDir(), "missing.nix"), []byte("content"), 1)
	if !errors.Is(err, ErrBufferNotOpen) {
		t.Fatalf("UpdateBuffer() error = %v, want ErrBufferNotOpen", err)
	}
}

func TestBufferVersionTracked(t *testing.T) {
	path := writeTempFile(t, "disk")
	store := New()

	// Version 0 is a legitimate LSP version and must survive round-trip, distinct
	// from "no version" (NoVersion) for a disk file.
	opened, err := store.OpenBuffer(path, []byte("zero"), 0)
	if err != nil {
		t.Fatalf("OpenBuffer() error = %v", err)
	}
	if opened.Version != 0 {
		t.Fatalf("opened File.Version = %d, want 0", opened.Version)
	}
	if v, ok := store.Version(path); !ok || v != 0 {
		t.Fatalf("store.Version() = (%d, %v), want (0, true)", v, ok)
	}
	snap := store.Snapshot()
	if v, ok := snap.Version(path); !ok || v != 0 {
		t.Fatalf("snapshot.Version() = (%d, %v), want (0, true)", v, ok)
	}
	if file, err := snap.ReadFile(path); err != nil || file.Version != 0 {
		t.Fatalf("snapshot.ReadFile().Version = %d (err %v), want 0", file.Version, err)
	}

	updated, err := store.UpdateBuffer(path, []byte("seven"), 7)
	if err != nil {
		t.Fatalf("UpdateBuffer() error = %v", err)
	}
	if updated.Version != 7 {
		t.Fatalf("updated File.Version = %d, want 7", updated.Version)
	}
	if v, ok := store.Version(path); !ok || v != 7 {
		t.Fatalf("store.Version() after update = (%d, %v), want (7, true)", v, ok)
	}

	// A disk file has no version: Version reports ok=false and ReadFile marks it
	// NoVersion, never a colliding zero.
	if err := store.CloseBuffer(path); err != nil {
		t.Fatalf("CloseBuffer() error = %v", err)
	}
	if v, ok := store.Version(path); ok {
		t.Fatalf("store.Version() after close = (%d, %v), want (_, false)", v, ok)
	}
	file, err := store.Snapshot().ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if file.Version != NoVersion {
		t.Fatalf("disk File.Version = %d, want NoVersion (%d)", file.Version, NoVersion)
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
