// Package imports extracts and resolves simple Nix import edges.
package imports

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

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

// Analyze extracts import-like relative paths from content and resolves them
// relative to sourcePath.
func Analyze(sourcePath string, content []byte, tracked map[string]bool) ([]Edge, error) {
	normalizedSource, err := vfs.NormalizePath(sourcePath)
	if err != nil {
		return nil, err
	}

	tokens := lex(content)
	edges := make([]Edge, 0)
	for i, token := range tokens {
		if !isImportContext(tokens, i) {
			continue
		}
		if !isRelativePathLiteral(token.text) {
			continue
		}

		edge, err := resolveEdge(normalizedSource, token, tracked)
		if err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, nil
}

func resolveEdge(sourcePath string, token token, tracked map[string]bool) (Edge, error) {
	target := filepath.Join(filepath.Dir(sourcePath), filepath.FromSlash(token.text))
	normalizedTarget, err := vfs.NormalizePath(target)
	if err != nil {
		return Edge{}, err
	}

	resolvedTarget, exists := resolveImportTarget(normalizedTarget)
	uri := ""
	gitTracked := false
	if exists {
		resolvedTarget, err = vfs.NormalizePath(resolvedTarget)
		if err != nil {
			return Edge{}, err
		}
		uri, err = vfs.PathToURI(resolvedTarget)
		if err != nil {
			return Edge{}, err
		}
		gitTracked = tracked[resolvedTarget]
	}

	return Edge{
		SourcePath: sourcePath,
		Literal:    token.text,
		Range:      syntax.Range{Start: token.start, End: token.end},
		TargetPath: resolvedTarget,
		TargetURI:  uri,
		Exists:     exists,
		GitTracked: gitTracked,
	}, nil
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

func isImportContext(tokens []token, index int) bool {
	if index == 0 {
		return false
	}
	previous := previousSignificant(tokens, index)
	if previous < 0 {
		return false
	}

	previousText := tokens[previous].text
	switch {
	case previousText == "import" || previousText == "callPackage" || strings.HasSuffix(previousText, ".callPackage"):
		return true
	case inImportsList(tokens, index):
		return true
	default:
		return false
	}
}

func previousSignificant(tokens []token, index int) int {
	for i := index - 1; i >= 0; i-- {
		return i
	}
	return -1
}

func inImportsList(tokens []token, index int) bool {
	for i := index - 1; i >= 0; i-- {
		switch tokens[i].text {
		case "]", ";":
			return false
		case "[":
			return i >= 2 && tokens[i-1].text == "=" && tokens[i-2].text == "imports"
		}
	}
	return false
}

func isRelativePathLiteral(text string) bool {
	return strings.HasPrefix(text, "./") || strings.HasPrefix(text, "../")
}

type token struct {
	text  string
	start int
	end   int
}

func lex(content []byte) []token {
	tokens := make([]token, 0)
	for i := 0; i < len(content); {
		r := rune(content[i])
		if unicode.IsSpace(r) {
			i++
			continue
		}
		if content[i] == '#' {
			i = skipLineComment(content, i)
			continue
		}
		if content[i] == '"' {
			i = skipString(content, i)
			continue
		}
		if isPunctuation(content[i]) {
			tokens = append(tokens, token{text: string(content[i]), start: i, end: i + 1})
			i++
			continue
		}

		start := i
		for i < len(content) && !unicode.IsSpace(rune(content[i])) && !isPunctuation(content[i]) && content[i] != '"' && content[i] != '#' {
			i++
		}
		if start != i {
			tokens = append(tokens, token{text: string(content[start:i]), start: start, end: i})
			continue
		}
		i++
	}
	return tokens
}

func skipLineComment(content []byte, start int) int {
	for i := start; i < len(content); i++ {
		if content[i] == '\n' {
			return i + 1
		}
	}
	return len(content)
}

func skipString(content []byte, start int) int {
	escaped := false
	for i := start + 1; i < len(content); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch content[i] {
		case '\\':
			escaped = true
		case '"':
			return i + 1
		}
	}
	return len(content)
}

func isPunctuation(b byte) bool {
	switch b {
	case '[', ']', '{', '}', '(', ')', ';', '=':
		return true
	default:
		return false
	}
}
