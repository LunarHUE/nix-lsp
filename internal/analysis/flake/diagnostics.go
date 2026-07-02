package flake

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// Diagnostic codes are stable, machine-readable identifiers for each flake
// diagnostic kind.
const (
	// CodeDanglingFollows marks a follows target whose first segment names no
	// declared input.
	CodeDanglingFollows = "dangling-follows"
	// CodeInputNotLocked marks a declared input missing from flake.lock.
	CodeInputNotLocked = "input-not-locked"
	// CodeStaleLockEntry marks a root flake.lock input with no matching input.
	CodeStaleLockEntry = "stale-lock-entry"
	// CodeUnusedInput marks a declared input never referenced by outputs.
	CodeUnusedInput = "unused-input"
)

// Diagnostics returns conservative diagnostics for a modeled flake.nix. Lock
// dependent checks run only when hasLock is true and lock is non-nil. When in
// any doubt no diagnostic is emitted.
func Diagnostics(file *File, lock *Lock, hasLock bool) []syntax.Diagnostic {
	if file == nil {
		return nil
	}

	declared := make(map[string]bool, len(file.Inputs))
	for _, in := range file.Inputs {
		declared[in.Name] = true
	}

	var diagnostics []syntax.Diagnostic
	diagnostics = append(diagnostics, danglingFollowsDiagnostics(file, declared)...)
	if hasLock && lock != nil {
		diagnostics = append(diagnostics, notLockedDiagnostics(file, lock)...)
		diagnostics = append(diagnostics, staleLockDiagnostics(file, lock, declared)...)
	}
	diagnostics = append(diagnostics, unusedInputDiagnostics(file)...)

	sort.SliceStable(diagnostics, func(i, j int) bool {
		return rangeLess(diagnostics[i].Range, diagnostics[j].Range)
	})
	return diagnostics
}

// danglingFollowsDiagnostics flags every top-level and nested follows target
// whose first slash-separated segment is not a declared input name.
func danglingFollowsDiagnostics(file *File, declared map[string]bool) []syntax.Diagnostic {
	var diagnostics []syntax.Diagnostic
	flag := func(target string, r syntax.Range) {
		seg := firstSegment(target)
		if seg == "" || declared[seg] {
			return
		}
		diagnostics = append(diagnostics, syntax.Diagnostic{
			Message:  fmt.Sprintf("follows target %q is not a declared input", target),
			Range:    r,
			Code:     CodeDanglingFollows,
			Severity: syntax.SeverityError,
		})
	}
	for _, in := range file.Inputs {
		if in.HasTopFollows {
			flag(in.TopFollows, in.TopFollowsRange)
		}
		for _, edge := range in.Follows {
			flag(edge.Target, edge.TargetRange)
		}
	}
	return diagnostics
}

// notLockedDiagnostics flags declared inputs absent from the lock's root inputs.
func notLockedDiagnostics(file *File, lock *Lock) []syntax.Diagnostic {
	rootInputs := lock.RootInputs()
	var diagnostics []syntax.Diagnostic
	for _, in := range file.Inputs {
		if _, ok := rootInputs[in.Name]; ok {
			continue
		}
		diagnostics = append(diagnostics, syntax.Diagnostic{
			Message:  fmt.Sprintf("input %q is not in flake.lock; run nix flake lock", in.Name),
			Range:    in.NameRange,
			Code:     CodeInputNotLocked,
			Severity: syntax.SeverityWarning,
		})
	}
	return diagnostics
}

// staleLockDiagnostics flags root lock inputs with no matching declared input.
// It anchors one diagnostic per stale name on the inputs range and needs the
// file to actually declare inputs so an unrelated file is never blamed.
func staleLockDiagnostics(file *File, lock *Lock, declared map[string]bool) []syntax.Diagnostic {
	if !file.HasInputs {
		return nil
	}
	var stale []string
	for name := range lock.RootInputs() {
		if !declared[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	diagnostics := make([]syntax.Diagnostic, 0, len(stale))
	for _, name := range stale {
		diagnostics = append(diagnostics, syntax.Diagnostic{
			Message:  fmt.Sprintf("flake.lock entry %q has no matching input; run nix flake lock", name),
			Range:    file.InputsRange,
			Code:     CodeStaleLockEntry,
			Severity: syntax.SeverityWarning,
		})
	}
	return diagnostics
}

// unusedInputDiagnostics flags inputs never referenced by the outputs formals.
// It runs only for a strict outputs signature (destructured formals, no `...`
// and no `@`-pattern) so a dynamically-consumed input is never wrongly flagged.
// `self` and inputs used as a follows target are exempt.
func unusedInputDiagnostics(file *File) []syntax.Diagnostic {
	out := file.Outputs
	if out == nil || !out.HasFormals || out.HasEllipsis || out.HasAtPattern {
		return nil
	}

	used := make(map[string]bool)
	for _, in := range file.Inputs {
		if in.HasTopFollows {
			if seg := firstSegment(in.TopFollows); seg != "" {
				used[seg] = true
			}
		}
		for _, edge := range in.Follows {
			if seg := firstSegment(edge.Target); seg != "" {
				used[seg] = true
			}
		}
	}

	var diagnostics []syntax.Diagnostic
	for _, in := range file.Inputs {
		if in.Name == "self" || used[in.Name] {
			continue
		}
		if _, ok := out.Formals[in.Name]; ok {
			continue
		}
		diagnostics = append(diagnostics, syntax.Diagnostic{
			Message:  fmt.Sprintf("input %q is never used", in.Name),
			Range:    in.NameRange,
			Code:     CodeUnusedInput,
			Severity: syntax.SeverityWarning,
		})
	}
	return diagnostics
}

// firstSegment returns the first slash-separated segment of a follows target.
func firstSegment(target string) string {
	if i := strings.IndexByte(target, '/'); i >= 0 {
		return target[:i]
	}
	return target
}

func rangeLess(a, b syntax.Range) bool {
	if a.Start.Line != b.Start.Line {
		return a.Start.Line < b.Start.Line
	}
	return a.Start.Character < b.Start.Character
}
