package syntax

import (
	"context"
	"unicode/utf16"
	"unicode/utf8"
)

// repair.go implements a bounded source-repair loop, the fixSrc half of gopls's
// fixAST/fixSrc idea. tree-sitter trees are immutable, so a repair cannot edit a
// tree in place; instead it rewrites the source bytes at a recovery anchor,
// re-parses, and repeats until the tree is clean, no fixer applies, or a small
// iteration bound is hit. Each full re-parse of a small Nix file is cheap and the
// result is memoized once (see facts.RepairedParseTree), so the loop costs at
// most a handful of parses and only on a file that already fails to parse.
//
// THE INVARIANT: repair NEVER feeds diagnostics. Diagnostics always come from the
// ORIGINAL tree (see Tree.Diagnostics and every server publish path). A repaired
// tree exists only for downstream consumers that must walk a well-formed tree —
// completion/hover context, option-path enrichment, later dataset analysis — and
// that would otherwise trip over error-recovery reshapes. A repair inserts only
// the tokens a human clearly omitted (today: a binding's missing ';'); it must
// never move, invent, or suppress an error that the user would see, because the
// errors the user sees are computed elsewhere, on the untouched original tree.
//
// The only edit the current fixer emits is inserting ";". That keeps the repaired
// source a byte-for-byte copy of the original except for inserted ';' bytes, which
// makes position mapping between original and repaired coordinates a single
// monotonic pass (see RepairResult.MapOffset): a ';' is one ASCII byte and never a
// newline, so it never changes line numbers and shifts columns only to its right.

// maxRepairIterations bounds the fixSrc loop. Each iteration fixes one recovery
// site and re-parses; the bound caps total parses so a pathological input that
// the fixer keeps "almost" repairing can never spin. Real broken buffers settle
// in one or two iterations (one insertion per missing ';').
const maxRepairIterations = 10

// RepairEdit records one edit the repair loop applied: an insertion of Text at
// byte Offset in the ORIGINAL content's coordinates. Storing every offset in
// original coordinates (never in the intermediate repaired coordinates) is what
// lets a consumer map a position with one ascending pass no matter how many edits
// accumulated. The current fixer only ever inserts ";".
type RepairEdit struct {
	Offset int
	Text   string
}

// RepairResult is the outcome of RepairParse. Tree is the final parse tree: the
// repaired tree when Repaired is true, otherwise the tree of the unchanged
// Original content. Original is the exact bytes repair started from (a private
// copy). Edits lists every insertion applied, ascending by Offset, and is empty
// when Repaired is false. A consumer walks Tree and maps any position expressed
// in Original coordinates through MapOffset/MapPosition.
type RepairResult struct {
	Tree     *Tree
	Repaired bool
	Original []byte
	Edits    []RepairEdit
}

// UnrepairedResult wraps an already-clean (or deliberately un-repaired) tree as a
// RepairResult with Repaired=false and no edits. It is the zero-cost happy-path
// constructor: a caller that already holds a clean parse tree avoids re-parsing.
func UnrepairedResult(tree *Tree) *RepairResult {
	return &RepairResult{Tree: tree, Repaired: false, Original: tree.Content()}
}

// RepairParse parses content and, while the tree has errors, repeatedly locates
// the first repairable recovery site, inserts the token it needs, and re-parses,
// up to maxRepairIterations times. It stops when the tree is clean, when no fixer
// applies to the remaining errors, or at the iteration bound. The returned
// RepairResult always carries a usable Tree (the best parse reached); Repaired
// reports whether any edit was applied. It returns an error only when a parse
// itself fails (e.g. ctx cancellation), never for an unrepairable input — an
// input the fixer cannot help yields Repaired=false with the original tree.
func RepairParse(ctx context.Context, content []byte) (*RepairResult, error) {
	original := cloneBytes(content)

	tree, err := ParseCtx(ctx, original)
	if err != nil {
		return nil, err
	}
	if !tree.Root().HasError() {
		return &RepairResult{Tree: tree, Repaired: false, Original: original}, nil
	}

	var edits []RepairEdit
	current := tree
	for i := 0; i < maxRepairIterations; i++ {
		anchorCur, ok := firstMissingSemicolonOffset(current)
		if !ok {
			break
		}
		anchorOrig := currentToOriginal(anchorCur, edits)
		edits = insertEditSorted(edits, RepairEdit{Offset: anchorOrig, Text: ";"})

		repairedSrc := applyEdits(original, edits)
		next, err := ParseCtx(ctx, repairedSrc)
		if err != nil {
			return nil, err
		}
		current = next
		if !current.Root().HasError() {
			break
		}
	}

	if len(edits) == 0 {
		// Nothing the fixer understands: hand back the original tree untouched so
		// consumers can still walk it, and report no repair.
		return &RepairResult{Tree: tree, Repaired: false, Original: original}, nil
	}
	return &RepairResult{Tree: current, Repaired: true, Original: original, Edits: edits}, nil
}

// firstMissingSemicolonOffset returns the earliest byte offset in tree's content
// where a ';' should be inserted to fix a missing-semicolon recovery, or ok=false
// when the tree has no such site. It reuses the ORIGINAL tree's own classifier
// anchors (Tree.Diagnostics already computes them for all four recovery shapes)
// rather than re-deriving the shapes: any diagnostic whose message is the
// missing-';' hint carries, as its zero-width range start, exactly the offset the
// ';' belongs at. Picking the smallest offset makes the loop fix errors front to
// back deterministically.
func firstMissingSemicolonOffset(tree *Tree) (int, bool) {
	content := tree.content
	best := -1
	for _, d := range tree.Diagnostics() {
		if !isMissingSemicolonDiagnostic(d) {
			continue
		}
		off := byteOffsetAt(content, d.Range.Start)
		if best == -1 || off < best {
			best = off
		}
	}
	if best == -1 {
		return 0, false
	}
	return best, true
}

// isMissingSemicolonDiagnostic reports whether d is one of the missing-';'
// classifications (the shared unnamed hint or the "before '<name>'" variant),
// the only diagnostics whose anchor doubles as a valid ';' insertion point. It
// keys on the message constants this package already emits, so the fixer and the
// classifier can never drift apart.
func isMissingSemicolonDiagnostic(d Diagnostic) bool {
	if d.Code != "syntax-error" {
		return false
	}
	return d.Message == missingSemicolonMessage || hasMissingSemicolonPrefix(d.Message)
}

// missingSemicolonPrefix is the shared lead of every missing-';' message
// (missingSemicolonMessage and the "…before '<name>'" variant both begin with
// it), so one prefix test recognizes the whole family.
const missingSemicolonPrefix = "syntax error: missing ';'"

func hasMissingSemicolonPrefix(message string) bool {
	return len(message) >= len(missingSemicolonPrefix) && message[:len(missingSemicolonPrefix)] == missingSemicolonPrefix
}

// applyEdits returns original with every edit inserted at its Offset. edits must
// be ascending by Offset. Reconstructing the repaired source from the original
// plus the whole edit list each iteration (rather than splicing incrementally)
// keeps every stored Offset in original coordinates and sidesteps cumulative
// offset bookkeeping.
func applyEdits(original []byte, edits []RepairEdit) []byte {
	out := make([]byte, 0, len(original)+len(edits))
	prev := 0
	for _, e := range edits {
		out = append(out, original[prev:e.Offset]...)
		out = append(out, e.Text...)
		prev = e.Offset
	}
	out = append(out, original[prev:]...)
	return out
}

// insertEditSorted returns edits with e inserted so the slice stays ascending by
// Offset. Ties keep the existing edit before the new one (a stable insert),
// which is irrelevant for correctness since edits are pure insertions.
func insertEditSorted(edits []RepairEdit, e RepairEdit) []RepairEdit {
	idx := len(edits)
	for i, existing := range edits {
		if e.Offset < existing.Offset {
			idx = i
			break
		}
	}
	edits = append(edits, RepairEdit{})
	copy(edits[idx+1:], edits[idx:])
	edits[idx] = e
	return edits
}

// currentToOriginal maps an offset in the current (repaired-so-far) content back
// to original coordinates. Every prior edit is an insertion of Text at its
// original Offset, occupying current positions [Offset+shift, Offset+shift+len)
// where shift is the total length of earlier insertions. A current offset at or
// past an inserted span subtracts that span's length; an offset that lands inside
// an inserted span (never happens for a fresh anchor, but handled defensively)
// maps to that edit's original Offset. edits must be ascending by Offset.
func currentToOriginal(cur int, edits []RepairEdit) int {
	orig := cur
	shift := 0
	for _, e := range edits {
		curStart := e.Offset + shift
		switch {
		case cur >= curStart+len(e.Text):
			orig -= len(e.Text)
		case cur >= curStart:
			return e.Offset
		default:
			return orig
		}
		shift += len(e.Text)
	}
	return orig
}

// MapOffset maps a byte offset in Original coordinates to the corresponding byte
// offset in the repaired content. Because every edit is an insertion, an original
// offset shifts right by the total length of insertions strictly before it; an
// insertion exactly at the offset does not move it, keeping a zero-width anchor
// attached to the value that precedes the inserted ';'. When Repaired is false
// this is the identity.
func (r *RepairResult) MapOffset(orig int) int {
	if r == nil {
		return orig
	}
	off := orig
	for _, e := range r.Edits {
		if e.Offset < orig {
			off += len(e.Text)
			continue
		}
		break
	}
	return off
}

// MapPosition maps an LSP position in Original coordinates to the corresponding
// position in the repaired content. It converts through byte offsets, so it is
// exact for multi-byte and astral-plane text. When Repaired is false it is the
// identity.
func (r *RepairResult) MapPosition(orig Position) Position {
	if r == nil || !r.Repaired {
		return orig
	}
	origOff := byteOffsetAt(r.Original, orig)
	repairedOff := r.MapOffset(origOff)
	return PositionAt(r.Tree.content, repairedOff)
}

// byteOffsetAt converts an LSP position (line, UTF-16 column) into a UTF-8 byte
// offset in content, the inverse of PositionAt. A position past the end of its
// line clamps to the line's newline; a position past the end of content clamps to
// len(content). It counts columns in UTF-16 code units to match PositionAt.
func byteOffsetAt(content []byte, pos Position) int {
	line, col := 0, 0
	for i := 0; i < len(content); {
		if line == pos.Line && col >= pos.Character {
			return i
		}
		r, size := utf8.DecodeRune(content[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if r == '\n' {
			if line == pos.Line {
				return i
			}
			line++
			col = 0
			i += size
			continue
		}
		col += len(utf16.Encode([]rune{r}))
		i += size
	}
	return len(content)
}
