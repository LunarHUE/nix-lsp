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
	if got := ix.Len(); got != 13 {
		t.Fatalf("Len = %d, want 13", got)
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

func TestLookupNearest(t *testing.T) {
	ix := loadFixture(t)
	cases := []struct {
		name        string
		path        []string
		wantOK      bool
		wantMatched []string
	}{
		{
			name:        "exact hit matches the full path",
			path:        []string{"networking", "firewall", "enable"},
			wantOK:      true,
			wantMatched: []string{"networking", "firewall", "enable"},
		},
		{
			name:        "instance segment falls back to the attrsOf prefix",
			path:        []string{"systemd", "services", "demo-web"},
			wantOK:      true,
			wantMatched: []string{"systemd", "services"},
		},
		{
			name:   "total miss (no documented prefix)",
			path:   []string{"networking"},
			wantOK: false,
		},
		{
			name:   "garbage path",
			path:   []string{"no", "such", "option"},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, matched, ok := ix.LookupNearest(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("LookupNearest(%v) ok = %v, want %v", tc.path, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if doc == nil {
				t.Fatalf("LookupNearest(%v) doc = nil with ok = true", tc.path)
			}
			if !slices.Equal(matched, tc.wantMatched) {
				t.Errorf("matched = %v, want %v", matched, tc.wantMatched)
			}
		})
	}

	// The instance-segment fallback resolves to the systemd.services doc itself.
	doc, _, ok := ix.LookupNearest([]string{"systemd", "services", "demo-web"})
	if !ok {
		t.Fatal("systemd.services.demo-web did not fall back to a prefix")
	}
	if !strings.Contains(doc.Description, "service units") {
		t.Errorf("fallback doc description = %q, want the systemd.services doc", doc.Description)
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

func TestChildrenTopLevel(t *testing.T) {
	ix := loadFixture(t)
	children := ix.Children(nil)
	var names []string
	for _, c := range children {
		names = append(names, c.Name)
	}
	want := []string{"boot", "environment", "networking", "nix", "services", "systemd", "time", "users"}
	if !slices.Equal(names, want) {
		t.Fatalf("Children(nil) names = %v, want %v", names, want)
	}
	// Top-level groups are interior nodes: no Doc, deeper segments present.
	for _, c := range children {
		if c.Doc != nil {
			t.Errorf("child %q Doc = %+v, want nil (group)", c.Name, c.Doc)
		}
		if !c.HasChildren {
			t.Errorf("child %q HasChildren = false, want true (group)", c.Name)
		}
	}
}

func TestChildrenLeafOptions(t *testing.T) {
	ix := loadFixture(t)
	children := ix.Children([]string{"networking", "firewall"})
	if len(children) != 2 {
		t.Fatalf("Children(networking.firewall) = %d entries, want 2", len(children))
	}
	// Sorted by Name: allowedTCPPorts before enable.
	if children[0].Name != "allowedTCPPorts" || children[1].Name != "enable" {
		t.Fatalf("names = [%q %q], want [allowedTCPPorts enable]", children[0].Name, children[1].Name)
	}
	for _, c := range children {
		if c.Doc == nil {
			t.Errorf("child %q Doc = nil, want the leaf option doc", c.Name)
		}
		if c.HasChildren {
			t.Errorf("child %q HasChildren = true, want false (leaf)", c.Name)
		}
	}
}

func TestChildrenThroughWildcard(t *testing.T) {
	ix := loadFixture(t)
	// A concrete instance segment descends through the <name> placeholder and
	// returns ITS children, exactly as Lookup tolerates wildcards.
	children := ix.Children([]string{"systemd", "services", "demo-web"})
	var names []string
	byName := map[string]Child{}
	for _, c := range children {
		names = append(names, c.Name)
		byName[c.Name] = c
	}
	want := []string{"description", "serviceConfig"}
	if !slices.Equal(names, want) {
		t.Fatalf("Children(systemd.services.demo-web) = %v, want %v", names, want)
	}
	if byName["description"].Doc == nil {
		t.Error("description child Doc = nil, want the wildcard-instance doc")
	}
	if byName["description"].HasChildren {
		t.Error("description HasChildren = true, want false")
	}
}

func TestChildrenPlaceholderNotListed(t *testing.T) {
	ix := loadFixture(t)
	// systemd.services holds only a <name> child in the trie; a placeholder is
	// not itself completable, so Children reports nothing.
	if got := ix.Children([]string{"systemd", "services"}); got != nil {
		t.Errorf("Children(systemd.services) = %v, want nil (only a <name> placeholder)", got)
	}
}

func TestChildrenUnknownAndLeafPaths(t *testing.T) {
	ix := loadFixture(t)
	if got := ix.Children([]string{"no", "such", "group"}); got != nil {
		t.Errorf("Children(unknown) = %v, want nil", got)
	}
	// A terminal leaf option has no children.
	if got := ix.Children([]string{"networking", "firewall", "enable"}); got != nil {
		t.Errorf("Children(leaf) = %v, want nil", got)
	}
}

func TestChildrenNilReceiver(t *testing.T) {
	var ix *Index
	if got := ix.Children([]string{"networking"}); got != nil {
		t.Errorf("nil.Children = %v, want nil", got)
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

// TestMarkdownForChannelLinksDeclaration proves that with a channel set, a
// nixpkgs-relative declaration path becomes a markdown link to the file on that
// channel's branch of nixpkgs on GitHub, using the exact blob URL.
func TestMarkdownForChannelLinksDeclaration(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup([]string{"networking", "firewall", "allowedTCPPorts"})
	if !ok {
		t.Fatal("allowedTCPPorts not found")
	}
	path := []string{"networking", "firewall", "allowedTCPPorts"}
	want := "**networking.firewall.allowedTCPPorts**\n\n" +
		"List of TCP ports on which incoming connections are\naccepted.\n\n" +
		"*Type:* `list of 16 bit unsigned integer; between 0 and 65535 (both inclusive)`\n\n" +
		"*Default:*\n```nix\n[ ]\n```\n\n" +
		"*Example:*\n```nix\n[\n  22\n  80\n]\n```\n\n" +
		"*Declared in:* [nixos/modules/services/networking/firewall.nix]" +
		"(https://github.com/NixOS/nixpkgs/blob/nixos-25.05/nixos/modules/services/networking/firewall.nix)"
	if got := doc.MarkdownForChannel(path, "nixos-25.05"); got != want {
		t.Errorf("MarkdownForChannel mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestMarkdownForChannelNonPathStaysBackticked proves a declaration that is not a
// nixpkgs-relative path (here an absolute store path) stays backticked even when
// a channel is set, so only linkable source paths become links.
func TestMarkdownForChannelNonPathStaysBackticked(t *testing.T) {
	doc := &Doc{
		Loc:          []string{"foo", "bar"},
		Type:         "boolean",
		Declarations: []string{"/nix/store/abc-source/default.nix"},
	}
	want := "**foo.bar**\n\n" +
		"*Type:* `boolean`\n\n" +
		"*Declared in:* `/nix/store/abc-source/default.nix`"
	if got := doc.MarkdownForChannel([]string{"foo", "bar"}, "nixos-25.05"); got != want {
		t.Errorf("MarkdownForChannel mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarkdownForGoldenConcreteInstance(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup([]string{"systemd", "services", "demo-web", "description"})
	if !ok {
		t.Fatal("systemd.services.demo-web.description not resolved via <name>")
	}
	want := "**systemd.services.demo-web.description**\n\n" +
		"Description of this unit used in systemd messages and progress indicators.\n\n" +
		"*Type:* `string`\n\n" +
		"*Default:*\n```nix\n\"\"\n```\n\n" +
		"*Declared in:* `nixos/modules/system/boot/systemd.nix`"
	got := doc.MarkdownFor([]string{"systemd", "services", "demo-web", "description"})
	if got != want {
		t.Errorf("MarkdownFor mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarkdownEscapesWildcardHeader(t *testing.T) {
	ix := loadFixture(t)
	doc, ok := ix.Lookup([]string{"users", "users", "<name>", "home"})
	if !ok {
		t.Fatal("users.users.<name>.home not found")
	}
	got := doc.Markdown()
	// The placeholder must survive markdown rendering as literal text, not be
	// stripped as an HTML tag, so the header carries backslash-escaped brackets.
	if !strings.HasPrefix(got, `**users.users.\<name\>.home**`) {
		t.Errorf("Markdown header = %q, want escaped \\<name\\> placeholder", got[:min(len(got), 40)])
	}
	if strings.Contains(got, "**users.users.<name>.home**") {
		t.Errorf("Markdown header carries a raw <name> tag:\n%s", got)
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
