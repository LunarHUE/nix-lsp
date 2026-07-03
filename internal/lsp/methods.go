package lsp

// LSP method names the server touches, as typed constants. Bare string literals
// for these methods used to be duplicated across the dispatch switch, the
// notification-swallow list, and tests, with nothing enforcing that an
// advertised capability had a wired dispatch case (or vice versa). Routing every
// reference through these constants — and the exhaustive capability<->handler
// tests that consume them — turns that class of drift into a compile/test error.
const (
	// Lifecycle.
	MethodInitialize    = "initialize"
	MethodInitialized   = "initialized"
	MethodShutdown      = "shutdown"
	MethodExit          = "exit"
	MethodCancelRequest = "$/cancelRequest"
	MethodProgress      = "$/progress"

	// Document sync (client -> server notifications) and the diagnostics
	// notification the server pushes back (server -> client).
	MethodTextDocumentDidOpen            = "textDocument/didOpen"
	MethodTextDocumentDidChange          = "textDocument/didChange"
	MethodTextDocumentDidClose           = "textDocument/didClose"
	MethodTextDocumentDidSave            = "textDocument/didSave"
	MethodTextDocumentPublishDiagnostics = "textDocument/publishDiagnostics"

	// Language features (client -> server requests).
	MethodTextDocumentDocumentSymbol    = "textDocument/documentSymbol"
	MethodTextDocumentDefinition        = "textDocument/definition"
	MethodTextDocumentHover             = "textDocument/hover"
	MethodTextDocumentCompletion        = "textDocument/completion"
	MethodCompletionItemResolve         = "completionItem/resolve"
	MethodTextDocumentDocumentHighlight = "textDocument/documentHighlight"
	MethodTextDocumentReferences        = "textDocument/references"
	MethodTextDocumentFoldingRange      = "textDocument/foldingRange"
	MethodTextDocumentCodeAction        = "textDocument/codeAction"

	// Workspace.
	MethodWorkspaceSymbol                 = "workspace/symbol"
	MethodWorkspaceExecuteCommand         = "workspace/executeCommand"
	MethodWorkspaceDidChangeWatchedFiles  = "workspace/didChangeWatchedFiles"
	MethodWorkspaceDidChangeConfiguration = "workspace/didChangeConfiguration"

	// Window (server -> client request).
	MethodWindowWorkDoneProgressCreate = "window/workDoneProgress/create"
)
