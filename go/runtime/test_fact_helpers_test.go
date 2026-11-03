package runtime

import (
	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

var fallbackTestEventSchema = golem.SchemaDigest{1, 2, 3, 4}

func newRuntimeTestFact(registry *schema.Registry, event mutationfact.EventID, requirement mutationir.FactRequirement, causation mutationfact.CausationID, ordinal uint32, before, after *mutationdecode.Row) (mutationfact.Envelope, error) {
	digest := fallbackTestEventSchema
	image := after
	if image == nil {
		image = before
	}
	if image != nil {
		if _, schemaDigest, _, err := mutationplan.ModelEventSchema(registry, image.ModelID()); err == nil {
			digest = golem.SchemaDigest(schemaDigest)
		}
	}
	bound, err := requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		return mutationfact.Envelope{}, err
	}
	return mutationfact.New(registry, digest, event, bound, causation, ordinal, 7, before, after)
}

func withTestRowVersion(registry *schema.Registry, model golem.ModelID, cells []mutationdecode.Cell) []mutationdecode.Cell {
	metadata, ok := registry.Model(model)
	if !ok {
		return cells
	}
	field, ok := metadata.RowVersion()
	if !ok {
		return cells
	}
	version, _ := policyir.SignedValue(policyir.ValueInt64, 1)
	return append(append([]mutationdecode.Cell(nil), cells...), mutationdecode.Value(policyir.FieldID(field), version))
}
