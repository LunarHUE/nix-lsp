package packages

import (
	"os"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) *Index {
	t.Helper()
	f, err := os.Open("testdata/packages.fixture.json")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	ix, err := ParseStream(f)
	if err != nil {
		t.Fatalf("ParseStream fixture: %v", err)
	}
	return ix
}

func TestParseStreamCount(t *testing.T) {
	ix := loadFixture(t)
	// Six well-formed entries; the malformed "broken" entry is skipped.
	if got := ix.Len(); got != 6 {
		t.Fatalf("Len = %d, want 6", got)
	}
}

func TestParseStreamFields(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup("claude-code")
	if !ok {
		t.Fatal("claude-code not found")
	}
	if doc.Pname != "claude-code" {
		t.Errorf("Pname = %q, want claude-code", doc.Pname)
	}
	if doc.Version != "2.1.193" {
		t.Errorf("Version = %q, want 2.1.193", doc.Version)
	}
	if doc.Description != "Agentic coding tool that lives in your terminal" {
		t.Errorf("Description = %q", doc.Description)
	}
	if doc.Homepage != "https://github.com/anthropics/claude-code" {
		t.Errorf("Homepage = %q", doc.Homepage)
	}

	htop, ok := ix.Lookup("htop")
	if !ok || htop.Version != "3.5.1" || htop.Description != "Interactive process viewer" {
		t.Errorf("htop = %+v, want 3.5.1 / Interactive process viewer", htop)
	}
}

func TestParseStreamDottedKey(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup("python312Packages.requests")
	if !ok {
		t.Fatal("dotted key python312Packages.requests not found")
	}
	if doc.Pname != "python3.12-requests" || doc.Version != "2.32.3" {
		t.Errorf("dotted doc = %+v", doc)
	}
}

func TestParseStreamListHomepage(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup("hello")
	if !ok {
		t.Fatal("hello not found")
	}
	if doc.Homepage != "https://www.gnu.org/software/hello/" {
		t.Errorf("Homepage = %q, want first list element", doc.Homepage)
	}
}

func TestParseStreamSkipsMalformed(t *testing.T) {
	ix := loadFixture(t)
	if _, ok := ix.Lookup("broken"); ok {
		t.Error("malformed entry \"broken\" should be skipped")
	}
	// git has no meta.homepage; it still parses with an empty homepage.
	git, ok := ix.Lookup("git")
	if !ok || git.Homepage != "" {
		t.Errorf("git = %+v, want empty homepage", git)
	}
}

func TestParseStreamTopLevelArrayErrors(t *testing.T) {
	if _, err := ParseStream(strings.NewReader(`[1,2,3]`)); err == nil {
		t.Fatal("ParseStream(array) = nil error, want rejection")
	}
}

func TestTrimmedRoundTrip(t *testing.T) {
	ix := loadFixture(t)
	data, err := ix.MarshalTrimmed()
	if err != nil {
		t.Fatalf("MarshalTrimmed: %v", err)
	}
	back, err := ParseTrimmed(data)
	if err != nil {
		t.Fatalf("ParseTrimmed: %v", err)
	}
	if back.Len() != ix.Len() {
		t.Fatalf("round-trip Len = %d, want %d", back.Len(), ix.Len())
	}
	for _, attr := range []string{"claude-code", "python312Packages.requests", "hello"} {
		a, _ := ix.Lookup(attr)
		b, ok := back.Lookup(attr)
		if !ok {
			t.Fatalf("round-trip lost %q", attr)
		}
		if *a != *b {
			t.Errorf("round-trip %q = %+v, want %+v", attr, *b, *a)
		}
	}
}

func TestMarkdownGolden(t *testing.T) {
	ix := loadFixture(t)

	doc, ok := ix.Lookup("claude-code")
	if !ok {
		t.Fatal("claude-code not found")
	}
	want := "**claude-code** `2.1.193`\n\n" +
		"Agentic coding tool that lives in your terminal\n\n" +
		"*Homepage:* https://github.com/anthropics/claude-code"
	if got := doc.Markdown(); got != want {
		t.Errorf("Markdown mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}

	git, _ := ix.Lookup("git")
	wantGit := "**git** `2.49.0`\n\nDistributed version control system"
	if got := git.Markdown(); got != wantGit {
		t.Errorf("git Markdown mismatch:\n got:\n%s\nwant:\n%s", got, wantGit)
	}
}

func TestMarkdownFallsBackToAttr(t *testing.T) {
	d := &Doc{Attr: "some.attr", Version: "1.0"}
	want := "**some.attr** `1.0`"
	if got := d.Markdown(); got != want {
		t.Errorf("Markdown = %q, want %q", got, want)
	}
}
