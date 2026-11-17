package typedvalue

import (
	compileerr "github.com/eleven-am/golem/go/internal/bindingerr"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// CheckOneOfInput is shared by coercion and prepared-input validation.
func CheckOneOfInput(definition ir.GraphQLTypeDefinitionIR, fields map[string]any) error {
	if !definition.OneOf {
		return nil
	}
	if len(fields) != 1 {
		return compileerr.New(compileerr.KindExactlyOne, compileerr.DetailNone)
	}
	for name, value := range fields {
		if value == nil {
			return compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path(name))
		}
	}
	return nil
}
