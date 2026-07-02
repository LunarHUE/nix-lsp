package syntax

/*
#cgo CFLAGS: -std=c11 -fPIC -I${SRCDIR}/../../third_party/tree-sitter-nix/src
#include "../../third_party/tree-sitter-nix/src/parser.c"
#include "../../third_party/tree-sitter-nix/src/scanner.c"
const TSLanguage *tree_sitter_nix(void);
*/
import "C"
import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

func nixLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_nix()))
}
