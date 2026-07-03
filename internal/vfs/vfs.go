// Package vfs provides a small virtual filesystem for language-server file
// reads. It layers open editor buffers over disk files and exposes immutable
// snapshots of that overlay state.
package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ErrBufferNotOpen is returned when updating a buffer that has not been opened.
var ErrBufferNotOpen = errors.New("vfs: buffer is not open")

// File is the result of reading a path from a snapshot.
type File struct {
	// Path is the normalized absolute path used as the VFS identity.
	Path string
	// Content is a caller-owned copy of the file bytes.
	Content []byte
	// Hash is the hex-encoded SHA-256 hash of Content.
	Hash string
	// Generation is the Store generation captured by the Snapshot.
	Generation uint64
	// Overlay reports whether Content came from an open editor buffer.
	Overlay bool
	// Version is the LSP document version of the open buffer this File was read
	// from. It is meaningful only when Overlay is true; for disk reads it is
	// NoVersion (there is no editor version for on-disk content).
	Version int32
}

// NoVersion marks a File that has no LSP document version (a disk read). It is
// deliberately not a valid LSP version so it can never collide with a real
// version 0.
const NoVersion int32 = -1

type overlayFile struct {
	content []byte
	hash    string
	version int32
}

// Store owns the current mutable set of open editor buffers.
//
// Use Snapshot to hand an immutable view to analysis code. All paths are
// normalized to absolute filesystem paths before lookup.
type Store struct {
	mu         sync.RWMutex
	generation uint64
	overlays   map[string]overlayFile
}

// New creates an empty VFS store.
func New() *Store {
	return &Store{overlays: make(map[string]overlayFile)}
}

// Generation returns the current overlay generation. It increments whenever an
// open buffer is opened, updated, or closed.
func (s *Store) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

// OpenBuffer opens or replaces an in-memory editor buffer for path at the given
// LSP document version.
func (s *Store) OpenBuffer(path string, content []byte, version int32) (File, error) {
	return s.setBuffer(path, content, version, true)
}

// UpdateBuffer replaces the contents of an already-open editor buffer, recording
// the new LSP document version.
func (s *Store) UpdateBuffer(path string, content []byte, version int32) (File, error) {
	return s.setBuffer(path, content, version, false)
}

// Version returns the LSP document version of the open buffer for path, if one
// is open. The second result is false when no buffer is open for path (a disk
// file has no version).
func (s *Store) Version(path string) (int32, bool) {
	normalized, err := NormalizePath(path)
	if err != nil {
		return NoVersion, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	overlay, ok := s.overlays[normalized]
	if !ok {
		return NoVersion, false
	}
	return overlay.version, true
}

// CloseBuffer removes an in-memory editor buffer. Later snapshots will fall
// back to disk for the same path.
func (s *Store) CloseBuffer(path string) error {
	normalized, err := NormalizePath(path)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.overlays[normalized]; !ok {
		return nil
	}
	delete(s.overlays, normalized)
	s.generation++
	return nil
}

// Snapshot returns an immutable copy of the current overlay state.
func (s *Store) Snapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	overlays := make(map[string]overlayFile, len(s.overlays))
	for path, overlay := range s.overlays {
		overlays[path] = overlayFile{
			content: cloneBytes(overlay.content),
			hash:    overlay.hash,
			version: overlay.version,
		}
	}
	return &Snapshot{generation: s.generation, overlays: overlays}
}

func (s *Store) setBuffer(path string, content []byte, version int32, allowCreate bool) (File, error) {
	normalized, err := NormalizePath(path)
	if err != nil {
		return File{}, err
	}

	copied := cloneBytes(content)
	hash := ContentHash(copied)

	s.mu.Lock()
	defer s.mu.Unlock()
	if !allowCreate {
		if _, ok := s.overlays[normalized]; !ok {
			return File{}, ErrBufferNotOpen
		}
	}
	s.generation++
	s.overlays[normalized] = overlayFile{content: copied, hash: hash, version: version}

	return File{
		Path:       normalized,
		Content:    cloneBytes(copied),
		Hash:       hash,
		Generation: s.generation,
		Overlay:    true,
		Version:    version,
	}, nil
}

// Snapshot is an immutable view of a Store's overlay state at one generation.
type Snapshot struct {
	generation uint64
	overlays   map[string]overlayFile
}

// Generation returns the Store generation captured by this snapshot.
func (s *Snapshot) Generation() uint64 {
	if s == nil {
		return 0
	}
	return s.generation
}

// OverlayPaths returns the normalized paths of every open buffer in this
// snapshot. The order is unspecified; callers that need determinism must sort.
func (s *Snapshot) OverlayPaths() []string {
	if s == nil {
		return nil
	}
	paths := make([]string, 0, len(s.overlays))
	for path := range s.overlays {
		paths = append(paths, path)
	}
	return paths
}

// HasOverlay reports whether this snapshot contains an open buffer for path.
func (s *Snapshot) HasOverlay(path string) (bool, error) {
	if s == nil {
		return false, nil
	}
	normalized, err := NormalizePath(path)
	if err != nil {
		return false, err
	}
	_, ok := s.overlays[normalized]
	return ok, nil
}

// ReadFile reads path from this snapshot. Open buffers take precedence over
// disk. Disk reads happen at call time when no overlay exists.
func (s *Snapshot) ReadFile(path string) (File, error) {
	if s == nil {
		return File{}, errors.New("vfs: nil snapshot")
	}
	normalized, err := NormalizePath(path)
	if err != nil {
		return File{}, err
	}

	if overlay, ok := s.overlays[normalized]; ok {
		return File{
			Path:       normalized,
			Content:    cloneBytes(overlay.content),
			Hash:       overlay.hash,
			Generation: s.generation,
			Overlay:    true,
			Version:    overlay.version,
		}, nil
	}

	content, err := os.ReadFile(normalized)
	if err != nil {
		return File{}, err
	}
	return File{
		Path:       normalized,
		Content:    cloneBytes(content),
		Hash:       ContentHash(content),
		Generation: s.generation,
		Overlay:    false,
		Version:    NoVersion,
	}, nil
}

// Version returns the LSP document version of the open buffer for path as
// captured by this snapshot. The second result is false when the snapshot has
// no open buffer for path.
func (s *Snapshot) Version(path string) (int32, bool) {
	if s == nil {
		return NoVersion, false
	}
	normalized, err := NormalizePath(path)
	if err != nil {
		return NoVersion, false
	}
	overlay, ok := s.overlays[normalized]
	if !ok {
		return NoVersion, false
	}
	return overlay.version, true
}

// ContentHash returns the hex-encoded SHA-256 hash of content.
func ContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// NormalizePath returns a cleaned absolute filesystem path.
func NormalizePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("vfs: empty path")
	}
	if strings.HasPrefix(path, "file://") {
		return URIToPath(path)
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

// PathToURI converts a local filesystem path to a file:// URI.
func PathToURI(path string) (string, error) {
	normalized, err := NormalizePath(path)
	if err != nil {
		return "", err
	}

	u := url.URL{Scheme: "file", Path: filepath.ToSlash(normalized)}
	if runtime.GOOS == "windows" && !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String(), nil
}

// URIToPath converts a file:// URI to a local filesystem path.
func URIToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("vfs: unsupported URI scheme %q", u.Scheme)
	}
	if u.Host != "" && u.Host != "localhost" {
		return "", fmt.Errorf("vfs: unsupported file URI host %q", u.Host)
	}
	if u.Path == "" {
		return "", errors.New("vfs: empty file URI path")
	}

	path := filepath.FromSlash(u.Path)
	if runtime.GOOS == "windows" {
		path = strings.TrimPrefix(path, string(filepath.Separator))
	}
	return NormalizePath(path)
}

func cloneBytes(content []byte) []byte {
	if content == nil {
		return nil
	}
	copied := make([]byte, len(content))
	copy(copied, content)
	return copied
}
