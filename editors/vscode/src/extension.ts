import * as fs from "fs";
import * as vscode from "vscode";
import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
} from "vscode-languageclient/node";

let client: LanguageClient | undefined;
// restarting guards against overlapping restarts: a second invocation while one
// is already in flight just returns.
let restarting = false;

// createClient builds a fresh LanguageClient, reading nixls.serverPath anew on
// every call so a rebuilt ./nixls or a changed serverPath is picked up on
// restart without a window reload. watchers are created once in activate and
// passed in so they survive client.stop(): the client hooks fresh change
// listeners onto them on each start and disposes only those listeners on stop,
// never the watchers themselves.
// bundledServerPath returns the platform binary shipped inside the extension
// (bin/nixls[.exe] in platform-specific VSIX packages), or undefined in a dev
// checkout where no binary is bundled.
function bundledServerPath(context: vscode.ExtensionContext): string | undefined {
  const exe = process.platform === "win32" ? "nixls.exe" : "nixls";
  const p = vscode.Uri.joinPath(context.extensionUri, "bin", exe).fsPath;
  return fs.existsSync(p) ? p : undefined;
}

function createClient(
  context: vscode.ExtensionContext,
  watchers: vscode.FileSystemWatcher[]
): LanguageClient {
  const nixlsConfig = vscode.workspace.getConfiguration("nixls");
  const configured = nixlsConfig.get<string>("serverPath");
  // Resolution order: an explicit nixls.serverPath (anything but the default
  // "nixls") wins, then the binary bundled in the VSIX, then PATH lookup.
  let command = "nixls";
  if (configured && configured.trim().length > 0 && configured !== "nixls") {
    command = configured;
  } else {
    command = bundledServerPath(context) ?? "nixls";
  }

  // nixls.optionsPath / nixls.packagesPath forward to the server as
  // initializationOptions: a local dataset file for NixOS option / package
  // hover, "off" to disable, or empty to auto-download and cache for the
  // locked nixpkgs channel.
  const optionsPath = nixlsConfig.get<string>("optionsPath") ?? "";
  const packagesPath = nixlsConfig.get<string>("packagesPath") ?? "";

  // The server speaks LSP/JSON-RPC over stdio; no arguments are required to
  // start it. Point nixls.serverPath at your built ./nixls binary.
  // Omit `transport`: stdio is the default for executables, and naming it
  // explicitly makes the client append a --stdio argument to the command.
  const serverOptions: ServerOptions = {
    run: { command },
    debug: { command },
  };

  const clientOptions: LanguageClientOptions = {
    documentSelector: [
      { scheme: "file", language: "nix" },
      { scheme: "untitled", language: "nix" },
    ],
    synchronize: {
      fileEvents: watchers,
    },
    initializationOptions: { optionsPath, packagesPath },
  };

  return new LanguageClient("nixls", "nixls", serverOptions, clientOptions);
}

export function activate(context: vscode.ExtensionContext): void {
  // Watch every .nix file plus flake.lock so external changes (branch switches,
  // git operations, out-of-editor edits, `nix flake lock`) reach the server as
  // workspace/didChangeWatchedFiles and refresh diagnostics. Created once and
  // reused across restarts; the client never disposes caller-provided watchers.
  const watchers = [
    vscode.workspace.createFileSystemWatcher("**/*.nix"),
    vscode.workspace.createFileSystemWatcher("**/flake.lock"),
  ];
  context.subscriptions.push(...watchers);

  // start() returns a promise; failures (e.g. binary not found) surface in the
  // "nixls" output channel.
  client = createClient(context, watchers);
  void client.start();
  context.subscriptions.push({
    dispose: () => {
      void client?.stop();
    },
  });

  // nixls.restart re-reads configuration and swaps in a fresh client, so a
  // rebuilt binary or a changed nixls.serverPath takes effect without reloading
  // the window.
  context.subscriptions.push(
    vscode.commands.registerCommand("nixls.restart", async () => {
      if (restarting) {
        return;
      }
      restarting = true;
      try {
        if (client) {
          // A dead server must not block the restart, so swallow stop errors.
          try {
            await client.stop();
          } catch {
            // ignore
          }
          client = undefined;
        }
        client = createClient(context, watchers);
        try {
          await client.start();
          vscode.window.setStatusBarMessage("nixls: server restarted", 3000);
        } catch (err) {
          vscode.window.showErrorMessage(
            `nixls: failed to restart server: ${err}`
          );
        }
      } finally {
        restarting = false;
      }
    })
  );
}

export function deactivate(): Thenable<void> | undefined {
  if (!client) {
    return undefined;
  }
  return client.stop();
}
