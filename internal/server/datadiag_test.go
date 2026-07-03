package server

import (
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/datadiag"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// waitForDiagnosticCode polls until a diagnostic with the given code is present
// for uri, returning it, or fails after a deadline.
func waitForDiagnosticCode(t *testing.T, handler *Handler, uri, code string) syntax.Diagnostic {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, d := range handler.Diagnostics(uri) {
			if d.Code == code {
				return d
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for diagnostic code %q on %s; have %+v", code, uri, handler.Diagnostics(uri))
	return syntax.Diagnostic{}
}

// diagnosticWithCode returns the first diagnostic with code, or ok=false.
func diagnosticWithCode(diags []syntax.Diagnostic, code string) (syntax.Diagnostic, bool) {
	for _, d := range diags {
		if d.Code == code {
			return d, true
		}
	}
	return syntax.Diagnostic{}, false
}

func TestDatasetNoOptionDiagnosticsWithoutIndex(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// No optionsPath: the dataset never loads, so a module with a typo gets no
	// dataset diagnostics.
	initWorkspace(t, handler, t.TempDir())
	uri := mustURI(t, "/tmp/mod.nix")
	src := `{ config, ... }:
{
  networking.firewal.enable = true;
}
`
	openDocument(t, handler, uri, src)
	// Let any background diagnostics settle, then assert none carry the dataset code.
	time.Sleep(100 * time.Millisecond)
	if _, ok := diagnosticWithCode(handler.Diagnostics(uri), datadiag.CodeUnknownOption); ok {
		t.Fatalf("unexpected unknown-option diagnostic without a loaded index: %+v", handler.Diagnostics(uri))
	}
}

func TestDatasetOptionDiagnosticAndCodeAction(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), "")
	uri := mustURI(t, "/tmp/mod.nix")
	src := `{ config, ... }:
{
  networking.firewal.enable = true;
}
`
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownOption)
	if want := "unknown option: networking.firewal (did you mean firewall?)"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}

	// A code action request on the flagged range offers the did-you-mean fix.
	actions := requestCodeActions(t, handler, uri,
		d.Range.Start.Line, d.Range.Start.Character, d.Range.End.Line, d.Range.End.Character, nil)
	action := actionByTitle(t, actions, "Change to 'firewall'")
	if action.Edit == nil {
		t.Fatal("action has no edit")
	}
	edits := action.Edit.Changes[uri]
	if len(edits) != 1 {
		t.Fatalf("got %d edits, want 1", len(edits))
	}
	got := applyEdits(t, src, edits)
	want := `{ config, ... }:
{
  networking.firewall.enable = true;
}
`
	if got != want {
		t.Errorf("applied edit =\n%q\nwant\n%q", got, want)
	}
}

func TestDatasetPackageDiagnosticAndCodeAction(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), "", packagesFixturePath(t))
	uri := mustURI(t, "/tmp/pkg.nix")
	src := `{ x = pkgs.htoop; }`
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownPackage)
	if want := "unknown package: pkgs.htoop (did you mean htop?)"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}

	actions := requestCodeActions(t, handler, uri,
		d.Range.Start.Line, d.Range.Start.Character, d.Range.End.Line, d.Range.End.Character, nil)
	action := actionByTitle(t, actions, "Change to 'htop'")
	got := applyEdits(t, src, action.Edit.Changes[uri])
	if want := `{ x = pkgs.htop; }`; got != want {
		t.Errorf("applied edit = %q, want %q", got, want)
	}
}

func TestDatasetBothDiagnosticsInOneFile(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), packagesFixturePath(t))
	uri := mustURI(t, "/tmp/both.nix")
	src := `{ config, pkgs, ... }:
{
  networking.firewal.enable = true;
  environment.systemPackages = [ pkgs.htoop ];
}
`
	openDocument(t, handler, uri, src)

	waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownOption)
	waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownPackage)
}

func TestDatasetOptionTypeMismatchPublished(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), "")
	uri := mustURI(t, "/tmp/mod.nix")
	src := `{ config, ... }:
{
  networking.firewall.enable = "yes";
}
`
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, datadiag.CodeOptionTypeMismatch)
	if want := "type mismatch: networking.firewall.enable expects boolean, got string"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
	// The flagged range must cover exactly the "yes" value expression.
	got := applyEdits(t, src, []TextEdit{{Range: toProtocolRange(d.Range), NewText: "REPL"}})
	want := `{ config, ... }:
{
  networking.firewall.enable = REPL;
}
`
	if got != want {
		t.Errorf("range did not cover the value; replaced source =\n%q", got)
	}
}

func TestDatasetRefreshOnLoadRepublishes(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// Open the module BEFORE any index exists: no dataset diagnostics yet.
	initWorkspace(t, handler, t.TempDir())
	uri := mustURI(t, "/tmp/mod.nix")
	src := `{ config, ... }:
{
  networking.firewal.enable = true;
}
`
	openDocument(t, handler, uri, src)
	time.Sleep(100 * time.Millisecond)
	if _, ok := diagnosticWithCode(handler.Diagnostics(uri), datadiag.CodeUnknownOption); ok {
		t.Fatalf("unexpected unknown-option before the index loaded")
	}

	// Inject the index through the same seam the async loader uses; its re-publish
	// hook must recompute diagnostics for the open document with no further edit.
	handler.loadOptionsFromFile(optionsFixturePath(t))
	waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownOption)
}
