package physical

import (
	"context"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type LowerOptions struct {
	Namespace PhysicalName
}

// Lowerer is the provider boundary consumed by the shared compiler. SQLite and
// PostgreSQL implementations own scalar/check/index lowering and capability
// requirements; migration planning consumes only their normalized result.
type Lowerer interface {
	Manifest() ProviderManifest
	Lower(context.Context, ir.ModelIR, LowerOptions) (PhysicalSchema, error)
}
