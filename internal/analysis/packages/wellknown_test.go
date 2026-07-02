package packages

import "testing"

func TestWellknownHit(t *testing.T) {
	doc, ok := Wellknown("runtimeShell")
	if !ok {
		t.Fatal("Wellknown(runtimeShell) ok = false, want hit")
	}
	if doc.Attr != "runtimeShell" {
		t.Errorf("Attr = %q, want runtimeShell", doc.Attr)
	}
	if doc.Pname != "" || doc.Version != "" || doc.Homepage != "" {
		t.Errorf("well-known doc must carry only Attr and Description, got %+v", doc)
	}
	if doc.Description == "" {
		t.Error("Description is empty")
	}

	// One member of each shared-description family resolves too.
	for _, attr := range []string{"lib", "callPackage", "mkShell", "fetchFromGitHub", "writeShellScriptBin", "symlinkJoin", "buildEnv"} {
		if _, ok := Wellknown(attr); !ok {
			t.Errorf("Wellknown(%q) ok = false, want hit", attr)
		}
	}
}

func TestWellknownMiss(t *testing.T) {
	for _, attr := range []string{"claude-code", "hello", "stdenv", ""} {
		if doc, ok := Wellknown(attr); ok {
			t.Errorf("Wellknown(%q) = %+v, want miss", attr, doc)
		}
	}
}

// TestWellknownMarkdownGolden pins the rendered form: the attr as the bold
// header with no version backticks, then the description.
func TestWellknownMarkdownGolden(t *testing.T) {
	doc, ok := Wellknown("runtimeShell")
	if !ok {
		t.Fatal("runtimeShell not found")
	}
	want := "**runtimeShell**\n\n" +
		"Path to the default build-time shell (non-interactive bash): " +
		"the string \"${bash}/bin/bash\", not a derivation."
	if got := doc.Markdown(); got != want {
		t.Errorf("Markdown mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}
