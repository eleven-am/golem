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
// a worker carries seven names and no storage. The physical format stays at
// version 3, no SystemObjectKind is introduced, and the store creates the
// objects itself before its first claim. A store creates the operator-history
// indexes only when its own application's allowlist admits them.
func QueueUnmanagedObjects() []UnmanagedObject {
	return []UnmanagedObject{
		{Kind: "table", Name: "golem_queue"},
		{Kind: "index", Name: "golem_queue_claim"},
		{Kind: "index", Name: "golem_queue_dedupe"},
		{Kind: "index", Name: "golem_queue_exclusive"},
		{Kind: "index", Name: QueueEnqueuedIndex},
		{Kind: "index", Name: QueueHistoryIndex},
		{Kind: "index", Name: QueueTerminalIndex},
	}
}

const (
	QueueEnqueuedIndex PhysicalName = "golem_queue_enqueued"
	QueueHistoryIndex  PhysicalName = "golem_queue_history"
	QueueTerminalIndex PhysicalName = "golem_queue_terminal"
)

func QueueHistoryIndexes() []UnmanagedObject {
	return []UnmanagedObject{
		{Kind: "index", Name: QueueEnqueuedIndex},
		{Kind: "index", Name: QueueHistoryIndex},
		{Kind: "index", Name: QueueTerminalIndex},
	}
}

func QueueHistoryAdmitted(unmanaged []UnmanagedObject) bool {
	admitted := make(map[UnmanagedObject]bool, len(unmanaged))
	for _, object := range unmanaged {
		admitted[object] = true
	}
	for _, object := range QueueHistoryIndexes() {
		if !admitted[object] {
			return false
		}
	}
	return true
}

// Lowerer is the provider boundary consumed by the shared compiler. SQLite and
// PostgreSQL implementations own scalar/check/index lowering and capability
// requirements; migration planning consumes only their normalized result.
type Lowerer interface {
	Manifest() ProviderManifest
	Lower(context.Context, ir.ModelIR, LowerOptions) (PhysicalSchema, error)
}
