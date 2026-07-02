# nix-lsp

`nix-lsp` is a Go language server for Nix focused on whole-workspace analysis, flake-aware diagnostics, and fast interactive editor features.

This repository is being built in phases. The initial implementation starts with the Phase 0 foundation:

- JSON-RPC/LSP transport over stdio
- request scheduling and cancellation seams
- VFS overlay snapshots for open editor buffers
- syntax diagnostics surface area
- memoized analysis primitives

See [docs/implementation-plan.md](docs/implementation-plan.md) for the phased roadmap.

## Development

```sh
go test ./...
go run ./cmd/nixls
```

## Testing in VS Code

The server speaks LSP/JSON-RPC over stdio. It publishes diagnostics and answers
within-file document symbols (outline), go-to-definition (which also follows
import paths and attribute selection into other files), find-all-references,
folding ranges,
document highlights, hover on flake inputs, and workspace-wide symbol search.
You can try it end-to-end
in VS Code with the bundled development client under
[editors/vscode](editors/vscode).

### 1. Build the server

From the repository root:

```sh
go build -o nixls ./cmd/nixls
```

This produces a `./nixls` binary in the repo root.

### 2. Sanity check

```sh
./nixls -version
```

This prints the version and exits. If it prints, the binary is good to go.

### 3. Try it in an editor

**a. Bundled dev extension (recommended)**

```sh
cd editors/vscode
npm install
npm run compile
```

Then open the `editors/vscode/` folder in VS Code and press `F5`
("Run Extension") to launch an Extension Development Host. In that new window,
set `nixls.serverPath` (Settings -> search "nixls") to the **absolute path** of
the built binary, e.g. `/absolute/path/to/nix-lsp/nixls`. Leave it as `nixls`
if the binary is already on your `PATH`. See
[editors/vscode/README.md](editors/vscode/README.md) for details.

After rebuilding `./nixls` (or changing `nixls.serverPath`), run **nixls: Restart
Server** from the Command Palette to pick up the change without reloading the
window. For a guided tour of the features, open
[examples/demo](examples/demo) as its own workspace folder — its README walks
through each diagnostic, hover, completion, and quick fix.

**b. Any generic LSP client (optional)**

Any editor/extension that can launch an arbitrary language server works too:
point it at the `nixls` binary, use **stdio** transport, and associate it with
`.nix` files. No command-line arguments are needed to start the server.

### 4. Smoke test

1. Open a folder in the Extension Development Host.
2. Create `default.nix` containing:

   ```nix
   import ./does-not-exist.nix
   ```

3. You should see a diagnostic (red squiggle) on the path with the message
   `missing import target ./does-not-exist.nix`.
4. Create the target file `does-not-exist.nix` (any valid Nix, e.g. `{}`), then
   save/edit `default.nix`. The diagnostic clears once the target resolves.
5. To see a syntax diagnostic, put a lone `{` in a file — tree-sitter reports
   the parse error as a diagnostic.
6. Unused, duplicate, and bad-`inherit` bindings surface as diagnostics with
   their own severities (unused bindings are warnings; the rest are errors).
7. Open the Outline view to see the file's attribute/let structure. Put the
   cursor on a `let` binding or attribute name (or a use of it) and try
   **Go to Definition**, **Find All References**, and **Highlight** (highlighting
   shows the definition as a write and every use as a read). These work within a
   single file. Use the editor's folding controls to collapse attribute sets,
   `let` blocks, lists, and functions.
8. Put the cursor on an `import ./foo.nix` path (or a `./x.nix` inside
   `imports = [ ... ]` or after `callPackage`) and **Go to Definition** to jump
   to the top of the target file.
9. **Go to Definition** also follows attribute selection. Put the cursor on the
   attribute part of `lib.foo` (where `let lib = import ./lib.nix`),
   `(import ./lib.nix).foo`, a called import (`import ./x.nix { }`), or
   `inherit (import ./lib.nix) foo` and jump straight to that attribute's
   definition inside the target file; nested paths (`lib.a.b`) land on the right
   binding. Selection into a local attribute set works too
   (`let cfg = { port = 80; }; in cfg.port`). Dynamic (`${...}`) segments and
   names provided by `with` are conservatively not followed.
10. Press `Ctrl+T` (Go to Symbol in Workspace) and type part of a name to search
   let/rec/attribute bindings across every `.nix` file in the workspace.
10a. On the workspace root `flake.nix`, hover over an input name (or its `url`,
    or a `follows` target) to see its declared url and, when a `flake.lock` is
    present, its locked source, revision, and last-modified date.
    **Go to Definition** on a `follows` target or an `outputs` formal jumps to
    that input's declaration.
11. External changes refresh automatically: switch git branches, `git add` an
    import target, or edit a `.nix` file outside the editor, and diagnostics
    update without reopening. This relies on the bundled client's file watcher
    (`**/*.nix`), which forwards changes as `workspace/didChangeWatchedFiles`;
    open editor buffers stay the source of truth for their own documents.
12. On a large workspace, an "Indexing Nix workspace" progress indication shows
    in the status bar during startup while the server computes initial
    diagnostics for every file.

The same import checks fire for `imports = [ ./x.nix ]` and
`callPackage ./x.nix` references. In a flake workspace under git, an import
target that exists but is not git-tracked reports
`import target ./x.nix exists but is not git-tracked; Nix flakes only see
git-tracked files, so run git add`. That warning carries a quick fix: put the
cursor on the import path and open the lightbulb (`Ctrl+.`) to run
**Run git add &lt;target&gt;**. Accepting it stages the file with `git add` and
the diagnostics refresh on their own once the file is tracked — no manual
reload. The quick fix only appears where the warning does; a missing (not just
untracked) import target has no fix, since `git add` cannot conjure the file.

### Current limitations

`nixls` is early. Today it provides:

- Syntax diagnostics (tree-sitter `ERROR`/`MISSING` nodes).
- Import diagnostics (missing / untracked `import`, `imports = [ ... ]`, and
  `callPackage` targets), with a **Run git add** quick fix for the untracked case.
- Binding diagnostics: unused bindings (warning), plus duplicate and
  bad-`inherit` bindings (error).
- Flake diagnostics on the workspace root `flake.nix`: a `follows` target that
  names no declared input (`dangling-follows`, error), a declared input missing
  from `flake.lock` (`input-not-locked`, warning), a `flake.lock` entry with no
  matching input (`stale-lock-entry`, warning), and an input never consumed by
  `outputs` (`unused-input`, warning). These are deliberately conservative:
  only the root `flake.nix` is analyzed, only static string URLs/targets are
  read (interpolated or dynamic values are ignored), the lock-dependent checks
  run only when a parseable `flake.lock` is present, and `unused-input` fires
  only when `outputs` uses a strict destructured signature with no `...` and no
  `@`-pattern (so `self` and follows-referenced inputs are never flagged).
- Edit-based quick fixes on the root `flake.nix`: an `unused-input` warning
  offers **Remove input '&lt;name&gt;'** (deletes every binding that declares it)
  and **Add '&lt;name&gt;' to outputs** (inserts it into the `outputs` formals),
  and a `dangling-follows` error offers **Change follows target to '&lt;name&gt;'**
  did-you-mean fixes for each declared input within edit distance 2 of the
  misspelled target (preserving any nested path after the first `/`). Each fix
  appears only where its own diagnostic does.
- Document symbols (outline), go-to-definition, find-all-references, folding
  ranges, and document highlights.
- Hover on the root `flake.nix` inputs (declared url plus locked source, rev,
  and last-modified date from `flake.lock`), and go-to-definition on a `follows`
  target or an `outputs` formal jumps to the input's declaration.
- Completion inside the root `flake.nix` for `follows` targets and `outputs`
  formals (declared input names; `self` in formals) — nothing else completes yet.
- Workspace symbol search (`Ctrl+T`) over let/rec/attribute bindings in every
  `.nix` file (case-insensitive substring match, results capped at 128).
- Automatic diagnostics refresh on external file changes and branch switches,
  driven by the bundled client's `**/*.nix` file watcher.

References and highlights are **within-file only**, and a bare identifier
reference that resolves to a binding in another file is not followed yet.
Go-to-definition does cross files in two cases: import paths (on an `import`,
`imports`, or `callPackage` path it jumps to the top of the target file), and
attribute selection through an import — `lib.foo`, `(import ./lib.nix).foo`,
called imports (`import ./x.nix { }`), and `inherit (import ./x.nix) name` land
on the attribute's definition in the target file (local attribute sets too).
This selection support is deliberately conservative: dynamic (`${...}`) keys,
names provided by `with`, and a base whose value has zero or multiple import
edges are not followed, so it never guesses a wrong jump.
Hover is currently limited to the root `flake.nix` inputs, and completion is
limited to `follows` targets and `outputs` formals in the root `flake.nix`;
`nixls` does **not** yet provide general expression completion or hover, and it
uses **full-document** text sync (the whole document is resent on each change).
