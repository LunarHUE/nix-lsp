package server

import (
	"context"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
)

// capabilityMethods maps each provider capability the server can advertise in its
// initialize ServerCapabilities to the dispatch method(s) that capability
// promises the client can call. `advertised` reads the flag straight off the
// real capabilities the server returns, so the table cannot drift into claiming
// something is advertised when it is not.
//
// It is the single source of truth both exhaustive tests below consume: (a)
// asserts every advertised capability has a wired dispatch case; (b) asserts
// every wired dispatch case is either mapped here or deliberately unadvertised.
type capabilityMapping struct {
	capability string
	advertised bool
	methods    []string
}

func capabilityMappings(caps lsp.ServerCapabilities) []capabilityMapping {
	return []capabilityMapping{
		{"textDocumentSync", caps.TextDocumentSync != 0, []string{
			lsp.MethodTextDocumentDidOpen,
			lsp.MethodTextDocumentDidChange,
			lsp.MethodTextDocumentDidClose,
		}},
		{"documentSymbolProvider", caps.DocumentSymbolProvider, []string{lsp.MethodTextDocumentDocumentSymbol}},
		{"definitionProvider", caps.DefinitionProvider, []string{lsp.MethodTextDocumentDefinition}},
		{"hoverProvider", caps.HoverProvider, []string{lsp.MethodTextDocumentHover}},
		{"documentHighlightProvider", caps.DocumentHighlightProvider, []string{lsp.MethodTextDocumentDocumentHighlight}},
		{"referencesProvider", caps.ReferencesProvider, []string{lsp.MethodTextDocumentReferences}},
		{"foldingRangeProvider", caps.FoldingRangeProvider, []string{lsp.MethodTextDocumentFoldingRange}},
		{"workspaceSymbolProvider", caps.WorkspaceSymbolProvider, []string{lsp.MethodWorkspaceSymbol}},
		{"codeActionProvider", caps.CodeActionProvider, []string{lsp.MethodTextDocumentCodeAction}},
		{"executeCommandProvider", caps.ExecuteCommandProvider != nil, []string{lsp.MethodWorkspaceExecuteCommand}},
		{"completionProvider", caps.CompletionProvider != nil, []string{
			lsp.MethodTextDocumentCompletion,
			lsp.MethodCompletionItemResolve,
		}},
	}
}

// intentionallyUnadvertised lists dispatch methods that are handled without a
// corresponding provider capability: lifecycle (initialize) and client-sent
// notifications the server tolerates or reacts to but does not advertise a
// static ServerCapabilities flag for. A dispatch case for one of these will not
// appear in capabilityMappings, so it must be listed here for test (b) to pass.
var intentionallyUnadvertised = []string{
	lsp.MethodInitialize,                      // lifecycle handshake, not a feature provider
	lsp.MethodTextDocumentDidSave,             // tolerated no-op; textDocumentSync=Full advertises no save
	lsp.MethodWorkspaceDidChangeConfiguration, // tolerated no-op notification
	lsp.MethodWorkspaceDidChangeWatchedFiles,  // client notification; no static capability field
}

// initializeCapabilities drives the real initialize path the way the handler
// tests do and returns the ServerCapabilities the server actually advertises.
func initializeCapabilities(t *testing.T) lsp.ServerCapabilities {
	t.Helper()
	handler := NewHandler()
	defer handler.Close()

	result, err := handler.Handle(context.Background(), lsp.MethodInitialize, nil)
	if err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	init, ok := result.(lsp.InitializeResult)
	if !ok {
		t.Fatalf("initialize result type = %T, want lsp.InitializeResult", result)
	}
	return init.Capabilities
}

// TestEveryAdvertisedCapabilityHasDispatchCase asserts (a): every capability the
// server advertises in its initialize response is backed by a wired dispatch
// case, so the server never promises a feature the client's request would then
// bounce off the method-not-found default.
func TestEveryAdvertisedCapabilityHasDispatchCase(t *testing.T) {
	caps := initializeCapabilities(t)
	handled := make(map[string]bool, len(handledMethods))
	for _, method := range handledMethods {
		handled[method] = true
	}

	for _, mapping := range capabilityMappings(caps) {
		if !mapping.advertised {
			continue
		}
		for _, method := range mapping.methods {
			if !handled[method] {
				t.Errorf("capability %q is advertised but method %q has no dispatch case: "+
					"add the case to Handle and to handledMethods in handler.go, "+
					"or stop advertising the capability in initialize",
					mapping.capability, method)
			}
		}
	}
}

// TestEveryDispatchCaseIsAdvertisedOrAllowlisted asserts (b): every method the
// dispatch switch handles is either mapped to an advertised capability or listed
// on the intentionallyUnadvertised allowlist. This is the reverse guard: it
// fails when a dispatch case is added without wiring it to a capability (or
// recording that it is deliberately unadvertised).
func TestEveryDispatchCaseIsAdvertisedOrAllowlisted(t *testing.T) {
	caps := initializeCapabilities(t)

	advertised := make(map[string]bool)
	for _, mapping := range capabilityMappings(caps) {
		if !mapping.advertised {
			continue
		}
		for _, method := range mapping.methods {
			advertised[method] = true
		}
	}
	allowlisted := make(map[string]bool, len(intentionallyUnadvertised))
	for _, method := range intentionallyUnadvertised {
		allowlisted[method] = true
	}

	for _, method := range handledMethods {
		if advertised[method] || allowlisted[method] {
			continue
		}
		t.Errorf("dispatch method %q is neither mapped to an advertised capability "+
			"nor allowlisted: if it backs a capability, add it to capabilityMappings "+
			"in methods_test.go; if it is a lifecycle/notification method, add it to "+
			"intentionallyUnadvertised", method)
	}
}
