// Package project discovers Nix workspace roots and files.
package project

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// File describes a discovered Nix source file.
type File struct {
	// Path is the normalized absolute filesystem path.
	Path string
	// URI is the file:// URI for Path.
	URI string
	// GitTracked reports whether git lists this file as tracked.
	GitTracked bool
}

// Workspace describes a discovered project root and its Nix files.
type Workspace struct {
	// Root is the normalized absolute workspace root path.
	Root string
	// RootURI is the file:// URI for Root.
	RootURI string
	// HasFlake reports whether Root contains flake.nix.
	HasFlake bool
	// HasGit reports whether Root is inside a git checkout.
	HasGit bool
	// Files is the deterministic list of Nix files under Root.
	Files []File
}

// Discover finds the workspace root for start and crawls Nix files below it.
func Discover(start string) (Workspace, error) {
	root, err := DetectRoot(start)
	if err != nil {
		return Workspace{}, err
	}
	files, err := Crawl(root)
	if err != nil {
		return Workspace{}, err
	}
	rootURI, err := PathToURI(root)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{
		Root:     root,
		RootURI:  rootURI,
		HasFlake: fileExists(filepath.Join(root, "flake.nix")),
		HasGit:   gitRoot(root),
		Files:    files,
	}, nil
}

// DetectRoot finds the nearest flake.nix, then nearest .git, else start's
// normalized directory.
func DetectRoot(start string) (string, error) {
	dir, err := normalizeStartDir(start)
	if err != nil {
		return "", err
	}

	if root, ok := findAncestor(dir, "flake.nix"); ok {
		return root, nil
	}
	if root, ok := findAncestor(dir, ".git"); ok {
		return root, nil
	}
	return dir, nil
}

// Crawl returns all .nix files under root in deterministic path order.
func Crawl(root string) ([]File, error) {
	normalizedRoot, err := NormalizePath(root)
	if err != nil {
		return nil, err
	}

	tracked := gitTrackedNixFiles(normalizedRoot)
	files := make([]File, 0)
	err = filepath.WalkDir(normalizedRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != normalizedRoot && shouldSkipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(name) != ".nix" {
			return nil
		}

		normalizedPath, err := NormalizePath(path)
		if err != nil {
			return err
		}
		uri, err := PathToURI(normalizedPath)
		if err != nil {
			return err
		}
		files = append(files, File{
			Path:       normalizedPath,
			URI:        uri,
			GitTracked: tracked[normalizedPath],
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// NormalizePath returns a cleaned absolute filesystem path.
func NormalizePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("project: empty path")
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
		return "", fmt.Errorf("project: unsupported URI scheme %q", u.Scheme)
	}
	if u.Host != "" && u.Host != "localhost" {
		return "", fmt.Errorf("project: unsupported file URI host %q", u.Host)
	}
	if u.Path == "" {
		return "", errors.New("project: empty file URI path")
	}

	path := filepath.FromSlash(u.Path)
	if runtime.GOOS == "windows" {
		path = strings.TrimPrefix(path, string(filepath.Separator))
	}
	return NormalizePath(path)
}

func normalizeStartDir(start string) (string, error) {
	normalized, err := NormalizePath(start)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(normalized)
	if err == nil && !info.IsDir() {
		return filepath.Dir(normalized), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err != nil && filepath.Ext(filepath.Base(normalized)) != "" {
		return filepath.Dir(normalized), nil
	}
	return normalized, nil
}

func findAncestor(start, marker string) (string, bool) {
	for dir := start; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
	}
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".direnv", ".cache", "node_modules", "result":
		return true
	}
	return strings.HasPrefix(name, "result-")
}

func gitTrackedNixFiles(root string) map[string]bool {
	tracked := make(map[string]bool)
	cmd := exec.Command("git", "-C", root, "ls-files", "--", "*.nix")
	output, err := cmd.Output()
	if err != nil {
		return tracked
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		rel := scanner.Text()
		if rel == "" {
			continue
		}
		path, err := NormalizePath(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		tracked[path] = true
	}
	return tracked
}

// GitAdd stages path in the git repository rooted at root by running
// `git -C root add -- path`. On failure it returns an error that includes the
// trimmed git output so the caller can surface why staging failed.
func GitAdd(root, path string) error {
	cmd := exec.Command("git", "-C", root, "add", "--", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return fmt.Errorf("git add %s: %w: %s", path, err, trimmed)
		}
		return fmt.Errorf("git add %s: %w", path, err)
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func gitRoot(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}
