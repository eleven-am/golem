package priorrelease

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func releasedFact(registry *schema.Registry, model golem.ModelID, key golem.FieldID, digest golem.SchemaDigest) ([]byte, error) {
	row, err := decode.NewRow(registry, policyir.ModelID(model), []decode.Cell{decode.Value(policyir.FieldID(key), policyir.UUIDValue([16]byte{15: 7}))})
	if err != nil {
		return nil, err
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(key)}, nil)
	if err != nil {
		return nil, err
	}
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		return nil, err
	}
	envelope, err := fact.New(registry, digest, fact.EventID{1}, requirement, fact.CausationID{2}, 1, 41, nil, &row)
	if err != nil {
		return nil, err
	}
	return fact.Encode(envelope)
}
