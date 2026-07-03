package facts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/memo"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestRegisteredQueriesProduceDiagnosticsAndDependencies(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	content := []byte("import ./missing.nix")
	id := FileID(source, vfs.ContentHash(content))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: content})

	diagnostics, err := FileDiagnostics(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}

	deps := keySet(engine.Dependencies(FileDiagnosticsKey(id)))
	if !deps[ParseTreeKey(id)] {
		t.Fatalf("FileDiagnostics did not depend on ParseTree: %v", deps)
	}
	if !deps[ImportEdgesKey(id)] {
		t.Fatalf("FileDiagnostics did not depend on ImportEdges: %v", deps)
	}
	if !deps[WorkspaceKey()] {
		t.Fatalf("FileDiagnostics did not depend on Workspace: %v", deps)
	}

	importDeps := keySet(engine.Dependencies(ImportEdgesKey(id)))
	if !importDeps[ParseTreeKey(id)] {
		t.Fatalf("ImportEdges did not depend on ParseTree: %v", importDeps)
	}
	if !importDeps[FileInputKey(id)] {
		t.Fatalf("ImportEdges did not depend on FileInput: %v", importDeps)
	}
}

// TestFileDiagnosticsDependsOnScopesAndReportsUnused verifies the scope-based
// binding diagnostics reach the diagnostics query through the memo path and that
// FileDiagnostics records its dependency on the Scopes query.
func TestFileDiagnosticsDependsOnScopesAndReportsUnused(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "let x = 1; in 2")
	content := []byte("let x = 1; in 2")
	id := FileID(source, vfs.ContentHash(content))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: content})

	diagnostics, err := FileDiagnostics(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want 1", diagnostics)
	}
	if diagnostics[0].Message != `unused binding "x"` {
		t.Fatalf("message = %q, want unused binding", diagnostics[0].Message)
	}
	if diagnostics[0].Severity != syntax.SeverityWarning {
		t.Fatalf("severity = %v, want warning", diagnostics[0].Severity)
	}

	deps := keySet(engine.Dependencies(FileDiagnosticsKey(id)))
	if !deps[ScopesKey(id)] {
		t.Fatalf("FileDiagnostics did not depend on Scopes: %v", deps)
	}

	scopeDeps := keySet(engine.Dependencies(ScopesKey(id)))
	if !scopeDeps[ParseTreeKey(id)] {
		t.Fatalf("Scopes did not depend on ParseTree: %v", scopeDeps)
	}
}

// TestIdenticalContentDistinctPathsDoNotCollide guards the composite (path,
// hash) key design: two files with byte-identical content but different
// directories must resolve their relative imports independently, not share one
// memo entry whose stored path flip-flops with analysis order.
func TestIdenticalContentDistinctPathsDoNotCollide(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()

	// Same content in two sibling directories. dirA has the import target on
	// disk; dirB does not. With hash-only keys these would collide.
	content := []byte("import ./target.nix")
	hash := vfs.ContentHash(content)

	dirA := filepath.Join(root, "a")
	sourceA := writeFile(t, filepath.Join(dirA, "default.nix"), string(content))
	writeFile(t, filepath.Join(dirA, "target.nix"), "{}")

	dirB := filepath.Join(root, "b")
	sourceB := writeFile(t, filepath.Join(dirB, "default.nix"), string(content))

	idA := FileID(sourceA, hash)
	idB := FileID(sourceB, hash)
	if idA == idB {
		t.Fatalf("FileID collided for distinct paths: %q", idA)
	}

	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, idA, FileInput{Path: sourceA, Content: content})
	SetFileInput(engine, idB, FileInput{Path: sourceB, Content: content})

	diagA, err := FileDiagnostics(context.Background(), engine, idA)
	if err != nil {
		t.Fatalf("FileDiagnostics A error = %v", err)
	}
	diagB, err := FileDiagnostics(context.Background(), engine, idB)
	if err != nil {
		t.Fatalf("FileDiagnostics B error = %v", err)
	}

	// a/target.nix exists -> no missing-import diagnostic.
	if len(diagA) != 0 {
		t.Fatalf("dir A diagnostics = %+v, want none (target exists)", diagA)
	}
	// b/target.nix does not exist -> exactly one missing-import diagnostic.
	if len(diagB) != 1 || diagB[0].Message != "missing import target ./target.nix" {
		t.Fatalf("dir B diagnostics = %+v, want one missing-import", diagB)
	}

	// Re-evaluating A must not have been disturbed by B's input.
	diagA2, err := FileDiagnostics(context.Background(), engine, idA)
	if err != nil {
		t.Fatalf("re-eval A error = %v", err)
	}
	if len(diagA2) != 0 {
		t.Fatalf("dir A diagnostics after B = %+v, want none", diagA2)
	}
}

func TestFileInputInvalidationRecomputesDiagnostics(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	first := []byte("import ./missing.nix")
	second := []byte("import ./still-missing.nix")
	// The file identity is stable across the edit here (same path); tests that
	// mimic the handler recompute an ID per content hash. Keep this test focused
	// on invalidation, so hold the ID fixed and swap only the input value.
	id := FileID(source, vfs.ContentHash(first))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: first})
	if _, err := FileDiagnostics(context.Background(), engine, id); err != nil {
		t.Fatalf("first FileDiagnostics error = %v", err)
	}

	SetFileInput(engine, id, FileInput{Path: source, Content: second})
	diagnostics, err := FileDiagnostics(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("second FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if diagnostics[0].Message != "missing import target ./still-missing.nix" {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}

	stats := engine.Stats()
	if stats.Recomputes[ParseTreeKey(id)] != 2 {
		t.Fatalf("ParseTree recomputes = %d, want 2", stats.Recomputes[ParseTreeKey(id)])
	}
	if stats.Recomputes[FileDiagnosticsKey(id)] != 2 {
		t.Fatalf("FileDiagnostics recomputes = %d, want 2", stats.Recomputes[FileDiagnosticsKey(id)])
	}
}

// TestImportEdgesGetterReturnsTypedEdgesThroughMemo verifies the exported
// ImportEdges getter surfaces resolved, typed edges via the memo path and records
// the same dependencies as the internal query.
func TestImportEdgesGetterReturnsTypedEdgesThroughMemo(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./target.nix")
	target := writeFile(t, filepath.Join(root, "target.nix"), "{}")
	content := []byte("import ./target.nix")
	id := FileID(source, vfs.ContentHash(content))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: content})

	edges, err := ImportEdges(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("ImportEdges error = %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d (%+v), want 1", len(edges), edges)
	}
	edge := edges[0]
	if edge.Literal != "./target.nix" {
		t.Errorf("edge literal = %q, want ./target.nix", edge.Literal)
	}
	if !edge.Exists {
		t.Errorf("edge exists = false, want true")
	}
	if edge.TargetPath != normalize(t, target) {
		t.Errorf("edge target = %q, want %q", edge.TargetPath, normalize(t, target))
	}

	deps := keySet(engine.Dependencies(ImportEdgesKey(id)))
	if !deps[ParseTreeKey(id)] {
		t.Fatalf("ImportEdges did not depend on ParseTree: %v", deps)
	}
	if !deps[FileInputKey(id)] {
		t.Fatalf("ImportEdges did not depend on FileInput: %v", deps)
	}
}

// TestFlakeDiagnosticsOnlyForRootFlake verifies flake diagnostics attach to the
// workspace root flake.nix and to no other file.
func TestFlakeDiagnosticsOnlyForRootFlake(t *testing.T) {
	engine := NewEngineForTest()
	SetFlakeLock(engine, nil)
	root := t.TempDir()
	normRoot := normalize(t, root)

	// Root flake.nix: input "other" follows undeclared "nixpkgs" -> dangling.
	flakeSrc := `{ inputs.other.follows = "nixpkgs"; }`
	flakePath := writeFile(t, filepath.Join(root, "flake.nix"), flakeSrc)
	flakeID := FileID(normalize(t, flakePath), vfs.ContentHash([]byte(flakeSrc)))

	// A non-flake file with identical follows content must get no flake diagnostic.
	otherSrc := flakeSrc
	otherPath := writeFile(t, filepath.Join(root, "other.nix"), otherSrc)
	otherID := FileID(normalize(t, otherPath), vfs.ContentHash([]byte(otherSrc)))

	SetWorkspace(engine, project.Workspace{Root: normRoot})
	SetFileInput(engine, flakeID, FileInput{Path: normalize(t, flakePath), Content: []byte(flakeSrc)})
	SetFileInput(engine, otherID, FileInput{Path: normalize(t, otherPath), Content: []byte(otherSrc)})

	flakeDiags, err := FileDiagnostics(context.Background(), engine, flakeID)
	if err != nil {
		t.Fatalf("flake FileDiagnostics error = %v", err)
	}
	if !containsCode(flakeDiags, "dangling-follows") {
		t.Fatalf("flake.nix diagnostics = %+v, want dangling-follows", flakeDiags)
	}

	otherDiags, err := FileDiagnostics(context.Background(), engine, otherID)
	if err != nil {
		t.Fatalf("other FileDiagnostics error = %v", err)
	}
	if containsCode(otherDiags, "dangling-follows") {
		t.Fatalf("non-flake file got flake diagnostic: %+v", otherDiags)
	}
}

// TestFlakeLockInvalidationChangesDiagnostics verifies that changing the lock
// input invalidates the root flake.nix diagnostics.
func TestFlakeLockInvalidationChangesDiagnostics(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	normRoot := normalize(t, root)

	flakeSrc := `{ inputs.a.url = "u"; outputs = { self, a }: {}; }`
	flakePath := writeFile(t, filepath.Join(root, "flake.nix"), flakeSrc)
	flakeID := FileID(normalize(t, flakePath), vfs.ContentHash([]byte(flakeSrc)))

	SetWorkspace(engine, project.Workspace{Root: normRoot})
	SetFileInput(engine, flakeID, FileInput{Path: normalize(t, flakePath), Content: []byte(flakeSrc)})

	// No lock -> input "a" reported not-locked.
	SetFlakeLock(engine, []byte(`{"root":"root","version":7,"nodes":{"root":{}}}`))
	diags, err := FileDiagnostics(context.Background(), engine, flakeID)
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if !containsCode(diags, "input-not-locked") {
		t.Fatalf("diagnostics = %+v, want input-not-locked", diags)
	}

	// Now the lock locks "a" -> the warning disappears (input invalidation).
	SetFlakeLock(engine, []byte(`{"root":"root","version":7,"nodes":{"root":{"inputs":{"a":"a"}},"a":{}}}`))
	diags2, err := FileDiagnostics(context.Background(), engine, flakeID)
	if err != nil {
		t.Fatalf("second FileDiagnostics error = %v", err)
	}
	if containsCode(diags2, "input-not-locked") {
		t.Fatalf("input-not-locked persisted after locking: %+v", diags2)
	}
}

// TestImportEdgesInvalidatedByGitState verifies the stale-untracked-warning fix at
// the memo layer: import-edges records the git-state input as a dependency, a token
// bump alone forces a recompute (previously the cached GitTracked:false edge was
// served), and a terminal `git add` followed by a re-discovery + token bump flips
// GitTracked to true without any file-content change.
func TestImportEdgesInvalidatedByGitState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	engine := NewEngineForTest()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	source := writeFile(t, filepath.Join(root, "importer.nix"), "import ./target.nix")
	writeFile(t, filepath.Join(root, "target.nix"), "{}")
	content := []byte("import ./target.nix")
	id := FileID(normalize(t, source), vfs.ContentHash(content))

	runGitFixture(t, root, "init")
	runGitFixture(t, root, "add", "flake.nix", "importer.nix")

	discover := func() project.Workspace {
		ws, err := project.Discover(root)
		if err != nil {
			t.Fatalf("Discover error = %v", err)
		}
		return ws
	}

	SetWorkspace(engine, discover())
	SetFileInput(engine, id, FileInput{Path: normalize(t, source), Content: content})
	SetGitState(engine, "token-1")

	edges, err := ImportEdges(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("ImportEdges error = %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edges = %d (%+v), want 1", len(edges), edges)
	}
	if edges[0].GitTracked {
		t.Fatalf("target reported git-tracked before git add")
	}

	deps := keySet(engine.Dependencies(ImportEdgesKey(id)))
	if !deps[GitStateKey()] {
		t.Fatalf("ImportEdges did not depend on GitState: %v", deps)
	}

	before := engine.Stats().Recomputes[ImportEdgesKey(id)]

	// A git-state token bump alone invalidates the cached edges (the crux of the
	// fix): the query recomputes though file content and the workspace value are
	// unchanged. The tracked set still derives from the stale workspace, so
	// GitTracked stays false until a re-discovery supplies fresh data.
	SetGitState(engine, "token-1b")
	if _, err := ImportEdges(context.Background(), engine, id); err != nil {
		t.Fatalf("ImportEdges after token bump error = %v", err)
	}
	if got := engine.Stats().Recomputes[ImportEdgesKey(id)]; got != before+1 {
		t.Fatalf("token bump did not recompute ImportEdges (recomputes %d -> %d)", before, got)
	}

	// Terminal git add now tracks target.nix. Re-discover the workspace (as the
	// server does on the .git/index event) and bump the token: GitTracked flips to
	// true with no file-content change.
	runGitFixture(t, root, "add", "target.nix")
	SetWorkspace(engine, discover())
	SetGitState(engine, "token-2")

	edges2, err := ImportEdges(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("ImportEdges after git add error = %v", err)
	}
	if !edges2[0].GitTracked {
		t.Fatalf("target still untracked after git add + token bump: %+v", edges2[0])
	}
}

// TestFileInputSupersessionBoundsEntries is the eviction proof: an edit burst on
// a single path mints a fresh FileID every keystroke, and without eviction the
// memo engine accumulates every superseded FileID's input and derived entries
// forever. After eviction the entry count must plateau near one file's worth of
// state plus the shared singletons, not grow linearly with the number of edits.
func TestFileInputSupersessionBoundsEntries(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := normalize(t, filepath.Join(root, "default.nix"))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})

	const edits = 40
	for i := 0; i < edits; i++ {
		content := []byte(fmt.Sprintf("let x = %d; in x", i))
		fileID := SupersedeFileInput(engine, source, vfs.ContentHash(content), content)
		if _, err := FileDiagnostics(context.Background(), engine, fileID); err != nil {
			t.Fatalf("edit %d FileDiagnostics error = %v", i, err)
		}
	}

	// One live file drives at most FileInput+ParseTree+ImportEdges+Scopes+
	// FileDiagnostics (5) derived/input entries, plus the Workspace and GitState
	// singletons (2). Allow generous headroom; the point is the count does not
	// scale with edits (which would be 5*40+2 without eviction).
	const bound = 12
	if entries := engine.Stats().Entries; entries > bound {
		t.Fatalf("engine retained %d entries after %d edits, want <= %d; superseded FileIDs are not evicted", entries, edits, bound)
	}
}

// TestFeatureRegistrationDoesNotEvictNewerFileID pins the writer/reader split:
// a feature request (hover/completion) holding a slightly OLDER snapshot
// registers its input via the non-evicting FileInputFor. If that registration
// evicted, it would tear down the NEWER FileID's entries out from under a
// mid-flight diagnostics compute, whose FileDiagnostics would then error with
// ErrNoQuery and that edit's publish would be lost with nothing to re-trigger
// it. Only the diagnostics coalescer (single writer per path) may supersede.
func TestFeatureRegistrationDoesNotEvictNewerFileID(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := normalize(t, filepath.Join(root, "default.nix"))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})

	// The diagnostics writer registers the newest edit.
	newContent := []byte("let x = 2; in x")
	newFileID := SupersedeFileInput(engine, source, vfs.ContentHash(newContent), newContent)

	// A feature request arrives holding the pre-edit snapshot and registers it.
	oldContent := []byte("let x = 1; in x")
	FileInputFor(engine, source, vfs.ContentHash(oldContent), oldContent)

	// The newer FileID's input must have survived: the diagnostics compute that
	// registered it can still complete.
	if _, err := FileDiagnostics(context.Background(), engine, newFileID); err != nil {
		t.Fatalf("newer FileID's compute failed after a feature registered an older snapshot: %v", err)
	}
}

// TestConcurrentReadDuringSupersession stresses the eviction-safety contract: one
// goroutine repeatedly supersedes a path's FileID (dropping the prior FileID's
// entries) while others hammer reads of an older FileID. A read of an evicted
// FileID must never panic or return wrong data — it either recomputes cleanly
// (the reader re-registers its own input via FileInputFor) or surfaces the
// engine's clean miss. Run under -race to catch data races on the entries map.
func TestConcurrentReadDuringSupersession(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := normalize(t, filepath.Join(root, "default.nix"))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})

	// A stable "old" FileID, registered once. The writer below will evict it as it
	// supersedes the path; the readers keep reading it without re-registering.
	oldContent := []byte("let old = 0; in old")
	oldFileID := FileInputFor(engine, source, vfs.ContentHash(oldContent), oldContent)

	var wg sync.WaitGroup
	errs := make(chan error, 32)

	// Writer: bursts of superseding edits (single writer per path, as the server's
	// coalescer guarantees; only this writer uses the evicting SupersedeFileInput —
	// readers register non-evictingly, mirroring the feature/diagnostics split).
	// Each edit's Forget drops the prior FileID — including the old one the readers
	// hold. The writer's own read always succeeds because no reader supersedes the
	// path.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			content := []byte(fmt.Sprintf("let x = %d; in x", i))
			fileID := SupersedeFileInput(engine, source, vfs.ContentHash(content), content)
			if _, err := FileDiagnostics(context.Background(), engine, fileID); err != nil {
				errs <- fmt.Errorf("writer edit %d: %w", i, err)
				return
			}
		}
	}()

	// Readers: hammer the old FileID the writer is evicting. A read either succeeds
	// (it beat the eviction, or recomputed before its input was dropped) or returns
	// ErrNoQuery — the engine's clean miss once the plain FileInput key is gone and
	// has no query to recompute from. Never a panic, a data race, or wrong data.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				_, err := ParseTree(context.Background(), engine, oldFileID)
				if err != nil && !errors.Is(err, memo.ErrNoQuery) {
					errs <- fmt.Errorf("reader iter %d: %w", i, err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestRepairedParseTreeCleanFileReusesPlainTree verifies the happy path: a clean
// file's repaired result carries Repaired=false and the very same tree the plain
// ParseTree query produced (no extra parse), and the query depends on ParseTree.
func TestRepairedParseTreeCleanFileReusesPlainTree(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "{ x = 1; }")
	content := []byte("{ x = 1; }")
	id := FileID(source, vfs.ContentHash(content))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: content})

	plain, err := ParseTree(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("ParseTree error = %v", err)
	}
	res, err := RepairedParseTree(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("RepairedParseTree error = %v", err)
	}
	if res.Repaired {
		t.Errorf("Repaired = true for clean file")
	}
	if res.Tree != plain {
		t.Errorf("repaired result did not reuse the memoized plain tree")
	}

	deps := keySet(engine.Dependencies(RepairedParseTreeKey(id)))
	if !deps[ParseTreeKey(id)] {
		t.Fatalf("RepairedParseTree did not depend on ParseTree: %v", deps)
	}
	if deps[FileInputKey(id)] {
		t.Errorf("clean-path RepairedParseTree read FileInput; it should short-circuit on the clean tree")
	}
}

// TestRepairedParseTreeBrokenFileRepairs verifies the broken path: an
// unterminated binding yields a repaired, error-free tree with Repaired=true,
// caches under the ORIGINAL fileID, and depends on both ParseTree and FileInput.
func TestRepairedParseTreeBrokenFileRepairs(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "{ x = 1 }")
	content := []byte("{ x = 1 }")
	id := FileID(source, vfs.ContentHash(content))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: content})

	res, err := RepairedParseTree(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("RepairedParseTree error = %v", err)
	}
	if !res.Repaired {
		t.Fatalf("Repaired = false, want true for broken file")
	}
	if res.Tree.Root().HasError() {
		t.Errorf("repaired tree still has errors")
	}
	if got := string(res.Tree.Content()); got != "{ x = 1; }" {
		t.Errorf("repaired content = %q, want %q", got, "{ x = 1; }")
	}
	// The plain ParseTree fact keeps reporting the error: repair never feeds it.
	plain, err := ParseTree(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("ParseTree error = %v", err)
	}
	if !plain.Root().HasError() {
		t.Errorf("plain ParseTree lost its error; repair must not touch the diagnostics tree")
	}

	deps := keySet(engine.Dependencies(RepairedParseTreeKey(id)))
	if !deps[ParseTreeKey(id)] {
		t.Fatalf("RepairedParseTree did not depend on ParseTree: %v", deps)
	}
	if !deps[FileInputKey(id)] {
		t.Fatalf("broken-path RepairedParseTree did not depend on FileInput: %v", deps)
	}
}

func runGitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v\n%s", args, err, out)
	}
}

func containsCode(diags []syntax.Diagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

func NewEngineForTest() *memo.Engine {
	engine := memo.New()
	Register(engine)
	// Seed the git-state input the import-edges query always reads; production
	// NewHandler seeds it the same way. Tests that exercise git tracking override
	// it with SetGitState.
	SetGitState(engine, "")
	return engine
}

func writeFile(t *testing.T, path string, content string) string {
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

func keySet(keys []memo.Key) map[memo.Key]bool {
	set := make(map[memo.Key]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}
