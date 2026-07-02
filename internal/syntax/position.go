package syntax

import (
	"unicode/utf16"
	"unicode/utf8"
)

// Position is a zero-based LSP position. Character is counted in UTF-16 code
// units, as required by the LSP specification.
type Position struct {
	Line      int
	Character int
}

// Range identifies a half-open LSP range.
type Range struct {
	Start Position
	End   Position
}

// PositionAt converts a UTF-8 byte offset into an LSP UTF-16 position.
func PositionAt(content []byte, offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}

	position := Position{}
	for i := 0; i < offset; {
		r, size := utf8.DecodeRune(content[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if r == '\n' {
			position.Line++
			position.Character = 0
			i += size
			continue
		}
		position.Character += len(utf16.Encode([]rune{r}))
		i += size
	}
	return position
}

func rangeForBytes(content []byte, start, end uint32) Range {
	return Range{
		Start: PositionAt(content, int(start)),
		End:   PositionAt(content, int(end)),
	}
}
