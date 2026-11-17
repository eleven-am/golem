package golem

import "github.com/eleven-am/golem/go/internal/dataincarnation"

// DataIncarnation is the provider-owned durable identity admitted by an App.
// Applications cannot construct a valid identity from a string.
type DataIncarnation = dataincarnation.Value

// WithDataIncarnation declares reviewed durable identity storage. It is
// required for PageQuery; opening an App never creates or repairs this value.
func WithDataIncarnation(_ *Schema) {}
