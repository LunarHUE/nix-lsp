// Package facts wires production analysis queries onto the memo engine.
package facts

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/flake"
	importedges "github.com/wesleybaldwin/nix-lsp/internal/analysis/imports"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/static"
	"github.com/wesleybaldwin/nix-lsp/internal/memo"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

const (
	QueryFileInput       = "FileInput"
	QueryWorkspace       = "Workspace"
	QueryParseTree       = "ParseTree"
	QueryImportEdges     = "ImportEdges"
	QueryScopes          = "Scopes"
	QueryFileDiagnostics = "FileDiagnostics"
	QueryFlakeLock       = "FlakeLock"
	QueryFlakeModel      = "FlakeModel"
)

const workspaceInputID = "current"

// flakeLockInputID keys the single current flake.lock input, mirroring the
// workspace singleton-input pattern.
const flakeLockInputID = "current"

// fileIDSeparator joins a file's path and content hash into a single memo key
// ID. Path is a genuine input to path-dependent queries (relative import
// resolution), so two files with identical content but different paths must not
// share one entry. The NUL byte cannot appear in a filesystem path, so the
// split is unambiguous.
const fileIDSeparator = "\x00"

// FileInput is the content and path data for one file identity.
type FileInput struct {
	Path    string
	Content []byte
}

// FileID returns the composite memo key ID for a file at path with the given
// content hash. All file-derived queries key on this so that identical content
// at different paths stays distinct.
func FileID(path, hash string) string {
	return path + fileIDSeparator + hash
}

// Register installs the production analysis queries.
func Register(engine *memo.Engine) {
	engine.Register(QueryParseTree, parseTree)
	engine.Register(QueryImportEdges, importEdges)
	engine.Register(QueryScopes, scopesQuery)
	engine.Register(QueryFlakeModel, flakeModel)
	engine.Register(QueryFileDiagnostics, fileDiagnostics)
}

// SetWorkspace stores the current workspace input.
func SetWorkspace(engine *memo.Engine, workspace project.Workspace) {
	engine.SetInput(WorkspaceKey(), workspace)
}

// SetFlakeLock stores the raw flake.lock bytes as the current lock input. An
// absent lock file is represented by nil content. The bytes are cloned so the
// DeepEqual input dedup keys on content.
func SetFlakeLock(engine *memo.Engine, content []byte) {
	engine.SetInput(FlakeLockKey(), cloneBytes(content))
}

// SetFileInput stores the current file input for fileID. fileID must be a
// composite produced by FileID(path, hash).
func SetFileInput(engine *memo.Engine, fileID string, input FileInput) {
	input.Content = cloneBytes(input.Content)
	engine.SetInput(FileInputKey(fileID), input)
}

// WorkspaceKey returns the current workspace input key.
func WorkspaceKey() memo.Key {
	return memo.Key{Kind: QueryWorkspace, ID: workspaceInputID}
}

// FlakeLockKey returns the current flake.lock input key.
func FlakeLockKey() memo.Key {
	return memo.Key{Kind: QueryFlakeLock, ID: flakeLockInputID}
}

// FlakeModelKey returns the flake input-model query key for fileID.
func FlakeModelKey(fileID string) memo.Key {
	return memo.Key{Kind: QueryFlakeModel, ID: fileID}
}

// FileInputKey returns the file input key for fileID.
func FileInputKey(fileID string) memo.Key {
	return memo.Key{Kind: QueryFileInput, ID: fileID}
}

// ParseTreeKey returns the parse tree query key for fileID.
func ParseTreeKey(fileID string) memo.Key {
	return memo.Key{Kind: QueryParseTree, ID: fileID}
}

// ImportEdgesKey returns the import edge query key for fileID.
func ImportEdgesKey(fileID string) memo.Key {
	return memo.Key{Kind: QueryImportEdges, ID: fileID}
}

// ScopesKey returns the scope-analysis query key for fileID.
func ScopesKey(fileID string) memo.Key {
	return memo.Key{Kind: QueryScopes, ID: fileID}
}

// FileDiagnosticsKey returns the diagnostics query key for fileID.
func FileDiagnosticsKey(fileID string) memo.Key {
	return memo.Key{Kind: QueryFileDiagnostics, ID: fileID}
}

// FileDiagnostics reads typed diagnostics from the memo engine. fileID must be a
// composite produced by FileID(path, hash).
func FileDiagnostics(ctx context.Context, engine *memo.Engine, fileID string) ([]syntax.Diagnostic, error) {
	value, err := engine.Get(ctx, FileDiagnosticsKey(fileID))
	if err != nil {
		return nil, err
	}
	diagnostics, ok := value.([]syntax.Diagnostic)
	if !ok {
		return nil, fmt.Errorf("facts: FileDiagnostics returned %T", value)
	}
	return cloneDiagnostics(diagnostics), nil
}

// Scopes reads the scope analysis for fileID from the memo engine. fileID must
// be a composite produced by FileID(path, hash). The returned *scopes.File is
// immutable after analysis and safe to share across callers.
func Scopes(ctx context.Context, engine *memo.Engine, fileID string) (*scopes.File, error) {
	value, err := engine.Get(ctx, ScopesKey(fileID))
	if err != nil {
		return nil, err
	}
	file, ok := value.(*scopes.File)
	if !ok {
		return nil, fmt.Errorf("facts: Scopes returned %T", value)
	}
	return file, nil
}

// ImportEdges reads the resolved import edges for fileID from the memo engine.
// fileID must be a composite produced by FileID(path, hash). The returned edges
// are immutable after analysis and safe to share across callers.
func ImportEdges(ctx context.Context, engine *memo.Engine, fileID string) ([]importedges.Edge, error) {
	value, err := engine.Get(ctx, ImportEdgesKey(fileID))
	if err != nil {
		return nil, err
	}
	edges, ok := value.([]importedges.Edge)
	if !ok {
		return nil, fmt.Errorf("facts: ImportEdges returned %T", value)
	}
	return edges, nil
}

// ParseTree reads the parse tree for fileID from the memo engine. fileID must be
// a composite produced by FileID(path, hash).
func ParseTree(ctx context.Context, engine *memo.Engine, fileID string) (*syntax.Tree, error) {
	value, err := engine.Get(ctx, ParseTreeKey(fileID))
	if err != nil {
		return nil, err
	}
	tree, ok := value.(*syntax.Tree)
	if !ok {
		return nil, fmt.Errorf("facts: ParseTree returned %T", value)
	}
	return tree, nil
}

// FlakeModel reads the flake input model for fileID from the memo engine.
// fileID must be a composite produced by FileID(path, hash).
func FlakeModel(ctx context.Context, engine *memo.Engine, fileID string) (*flake.File, error) {
	value, err := engine.Get(ctx, FlakeModelKey(fileID))
	if err != nil {
		return nil, err
	}
	model, ok := value.(*flake.File)
	if !ok {
		return nil, fmt.Errorf("facts: FlakeModel returned %T", value)
	}
	return model, nil
}

// FlakeLock reads and parses the current flake.lock input from the memo engine.
// The lock input is a plain input key (raw bytes set via SetFlakeLock), so a
// direct engine.Get returns the stored value without a query. Absent or
// unparseable content yields (nil, false, nil), matching the tolerant parse the
// diagnostics query uses. The returned *flake.Lock is immutable after parsing.
func FlakeLock(ctx context.Context, engine *memo.Engine) (*flake.Lock, bool, error) {
	value, err := engine.Get(ctx, FlakeLockKey())
	if err != nil {
		return nil, false, err
	}
	if value == nil {
		return nil, false, nil
	}
	content, ok := value.([]byte)
	if !ok {
		return nil, false, fmt.Errorf("facts: FlakeLock returned %T", value)
	}
	lock, has := parseFlakeLock(content)
	return lock, has, nil
}

func parseTree(ctx context.Context, q *memo.Context, key memo.Key) (any, error) {
	input, err := getFileInput(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	return syntax.Parse(input.Content)
}

func importEdges(ctx context.Context, q *memo.Context, key memo.Key) (any, error) {
	input, err := getFileInput(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	tree, err := getParseTree(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	workspace, err := getWorkspace(ctx, q)
	if err != nil {
		return nil, err
	}
	return importedges.Analyze(input.Path, tree, static.TrackedFiles(workspace))
}

func scopesQuery(ctx context.Context, q *memo.Context, key memo.Key) (any, error) {
	tree, err := getParseTree(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	return scopes.Analyze(tree), nil
}

func flakeModel(ctx context.Context, q *memo.Context, key memo.Key) (any, error) {
	tree, err := getParseTree(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	return flake.AnalyzeInputs(tree), nil
}

func fileDiagnostics(ctx context.Context, q *memo.Context, key memo.Key) (any, error) {
	input, err := getFileInput(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	tree, err := getParseTree(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	edges, err := getImportEdges(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	file, err := getScopes(ctx, q, key.ID)
	if err != nil {
		return nil, err
	}
	workspace, err := getWorkspace(ctx, q)
	if err != nil {
		return nil, err
	}

	diagnostics := tree.Diagnostics()
	diagnostics = append(diagnostics, static.ImportDiagnostics(workspace, edges)...)
	diagnostics = append(diagnostics, static.BindingDiagnostics(file, tree)...)

	// Only the workspace root flake.nix carries flake diagnostics.
	if workspace.Root != "" && input.Path == filepath.Join(workspace.Root, "flake.nix") {
		model, err := getFlakeModel(ctx, q, key.ID)
		if err != nil {
			return nil, err
		}
		lockBytes, err := getFlakeLock(ctx, q)
		if err != nil {
			return nil, err
		}
		lock, hasLock := parseFlakeLock(lockBytes)
		diagnostics = append(diagnostics, flake.Diagnostics(model, lock, hasLock)...)
	}
	return diagnostics, nil
}

// parseFlakeLock parses raw lock bytes tolerantly: nil/empty content or a parse
// error yields no usable lock, which suppresses the lock-dependent diagnostics
// rather than reporting the lock itself as broken.
func parseFlakeLock(content []byte) (*flake.Lock, bool) {
	if len(content) == 0 {
		return nil, false
	}
	lock, err := flake.ParseLock(content)
	if err != nil {
		return nil, false
	}
	return lock, true
}

func getFileInput(ctx context.Context, q *memo.Context, fileID string) (FileInput, error) {
	value, err := q.Get(ctx, FileInputKey(fileID))
	if err != nil {
		return FileInput{}, err
	}
	input, ok := value.(FileInput)
	if !ok {
		return FileInput{}, fmt.Errorf("facts: FileInput returned %T", value)
	}
	input.Content = cloneBytes(input.Content)
	return input, nil
}

func getWorkspace(ctx context.Context, q *memo.Context) (project.Workspace, error) {
	value, err := q.Get(ctx, WorkspaceKey())
	if err != nil {
		return project.Workspace{}, err
	}
	workspace, ok := value.(project.Workspace)
	if !ok {
		return project.Workspace{}, fmt.Errorf("facts: Workspace returned %T", value)
	}
	return workspace, nil
}

func getParseTree(ctx context.Context, q *memo.Context, fileID string) (*syntax.Tree, error) {
	value, err := q.Get(ctx, ParseTreeKey(fileID))
	if err != nil {
		return nil, err
	}
	tree, ok := value.(*syntax.Tree)
	if !ok {
		return nil, fmt.Errorf("facts: ParseTree returned %T", value)
	}
	return tree, nil
}

func getScopes(ctx context.Context, q *memo.Context, fileID string) (*scopes.File, error) {
	value, err := q.Get(ctx, ScopesKey(fileID))
	if err != nil {
		return nil, err
	}
	file, ok := value.(*scopes.File)
	if !ok {
		return nil, fmt.Errorf("facts: Scopes returned %T", value)
	}
	return file, nil
}

func getFlakeModel(ctx context.Context, q *memo.Context, fileID string) (*flake.File, error) {
	value, err := q.Get(ctx, FlakeModelKey(fileID))
	if err != nil {
		return nil, err
	}
	model, ok := value.(*flake.File)
	if !ok {
		return nil, fmt.Errorf("facts: FlakeModel returned %T", value)
	}
	return model, nil
}

func getFlakeLock(ctx context.Context, q *memo.Context) ([]byte, error) {
	value, err := q.Get(ctx, FlakeLockKey())
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	content, ok := value.([]byte)
	if !ok {
		return nil, fmt.Errorf("facts: FlakeLock returned %T", value)
	}
	return content, nil
}

func getImportEdges(ctx context.Context, q *memo.Context, fileID string) ([]importedges.Edge, error) {
	value, err := q.Get(ctx, ImportEdgesKey(fileID))
	if err != nil {
		return nil, err
	}
	edges, ok := value.([]importedges.Edge)
	if !ok {
		return nil, fmt.Errorf("facts: ImportEdges returned %T", value)
	}
	return edges, nil
}

func cloneBytes(content []byte) []byte {
	if len(content) == 0 {
		return nil
	}
	cloned := make([]byte, len(content))
	copy(cloned, content)
	return cloned
}

func cloneDiagnostics(diagnostics []syntax.Diagnostic) []syntax.Diagnostic {
	if len(diagnostics) == 0 {
		return nil
	}
	cloned := make([]syntax.Diagnostic, len(diagnostics))
	copy(cloned, diagnostics)
	return cloned
}
