// Package imports extracts and resolves simple Nix import edges from the CST.
package imports

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

// Edge is a resolved or unresolved import-like path reference.
type Edge struct {
	SourcePath string
	Literal    string
	Range      syntax.Range
	TargetPath string
	TargetURI  string
	Exists     bool
	GitTracked bool
}

// Analyze extracts import-like relative paths from tree and resolves them
// relative to sourcePath.
func Analyze(sourcePath string, tree *syntax.Tree, tracked map[string]bool) ([]Edge, error) {
	normalizedSource, err := vfs.NormalizePath(sourcePath)
	if err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, nil
	}

	var edges []Edge
	var analyzeErr error
	tree.Walk(func(node syntax.Node) bool {
		if analyzeErr != nil {
			return false
		}

		if apply, ok := syntax.AsApply(node); ok {
			edge, ok, err := analyzeApply(normalizedSource, apply, tracked)
			if err != nil {
				analyzeErr = err
				return false
			}
			if ok {
				edges = append(edges, edge)
			}
		}

		if binding, ok := syntax.AsBinding(node); ok {
			listEdges, err := analyzeImportsBinding(normalizedSource, binding, tracked)
			if err != nil {
				analyzeErr = err
				return false
			}
			edges = append(edges, listEdges...)
		}

		return true
	})
	if analyzeErr != nil {
		return nil, analyzeErr
	}
	return edges, nil
}

func analyzeApply(sourcePath string, apply syntax.Apply, tracked map[string]bool) (Edge, bool, error) {
	function := apply.Function()
	argument := unwrapParenthesized(apply.Argument())

	if function.Text() == "import" {
		return edgeForPath(sourcePath, argument, tracked)
	}
	if isCallPackageFunction(function) {
		return edgeForPath(sourcePath, argument, tracked)
	}
	return Edge{}, false, nil
}

func analyzeImportsBinding(sourcePath string, binding syntax.Binding, tracked map[string]bool) ([]Edge, error) {
	if binding.AttrPath().Text() != "imports" {
		return nil, nil
	}

	list, ok := syntax.AsList(unwrapParenthesized(binding.Expression()))
	if !ok {
		return nil, nil
	}

	edges := make([]Edge, 0)
	for _, element := range list.Elements() {
		edge, ok, err := edgeForPath(sourcePath, unwrapParenthesized(element), tracked)
		if err != nil {
			return nil, err
		}
		if ok {
			edges = append(edges, edge)
		}
	}
	return edges, nil
}

func edgeForPath(sourcePath string, node syntax.Node, tracked map[string]bool) (Edge, bool, error) {
	path, ok := syntax.AsPathLiteral(node)
	if !ok || !isStaticRelativePath(path) || isInsideString(path.Node) {
		return Edge{}, false, nil
	}

	target := filepath.Join(filepath.Dir(sourcePath), filepath.FromSlash(path.Text()))
	normalizedTarget, err := vfs.NormalizePath(target)
	if err != nil {
		return Edge{}, false, err
	}

	resolvedTarget, exists := resolveImportTarget(normalizedTarget)
	uri := ""
	gitTracked := false
	if exists {
		resolvedTarget, err = vfs.NormalizePath(resolvedTarget)
		if err != nil {
			return Edge{}, false, err
		}
		uri, err = vfs.PathToURI(resolvedTarget)
		if err != nil {
			return Edge{}, false, err
		}
		gitTracked = tracked[resolvedTarget]
	}

	return Edge{
		SourcePath: sourcePath,
		Literal:    path.Text(),
		Range:      path.Range(),
		TargetPath: resolvedTarget,
		TargetURI:  uri,
		Exists:     exists,
		GitTracked: gitTracked,
	}, true, nil
}

func resolveImportTarget(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return path, false
	}
	if !info.IsDir() {
		return path, true
	}

	defaultPath := filepath.Join(path, "default.nix")
	if info, err := os.Stat(defaultPath); err == nil && !info.IsDir() {
		return defaultPath, true
	}
	return path, true
}

func unwrapParenthesized(node syntax.Node) syntax.Node {
	for node.Kind() == "parenthesized_expression" {
		next := node.ChildByFieldName("expression")
		if next.IsZero() {
			return node
		}
		node = next
	}
	return node
}

func isCallPackageFunction(node syntax.Node) bool {
	text := node.Text()
	return text == "callPackage" || strings.HasSuffix(text, ".callPackage")
}

func isInsideString(node syntax.Node) bool {
	for parent := node.Parent(); !parent.IsZero(); parent = parent.Parent() {
		switch parent.Kind() {
		case "string_expression", "indented_string_expression", "interpolation":
			return true
		}
	}
	return false
}

func isStaticRelativePath(path syntax.PathLiteral) bool {
	text := path.Text()
	if !(strings.HasPrefix(text, "./") || strings.HasPrefix(text, "../")) {
		return false
	}
	for _, child := range path.NamedChildren() {
		if child.Kind() == "interpolation" {
			return false
		}
	}
	return true
}
