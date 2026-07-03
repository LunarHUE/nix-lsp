package datadiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// loadOptionsIndex parses the shared options fixture (the same 21-entry file the
// server option tests use) into an index.
func loadOptionsIndex(t *testing.T) *options.Index {
	t.Helper()
	path := filepath.Join("..", "options", "testdata", "options.fixture.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read options fixture: %v", err)
	}
	index, err := options.Parse(data)
	if err != nil {
		t.Fatalf("parse options fixture: %v", err)
	}
	return index
}

// loadPackagesIndex parses the shared packages fixture into an index.
func loadPackagesIndex(t *testing.T) *packages.Index {
	t.Helper()
	path := filepath.Join("..", "packages", "testdata", "packages.fixture.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open packages fixture: %v", err)
	}
	defer f.Close()
	index, err := packages.ParseStream(f)
	if err != nil {
		t.Fatalf("parse packages fixture: %v", err)
	}
	return index
}

func mustParse(t *testing.T, src string) *syntax.Tree {
	t.Helper()
	tree, err := syntax.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse nix: %v", err)
	}
	return tree
}

// optionDiags is a shorthand that parses src and runs OptionDiagnostics.
func optionDiags(t *testing.T, src string) []Diagnostic {
	t.Helper()
	return OptionDiagnostics(mustParse(t, src), loadOptionsIndex(t))
}

func TestOptionModuleGateConfigFormalTypo(t *testing.T) {
	src := `{ config, pkgs, ... }:
{
  networking.firewal.enable = true;
}
`
	diags := optionDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Code != CodeUnknownOption {
		t.Errorf("code = %q, want %q", d.Code, CodeUnknownOption)
	}
	if want := "unknown option: networking.firewal (did you mean firewall?)"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
	if d.Severity != syntax.SeverityWarning {
		t.Errorf("severity = %v, want warning", d.Severity)
	}
	if got := []string{"firewall"}; !equalStrings(d.Suggestions, got) {
		t.Errorf("suggestions = %v, want %v", d.Suggestions, got)
	}
	// The range must cover exactly the `firewal` segment, not the whole path.
	if got := textInRange(t, src, d.Range); got != "firewal" {
		t.Errorf("flagged text = %q, want %q", got, "firewal")
	}
}

func TestOptionModuleGatePlainDataAttrsetNoDiagnostics(t *testing.T) {
	// No config formal and fewer than two exact option hits: an arbitrary data
	// attrset whose keys merely resemble options must not be flagged.
	src := `{
  networking.foo = 1;
  bar.baz = 2;
}
`
	if diags := optionDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionModuleGatePlainAttrsetWithTwoHitsPasses(t *testing.T) {
	// Two exact documented-option hits make this a module by the fallback rule; the
	// third path is a typo that must then be flagged.
	src := `{
  networking.firewall.enable = true;
  services.openssh.enable = true;
  time.timezone = "UTC";
}
`
	diags := optionDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "unknown option: time.timezone (did you mean timeZone?)"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestOptionWildcardInstanceNeverFlagged(t *testing.T) {
	// The instance name (myWeirdName) is arbitrary, and systemd.services is itself a
	// documented freeform option, so nothing under it is flagged.
	src := `{ config, ... }:
{
  systemd.services.myWeirdName.serviceConfig = { RestartSec = 5; };
}
`
	if diags := optionDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionWildcardAcceptsInstanceButFlagsLeafTypo(t *testing.T) {
	// users.users has a <name> wildcard but no doc, so the instance (alice) is
	// accepted and the misspelled leaf (shel) is flagged with a suggestion.
	src := `{ config, ... }:
{
  users.users.alice.shel = "/bin/zsh";
}
`
	diags := optionDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "unknown option: users.users.alice.shel (did you mean shell?)"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
	if got := textInRange(t, src, diags[0].Range); got != "shel" {
		t.Errorf("flagged text = %q, want %q", got, "shel")
	}
}

func TestOptionUnknownFirstSegmentSilent(t *testing.T) {
	// isoImage is not a top-level option namespace here (installer profile), so the
	// first segment reaching no trie node stays silent.
	src := `{ config, ... }:
{
  isoImage.isoName = "foo";
}
`
	if diags := optionDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionBeyondLeafSilent(t *testing.T) {
	// nix.settings is a freeform option; deeper segments are its values, not
	// unknown options.
	src := `{ config, ... }:
{
  nix.settings.foo.bar = 1;
}
`
	if diags := optionDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionSkipsOptionsImportsModule(t *testing.T) {
	src := `{ config, ... }:
{
  options.networking.firewal = lib.mkOption {};
  imports = [ ./x.nix ];
  _module.args.foo = 1;
  disabledModules.bar = 1;
  networking.firewall.enable = true;
}
`
	if diags := optionDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionLetBindingsSkipped(t *testing.T) {
	// The typo lives in a let binding list, which is never an option path; the in
	// body holds only a valid option, so nothing is flagged.
	src := `{ config, ... }:
let
  networking.firewal = 1;
in
{
  services.openssh.enable = true;
}
`
	if diags := optionDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionDynamicSegmentBails(t *testing.T) {
	src := `{ config, ... }:
{
  networking.${toString 1}.enable = true;
  services.openssh.enable = true;
}
`
	if diags := optionDiags(t, src); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestOptionOneDiagnosticPerPath(t *testing.T) {
	// Two bad segments on one path; only the first (firewal) is reported.
	src := `{ config, ... }:
{
  networking.firewal.enabel = true;
}
`
	diags := optionDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if got := textInRange(t, src, diags[0].Range); got != "firewal" {
		t.Errorf("flagged text = %q, want %q", got, "firewal")
	}
}

func TestOptionUnknownWithoutSuggestionSilent(t *testing.T) {
	// An unknown child of a known group with NO sibling within edit distance 2
	// stays silent: the dataset only carries documented options, and NixOS
	// declares some real options internal/invisible (system.disableInstallerTools
	// is absent from the channel options.json while its siblings are present), so
	// a far-from-everything name is more likely such a hidden real option than a
	// typo. The same shape against the fixture: `disableInstallerTools` under the
	// known `networking` group, nowhere near `firewall` or `wireguard`.
	tests := []string{
		`{ config, ... }:
{
  networking.disableInstallerTools = true;
}
`,
		// The original suggestion-less case: a group typo with no near child.
		`{ config, ... }:
{
  boot.loxxxxxx.enable = true;
}
`,
	}
	for _, src := range tests {
		if diags := optionDiags(t, src); len(diags) != 0 {
			t.Errorf("got %d diagnostics for %q, want 0 (no near-miss sibling): %+v", len(diags), src, diags)
		}
	}
}

// userRealModule is a user's real minimal-profile module, verbatim. It distills
// every conservative rule into one fixture: an interpolated import (skipped
// namespace), a real-but-undocumented option (system.disableInstallerTools is
// declared internal, so it is absent from options.json while its sibling
// stateVersion is present), a freeform-leaf descent (nix.settings.*), and a
// lib.mkDefault value the type check must skip. It must produce ZERO dataset
// diagnostics — with the module gate ARMED, not failed.
const userRealModule = `{ modulesPath, lib, ... }:
{
  imports = [
    "${modulesPath}/profiles/minimal.nix"
  ];
  system.disableInstallerTools = true;
  boot.initrd.systemd.enable = true;
  boot.loader.systemd-boot.configurationLimit = lib.mkDefault 2;
  nix.settings.auto-optimise-store = true;
}
`

func TestOptionUserRealModuleSilent(t *testing.T) {
	tree := mustParse(t, userRealModule)
	index := loadOptionsIndex(t)

	// The file has no config formal, so the gate must arm via the >=2
	// exact-documented-hits path (boot.initrd.systemd.enable and
	// boot.loader.systemd-boot.configurationLimit resolve exactly). Assert the
	// gate is genuinely armed so the zero-diagnostics assertions below prove the
	// conservative rules, not a failed gate.
	if _, gated := gatherModuleBindings(tree, index); !gated {
		t.Fatal("module gate did not arm; the silence assertions would be vacuous")
	}
	if diags := OptionDiagnostics(tree, index); len(diags) != 0 {
		t.Errorf("OptionDiagnostics = %+v, want none", diags)
	}
	if diags := OptionTypeDiagnostics(tree, index); len(diags) != 0 {
		t.Errorf("OptionTypeDiagnostics = %+v, want none", diags)
	}
}

func TestOptionConfigPrefixStripped(t *testing.T) {
	src := `{ config, ... }:
{
  config.networking.firewal.enable = true;
}
`
	diags := optionDiags(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "unknown option: networking.firewal (did you mean firewall?)"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
}

func TestPackageUnknownWithSuggestion(t *testing.T) {
	src := `{ x = pkgs.htoop; }`
	diags := PackageDiagnostics(mustParse(t, src), loadPackagesIndex(t))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Code != CodeUnknownPackage {
		t.Errorf("code = %q, want %q", d.Code, CodeUnknownPackage)
	}
	if want := "unknown package: pkgs.htoop (did you mean htop?)"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
	if got := []string{"htop"}; !equalStrings(d.Suggestions, got) {
		t.Errorf("suggestions = %v, want %v", d.Suggestions, got)
	}
	if got := textInRange(t, src, d.Range); got != "htoop" {
		t.Errorf("flagged text = %q, want %q", got, "htoop")
	}
}

func TestPackageNestedNamespaceTypo(t *testing.T) {
	src := `{ x = pkgs.python312Packages.reqests; }`
	diags := PackageDiagnostics(mustParse(t, src), loadPackagesIndex(t))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if want := "unknown package: pkgs.python312Packages.reqests (did you mean python312Packages.requests?)"; diags[0].Message != want {
		t.Errorf("message = %q, want %q", diags[0].Message, want)
	}
	// The flagged range spans the whole selection so the fix replaces it wholesale.
	if got := textInRange(t, src, diags[0].Range); got != "python312Packages.reqests" {
		t.Errorf("flagged text = %q, want %q", got, "python312Packages.reqests")
	}
}

func TestPackageShortAttrSilent(t *testing.T) {
	// A two-character attr is too ambiguous to correct, so it stays silent even
	// though "xy" is not a package.
	src := `{ x = pkgs.xy; }`
	if diags := PackageDiagnostics(mustParse(t, src), loadPackagesIndex(t)); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestPackageWellknownNeverFlagged(t *testing.T) {
	src := `{ x = pkgs.lib; }`
	if diags := PackageDiagnostics(mustParse(t, src), loadPackagesIndex(t)); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestPackageKnownNeverFlagged(t *testing.T) {
	src := `{ x = pkgs.htop; }`
	if diags := PackageDiagnostics(mustParse(t, src), loadPackagesIndex(t)); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestPackageNoNearMissSilent(t *testing.T) {
	// An unknown attr with no package within edit distance 2 is more likely a
	// package we do not know than a typo, so it stays silent (conservative).
	src := `{ x = pkgs.zzzzznope; }`
	if diags := PackageDiagnostics(mustParse(t, src), loadPackagesIndex(t)); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestPackageEmptyIndexSilent(t *testing.T) {
	empty, err := packages.ParseStream(strings.NewReader(`{"packages":{}}`))
	if err != nil {
		t.Fatalf("parse empty index: %v", err)
	}
	src := `{ x = pkgs.htoop; }`
	if diags := PackageDiagnostics(mustParse(t, src), empty); len(diags) != 0 {
		t.Fatalf("got %d diagnostics, want 0: %+v", len(diags), diags)
	}
}

func TestNilInputs(t *testing.T) {
	if diags := OptionDiagnostics(nil, loadOptionsIndex(t)); diags != nil {
		t.Errorf("OptionDiagnostics(nil tree) = %v, want nil", diags)
	}
	if diags := OptionDiagnostics(mustParse(t, `{}`), nil); diags != nil {
		t.Errorf("OptionDiagnostics(nil index) = %v, want nil", diags)
	}
	if diags := PackageDiagnostics(nil, loadPackagesIndex(t)); diags != nil {
		t.Errorf("PackageDiagnostics(nil tree) = %v, want nil", diags)
	}
	if diags := PackageDiagnostics(mustParse(t, `{}`), nil); diags != nil {
		t.Errorf("PackageDiagnostics(nil index) = %v, want nil", diags)
	}
}

// equalStrings reports whether a and b hold the same strings in order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// textInRange returns the substring of src covered by a single-line range r.
func textInRange(t *testing.T, src string, r syntax.Range) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	if r.Start.Line != r.End.Line {
		t.Fatalf("range spans lines %d..%d, helper handles single line", r.Start.Line, r.End.Line)
	}
	if r.Start.Line < 0 || r.Start.Line >= len(lines) {
		t.Fatalf("range line %d out of bounds", r.Start.Line)
	}
	line := lines[r.Start.Line]
	if r.Start.Character < 0 || r.End.Character > len(line) {
		t.Fatalf("range chars %d..%d out of bounds on %q", r.Start.Character, r.End.Character, line)
	}
	return line[r.Start.Character:r.End.Character]
}
