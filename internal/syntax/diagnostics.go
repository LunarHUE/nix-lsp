package syntax

// Diagnostic is the syntax package's internal diagnostic shape. The LSP layer
// is responsible for converting it to protocol-specific diagnostics.
type Diagnostic struct {
	Message string
	Range   Range
}

// Range identifies a half-open byte range in a source file.
type Range struct {
	Start int
	End   int
}

// Analyzer is the Phase 0 syntax analysis seam. It intentionally starts small
// so the tree-sitter integration can land behind this API.
type Analyzer struct{}

// NewAnalyzer creates a syntax analyzer.
func NewAnalyzer() *Analyzer {
	return &Analyzer{}
}

// Diagnostics returns syntax diagnostics for content.
//
// The starter implementation is conservative and dependency-free. Phase 0's
// parser spike will replace this with tree-sitter ERROR/MISSING node handling.
func (a *Analyzer) Diagnostics(content []byte) []Diagnostic {
	if len(content) == 0 {
		return nil
	}

	stack := make([]int, 0)
	diagnostics := make([]Diagnostic, 0)

	for i, b := range content {
		switch b {
		case '{', '[', '(':
			stack = append(stack, i)
		case '}', ']', ')':
			if len(stack) == 0 {
				diagnostics = append(diagnostics, Diagnostic{
					Message: "unmatched closing delimiter",
					Range:   Range{Start: i, End: i + 1},
				})
				continue
			}
			stack = stack[:len(stack)-1]
		}
	}

	for _, pos := range stack {
		diagnostics = append(diagnostics, Diagnostic{
			Message: "unclosed delimiter",
			Range:   Range{Start: pos, End: pos + 1},
		})
	}

	return diagnostics
}
