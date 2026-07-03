package options

import (
	"slices"
	"testing"
)

func TestEnumValuesNullOr(t *testing.T) {
	values, allowNull, ok := EnumValues(`null or one of "yes", "without-password", "prohibit-password", "forced-commands-only", "no"`)
	if !ok {
		t.Fatal("EnumValues ok = false, want true")
	}
	if !allowNull {
		t.Error("allowNull = false, want true for a null-or enum")
	}
	want := []string{"yes", "without-password", "prohibit-password", "forced-commands-only", "no"}
	if !slices.Equal(values, want) {
		t.Errorf("values = %v, want %v", values, want)
	}
}

func TestEnumValuesPlain(t *testing.T) {
	values, allowNull, ok := EnumValues(`one of "a", "b"`)
	if !ok || allowNull {
		t.Fatalf("EnumValues = (%v, %v, %v), want values with allowNull=false", values, allowNull, ok)
	}
	if want := []string{"a", "b"}; !slices.Equal(values, want) {
		t.Errorf("values = %v, want %v", values, want)
	}
}

// TestEnumValuesNonString covers the conservative rejections: an integer enum, a
// mixed list, a non-enum type, and a trailing-junk shape all yield ok=false so no
// value reasoning is grounded in a set the parser could not fully read.
func TestEnumValuesNonString(t *testing.T) {
	for _, typ := range []string{
		`one of 1, 2, 3`,
		`one of "a", 2`,
		`one of "a", "b" or list of string`,
		`string`,
		`null or string`,
		`boolean`,
		`one of `,
		`one of "unterminated`,
	} {
		if v, _, ok := EnumValues(typ); ok {
			t.Errorf("EnumValues(%q) ok = true (values %v), want false", typ, v)
		}
	}
}
