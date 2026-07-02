package packages

import (
	"fmt"
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

func attrsOf(docs []*Doc) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Attr
	}
	return out
}

func TestCompletePrefix(t *testing.T) {
	ix := loadFixture(t)
	got := attrsOf(ix.Complete("cl", 10))
	if len(got) != 1 || got[0] != "claude-code" {
		t.Fatalf("Complete(cl) = %v, want [claude-code]", got)
	}
}

func TestCompleteSortedCap(t *testing.T) {
	ix := loadFixture(t)
	// Fixture attrs sorted: claude-code, git, go, hello, htop, python312Packages.requests.
	got := attrsOf(ix.Complete("", 3))
	want := []string{"claude-code", "git", "go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Complete(\"\",3) = %v, want %v (first 3 sorted)", got, want)
	}
}

func TestCompleteMiss(t *testing.T) {
	ix := loadFixture(t)
	if got := ix.Complete("zzz", 10); got != nil {
		t.Errorf("Complete(zzz) = %v, want nil", got)
	}
}

func TestCompleteLimitZero(t *testing.T) {
	ix := loadFixture(t)
	if got := ix.Complete("c", 0); got != nil {
		t.Errorf("Complete(c,0) = %v, want nil", got)
	}
	if got := ix.Complete("c", -1); got != nil {
		t.Errorf("Complete(c,-1) = %v, want nil", got)
	}
}

func TestCompleteFullAttrPrefix(t *testing.T) {
	ix := loadFixture(t)
	// A prefix equal to a whole attribute still matches that attribute.
	got := attrsOf(ix.Complete("claude-code", 10))
	if len(got) != 1 || got[0] != "claude-code" {
		t.Fatalf("Complete(claude-code) = %v, want [claude-code]", got)
	}
}

func TestCompleteDottedPrefix(t *testing.T) {
	ix := loadFixture(t)
	// A dotted prefix descends into a nested attribute namespace.
	got := attrsOf(ix.Complete("python312Packages.", 10))
	if len(got) != 1 || got[0] != "python312Packages.requests" {
		t.Fatalf("Complete(python312Packages.) = %v, want [python312Packages.requests]", got)
	}
}

func TestCompleteDeterministic(t *testing.T) {
	ix := loadFixture(t)
	a := attrsOf(ix.Complete("", 10))
	b := attrsOf(ix.Complete("", 10))
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("Complete not deterministic:\n first: %v\nsecond: %v", a, b)
	}
}

func TestCompleteNilReceiver(t *testing.T) {
	var ix *Index
	if got := ix.Complete("p", 10); got != nil {
		t.Errorf("nil.Complete = %v, want nil", got)
	}
}

func TestCompleteLargeIndex(t *testing.T) {
	docs := make(map[string]*Doc, 10000)
	for i := 0; i < 10000; i++ {
		attr := fmt.Sprintf("pkg%05d", i)
		docs[attr] = &Doc{Attr: attr}
	}
	ix := &Index{docs: docs}

	got := attrsOf(ix.Complete("p", 50))
	if len(got) != 50 {
		t.Fatalf("Complete(p,50) returned %d, want 50", len(got))
	}
	// Deterministic ascending order: pkg00000..pkg00049.
	for i, attr := range got {
		want := fmt.Sprintf("pkg%05d", i)
		if attr != want {
			t.Fatalf("Complete(p,50)[%d] = %q, want %q", i, attr, want)
		}
	}
	// A second call yields the identical slice contents.
	again := attrsOf(ix.Complete("p", 50))
	if strings.Join(got, ",") != strings.Join(again, ",") {
		t.Fatal("Complete(p,50) not deterministic across calls")
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
		"*Homepage:* [https://github.com/anthropics/claude-code](https://github.com/anthropics/claude-code)"
	if got := doc.Markdown(); got != want {
		t.Errorf("Markdown mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}

	git, _ := ix.Lookup("git")
	wantGit := "**git** `2.49.0`\n\nDistributed version control system"
	if got := git.Markdown(); got != wantGit {
		t.Errorf("git Markdown mismatch:\n got:\n%s\nwant:\n%s", got, wantGit)
	}
}

// TestMarkdownHomepageNonURL proves a homepage that does not parse as an http(s)
// URL stays plain text rather than becoming a (broken) markdown link.
func TestMarkdownHomepageNonURL(t *testing.T) {
	d := &Doc{Pname: "odd", Version: "1.0", Homepage: "see readme"}
	want := "**odd** `1.0`\n\n*Homepage:* see readme"
	if got := d.Markdown(); got != want {
		t.Errorf("Markdown = %q, want %q", got, want)
	}
}

func TestMarkdownFallsBackToAttr(t *testing.T) {
	d := &Doc{Attr: "some.attr", Version: "1.0"}
	want := "**some.attr** `1.0`"
	if got := d.Markdown(); got != want {
		t.Errorf("Markdown = %q, want %q", got, want)
	}
}
