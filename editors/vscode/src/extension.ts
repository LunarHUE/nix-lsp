import * as vscode from "vscode";
import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
  TransportKind,
} from "vscode-languageclient/node";

let client: LanguageClient | undefined;

export function activate(context: vscode.ExtensionContext): void {
  const configured = vscode.workspace
    .getConfiguration("nixls")
    .get<string>("serverPath");
  const command =
    configured && configured.trim().length > 0 ? configured : "nixls";

  // The server speaks LSP/JSON-RPC over stdio; no arguments are required to
  // start it. Point nixls.serverPath at your built ./nixls binary.
  const serverOptions: ServerOptions = {
    run: { command, transport: TransportKind.stdio },
    debug: { command, transport: TransportKind.stdio },
  };

  const clientOptions: LanguageClientOptions = {
    documentSelector: [
      { scheme: "file", language: "nix" },
      { scheme: "untitled", language: "nix" },
    ],
  };

  client = new LanguageClient(
    "nixls",
    "nixls",
    serverOptions,
    clientOptions
  );

  // start() returns a promise; failures (e.g. binary not found) surface in the
  // "nixls" output channel.
  void client.start();
  context.subscriptions.push({
    dispose: () => {
      void client?.stop();
    },
  });
}

export function deactivate(): Thenable<void> | undefined {
  if (!client) {
    return undefined;
  }
  return client.stop();
}
