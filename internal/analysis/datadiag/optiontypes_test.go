package datadiag

import (
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// optionTypeDiags parses src and runs OptionTypeDiagnostics against the shared
// options fixture.
func optionTypeDiags(t *testing.T, src string) []Diagnostic {
	t.Helper()
	return OptionTypeDiagnostics(mustParse(t, src), loadOptionsIndex(t))
}

// module wraps a body in a config-formal function so the module gate always
// passes, isolating the type check from the gate under test elsewhere.
func module(body string) string {
	return "{ config, pkgs, lib, ... }:\n{\n" + body + "\n}\n"
}

func TestOptionTypeBooleanMismatch(t *testing.T) {
	src := module(`  networking.firewall.enable = "yes";`)
	diags := optionTypeDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Code != CodeOptionTypeMismatch {
		t.Errorf("code = %q, want %q", d.Code, CodeOptionTypeMismatch)
	}
	if d.Severity != syntax.SeverityWarning {
		t.Errorf("severity = %v, want warning", d.Severity)
	}
	if want := "type mismatch: networking.firewall.enable expects boolean, got string"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
	if got := textInRange(t, src, d.Range); got != `"yes"` {
		t.Errorf("flagged text = %q, want %q", got, `"yes"`)
	}
}

// TestOptionTypeBooleanOtherKinds covers each non-boolean literal against a
// boolean option, asserting the exact "got" wording per kind.
func TestOptionTypeBooleanOtherKinds(t *testing.T) {
	tests := []struct {
		value string
		got   string
	}{
		{"5", "integer"},
		{"-5", "integer"},
		{`"s"`, "string"},
		{"[ 1 ]", "list"},
		{"{ a = 1; }", "attribute set"},
	}
	for _, tc := range tests {
		t.Run(tc.got, func(t *testing.T) {
			src := module("  networking.firewall.enable = " + tc.value + ";")
			diags := optionTypeDiags(t, src)
			if len(diags) != 1 {
				t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
			}
			want := "type mismatch: networking.firewall.enable expects boolean, got " + tc.got
			if diags[0].Message != want {
				t.Errorf("message = %q, want %q", diags[0].Message, want)
			}
		})
	}
}

func TestOptionTypeStringMismatchWildcardLeaf(t *testing.T) {
	// systemd.services.<name>.description is a real leaf reached through a wildcard
	// instance segment; its declared type is "string".
	src := module(`  systemd.services.web.description = 5;`)
	diags := optionTypeDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "type mismatch: systemd.services.web.description expects string, got integer"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestOptionTypeListMismatch(t *testing.T) {
	// "list of 16 bit unsigned integer; ..." must read as a list, not an integer.
	src := module(`  networking.firewall.allowedTCPPorts = "x";`)
	diags := optionTypeDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "type mismatch: networking.firewall.allowedTCPPorts expects list, got string"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestOptionTypeAttrsetMismatch(t *testing.T) {
	// nix.settings is "open submodule of ..."; systemd.services is "attribute set of
	// (submodule)". Both must expect an attribute set.
	for _, tc := range []struct{ path, want string }{
		{"nix.settings", "type mismatch: nix.settings expects attribute set, got string"},
		{"systemd.services", "type mismatch: systemd.services expects attribute set, got string"},
	} {
		src := module("  " + tc.path + ` = "x";`)
		diags := optionTypeDiags(t, src)
		if len(diags) != 1 {
			t.Fatalf("%s: got %d diagnostics, want 1: %+v", tc.path, len(diags), diags)
		}
		if diags[0].Message != tc.want {
			t.Errorf("message = %q, want %q", diags[0].Message, tc.want)
		}
	}
}

func TestOptionTypeIntegerMismatch(t *testing.T) {
	// The fixture has no standalone integer option, so drive the integer branch with
	// a tiny purpose-built index. A config-formal module gates it in.
	index := mustIndex(t, `{
	  "foo.port": { "loc": ["foo","port"], "type": "16 bit unsigned integer; between 0 and 65535 (both inclusive)" },
	  "foo.name": { "loc": ["foo","name"], "type": "string" }
	}`)
	diags := OptionTypeDiagnostics(mustParse(t, module(`  foo.port = "x";`)), index)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "type mismatch: foo.port expects integer, got string"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestOptionTypeCorrectLiteralsSilent(t *testing.T) {
	src := module(`  networking.firewall.enable = true;
  boot.loader.systemd-boot.enable = false;
  systemd.services.web.description = "web";
  networking.firewall.allowedTCPPorts = [ 22 80 ];
  systemd.services.web.serviceConfig = { RestartSec = 5; };
  nix.settings = { };
  time.timeZone = "UTC";
  networking.firewall.enable = (true);`)
	if diags := optionTypeDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionTypeNullOrAcceptsNull(t *testing.T) {
	// time.timeZone is "null or string without spaces": null is fine, an integer is not.
	if diags := optionTypeDiags(t, module(`  time.timeZone = null;`)); len(diags) != 0 {
		t.Fatalf("null value: got %d diagnostics, want 0: %+v", len(diags), diags)
	}
	diags := optionTypeDiags(t, module(`  time.timeZone = 5;`))
	if len(diags) != 1 {
		t.Fatalf("integer value: got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "type mismatch: time.timeZone expects string, got integer"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestOptionTypeUnrecognizedTypesSilent(t *testing.T) {
	// Enum, package, and path types are outside the mapping and never flagged, even
	// with an obviously wrong literal kind.
	src := module(`  services.openssh.settings.PermitRootLogin = 5;
  users.users.alice.shell = 5;
  users.users.alice.home = 5;`)
	if diags := optionTypeDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionTypeComputedValuesSilent(t *testing.T) {
	// References, function calls, selects, and interpolation-bearing strings are all
	// dynamic: the type check must leave every one of them alone.
	src := module(`  networking.firewall.enable = lib.mkForce true;
  boot.loader.systemd-boot.enable = cfg.wanted;
  systemd.services.web.description = config.networking.hostName;
  networking.firewall.enable = "${toString y}";`)
	if diags := optionTypeDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionTypeNonModuleSilent(t *testing.T) {
	// A plain data attrset with a single option-shaped path fails the module gate, so
	// even a wrong literal kind produces nothing (gate reuse).
	src := `{ networking.firewall.enable = "yes"; }`
	if diags := optionTypeDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionTypeNilInputs(t *testing.T) {
	if diags := OptionTypeDiagnostics(nil, loadOptionsIndex(t)); diags != nil {
		t.Errorf("OptionTypeDiagnostics(nil tree) = %v, want nil", diags)
	}
	if diags := OptionTypeDiagnostics(mustParse(t, `{}`), nil); diags != nil {
		t.Errorf("OptionTypeDiagnostics(nil index) = %v, want nil", diags)
	}
}

// TestOptionExpectedKind pins the type-string mapping, including the cases the
// fixture cannot cover (plain integers) and the ordering hazard where a list of
// integers must not be read as an integer.
func TestOptionExpectedKind(t *testing.T) {
	tests := []struct {
		typ       string
		kind      valueKind
		allowNull bool
		ok        bool
	}{
		{"boolean", valueBool, false, true},
		{"null or boolean", valueBool, true, true},
		{"signed integer", valueInt, false, true},
		{"unsigned integer", valueInt, false, true},
		{"16 bit unsigned integer; between 0 and 65535 (both inclusive)", valueInt, false, true},
		{"string", valueString, false, true},
		{"null or string without spaces", valueString, true, true},
		{"single-line string", valueString, false, true},
		{"(optionally newline-terminated) single-line string", valueString, false, true},
		{"list of package", valueList, false, true},
		{"list of 16 bit unsigned integer; between 0 and 65535 (both inclusive)", valueList, false, true},
		{"attribute set of (submodule)", valueAttrset, false, true},
		{"submodule", valueAttrset, false, true},
		{"open submodule of attribute set of (...)", valueAttrset, false, true},
		{`one of "yes", "no"`, 0, false, false},
		{"package", 0, false, false},
		{"absolute path, not containing newlines or colons", 0, false, false},
		{"either bool or string", 0, false, false},
		{"", 0, false, false},
	}
	for _, tc := range tests {
		kind, allowNull, ok := optionExpectedKind(tc.typ)
		if ok != tc.ok || allowNull != tc.allowNull || (ok && kind != tc.kind) {
			t.Errorf("optionExpectedKind(%q) = (%v, %v, %v), want (%v, %v, %v)",
				tc.typ, kind, allowNull, ok, tc.kind, tc.allowNull, tc.ok)
		}
	}
}

// mustIndex parses inline options JSON into an index for tests that need a type
// the shared fixture does not carry.
func mustIndex(t *testing.T, data string) *options.Index {
	t.Helper()
	index, err := options.Parse([]byte(data))
	if err != nil {
		t.Fatalf("parse inline options: %v", err)
	}
	return index
}

func TestOptionTypeEnumMismatch(t *testing.T) {
	src := module(`  services.openssh.settings.PermitRootLogin = "maybe";`)
	diags := optionTypeDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Code != CodeOptionTypeMismatch {
		t.Errorf("code = %q, want %q", d.Code, CodeOptionTypeMismatch)
	}
	want := `type mismatch: services.openssh.settings.PermitRootLogin expects one of "yes", "without-password", "prohibit-password", "forced-commands-only", "no"; got "maybe"`
	if d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
	if got := textInRange(t, src, d.Range); got != `"maybe"` {
		t.Errorf("flagged text = %q, want %q", got, `"maybe"`)
	}
	// "maybe" is not within two edits of any legal value, so no did-you-mean.
	if len(d.Suggestions) != 0 {
		t.Errorf("suggestions = %v, want none", d.Suggestions)
	}
}

func TestOptionTypeEnumSuggestion(t *testing.T) {
	// "noo" is one edit from the legal "no", so a quoted did-you-mean is offered.
	src := module(`  services.openssh.settings.PermitRootLogin = "noo";`)
	diags := optionTypeDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := []string{`"no"`}; len(diags[0].Suggestions) != 1 || diags[0].Suggestions[0] != want[0] {
		t.Errorf("suggestions = %v, want %v", diags[0].Suggestions, want)
	}
}

func TestOptionTypeEnumValidSilent(t *testing.T) {
	for _, v := range []string{`"prohibit-password"`, `"no"`, `null`} {
		src := module("  services.openssh.settings.PermitRootLogin = " + v + ";")
		if diags := optionTypeDiags(t, src); len(diags) != 0 {
			t.Errorf("value %s: got %d diagnostics, want 0: %+v", v, len(diags), diags)
		}
	}
}

func TestOptionTypeEnumInterpolationSilent(t *testing.T) {
	src := module("  services.openssh.settings.PermitRootLogin = \"${toString x}\";")
	if diags := optionTypeDiags(t, src); len(diags) != 0 {
		t.Errorf("interpolated enum value flagged: %+v", diags)
	}
}

// TestOptionTypeEnumNonStringSkipped proves an integer enum is not value-checked:
// the parser reports no string members, so any literal there is left alone.
func TestOptionTypeEnumNonStringSkipped(t *testing.T) {
	index := mustIndex(t, `{"x.y": {"loc": ["x","y"], "type": "one of 1, 2, 3"}, "x.z": {"loc": ["x","z"], "type": "boolean"}}`)
	tree := mustParse(t, "{ config, ... }:\n{\n  x.y = \"maybe\";\n  x.z = true;\n}\n")
	if diags := OptionTypeDiagnostics(tree, index); len(diags) != 0 {
		t.Errorf("integer enum value flagged: %+v", diags)
	}
}

func TestOptionTypePatternMismatch(t *testing.T) {
	src := module(`  networking.hostId = "xyz";`)
	diags := optionTypeDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "type mismatch: networking.hostId does not match the expected pattern [0-9a-f]{8}"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
	if got := textInRange(t, src, diags[0].Range); got != `"xyz"` {
		t.Errorf("flagged text = %q, want %q", got, `"xyz"`)
	}
}

func TestOptionTypePatternValidSilent(t *testing.T) {
	src := module(`  networking.hostId = "4e98920d";`)
	if diags := optionTypeDiags(t, src); len(diags) != 0 {
		t.Errorf("matching hostId flagged: %+v", diags)
	}
}

// TestOptionTypePatternUncompilableSilent proves a pattern Go's regexp cannot
// compile yields no diagnostic rather than a wrong one.
func TestOptionTypePatternUncompilableSilent(t *testing.T) {
	// A lookahead is Perl-only syntax Go's regexp rejects at compile time.
	index := mustIndex(t, `{"x.y": {"loc": ["x","z"], "type": "string matching the pattern (?=a)b"}}`)
	tree := mustParse(t, "{ config, ... }:\n{\n  x.z = \"bbb\";\n}\n")
	if diags := OptionTypeDiagnostics(tree, index); len(diags) != 0 {
		t.Errorf("uncompilable pattern flagged: %+v", diags)
	}
}

func TestOptionTypeStringWithoutSpaces(t *testing.T) {
	src := module(`  time.timeZone = "New York";`)
	diags := optionTypeDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "type mismatch: time.timeZone expects a string without spaces"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestOptionTypeStringWithoutSpacesValid(t *testing.T) {
	src := module(`  time.timeZone = "America/New_York";`)
	if diags := optionTypeDiags(t, src); len(diags) != 0 {
		t.Errorf("space-free timezone flagged: %+v", diags)
	}
}
