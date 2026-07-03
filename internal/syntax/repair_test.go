package syntax

import (
	"context"
	"strings"
	"testing"
)

// sexp returns the tree-sitter S-expression of src, used to pin the raw recovery
// shape a broken input parses to BEFORE asserting how the fixer rewrites it. If a
// grammar bump reshapes recovery, the pinned string changes and the test flags it
// before the fixer's assumptions silently rot.
func sexp(t *testing.T, src string) string {
	t.Helper()
	tree, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return tree.tree.RootNode().String()
}

// repairCase pins, for one broken input, both its raw recovery S-expression and
// the source the fixer must produce.
type repairCase struct {
	name      string
	src       string
	wantSexp  string
	wantFixed string
}

var repairCases = []repairCase{
	{
		name:      "simple-unterminated-binding",
		src:       "{ x = 1 }",
		wantSexp:  `(source_code expression: (attrset_expression (binding_set binding: (binding attrpath: (attrpath attr: (identifier)) expression: (integer_expression) (MISSING ";")))))`,
		wantFixed: "{ x = 1; }",
	},
	{
		name:      "nested-attrset-missing-inner-semicolon",
		src:       "{ config, ... }:\n{\n  networking.wireguard.interfaces = {\n    wg0 = {\n      \n    }\n  };\n}\n",
		wantSexp:  `(source_code expression: (function_expression formals: (formals formal: (formal name: (identifier)) ellipses: (ellipses)) body: (attrset_expression (binding_set binding: (binding attrpath: (attrpath attr: (identifier) attr: (identifier) attr: (identifier)) expression: (attrset_expression (binding_set binding: (binding attrpath: (attrpath attr: (identifier)) expression: (attrset_expression) (MISSING ";")))))))))`,
		wantFixed: "{ config, ... }:\n{\n  networking.wireguard.interfaces = {\n    wg0 = {\n      \n    };\n  };\n}\n",
	},
	{
		name:      "swallowed-following-binding",
		src:       "{ a = 1\n  b = 2; }",
		wantSexp:  `(source_code expression: (attrset_expression (binding_set binding: (binding attrpath: (attrpath attr: (identifier)) expression: (apply_expression function: (apply_expression function: (integer_expression) argument: (variable_expression name: (identifier))) (ERROR) argument: (integer_expression))))))`,
		wantFixed: "{ a = 1;\n  b = 2; }",
	},
}

func TestRepairParseFixesMissingSemicolon(t *testing.T) {
	for _, tc := range repairCases {
		t.Run(tc.name, func(t *testing.T) {
			// Pin the raw recovery shape first (dump-before-code).
			if got := sexp(t, tc.src); got != tc.wantSexp {
				t.Fatalf("recovery S-expression drifted:\n got: %s\nwant: %s", got, tc.wantSexp)
			}

			res, err := RepairParse(context.Background(), []byte(tc.src))
			if err != nil {
				t.Fatalf("RepairParse: %v", err)
			}
			if !res.Repaired {
				t.Fatalf("Repaired = false, want true for %q", tc.src)
			}
			// (a) the repaired tree has no errors.
			if res.Tree.Root().HasError() {
				t.Errorf("repaired tree still has errors\nsexp: %s", res.Tree.tree.RootNode().String())
			}
			// (b) the repaired source equals the expected fixed source.
			if got := string(res.Tree.Content()); got != tc.wantFixed {
				t.Errorf("repaired source = %q, want %q", got, tc.wantFixed)
			}
			// (e) repair changed nothing but inserted ";" bytes.
			assertOnlySemicolonInsertions(t, tc.src, string(res.Tree.Content()))
		})
	}
}

func TestRepairParseCleanInputIsUntouched(t *testing.T) {
	src := "{ x = 1; y = 2; }"
	res, err := RepairParse(context.Background(), []byte(src))
	if err != nil {
		t.Fatalf("RepairParse: %v", err)
	}
	if res.Repaired {
		t.Errorf("Repaired = true for clean input")
	}
	if len(res.Edits) != 0 {
		t.Errorf("Edits = %v, want none for clean input", res.Edits)
	}
	if got := string(res.Tree.Content()); got != src {
		t.Errorf("content = %q, want unchanged %q", got, src)
	}
	if res.Tree.Root().HasError() {
		t.Errorf("clean input reported as having errors")
	}
}

func TestRepairParseUnrepairableInputKeepsOriginal(t *testing.T) {
	// A lone attribute / unterminated string the missing-';' fixer does not touch:
	// it must return the ORIGINAL tree with Repaired=false, never invent an edit.
	for _, src := range []string{
		`{ "unterminated`,
		`{ name }`,
		`{ x = { y = 1 } }`, // whole-attrset ERROR blob, not a missing-';' shape
	} {
		res, err := RepairParse(context.Background(), []byte(src))
		if err != nil {
			t.Fatalf("RepairParse(%q): %v", src, err)
		}
		if res.Repaired {
			t.Errorf("Repaired = true for unrepairable %q", src)
		}
		if len(res.Edits) != 0 {
			t.Errorf("Edits = %v for unrepairable %q, want none", res.Edits, src)
		}
		if got := string(res.Tree.Content()); got != src {
			t.Errorf("content = %q, want original %q", got, src)
		}
		if !res.Tree.Root().HasError() {
			t.Errorf("original tree for %q lost its error", src)
		}
	}
}

func TestRepairParseMultipleSemicolonsAndMapping(t *testing.T) {
	// Two missing ';' in a row: the loop fixes both, and the edit list maps a
	// position past both insertions correctly (nontrivial mapping case (c)).
	src := "{ x = 1\n  y = 2\n  z = 3; }"
	res, err := RepairParse(context.Background(), []byte(src))
	if err != nil {
		t.Fatalf("RepairParse: %v", err)
	}
	if !res.Repaired {
		t.Fatal("Repaired = false, want true")
	}
	if res.Tree.Root().HasError() {
		t.Fatalf("repaired tree still has errors\nsexp: %s", res.Tree.tree.RootNode().String())
	}
	want := "{ x = 1;\n  y = 2;\n  z = 3; }"
	if got := string(res.Tree.Content()); got != want {
		t.Fatalf("repaired source = %q, want %q", got, want)
	}
	if len(res.Edits) != 2 {
		t.Fatalf("Edits = %v, want 2 insertions", res.Edits)
	}
	assertOnlySemicolonInsertions(t, src, string(res.Tree.Content()))

	// Original offset 0 is before every insertion: unchanged.
	if got := res.MapOffset(0); got != 0 {
		t.Errorf("MapOffset(0) = %d, want 0", got)
	}
	// The two ';' are inserted at original offsets 7 (end of "1") and 15 (end of
	// "2"). A position after both shifts right by 2.
	origEndOfThree := strings.Index(src, "3") + 1 // offset just past the "3"
	if got := res.MapOffset(origEndOfThree); got != origEndOfThree+2 {
		t.Errorf("MapOffset(%d) = %d, want %d", origEndOfThree, got, origEndOfThree+2)
	}
	// A position between the two insertions shifts by exactly one.
	origEndOfTwo := strings.Index(src, "2") + 1
	if got := res.MapOffset(origEndOfTwo); got != origEndOfTwo+1 {
		t.Errorf("MapOffset(%d) = %d, want %d", origEndOfTwo, got, origEndOfTwo+1)
	}
	// The mapped position round-trips through the repaired content and lands on
	// the same character it named in the original.
	origPos := PositionAt([]byte(src), origEndOfThree)
	mapped := res.MapPosition(origPos)
	if got := byteOffsetAt(res.Tree.content, mapped); got != origEndOfThree+2 {
		t.Errorf("MapPosition round-trip byte = %d, want %d", got, origEndOfThree+2)
	}
}

func TestRepairParseBounded(t *testing.T) {
	// A deep pile of unterminated bindings settles well within the bound; this
	// guards the loop terminates and does not exceed maxRepairIterations edits.
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 5; i++ {
		b.WriteString(" a = 1")
	}
	b.WriteString(" }")
	res, err := RepairParse(context.Background(), []byte(b.String()))
	if err != nil {
		t.Fatalf("RepairParse: %v", err)
	}
	if len(res.Edits) > maxRepairIterations {
		t.Errorf("emitted %d edits, exceeds bound %d", len(res.Edits), maxRepairIterations)
	}
	assertOnlySemicolonInsertions(t, b.String(), string(res.Tree.Content()))
}

// assertOnlySemicolonInsertions verifies fixed is original with only ";" bytes
// inserted: deleting every ";" that is not already accounted for must recover the
// original. It checks the invariant directly by walking both strings and allowing
// the fixed side to get ahead only on ';' characters.
func assertOnlySemicolonInsertions(t *testing.T, original, fixed string) {
	t.Helper()
	oi := 0
	for fi := 0; fi < len(fixed); fi++ {
		if oi < len(original) && fixed[fi] == original[oi] {
			oi++
			continue
		}
		if fixed[fi] == ';' {
			continue // an inserted semicolon
		}
		t.Fatalf("repair changed a non-';' byte at fixed[%d]=%q; original=%q fixed=%q", fi, fixed[fi], original, fixed)
	}
	if oi != len(original) {
		t.Fatalf("repair dropped original bytes (consumed %d of %d); original=%q fixed=%q", oi, len(original), original, fixed)
	}
}
