package bind

import (
	"errors"

	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
)

func isKind(err error, kind compileerr.Kind) bool {
	var typed *compileerr.Error
	return errors.As(err, &typed) && typed.Kind() == kind
}
