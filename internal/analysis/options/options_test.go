package options

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) *Index {
	t.Helper()
	data, err := os.ReadFile("testdata/options.fixture.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	ix, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse fixture: %v", err)
	}
	return ix
}

func TestParseCount(t *testing.T) {
	ix := loadFixture(t)
	if got := ix.Len(); got != 11 {
		t.Fatalf("Len = %d, want 11", got)
	}
}

func TestParseFieldsRoundTrip(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup([]string{"networking", "firewall", "allowedTCPPorts"})
	if !ok {
		t.Fatal("allowedTCPPorts not found")
	}
	if got, want := doc.Type, "list of 16 bit unsigned integer; between 0 and 65535 (both inclusive)"; got != want {
		t.Errorf("Type = %q, want %q", got, want)
	}
	if got := doc.Default; got != "[ ]" {
		t.Errorf("Default = %q, want %q", got, "[ ]")
	}
	if doc.DefaultIsMD {
		t.Error("DefaultIsMD = true, want false")
	}
	if !strings.Contains(doc.Example, "22") {
		t.Errorf("Example = %q, want it to contain %q", doc.Example, "22")
	}
	if len(doc.Declarations) != 1 || doc.Declarations[0] != "nixos/modules/services/networking/firewall.nix" {
		t.Errorf("Declarations = %v, want firewall.nix path", doc.Declarations)
	}
	wantLoc := []string{"networking", "firewall", "allowedTCPPorts"}
	if !slices.Equal(doc.Loc, wantLoc) {
		t.Errorf("Loc = %v, want %v", doc.Loc, wantLoc)
	}
	if doc.ReadOnly {
		t.Error("ReadOnly = true, want false")
	}
}

func TestLookupExact(t *testing.T) {
	ix := loadFixture(t)
	cases := []struct {
		name string
		path []string
		want bool
	}{
		{"leaf option", []string{"networking", "firewall", "enable"}, true},
		{"group not an option", []string{"networking", "firewall"}, false},
		{"unknown segment", []string{"networking", "nonsense"}, false},
		{"empty path", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := ix.Lookup(tc.path)
			if ok != tc.want {
				t.Errorf("Lookup(%v) found = %v, want %v", tc.path, ok, tc.want)
			}
		})
	}
}

func TestLookupWildcard(t *testing.T) {
	ix := loadFixture(t)

	doc, ok := ix.Lookup([]string{"users", "users", "wesley", "home"})
	if !ok {
		t.Fatal("users.users.wesley.home not resolved via <name>")
	}
	if !slices.Equal(doc.Loc, []string{"users", "users", "<name>", "home"}) {
		t.Errorf("Loc = %v, want the <name> doc", doc.Loc)
	}

	if _, ok := ix.Lookup([]string{"systemd", "services", "foo-bar.service", "serviceConfig"}); !ok {
		t.Error("systemd.services.<name>.serviceConfig not resolved")
	}
}

func TestLookupExactOverWildcard(t *testing.T) {
	const src = `{
      "a.<name>.x": {"type":"wild","loc":["a","<name>","x"],"declarations":["wild.nix"]},
      "a.b.x": {"type":"exact","loc":["a","b","x"],"declarations":["exact.nix"]}
    }`
	ix, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	doc, ok := ix.Lookup([]string{"a", "b", "x"})
	if !ok || doc.Type != "exact" {
		t.Errorf("exact path resolved to %+v, want the exact doc", doc)
	}

	doc, ok = ix.Lookup([]string{"a", "c", "x"})
	if !ok || doc.Type != "wild" {
		t.Errorf("wildcard path resolved to %+v, want the wildcard doc", doc)
	}
}

func TestParseTopLevelArrayErrors(t *testing.T) {
	if _, err := Parse([]byte(`[1,2,3]`)); err == nil {
		t.Fatal("Parse(array) = nil error, want rejection")
	}
}

func TestParseSkipsMalformedEntry(t *testing.T) {
	const src = `{
      "good.option": {"type":"boolean","loc":["good","option"]},
      "broken": [1,2,3],
      "also.broken": "not an object"
    }`
	ix, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := ix.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1 (only the good entry)", got)
	}
	if _, ok := ix.Lookup([]string{"good", "option"}); !ok {
		t.Error("good.option not parsed alongside malformed entries")
	}
}

func TestMarkdownGoldenWithExample(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup([]string{"networking", "firewall", "allowedTCPPorts"})
	if !ok {
		t.Fatal("allowedTCPPorts not found")
	}
	want := "**networking.firewall.allowedTCPPorts**\n\n" +
		"List of TCP ports on which incoming connections are\naccepted.\n\n" +
		"*Type:* `list of 16 bit unsigned integer; between 0 and 65535 (both inclusive)`\n\n" +
		"*Default:*\n```nix\n[ ]\n```\n\n" +
		"*Example:*\n```nix\n[\n  22\n  80\n]\n```\n\n" +
		"*Declared in:* `nixos/modules/services/networking/firewall.nix`"
	if got := doc.Markdown(); got != want {
		t.Errorf("Markdown mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarkdownGoldenNoExample(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup([]string{"networking", "firewall", "enable"})
	if !ok {
		t.Fatal("firewall.enable not found")
	}
	want := "**networking.firewall.enable**\n\n" +
		"Whether to enable the firewall.  This is a simple stateful\n" +
		"firewall that blocks connection attempts to unauthorised TCP\n" +
		"or UDP ports on this machine.\n\n" +
		"*Type:* `boolean`\n\n" +
		"*Default:*\n```nix\ntrue\n```\n\n" +
		"*Declared in:* `nixos/modules/services/networking/firewall.nix`"
	if got := doc.Markdown(); got != want {
		t.Errorf("Markdown mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}
