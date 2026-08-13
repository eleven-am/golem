package golem

import "testing"

type generationIdentityModel struct{}
type generationIdentityTarget struct{}

func TestGeneratedModelAndFieldHandlesCarryExactGenerationIdentity(t *testing.T) {
	first := SchemaDigest{0x01}
	second := SchemaDigest{0x02}
	model := ModelID{0x11}
	field := FieldID{0x22}
	relation := RelationID{0x33}
	target := ModelID{0x44}

	left := GeneratedStampedModelDescriptor[generationIdentityModel](first, model, GeneratedDescriptorShape([]FieldID{field}, nil, nil, nil))
	right := GeneratedStampedModelDescriptor[generationIdentityModel](second, model, GeneratedDescriptorShape([]FieldID{field}, nil, nil, nil))
	if left.Metadata().ModelID() != right.Metadata().ModelID() || left.GenerationDigest() == right.GenerationDigest() {
		t.Fatal("byte-identical model metadata did not retain distinct generation identities")
	}

	leftField := GeneratedStampedTextField[generationIdentityModel, string](first, field)
	rightField := GeneratedStampedTextField[generationIdentityModel, string](second, field)
	leftID, leftIDOK := FieldIdentity[generationIdentityModel](leftField)
	rightID, rightIDOK := FieldIdentity[generationIdentityModel](rightField)
	leftGeneration, leftGenerationOK := FieldGenerationDigest[generationIdentityModel](leftField)
	rightGeneration, rightGenerationOK := FieldGenerationDigest[generationIdentityModel](rightField)
	if !leftIDOK || !rightIDOK || leftID != rightID || !leftGenerationOK || !rightGenerationOK || leftGeneration == rightGeneration {
		t.Fatal("byte-identical scalar fields did not retain distinct generation identities")
	}

	leftRelation := GeneratedStampedToOne[generationIdentityModel, generationIdentityTarget](first, field, relation, target)
	rightRelation := GeneratedStampedToOne[generationIdentityModel, generationIdentityTarget](second, field, relation, target)
	leftGeneration, leftGenerationOK = FieldGenerationDigest[generationIdentityModel](leftRelation)
	rightGeneration, rightGenerationOK = FieldGenerationDigest[generationIdentityModel](rightRelation)
	if !leftGenerationOK || !rightGenerationOK || leftGeneration == rightGeneration {
		t.Fatal("byte-identical relation fields did not retain distinct generation identities")
	}

	unstamped := GeneratedTextField[generationIdentityModel, string](field)
	if _, ok := FieldGenerationDigest[generationIdentityModel](unstamped); ok {
		t.Fatal("an unstamped compatibility constructor claimed generated authority")
	}
}
