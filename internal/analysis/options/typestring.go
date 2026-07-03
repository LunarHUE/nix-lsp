package options

import "strings"

// typestring.go parses the free-form option type strings the NixOS options.json
// carries (types.enum, types.strMatching, ...) into the small facts other layers
// need: the legal value list of an enum. It lives in the options package because
// a type string is part of the option model, so both the datadiag value checks
// and the server's in-string value completion can share one parser without either
// importing the other (datadiag already depends on options; server depends on
// both). Like the rest of this package it is a pure function of its input.

// EnumValues parses an option type of the form `one of "a", "b", ...`, optionally
// preceded by `null or ` (types.nullOr wrapping types.enum), into its ordered
// list of legal string values. allowNull is true when a leading `null or ` was
// stripped, meaning a bare `null` is also legal. ok is false when the type is not
// a string enum at all, or when any member is not a double-quoted string literal
// (an integer enum like `one of 1, 2` or a mixed list), so a caller stays
// conservative and skips value reasoning it cannot ground in a known set.
func EnumValues(typeStr string) (values []string, allowNull bool, ok bool) {
	t := strings.TrimSpace(typeStr)
	if rest, found := strings.CutPrefix(t, "null or "); found {
		allowNull = true
		t = strings.TrimSpace(rest)
	}
	body, found := strings.CutPrefix(t, "one of ")
	if !found {
		return nil, false, false
	}
	vals, parsed := parseQuotedList(body)
	if !parsed {
		return nil, false, false
	}
	return vals, allowNull, true
}

// parseQuotedList reads a comma-separated list of double-quoted string literals
// (`"a", "b", "c"`) into its unquoted values. It reports ok=false the moment it
// meets a member that is not a `"..."` literal (a bare number or identifier), an
// unterminated quote, or trailing junk after the list, so any type richer than a
// plain string enum (`one of "a", "b" or list of ...`) is rejected rather than
// half-parsed. An empty list is not a valid enum and yields ok=false. Enum
// members from the dataset never contain an embedded double quote, so no escape
// handling is needed; a member that would need one is treated as list-ending.
func parseQuotedList(s string) (values []string, ok bool) {
	i := 0
	for {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] != '"' {
			// A non-string member (number, identifier): not a string enum.
			return nil, false
		}
		i++
		start := i
		for i < len(s) && s[i] != '"' {
			i++
		}
		if i >= len(s) {
			// An unterminated quote: malformed, bail conservatively.
			return nil, false
		}
		values = append(values, s[start:i])
		i++ // consume the closing quote
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] != ',' {
			// Anything other than a comma separator (e.g. a trailing " or ...")
			// means this is not a pure enum; reject the whole parse.
			return nil, false
		}
		i++
	}
	if len(values) == 0 {
		return nil, false
	}
	return values, true
}
