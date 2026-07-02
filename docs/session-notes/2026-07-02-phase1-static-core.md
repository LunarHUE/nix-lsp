# Phase 1 Static Core — Session Notes (2026-07-02)

State handoff for the next session. Read this before continuing work.

## Where the project stands

Phase 0 (skeleton) and the foundations course-correction are complete. Phase 1
(whole-workspace static core, `docs/implementation-plan.md`) is now ~80% done.

Commits this session, newest first (all gate-verified: build, vet, test,
test -race, plus a real-binary stdio smoke test per feature stage):

- `904bad0` server: add workspace symbols and watched-file diagnostics refresh
- `4c65ba8` server: add references, folding ranges, and import-path definition
- `8153750` server: add document symbols, definition, and highlights
- `54c2eb1` analysis: add unused, duplicate, and bad-inherit diagnostics with severities
- `6e096cf` analysis: add scope tree and symbol table package

## Feature inventory (what the server does now)

- Diagnostics, all through one memoized `FileDiagnostics` query with a
  debounced generation-guarded publisher: syntax errors (Error), missing
  import target (Error), flake import-target-not-git-tracked (Warning),
  unused let bindings (Warning, `_`-prefix skipped), duplicate bindings in
  one binding set (Error), bare `inherit` of undefined variable (Error).
- textDocument: documentSymbol (hierarchical), definition (within-file
  identifiers + cross-file jump on import/imports/callPackage path literals),
  documentHighlight (write + reads), references (within-file,
  includeDeclaration honored, builtins get no declaration), foldingRange
  (attrset/rec/let/list/function, nested same-span chains deduped).
- workspace: symbol (all files via memo cache, case-insensitive substring,
  cap 128), didChangeWatchedFiles (one re-discovery per batch; changed
  non-open files recomputed via the same generation path; deletions clear
  diagnostics like didClose; open buffers always win over disk; open files
  re-checked after tracked-set changes so untracked-import warnings update).
- VS Code dev client (`editors/vscode`) registers a `**/*.nix` watcher via
  `synchronize.fileEvents`. README "Testing in VS Code" is current.

## Phase 1 remaining

- Code action for the flake untracked-import trap (diagnostic exists; the
  "run git add" quick fix does not). Recommended next slice.
- Parse pool with LSP progress reporting during workspace discovery.
- Persistent file facts keyed by content hash (was explicitly deferred).
- Cross-file *identifier* definition (only import paths cross files today).
- Incremental reparse (Reparse is API-stable but does a full parse).

Then Phase 2: flake.lock parsing, input hover/navigation/completion,
unused-input diagnostics.

## Architecture facts the next session must know

- Memo keys: file-derived queries key on composite
  `facts.FileID(path, hash) = path + "\x00" + hash` — NOT hash alone
  (identical content in two dirs must not share import resolution; this
  deliberately overrides the original plan doc, user-approved).
- Query graph: FileDiagnostics → {ParseTree, ImportEdges, Scopes, Workspace};
  ImportEdges → {FileInput, ParseTree, Workspace}; Scopes → ParseTree.
  `Context.Get` records edges automatically; `SetInput` dedups clean inputs
  via `reflect.DeepEqual` (so re-setting the same FileInput is a true no-op —
  feature handlers rely on this by calling SetFileInput per request).
- Handler diagnostics ordering: per-URI `diagGeneration` map guards the
  handler cache AND the publisher drops stale generations. Any new
  diagnostics-producing path must go through `computeFileDiagnostics` /
  `publishEmptyDiagnostics` with `h.nextGeneration()` — never write
  `h.diagnostics` directly.
- `syntax.Tree` navigation is serialized by a per-tree mutex because the
  vendored smacker/go-tree-sitter mutates an unlocked node cache in
  RootNode/ChildByFieldName/NamedChild/Parent. Node.Kind/Text/Range are
  lock-free pure reads. Memoized trees ARE shared across goroutines; keep it
  that way only through the `internal/syntax` wrappers.
- `scopes.Binding.DefScope` for plain attrset keys is the enclosing *lexical*
  scope, shared by sibling attrsets — never group AttrBindings by DefScope
  (the duplicate-binding check walks binding_set nodes directly for this
  reason). Duplicate check keys full attrpath text; `a.b`+`a.c` legal,
  sibling sets never collide.
- Scope semantics implemented to match Nix: lexical bindings beat `with`
  (references unresolved under `with` get `WithUncertain`, not nil-error);
  bare-inherit sources resolve in the OUTER scope; formal defaults see
  sibling formals and the @-pattern; builtins resolve last (package-level
  `builtinNames` set — deliberately conservative/incomplete, which is why
  there is NO general undefined-variable diagnostic yet; only
  `Reference.FromInherit` refs are flagged).
- `syntax.Severity` zero value is SeverityError on purpose (old construction
  sites stay errors). Handler maps to LSP ints in `lspSeverity`.
- Feature request handlers live in `internal/server/features.go`: decode →
  `fileInputForURI` (uri→path→snapshot read→FileID→SetFileInput) → memo
  getter → pure helper function; every failure returns `nil, nil` (LSP null),
  never an error. Open-buffer detection = `Snapshot.HasOverlay` /
  `OverlayPaths` (there is no separate open-doc registry).

## Environment & workflow (user-set rules)

- Go is ONLY inside the nix devshell: `nix develop --command go ...`
  (currently go 1.26.4). Bare `go` is not on PATH. flake.nix provides it.
- Working model split: Fable 5 plans/reviews/commits; Opus subagents write
  the code. Review every agent diff before committing; agents must not commit.
- Commits: ONE line, conventional prefix (`analysis:`, `server:`, `docs:`,
  `foundation:`), NO Co-Authored-By or any trailer. Commit per green stage.
- NEVER `git add -A` from repo root — editors/vscode/node_modules got
  committed once and had to be amended out (node_modules is gitignored now,
  but stay explicit: `git add README.md internal/ ...`).
- Verification gate per stage: build, vet, `test ./... -count=1`,
  `test -race ./internal/server/`, plus an end-to-end smoke against the real
  binary over framed stdio (drivers were written ad hoc in the session
  scratchpad; pattern: initialize → didOpen → requests → assert published
  JSON). Delete smoke binaries/workspaces afterwards.
- User's uncommitted `flake.nix` change (adding go) must be left alone for
  them to commit.

## Known warts / deferred small items

- workspace/symbol early-stop scans files in path order and assumes it
  matches URI order (true for ASCII paths; revisit if it ever matters).
- Every workspace/symbol request re-reads all workspace files from the
  snapshot (SetFileInput dedup makes analysis cached, but reads are O(files)).
- Folding emits raw node end line; some clients prefer endLine-1 to keep the
  closing brace visible — revisit if VS Code folding looks off.
- LifecycleHandler (minimal lsp fallback) does not serve feature requests;
  capabilities are tested via JSON round-trip in internal/lsp.
