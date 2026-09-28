package drift

import (
	"errors"
	"fmt"
)

// Object identifies the catalog object that failed schema verification.
type Object struct {
	Type  string
	Name  string
	Table string
}

type objectError struct {
	object Object
	detail string
}

func (value objectError) Error() string {
	return value.detail
}

// New returns a schema-drift error carrying safe catalog identity.
func New(object Object, format string, arguments ...any) error {
	return objectError{object: object, detail: fmt.Sprintf(format, arguments...)}
}

// Inspect returns catalog identity attached to a schema-drift error.
func Inspect(err error) (Object, bool) {
	var target objectError
	if !errors.As(err, &target) {
		return Object{}, false
	}
	return target.object, true
}
