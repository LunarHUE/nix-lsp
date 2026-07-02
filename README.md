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

The server speaks LSP/JSON-RPC over stdio and currently publishes diagnostics
only. You can try it end-to-end in VS Code with the bundled development client
under [editors/vscode](editors/vscode).

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

The same import checks fire for `imports = [ ./x.nix ]` and
`callPackage ./x.nix` references. In a flake workspace under git, an import
target that exists but is not git-tracked reports
`import target ./x.nix exists but is not git-tracked; Nix flakes only see
git-tracked files, so run git add`.

### Current limitations

`nixls` is early. Today it provides:

- Syntax diagnostics (tree-sitter `ERROR`/`MISSING` nodes).
- Import diagnostics (missing / untracked `import`, `imports = [ ... ]`, and
  `callPackage` targets).

It does **not** yet provide completion, hover, or go-to-definition, and it uses
**full-document** text sync (the whole document is resent on each change).
