# Foundations Course Correction

## Tree-sitter Binding and Grammar

Use `github.com/smacker/go-tree-sitter` as the Go binding and vendor the upstream `tree-sitter-nix` grammar under `third_party/tree-sitter-nix/`. Record the upstream repository and revision in that directory. CGO is accepted. The syntax package will own the cgo language bridge so the rest of the codebase depends only on `internal/syntax`.

## Syntax API

Replace the placeholder delimiter checker with a parser seam:

- `Parse(content []byte) (*Tree, error)`
- `Reparse(tree *Tree, edits []Edit, content []byte) (*Tree, error)` with full reparse behind the incremental-shaped API for now.
- `Tree.Diagnostics() []Diagnostic`
- `Tree.Walk(func(Node) bool)`
- Minimal typed helpers/wrappers for this session: `SelectExpr`, `Apply`, `Binding`, `List`, `PathLiteral`.
- Ranges become LSP-oriented line/character ranges via a position helper. Byte offsets may exist internally on nodes, but diagnostics and analysis-facing ranges should use line/character positions.

## Memo API

Replace manual dependency lists with tracked execution:

- Keys are structured as `(queryKind, key)`.
- A `Context.Get(queryKey)` call records a dependency edge from the currently running query to the read query automatically.
- File-derived queries key by content hash from a pinned VFS snapshot.
- Invalidation marks inputs dirty and lazily recomputes dependents on next read.
- Cycle detection returns an error instead of deadlocking.
- Production queries for this session: `ParseTree(fileHash)`, `ImportEdges(fileHash)`, `FileDiagnostics(fileHash)`.

## Unified Diagnostics Flow

One path computes diagnostics:

1. `didChange` applies the edit to the VFS overlay.
2. The handler pins a VFS snapshot.
3. The changed file content hash is used as the file fact key.
4. Memo invalidates the old file input and evaluates `FileDiagnostics(fileHash)`.
5. The diagnostics publisher receives `(uri, diagnostics, generation)`.
6. The publisher debounces edit-driven updates, drops stale generations, and sends `textDocument/publishDiagnostics`.

Workspace discovery uses the same path: pin one snapshot, evaluate `FileDiagnostics(fileHash)` for each workspace file, and submit results to the same publisher.

## Diagnostics Publisher

Add a single owned publisher goroutine started by the handler and stopped by handler shutdown. All diagnostics publication goes through it. Edit-driven updates debounce per URI around 150ms. Workspace results are queued through the same channel and drained at a bounded rate. No ad hoc per-file publishing goroutines.
