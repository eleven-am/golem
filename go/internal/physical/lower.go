package physical

import (
	"context"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type LowerOptions struct {
	Namespace PhysicalName
}

// QueueUnmanagedObjects is the durable job queue's provider-owned storage. It
// is admitted to every reviewed schema unconditionally: the allowlist tolerates
// these objects rather than requiring them, so an application that never starts
// a worker carries four names and no storage. The physical format stays at
// version 3, no SystemObjectKind is introduced, and the store creates the
// objects itself before its first claim.
func QueueUnmanagedObjects() []UnmanagedObject {
	return []UnmanagedObject{
		{Kind: "table", Name: "golem_queue"},
		{Kind: "index", Name: "golem_queue_claim"},
		{Kind: "index", Name: "golem_queue_dedupe"},
		{Kind: "index", Name: "golem_queue_exclusive"},
	}
}

// Lowerer is the provider boundary consumed by the shared compiler. SQLite and
// PostgreSQL implementations own scalar/check/index lowering and capability
// requirements; migration planning consumes only their normalized result.
type Lowerer interface {
	Manifest() ProviderManifest
	Lower(context.Context, ir.ModelIR, LowerOptions) (PhysicalSchema, error)
}
