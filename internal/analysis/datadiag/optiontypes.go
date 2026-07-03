package datadiag

import (
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
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
		want, allowNull, ok := optionExpectedKind(doc.Type)
		if !ok {
			// A type this check does not map to a single literal kind: stay silent.
			continue
		}
		got, ok := literalValueKind(b.value)
		if !ok {
			// Not a judgeable literal (reference, call, interpolated string, ...): skip.
			continue
		}
		if got == valueNull && allowNull {
			continue
		}
		if got == want {
			continue
		}
		out = append(out, Diagnostic{
			Diagnostic: syntax.Diagnostic{
				Message:  "type mismatch: " + strings.Join(b.segs, ".") + " expects " + want.word() + ", got " + got.word(),
				Range:    b.value.Range(),
				Code:     CodeOptionTypeMismatch,
				Severity: syntax.SeverityWarning,
			},
		})
	}
	sortByRange(out)
	return out
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
