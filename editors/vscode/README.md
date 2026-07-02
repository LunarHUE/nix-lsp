# nixls dev client

A minimal VS Code extension used to exercise the `nixls` Nix language server
during development. It launches the server over stdio and wires up diagnostics
for `.nix` files. It intentionally does nothing else.

## Build

```sh
cd editors/vscode
npm install
npm run compile
```

`npm run compile` runs `tsc` and emits JavaScript into `out/`.

## Run (Extension Development Host)

1. Build the server from the repo root: `go build -o nixls ./cmd/nixls`.
2. Open the `editors/vscode/` folder in VS Code.
3. Press `F5` (Run and Debug -> "Run Extension") to launch an Extension
   Development Host window.
4. In that new window, set `nixls.serverPath` (Settings -> search "nixls") to
   the absolute path of your built `nixls` binary, e.g. `/workspace/nixls`.
   Leave it as `nixls` if the binary is already on your `PATH`.
5. Open a folder containing `.nix` files. The server publishes diagnostics on
   `initialize` for git-tracked `.nix` files, and on open/change for the file
   you are editing.

## Configuration

| Setting             | Default | Description                                              |
| ------------------- | ------- | -------------------------------------------------------- |
| `nixls.serverPath`  | `nixls` | Path to the `nixls` binary (absolute path recommended).  |

## Package (optional)

```sh
npm install -g @vscode/vsce
vsce package
```

This produces a `.vsix` you can install via
`code --install-extension nixls-dev-client-0.0.1.vsix`.
