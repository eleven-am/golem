package bind

import (
	"errors"

	compileerr "github.com/eleven-am/golem/go/internal/bindingerr"
)

func isKind(err error, kind compileerr.Kind) bool {
	var typed *compileerr.Error
	return errors.As(err, &typed) && typed.Kind() == kind
}
