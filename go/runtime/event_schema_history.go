package runtime

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/event/schemahistory"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type eventSchemaHistory = schemahistory.History

func newEventSchemaHistory(active *schema.Registry, historical []golem.SchemaBundle) (*eventSchemaHistory, error) {
	return schemahistory.New(active, historical)
}
