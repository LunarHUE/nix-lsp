package flake

import (
	"testing"
)

func lockWith(names ...string) *Lock {
	inputs := make(map[string]Ref)
	nodes := map[string]Node{"root": {Inputs: inputs}}
	for _, n := range names {
		inputs[n] = Ref{Key: n}
		nodes[n] = Node{}
	}
	return &Lock{Root: "root", Version: 7, Nodes: nodes}
}

func diagCodes(f *File, lock *Lock, hasLock bool) []string {
	var out []string
	for _, d := range Diagnostics(f, lock, hasLock) {
		out = append(out, d.Code)
	}
	return out
}

func hasCode(f *File, lock *Lock, hasLock bool, code string) bool {
	for _, c := range diagCodes(f, lock, hasLock) {
		if c == code {
			return true
		}
	}
	return false
}

func TestDanglingFollowsFires(t *testing.T) {
	// other follows "nixpkgs" but only "pkgs" is declared -> dangling.
	f := analyze(t, `{
  inputs.pkgs.url = "u";
  inputs.other.follows = "nixpkgs";
}`)
	if !hasCode(f, nil, false, CodeDanglingFollows) {
		t.Fatalf("expected dangling-follows, got %v", diagCodes(f, nil, false))
	}
}

func TestDanglingFollowsNearMissDeclared(t *testing.T) {
	// other follows "nixpkgs" which IS declared -> no dangling.
	f := analyze(t, `{
  inputs.nixpkgs.url = "u";
  inputs.other.follows = "nixpkgs";
}`)
	if hasCode(f, nil, false, CodeDanglingFollows) {
		t.Fatalf("unexpected dangling-follows, got %v", diagCodes(f, nil, false))
	}
}

func TestDanglingFollowsNestedEdgeUsesFirstSegment(t *testing.T) {
	// Slash target: first segment "nixpkgs" declared -> not dangling.
	ok := analyze(t, `{
  inputs.nixpkgs.url = "u";
  inputs.hm.inputs.x.follows = "nixpkgs/legacyPackages";
}`)
	if hasCode(ok, nil, false, CodeDanglingFollows) {
		t.Errorf("slash target should resolve on first segment: %v", diagCodes(ok, nil, false))
	}
	bad := analyze(t, `{
  inputs.nixpkgs.url = "u";
  inputs.hm.inputs.x.follows = "nope/legacyPackages";
}`)
	if !hasCode(bad, nil, false, CodeDanglingFollows) {
		t.Errorf("nested dangling edge should fire: %v", diagCodes(bad, nil, false))
	}
}

func TestInputNotLockedFires(t *testing.T) {
	f := analyze(t, `{ inputs.a.url = "u"; inputs.b.url = "v"; }`)
	// Lock has a but not b.
	if !hasCode(f, lockWith("a"), true, CodeInputNotLocked) {
		t.Fatalf("expected input-not-locked, got %v", diagCodes(f, lockWith("a"), true))
	}
}

func TestInputNotLockedNearMissLocked(t *testing.T) {
	f := analyze(t, `{ inputs.a.url = "u"; }`)
	if hasCode(f, lockWith("a"), true, CodeInputNotLocked) {
		t.Fatalf("unexpected input-not-locked when locked: %v", diagCodes(f, lockWith("a"), true))
	}
}

func TestInputNotLockedNoLockFileSuppressed(t *testing.T) {
	f := analyze(t, `{ inputs.a.url = "u"; }`)
	if hasCode(f, nil, false, CodeInputNotLocked) {
		t.Fatalf("lock-dependent diagnostic fired without a lock: %v", diagCodes(f, nil, false))
	}
}

func TestStaleLockEntryFires(t *testing.T) {
	f := analyze(t, `{ inputs.a.url = "u"; }`)
	// Lock has both a and stale entry z.
	if !hasCode(f, lockWith("a", "z"), true, CodeStaleLockEntry) {
		t.Fatalf("expected stale-lock-entry, got %v", diagCodes(f, lockWith("a", "z"), true))
	}
}

func TestStaleLockEntryNearMissMatching(t *testing.T) {
	f := analyze(t, `{ inputs.a.url = "u"; }`)
	if hasCode(f, lockWith("a"), true, CodeStaleLockEntry) {
		t.Fatalf("unexpected stale-lock-entry when matching: %v", diagCodes(f, lockWith("a"), true))
	}
}

func TestStaleLockEntryRequiresDeclaredInputs(t *testing.T) {
	// A flake with no inputs block never gets blamed for stale lock entries.
	f := analyze(t, `{ outputs = { self }: {}; }`)
	if hasCode(f, lockWith("z"), true, CodeStaleLockEntry) {
		t.Fatalf("stale-lock-entry fired without an inputs block: %v", diagCodes(f, lockWith("z"), true))
	}
}

func TestUnusedInputFires(t *testing.T) {
	f := analyze(t, `{
  inputs.used.url = "u";
  inputs.dead.url = "v";
  outputs = { self, used }: {};
}`)
	if !hasCode(f, nil, false, CodeUnusedInput) {
		t.Fatalf("expected unused-input, got %v", diagCodes(f, nil, false))
	}
	// Only "dead" should be flagged, not "used".
	for _, d := range Diagnostics(f, nil, false) {
		if d.Code == CodeUnusedInput && d.Message != `input "dead" is never used` {
			t.Errorf("unexpected unused message: %q", d.Message)
		}
	}
}

func TestUnusedInputUsedViaFormal(t *testing.T) {
	f := analyze(t, `{
  inputs.a.url = "u";
  outputs = { self, a }: {};
}`)
	if hasCode(f, nil, false, CodeUnusedInput) {
		t.Fatalf("input used via formal flagged: %v", diagCodes(f, nil, false))
	}
}

func TestUnusedInputUsedViaFollowsTarget(t *testing.T) {
	// nixpkgs is not a formal but is used as a follows target -> not unused.
	f := analyze(t, `{
  inputs.nixpkgs.url = "u";
  inputs.hm.url = "v";
  inputs.hm.inputs.x.follows = "nixpkgs";
  outputs = { self, hm }: {};
}`)
	if hasCode(f, nil, false, CodeUnusedInput) {
		t.Fatalf("input used via follows target flagged: %v", diagCodes(f, nil, false))
	}
}

func TestUnusedInputEllipsisNeverFires(t *testing.T) {
	f := analyze(t, `{
  inputs.dead.url = "v";
  outputs = { self, ... }: {};
}`)
	if hasCode(f, nil, false, CodeUnusedInput) {
		t.Fatalf("unused-input fired with ellipsis present: %v", diagCodes(f, nil, false))
	}
}

func TestUnusedInputAtPatternNeverFires(t *testing.T) {
	f := analyze(t, `{
  inputs.dead.url = "v";
  outputs = { self } @ args: {};
}`)
	if hasCode(f, nil, false, CodeUnusedInput) {
		t.Fatalf("unused-input fired with at-pattern present: %v", diagCodes(f, nil, false))
	}
}

func TestUnusedInputSelfNeverFlagged(t *testing.T) {
	// self is not among formals here yet must never be flagged as unused.
	f := analyze(t, `{
  inputs.self.url = "u";
  inputs.a.url = "v";
  outputs = { a }: {};
}`)
	for _, d := range Diagnostics(f, nil, false) {
		if d.Code == CodeUnusedInput && d.Message == `input "self" is never used` {
			t.Fatalf("self was flagged unused")
		}
	}
}

func TestUnusedInputNoOutputsNeverFires(t *testing.T) {
	f := analyze(t, `{ inputs.dead.url = "v"; }`)
	if hasCode(f, nil, false, CodeUnusedInput) {
		t.Fatalf("unused-input fired without outputs: %v", diagCodes(f, nil, false))
	}
}

func TestDiagnosticsNilFile(t *testing.T) {
	if got := Diagnostics(nil, lockWith("a"), true); got != nil {
		t.Fatalf("nil file diagnostics = %+v, want nil", got)
	}
}
