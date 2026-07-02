// Package static produces conservative static diagnostics from indexed facts.
package static

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	importedges "github.com/wesleybaldwin/nix-lsp/internal/analysis/imports"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

// Diagnostic codes are stable, machine-readable identifiers for each static
// diagnostic kind. Code actions and clients key on them.
const (
	// CodeMissingImport marks an import whose target does not exist.
	CodeMissingImport = "missing-import"
	// CodeUntrackedImport marks a flake import whose target exists on disk but
	// is not git-tracked (so a Nix flake will not see it).
	CodeUntrackedImport = "untracked-import"
	// CodeUnusedBinding marks a let binding that is never referenced.
	CodeUnusedBinding = "unused-binding"
	// CodeDuplicateBinding marks a name introduced twice in one binding set.
	CodeDuplicateBinding = "duplicate-binding"
	// CodeBadInherit marks a bare inherit of an undefined variable.
	CodeBadInherit = "bad-inherit"
)

// FileDiagnostics returns static diagnostics for one parsed file.
func FileDiagnostics(workspace project.Workspace, sourcePath string, tree *syntax.Tree) ([]syntax.Diagnostic, error) {
	tracked := trackedFiles(workspace)
	edges, err := importedges.Analyze(sourcePath, tree, tracked)
	if err != nil {
		return nil, err
	}
	return ImportDiagnostics(workspace, edges), nil
}

// ImportDiagnostics converts import edges into user-facing diagnostics.
func ImportDiagnostics(workspace project.Workspace, edges []importedges.Edge) []syntax.Diagnostic {
	diagnostics := make([]syntax.Diagnostic, 0)
	for _, edge := range edges {
		if !edge.Exists {
			diagnostics = append(diagnostics, syntax.Diagnostic{
				Message: fmt.Sprintf("missing import target %s", edge.Literal),
				Range:   edge.Range,
				Code:    CodeMissingImport,
			})
			continue
		}
		if ShouldWarnUntracked(workspace, edge) {
			diagnostics = append(diagnostics, syntax.Diagnostic{
				Message:  fmt.Sprintf("import target %s exists but is not git-tracked; Nix flakes only see git-tracked files, so run git add", edge.Literal),
				Range:    edge.Range,
				Code:     CodeUntrackedImport,
				Severity: syntax.SeverityWarning,
			})
		}
	}
	return diagnostics
}

// BindingDiagnostics returns conservative diagnostics derived from a scope
// analysis: unused let bindings (warning), duplicate names within a single
// binding set (error), and bare inherits of an undefined variable (error). The
// bar is zero false positives, so every check bails out on anything it cannot
// understand.
func BindingDiagnostics(file *scopes.File, tree *syntax.Tree) []syntax.Diagnostic {
	if file == nil {
		return nil
	}

	diagnostics := make([]syntax.Diagnostic, 0)
	diagnostics = append(diagnostics, unusedBindingDiagnostics(file)...)
	diagnostics = append(diagnostics, duplicateBindingDiagnostics(tree)...)
	diagnostics = append(diagnostics, badInheritDiagnostics(file)...)

	sort.SliceStable(diagnostics, func(i, j int) bool {
		return rangeLess(diagnostics[i].Range, diagnostics[j].Range)
	})
	return diagnostics
}

// unusedBindingDiagnostics flags let bindings and let-scoped inherit entries
// that are never referenced. Function params, rec attrs, and plain attribute
// keys are deliberately never flagged, and names beginning with an underscore
// are treated as intentionally unused.
func unusedBindingDiagnostics(file *scopes.File) []syntax.Diagnostic {
	var diagnostics []syntax.Diagnostic
	for _, b := range file.UnusedBindings() {
		if b.Dynamic || b.DefScope == nil || b.DefScope.Kind != scopes.ScopeLet {
			continue
		}
		if b.Kind != scopes.LetBinding && b.Kind != scopes.InheritEntry {
			continue
		}
		if strings.HasPrefix(b.Name, "_") {
			continue
		}
		// A name defined more than once in the same let is a duplicate, reported
		// by the duplicate check. Only the first definition resolves references,
		// so the later ones look unused; suppress the redundant unused warning.
		if countInScope(b.DefScope, b.Name) > 1 {
			continue
		}
		diagnostics = append(diagnostics, syntax.Diagnostic{
			Message:  fmt.Sprintf("unused binding %q", b.Name),
			Range:    b.NameRange,
			Code:     CodeUnusedBinding,
			Severity: syntax.SeverityWarning,
		})
	}
	return diagnostics
}

// countInScope returns how many of a scope's variable bindings share name.
func countInScope(scope *scopes.Scope, name string) int {
	if scope == nil {
		return 0
	}
	count := 0
	for _, b := range scope.Bindings {
		if b.Name == name {
			count++
		}
	}
	return count
}

// badInheritDiagnostics flags bare `inherit name;` entries whose implied outer
// reference resolves to nothing and is not made uncertain by an enclosing
// `with`. The `inherit (expr) name;` form implies no such reference and is
// therefore never flagged.
func badInheritDiagnostics(file *scopes.File) []syntax.Diagnostic {
	var diagnostics []syntax.Diagnostic
	for _, ref := range file.References {
		if !ref.FromInherit || ref.Target != nil || ref.WithUncertain {
			continue
		}
		diagnostics = append(diagnostics, syntax.Diagnostic{
			Message:  fmt.Sprintf("inherit of undefined variable %q", ref.Name),
			Range:    ref.Range,
			Code:     CodeBadInherit,
			Severity: syntax.SeverityError,
		})
	}
	return diagnostics
}

// duplicateBindingDiagnostics walks every binding_set node and reports names
// introduced more than once in the same set. It compares plain bindings by full
// attribute-path text (so `a.b` and `a.c` do not collide, but two `a.b` do) and
// inherit entries by name (so `inherit a; a = 1;` collides). Dynamic `${...}`
// keys are skipped, and binding sets that contain a syntax error are skipped
// entirely to avoid noise while the user is still typing.
func duplicateBindingDiagnostics(tree *syntax.Tree) []syntax.Diagnostic {
	if tree == nil {
		return nil
	}
	var diagnostics []syntax.Diagnostic
	tree.Walk(func(node syntax.Node) bool {
		if node.Kind() != "binding_set" || node.HasError() {
			return true
		}
		seen := make(map[string]bool)
		for _, entry := range node.NamedChildren() {
			for _, name := range introducedNames(entry) {
				if seen[name.key] {
					diagnostics = append(diagnostics, syntax.Diagnostic{
						Message:  fmt.Sprintf("duplicate binding %q", name.display),
						Range:    name.rng,
						Code:     CodeDuplicateBinding,
						Severity: syntax.SeverityError,
					})
					continue
				}
				seen[name.key] = true
			}
		}
		return true
	})
	return diagnostics
}

// introducedName is one name a binding-set entry brings into the set, with the
// key used for collision detection, the text shown in a diagnostic, and the
// range to anchor a diagnostic on.
type introducedName struct {
	key     string
	display string
	rng     syntax.Range
}

// introducedNames returns the names a single binding_set entry introduces, or
// nil for entries whose keys are dynamic or otherwise not comparable.
func introducedNames(entry syntax.Node) []introducedName {
	switch entry.Kind() {
	case "binding":
		attrpath := entry.ChildByFieldName("attrpath")
		segments := attrpath.NamedChildren()
		if len(segments) == 0 {
			return nil
		}
		// Skip any path containing a dynamic segment (`a.${x} = ...`).
		for _, seg := range segments {
			if seg.Kind() != "identifier" {
				return nil
			}
		}
		// Key is the raw path text so a single-segment binding (`a`) collides
		// with an inherit of the same name, while a multi-segment path (`a.b`,
		// which always contains a dot) never collides with a bare name.
		path := attrpath.Text()
		return []introducedName{{key: path, display: path, rng: attrpath.Range()}}
	case "inherit", "inherit_from":
		attrs := entry.ChildByFieldName("attrs")
		var names []introducedName
		for _, attr := range attrs.NamedChildren() {
			if attr.Kind() != "identifier" {
				continue
			}
			text := attr.Text()
			names = append(names, introducedName{key: text, display: text, rng: attr.Range()})
		}
		return names
	default:
		return nil
	}
}

// rangeLess orders ranges by start position for stable diagnostic output.
func rangeLess(a, b syntax.Range) bool {
	if a.Start.Line != b.Start.Line {
		return a.Start.Line < b.Start.Line
	}
	return a.Start.Character < b.Start.Character
}

// TrackedFiles returns a path-keyed set of git-tracked files in workspace.
func TrackedFiles(workspace project.Workspace) map[string]bool {
	return trackedFiles(workspace)
}

// WorkspaceDiagnostics returns static diagnostics for every readable workspace
// file, keyed by file URI.
func WorkspaceDiagnostics(workspace project.Workspace, snapshot *vfs.Snapshot) map[string][]syntax.Diagnostic {
	diagnostics := make(map[string][]syntax.Diagnostic)
	if snapshot == nil {
		return diagnostics
	}

	for _, file := range workspace.Files {
		read, err := snapshot.ReadFile(file.Path)
		if err != nil {
			continue
		}
		tree, err := syntax.Parse(read.Content)
		if err != nil {
			continue
		}
		fileDiagnostics, err := FileDiagnostics(workspace, file.Path, tree)
		if err != nil || len(fileDiagnostics) == 0 {
			continue
		}
		diagnostics[file.URI] = fileDiagnostics
	}
	return diagnostics
}

// ShouldWarnUntracked reports whether edge triggers the flake untracked-import
// warning: a flake+git workspace, an existing but untracked .nix target inside
// the workspace root. The code-action handler reuses it to decide where the
// quick fix applies, so it must stay in lockstep with the diagnostic.
func ShouldWarnUntracked(workspace project.Workspace, edge importedges.Edge) bool {
	if !workspace.HasFlake || !workspace.HasGit || edge.GitTracked {
		return false
	}
	if edge.TargetPath == "" || filepath.Ext(edge.TargetPath) != ".nix" {
		return false
	}
	return withinRoot(workspace.Root, edge.TargetPath)
}

func trackedFiles(workspace project.Workspace) map[string]bool {
	tracked := make(map[string]bool, len(workspace.Files))
	for _, file := range workspace.Files {
		if file.GitTracked {
			tracked[file.Path] = true
		}
	}
	return tracked
}

func withinRoot(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
