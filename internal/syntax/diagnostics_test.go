package syntax

import "testing"

func TestDiagnosticsEmptyContent(t *testing.T) {
	analyzer := NewAnalyzer()

	if got := analyzer.Diagnostics(nil); len(got) != 0 {
		t.Fatalf("expected no diagnostics, got %v", got)
	}
}

func TestDiagnosticsUnmatchedClosingDelimiter(t *testing.T) {
	analyzer := NewAnalyzer()

	got := analyzer.Diagnostics([]byte("}"))
	if len(got) != 1 {
		t.Fatalf("expected one diagnostic, got %d", len(got))
	}
	if got[0].Message != "unmatched closing delimiter" {
		t.Fatalf("unexpected message: %q", got[0].Message)
	}
}

func TestDiagnosticsUnclosedDelimiter(t *testing.T) {
	analyzer := NewAnalyzer()

	got := analyzer.Diagnostics([]byte("{ foo = 1;"))
	if len(got) != 1 {
		t.Fatalf("expected one diagnostic, got %d", len(got))
	}
	if got[0].Range.Start != 0 {
		t.Fatalf("expected opening delimiter at byte 0, got %d", got[0].Range.Start)
	}
}
