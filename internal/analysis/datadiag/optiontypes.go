package datadiag

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// enumMessageLimit is the largest number of enum values named in full in a
// mismatch message; a longer list shows enumMessageHead values plus ", ...".
const (
	enumMessageLimit = 6
	enumMessageHead  = 5
)

// CodeOptionTypeMismatch marks a documented option whose bound value is a literal
// of a kind that cannot match the option's declared type.
const CodeOptionTypeMismatch = "option-type-mismatch"

// OptionTypeDiagnostics reports module option bindings whose value is a literal
// of a kind that clearly cannot match the option's documented type (a string
// where a boolean is expected, an integer where a string is expected, and so on).
// It runs behind the same module gate as OptionDiagnostics and judges only
// unambiguous literals: any value that is a reference, a function call
// (including lib.mkIf / mkForce / mkDefault), a select, a `with`, a `let`, or an
// interpolation-bearing string is left alone, so the check never second-guesses a
// computed value. Types it cannot map to a single literal kind (enums, packages,
// paths, `either`, coercions) are skipped. A nil tree or index yields none.
func OptionTypeDiagnostics(tree *syntax.Tree, index *options.Index) []Diagnostic {
	if tree == nil || index == nil {
		return nil
	}
	bindings, gated := gatherModuleBindings(tree, index)
	if !gated {
		return nil
	}

	var out []Diagnostic
	for _, b := range bindings {
		doc, ok := index.Lookup(b.segs)
		if !ok || doc == nil {
			// Not a documented leaf (a group prefix, a wildcard placeholder with no
			// doc, or an unknown path): the type check has nothing to compare against.
			continue
		}
		// Three checks in precedence order, each judging at most one binding. Their
		// type domains are disjoint (a coarse kind, a string enum, a constrained
		// string), so at most one ever fires, but the ordering keeps a kind
		// mismatch (string where an integer belongs) ahead of a value check that
		// would never see a non-string literal anyway.
		if d, ok := kindMismatchDiagnostic(doc, b); ok {
			out = append(out, d)
			continue
		}
		if d, ok := enumValueDiagnostic(doc, b); ok {
			out = append(out, d)
			continue
		}
		if d, ok := stringConstraintDiagnostic(doc, b); ok {
			out = append(out, d)
			continue
		}
	}
	sortByRange(out)
	return out
}

// kindMismatchDiagnostic reports a binding whose literal value is of a coarse
// kind (boolean, integer, list, ...) that cannot match the option's documented
// type. It is the original type check, factored out so the enum and pattern
// checks below can share the same per-binding loop. ok is false when the type
// maps to no single kind, the value is not a judgeable literal, or the kinds
// agree (including a null literal under a nullable type).
func kindMismatchDiagnostic(doc *options.Doc, b moduleBinding) (Diagnostic, bool) {
	want, allowNull, ok := optionExpectedKind(doc.Type)
	if !ok {
		return Diagnostic{}, false
	}
	got, ok := literalValueKind(b.value)
	if !ok {
		return Diagnostic{}, false
	}
	if got == valueNull && allowNull {
		return Diagnostic{}, false
	}
	if got == want {
		return Diagnostic{}, false
	}
	return Diagnostic{
		Diagnostic: syntax.Diagnostic{
			Message:  "type mismatch: " + strings.Join(b.segs, ".") + " expects " + want.word() + ", got " + got.word(),
			Range:    b.value.Range(),
			Code:     CodeOptionTypeMismatch,
			Severity: syntax.SeverityWarning,
		},
	}, true
}

// enumValueDiagnostic reports a binding whose plain string literal is not one of
// the legal values of an enum-typed option (`one of "a", "b", ...`, optionally
// nullable). Only a documented string enum is judged; a non-string enum, a
// non-string type, or a value that is not a plain (interpolation-free) string
// literal is left alone. When the wrong literal sits within maxDistance edits of
// a legal value, that value (quotes included) is offered as a did-you-mean
// replacement for the flagged range, wired through datasetCodeActions exactly
// like the unknown-option fix.
func enumValueDiagnostic(doc *options.Doc, b moduleBinding) (Diagnostic, bool) {
	values, _, ok := options.EnumValues(doc.Type)
	if !ok {
		return Diagnostic{}, false
	}
	content, r, ok := plainStringLiteral(b.value)
	if !ok {
		// A null literal, a reference, an interpolated string, ...: a nullable enum
		// accepts null (judged as a kind, not here), and everything dynamic is
		// deliberately never second-guessed.
		return Diagnostic{}, false
	}
	if slices.Contains(values, content) {
		return Diagnostic{}, false
	}

	var suggestions []string
	for _, v := range values {
		if levenshtein(content, v) <= maxDistance {
			suggestions = append(suggestions, strconv.Quote(v))
			if len(suggestions) == maxSuggestions {
				break
			}
		}
	}
	return Diagnostic{
		Diagnostic: syntax.Diagnostic{
			Message:  "type mismatch: " + strings.Join(b.segs, ".") + " expects " + describeEnum(values) + "; got " + strconv.Quote(content),
			Range:    r,
			Code:     CodeOptionTypeMismatch,
			Severity: syntax.SeverityWarning,
		},
		Suggestions: suggestions,
	}, true
}

// stringConstraintDiagnostic reports a plain string literal that violates a
// lightweight string-shape constraint carried in the option type: a `string
// without spaces` that contains a space or tab, or a `string matching the pattern
// <regex>` whose value does not match that regex anchored end to end. A `string
// (with check: ...)` predicate is opaque and skipped, a regex Go cannot compile
// is skipped, and an interpolated value is never a plain literal so never judged.
// These constraints carry no did-you-mean fix, so the diagnostic has no
// suggestions.
func stringConstraintDiagnostic(doc *options.Doc, b moduleBinding) (Diagnostic, bool) {
	content, r, ok := plainStringLiteral(b.value)
	if !ok {
		return Diagnostic{}, false
	}
	t := strings.TrimSpace(doc.Type)
	if rest, found := strings.CutPrefix(t, "null or "); found {
		t = strings.TrimSpace(rest)
	}
	if strings.HasPrefix(t, "string (with check:") {
		// An arbitrary predicate the type only names, not describes: unjudgeable.
		return Diagnostic{}, false
	}
	if strings.Contains(t, "string without spaces") {
		if strings.ContainsAny(content, " \t") {
			return stringConstraintDiag(b.segs, r, "expects a string without spaces"), true
		}
		return Diagnostic{}, false
	}
	if pat, found := strings.CutPrefix(t, "string matching the pattern "); found {
		pat = strings.TrimSpace(pat)
		// Anchor the pattern end to end, grouping so a top-level alternation binds
		// under the anchors (^(?:a|b)$, not ^a|b$). A pattern Go's regexp cannot
		// compile (a PCRE-only construct) yields no diagnostic rather than a wrong
		// one.
		re, err := regexp.Compile("^(?:" + pat + ")$")
		if err != nil {
			return Diagnostic{}, false
		}
		if !re.MatchString(content) {
			return stringConstraintDiag(b.segs, r, "does not match the expected pattern "+pat), true
		}
	}
	return Diagnostic{}, false
}

// stringConstraintDiag builds a suggestion-less string-constraint warning naming
// the option path and the given expectation phrase.
func stringConstraintDiag(segs []string, r syntax.Range, phrase string) Diagnostic {
	return Diagnostic{
		Diagnostic: syntax.Diagnostic{
			Message:  "type mismatch: " + strings.Join(segs, ".") + " " + phrase,
			Range:    r,
			Code:     CodeOptionTypeMismatch,
			Severity: syntax.SeverityWarning,
		},
	}
}

// describeEnum renders the legal-value phrase of an enum mismatch message:
// `one of "a", "b", ...`. Up to enumMessageLimit values are named in full; a
// longer list is truncated to enumMessageHead values followed by ", ..." so a
// wide enum does not bloat the message.
func describeEnum(values []string) string {
	show := values
	truncated := false
	if len(values) > enumMessageLimit {
		show = values[:enumMessageHead]
		truncated = true
	}
	quoted := make([]string, 0, len(show))
	for _, v := range show {
		quoted = append(quoted, strconv.Quote(v))
	}
	out := "one of " + strings.Join(quoted, ", ")
	if truncated {
		out += ", ..."
	}
	return out
}

// plainStringLiteral returns the unquoted content and full source range of a
// value that is a plain double-quoted string literal with no interpolation or
// escape, unwrapping a single enclosing pair of parentheses. ok is false for an
// indented string, an interpolated string, a string carrying an escape sequence,
// or any non-string value, so the enum and pattern checks only ever judge a
// literal they can read verbatim.
func plainStringLiteral(node syntax.Node) (string, syntax.Range, bool) {
	node = unwrapParensOnce(node)
	if node.Kind() != "string_expression" {
		return "", syntax.Range{}, false
	}
	if hasInterpolation(node) {
		return "", syntax.Range{}, false
	}
	var b strings.Builder
	for _, child := range node.NamedChildren() {
		if child.Kind() != "string_fragment" {
			// An escape_sequence (or anything but a plain fragment): not verbatim.
			return "", syntax.Range{}, false
		}
		b.WriteString(child.Text())
	}
	return b.String(), node.Range(), true
}

// valueKind is the coarse literal kind the type check judges a value or expects
// an option to hold.
type valueKind int

const (
	valueBool valueKind = iota
	valueInt
	valueString
	valueList
	valueAttrset
	valueNull
)

// word is the human-facing name of a kind used in the mismatch message.
func (k valueKind) word() string {
	switch k {
	case valueBool:
		return "boolean"
	case valueInt:
		return "integer"
	case valueString:
		return "string"
	case valueList:
		return "list"
	case valueAttrset:
		return "attribute set"
	case valueNull:
		return "null"
	}
	return ""
}

// optionExpectedKind maps a documented option type string to the single literal
// kind its value must be, honoring only the deliberately small, unambiguous set of
// type prefixes. allowNull is set when a leading "null or " was stripped, meaning
// a `null` literal is also acceptable. ok is false for any type this check cannot
// pin to one literal kind (enums, packages, paths, `either`, coercions, unknown),
// which the caller then skips. The `starts with` cases are tested before the
// "integer before the first ';'" fallback so "list of ... integer; ..." reads as a
// list, not an integer.
func optionExpectedKind(typeStr string) (kind valueKind, allowNull bool, ok bool) {
	t := strings.TrimSpace(typeStr)
	if rest, found := strings.CutPrefix(t, "null or "); found {
		allowNull = true
		t = strings.TrimSpace(rest)
	}
	switch {
	case strings.HasPrefix(t, "boolean"):
		return valueBool, allowNull, true
	case strings.HasPrefix(t, "list of"):
		return valueList, allowNull, true
	case strings.HasPrefix(t, "attribute set"),
		strings.HasPrefix(t, "submodule"),
		strings.HasPrefix(t, "open submodule"):
		return valueAttrset, allowNull, true
	case strings.HasPrefix(t, "string"),
		strings.HasPrefix(t, "(optionally newline-terminated)"),
		strings.HasPrefix(t, "single-line string"):
		return valueString, allowNull, true
	case strings.HasPrefix(t, "signed integer"),
		strings.HasPrefix(t, "unsigned integer"),
		integerBeforeSemicolon(t):
		return valueInt, allowNull, true
	}
	return 0, false, false
}

// integerBeforeSemicolon reports whether "integer" appears in the type text ahead
// of the first ';', the shape of range-annotated integer types like
// "16 bit unsigned integer; between 0 and 65535 (both inclusive)".
func integerBeforeSemicolon(t string) bool {
	head := t
	if i := strings.IndexByte(t, ';'); i >= 0 {
		head = t[:i]
	}
	return strings.Contains(head, "integer")
}

// literalValueKind judges the kind of a bound value, but only when it is an
// unambiguous literal; ok is false for every other value shape (identifier
// reference, function application, select, `with`, `let`, interpolation-bearing
// string), which the caller then leaves alone. A single enclosing pair of
// parentheses is unwrapped and the inner value re-judged.
func literalValueKind(node syntax.Node) (valueKind, bool) {
	node = unwrapParensOnce(node)
	switch node.Kind() {
	case "string_expression", "indented_string_expression":
		if hasInterpolation(node) {
			// A string that splices in a value is dynamic: never judged.
			return 0, false
		}
		return valueString, true
	case "integer_expression":
		return valueInt, true
	case "unary_expression":
		// A negated integer (-5) is still an integer literal; a logical not (!x) is
		// not a literal at all.
		if node.ChildByFieldName("operator").Text() == "-" &&
			node.ChildByFieldName("argument").Kind() == "integer_expression" {
			return valueInt, true
		}
		return 0, false
	case "list_expression":
		return valueList, true
	case "attrset_expression", "rec_attrset_expression":
		return valueAttrset, true
	case "variable_expression":
		switch node.Text() {
		case "true", "false":
			return valueBool, true
		case "null":
			return valueNull, true
		}
		return 0, false
	}
	return 0, false
}

// unwrapParensOnce returns the inner expression of a single parenthesized
// expression, or node unchanged. It unwraps exactly one level, so a doubly
// parenthesized value is conservatively left unjudged.
func unwrapParensOnce(node syntax.Node) syntax.Node {
	if node.Kind() == "parenthesized_expression" {
		if inner := node.ChildByFieldName("expression"); !inner.IsZero() {
			return inner
		}
	}
	return node
}

// hasInterpolation reports whether a string node contains a `${...}` interpolation
// child (as opposed to being made only of literal fragments).
func hasInterpolation(node syntax.Node) bool {
	for _, child := range node.NamedChildren() {
		if child.Kind() == "interpolation" {
			return true
		}
	}
	return false
}
