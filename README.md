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
within-file document symbols (outline), go-to-definition (which also jumps
through import paths to other files), find-all-references, folding ranges,
document highlights, and workspace-wide symbol search. You can try it end-to-end
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
9. Press `Ctrl+T` (Go to Symbol in Workspace) and type part of a name to search
   let/rec/attribute bindings across every `.nix` file in the workspace.
10. External changes refresh automatically: switch git branches, `git add` an
    import target, or edit a `.nix` file outside the editor, and diagnostics
    update without reopening. This relies on the bundled client's file watcher
    (`**/*.nix`), which forwards changes as `workspace/didChangeWatchedFiles`;
    open editor buffers stay the source of truth for their own documents.
11. On a large workspace, an "Indexing Nix workspace" progress indication shows
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
- Document symbols (outline), go-to-definition, find-all-references, folding
  ranges, and document highlights.
- Workspace symbol search (`Ctrl+T`) over let/rec/attribute bindings in every
  `.nix` file (case-insensitive substring match, results capped at 128).
- Automatic diagnostics refresh on external file changes and branch switches,
  driven by the bundled client's `**/*.nix` file watcher.

References, highlights, and identifier go-to-definition are **within-file only**:
a reference that resolves to a binding in another file is not followed yet. The
one exception is import paths — go-to-definition on an `import`, `imports`, or
`callPackage` path does cross files, jumping to the top of the target file.
`nixls` also does **not** yet provide completion or hover, and it uses
**full-document** text sync (the whole document is resent on each change).
