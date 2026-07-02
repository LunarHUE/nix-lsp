# Nix LSP Implementation Plan

This project is a Go language server for Nix with whole-workspace static analysis, flake-native diagnostics, and asynchronous Nix-backed semantic indexes.

## Architecture

The server is split into five layers:

1. **LSP frontend:** JSON-RPC transport, request scheduling, cancellation, progress, diagnostics publishing.
2. **Syntax:** tree-sitter parsing, CST helpers, range conversion, syntax diagnostics.
3. **Static analysis:** scopes, symbols, imports, inferred value shapes, static diagnostics.
4. **Semantic oracle:** cached `nix` subprocess workers for package indexes, option indexes, flake metadata, and eval-backed facts.
5. **Project model:** VFS overlays, workspace crawl, git tracked-set state, file watching, persistence.

The key rule is that interactive LSP requests must only use in-memory facts and cached oracle data. Fresh `nix` subprocess work always runs in the background and refreshes caches later.

## Phase 0: Skeleton

Goal: create a runnable server foundation.

Scope:

- Stdio JSON-RPC/LSP transport.
- Scheduler seams for interactive, responsive, and background work.
- Context cancellation through request handlers.
- VFS with editor-buffer overlays and immutable snapshots.
- In-memory memoization primitives with dependent invalidation.
- Syntax diagnostics API and tree-sitter integration spike.
- Basic `initialize`, `shutdown`, and `exit` flow.

Exit criteria:

- `go test ./...` passes.
- `go run ./cmd/nixls` starts an LSP server over stdio.
- A client can initialize and shut down cleanly.
- VFS snapshots remain stable across edits.

## Phase 1: Whole-Workspace Static Core

Goal: ship the first differentiated behavior.

Scope:

- Workspace root detection from `flake.nix`, `.git`, or LSP root URI.
- `git ls-files '*.nix'` tracked-file crawl plus bounded untracked-file discovery.
- Parse pool with progress reporting.
- Scope tree, symbol tables, import graph, and static diagnostics.
- Diagnostics for syntax errors, missing imports, untracked imported files in flakes, unused bindings, duplicate attrs, and bad `inherit`.
- Definition, references, document symbols, workspace symbols, highlights, folding.
- File watching, git-index refresh, graph fan-out invalidation.
- Persistent file facts keyed by content hash.

Exit criteria:

- Diagnostics appear for unopened files shortly after workspace open.
- The flake untracked-file trap is detected with a code action-ready diagnostic.
- Branch switches re-analyze changed content and dependents only.

## Phase 2: Flake Intelligence

Goal: make `flake.nix` and `flake.lock` first-class.

Scope:

- Parse `flake.lock` directly.
- Model inputs, follows edges, locked revs, nar hashes, and timestamps.
- Hover and navigation for inputs and lock entries.
- Diagnostics for unused inputs, dangling follows, and duplicate nixpkgs revisions.
- Completion for input names and follows targets.
- Code actions to add/remove/update inputs.

Exit criteria:

- A flake workspace demo shows input completion, hover, navigation, and stale/dangling input diagnostics.

## Phase 3: Package Index

Goal: provide locked-revision package intelligence without blocking editing.

Scope:

- Nix runner pool with timeouts, cancellation, stderr classification, and JSON parsing.
- Package index keyed by the locked nixpkgs revision.
- Sharded attr dump with `tryEval` armor.
- bbolt-backed storage and in-memory trigram index.
- Bundled bootstrap index for cold starts.
- `pkgs.` completion and hover.
- Unknown package, alias, and did-you-mean diagnostics.

Exit criteria:

- `pkgs.htoop` warns with a suggestion for `htop` against the locked nixpkgs revision.
- Cold indexing is visible as background progress and never blocks completion or hover.

## Phase 4: Option Index

Goal: provide NixOS, home-manager, nix-darwin, and user-flake module option intelligence.

Scope:

- Option evaluators for upstream module systems and user flake configurations.
- Option completion, hover, and go-to declared definition.
- Unknown option diagnostics.
- Static value-kind checks using option type strings.
- Background rebuilds when lockfiles or module imports change.

Exit criteria:

- Completion includes options declared by the user's own modules.
- Unknown and wrong-kind option diagnostics degrade gracefully when eval fails.

## Phase 5: Editing Polish

Goal: move from useful diagnostics to comfortable daily editing.

Scope:

- Rename with conflict detection and `with`-scope uncertainty warnings.
- Conservative attrset shape inference.
- User-value completion and hover.
- Signature help and inlay hints.
- Semantic tokens.
- Organize package lists and inherit groups.

Exit criteria:

- Rename works across files and refuses unsafe shadowing cases.
- Inference-backed completions are useful but never knowingly wrong.

## Phase 6: Actions, Lenses, and Staleness

Goal: add opt-in commands that can run longer Nix operations.

Scope:

- Hash replacement workflow for fixed-output derivations.
- Build, check, and closure-size lenses.
- Flake input staleness checks.
- Progress and cancellation for every long-running command.

Exit criteria:

- Long-running commands are cancellable, surfaced with progress, and disabled by default unless appropriate.

## What We Need To Start

- Choose the initial LSP protocol approach after the Phase 0 framing spike. The current starter uses a minimal internal JSON-RPC layer so we can switch libraries later without touching analysis code.
- Decide when to introduce tree-sitter. Phase 0 should pin the grammar and prove parsing/range conversion before Phase 1 depends on it.
- Decide persistence encoding before Phase 1 warm-start work: gob is fastest to implement; msgpack can be benchmarked after the file-facts schema settles.
- Confirm the first editor target for integration testing. Neovim is a good default because it is easy to drive locally; VS Code can follow once capabilities stabilize.
- Confirm module path and repository remote before publishing. The scaffold currently uses `github.com/wesleybaldwin/nix-lsp`.

## Current Parallel Workstreams

- **Worker A:** `internal/vfs` overlay filesystem and immutable snapshots.
- **Worker B:** `internal/memo` in-memory memoization and invalidation.
- **Worker C:** `internal/lsp` JSON-RPC framing and minimal server loop.
- **Main thread:** repository setup, roadmap, package seams, and integration.
