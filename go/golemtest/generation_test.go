package golemtest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

func expectCode(t *testing.T, err error, want ErrorCode, context string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted foreign input", context)
	}
	code, ok := CodeOf(err)
	if !ok {
		t.Fatalf("%s: error carries no classification", context)
	}
	if code != want {
		t.Fatalf("%s: classified %s, want %s", context, code, want)
	}
}

func rebuildTagDescriptor(metadata golem.ModelMetadata, id golem.ModelID, generation golem.SchemaDigest) golem.ModelDescriptor[p5social.Tag] {
	return golem.GeneratedStampedModelDescriptor[p5social.Tag](generation, id, golem.GeneratedDescriptorShape(
		metadata.ScanFields(),
		metadata.WriteFields(),
		metadata.Identities(),
		metadata.Relations(),
	))
}

func TestPolicyTestKitRejectsForeignGenerationModelAndFieldHandles(t *testing.T) {
	socialBindings, err := p5social.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	socialDescriptors, err := p5social.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	metricBindings, err := p6metrics.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	metricDescriptors, err := p6metrics.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	socialBundle := p5social.GolemGeneratedSchemaBundle()
	metricBundle := p6metrics.GolemGeneratedSchemaBundle()
	if socialBindings.GenerationDigest() == metricBindings.GenerationDigest() {
		t.Fatal("the two fixtures share a generation, so this gate proves nothing")
	}

	t.Run("NewRejectsCrossGenerationBindingsAndDescriptors", func(t *testing.T) {
		_, err := New(socialBindings, metricDescriptors, socialBundle)
		expectCode(t, err, ErrorGenerationMismatch, "social bindings with metric descriptors")
		_, err = New(metricBindings, socialDescriptors, metricBundle)
		expectCode(t, err, ErrorGenerationMismatch, "metric bindings with social descriptors")
	})

	t.Run("NewRejectsACrossGenerationSchemaBundle", func(t *testing.T) {
		_, err := New(socialBindings, socialDescriptors, metricBundle)
		expectCode(t, err, ErrorGenerationMismatch, "social artifacts with the metric schema bundle")
		_, err = New(metricBindings, metricDescriptors, socialBundle)
		expectCode(t, err, ErrorGenerationMismatch, "metric artifacts with the social schema bundle")
	})

	t.Run("NewRejectsUnstampedRegistries", func(t *testing.T) {
		_, err := New(golem.ApplicationBindings[p5social.Actor]{}, socialDescriptors, socialBundle)
		expectCode(t, err, ErrorGenerationMismatch, "unstamped bindings")
		_, err = New(socialBindings, golem.ApplicationDescriptors{}, socialBundle)
		expectCode(t, err, ErrorGenerationMismatch, "unstamped descriptors")
		_, err = New(socialBindings, socialDescriptors, golem.SchemaBundle{})
		expectCode(t, err, ErrorGenerationMismatch, "unstamped schema bundle")
	})

	t.Run("NewRejectsRestampedForeignDescriptors", func(t *testing.T) {
		foreign := foreignGeneration()
		restamped, err := golem.GeneratedApplicationDescriptors(
			foreign,
			golem.GeneratedStampedPackageDescriptors(foreign, socialDescriptors.Models()...),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = New(socialBindings, restamped, socialBundle)
		expectCode(t, err, ErrorGenerationMismatch, "descriptors restamped with a foreign generation")
	})

	kit, err := New(socialBindings, socialDescriptors, socialBundle)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	policies, err := kit.ForActor(p5social.Actor{AllowedTag: "alpha"})
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}

	tagMetadata := p5social.GolemGeneratedTagDescriptor.Metadata()
	socialGeneration := socialBindings.GenerationDigest()

	t.Run("ModelRejectsADescriptorFromAnotherApplication", func(t *testing.T) {
		_, err := Model(policies, p6metrics.GolemGeneratedCategoryDescriptor)
		expectCode(t, err, ErrorGenerationMismatch, "descriptor from another application")
	})

	t.Run("ModelRejectsAForeignModelIdentityWithAMatchingGoType", func(t *testing.T) {
		foreign := golem.ModelID{0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x00, 0x0f, 0x0e, 0x0d, 0x0c, 0x0b, 0x0a, 0x09, 0x08}
		_, err := Model(policies, rebuildTagDescriptor(tagMetadata, foreign, socialGeneration))
		expectCode(t, err, ErrorGenerationMismatch, "foreign model identity with a matching Go model type")
	})

	t.Run("ModelRejectsAForeignScanFieldHandle", func(t *testing.T) {
		scan := tagMetadata.ScanFields()
		if len(scan) == 0 {
			t.Fatal("the fixture model declares no scan fields")
		}
		scan[len(scan)-1] = golem.FieldID{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
		tampered := golem.GeneratedStampedModelDescriptor[p5social.Tag](socialGeneration, tagMetadata.ModelID(), golem.GeneratedDescriptorShape(
			scan, tagMetadata.WriteFields(), tagMetadata.Identities(), tagMetadata.Relations(),
		))
		_, err := Model(policies, tampered)
		expectCode(t, err, ErrorGenerationMismatch, "foreign scan field handle under a registered model identity")
	})

	t.Run("ModelRejectsAForeignIdentityKeyHandle", func(t *testing.T) {
		identities := tagMetadata.Identities()
		if len(identities) == 0 {
			t.Fatal("the fixture model declares no identities")
		}
		identities[0] = golem.GeneratedIdentityMetadata(
			tagMetadata.ModelID(),
			golem.KeyID{0x5a, 0x5b, 0x5c, 0x5d, 0x5e, 0x5f, 0x60, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69},
			identities[0].Kind(),
			identities[0].Fields()...,
		)
		tampered := golem.GeneratedStampedModelDescriptor[p5social.Tag](socialGeneration, tagMetadata.ModelID(), golem.GeneratedDescriptorShape(
			tagMetadata.ScanFields(), tagMetadata.WriteFields(), identities, tagMetadata.Relations(),
		))
		_, err := Model(policies, tampered)
		expectCode(t, err, ErrorGenerationMismatch, "foreign identity key handle under a registered model identity")
	})

	t.Run("ModelRejectsAForeignRelationHandle", func(t *testing.T) {
		relations := tagMetadata.Relations()
		if len(relations) == 0 {
			t.Fatal("the fixture model declares no relations")
		}
		relations[0] = golem.GeneratedRelationMetadata(
			relations[0].ModelID(),
			relations[0].TargetModelID(),
			golem.FieldID{0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13, 0x13},
			relations[0].RelationID(),
			relations[0].Role(),
			relations[0].Cardinality(),
		)
		tampered := golem.GeneratedStampedModelDescriptor[p5social.Tag](socialGeneration, tagMetadata.ModelID(), golem.GeneratedDescriptorShape(
			tagMetadata.ScanFields(), tagMetadata.WriteFields(), tagMetadata.Identities(), relations,
		))
		_, err := Model(policies, tampered)
		expectCode(t, err, ErrorGenerationMismatch, "foreign relation field handle under a registered model identity")
	})

	t.Run("ModelRejectsADroppedFieldHandle", func(t *testing.T) {
		scan := tagMetadata.ScanFields()
		if len(scan) < 2 {
			t.Fatal("the fixture model declares too few scan fields")
		}
		tampered := golem.GeneratedStampedModelDescriptor[p5social.Tag](socialGeneration, tagMetadata.ModelID(), golem.GeneratedDescriptorShape(
			scan[:len(scan)-1], tagMetadata.WriteFields(), tagMetadata.Identities(), tagMetadata.Relations(),
		))
		_, err := Model(policies, tampered)
		expectCode(t, err, ErrorGenerationMismatch, "descriptor missing a registered field handle")
	})

	t.Run("ModelAcceptsTheRegisteredDescriptor", func(t *testing.T) {
		if _, err := Model(policies, p5social.GolemGeneratedTagDescriptor); err != nil {
			t.Fatalf("registered descriptor was rejected: %v", err)
		}
		if _, err := Model(policies, rebuildTagDescriptor(tagMetadata, tagMetadata.ModelID(), socialGeneration)); err != nil {
			t.Fatalf("faithfully rebuilt descriptor was rejected: %v", err)
		}
	})

	t.Run("ModelRejectsByteIdenticalMetadataFromAnotherGeneration", func(t *testing.T) {
		foreign := rebuildTagDescriptor(tagMetadata, tagMetadata.ModelID(), foreignGeneration())
		_, err := Model(policies, foreign)
		expectCode(t, err, ErrorGenerationMismatch, "byte-identical descriptor carrying a foreign generation")
	})

	t.Run("FieldAndRelationHandlesRequireTheExactGeneration", func(t *testing.T) {
		tags, err := Model(policies, p5social.GolemGeneratedTagDescriptor)
		if err != nil {
			t.Fatal(err)
		}
		nameID, ok := golem.FieldIdentity(p5social.Tags.Name)
		if !ok {
			t.Fatal("generated tag name has no identity")
		}
		foreignName := golem.GeneratedStampedTextField[p5social.Tag, string](foreignGeneration(), nameID)
		if _, err := tags.ClassifyReadFields(UseProjection, golem.FrozenActionRead, foreignName); err == nil {
			t.Fatal("classification accepted a byte-identical scalar field from another generation")
		} else {
			expectCode(t, err, ErrorGenerationMismatch, "foreign-generation scalar field")
		}

		postTagModel := p5social.GolemGeneratedPostTagDescriptor.Metadata().ModelID()
		var relation golem.RelationMetadata
		for _, candidate := range tagMetadata.Relations() {
			if candidate.TargetModelID() == postTagModel {
				relation = candidate
				break
			}
		}
		if relation.FieldID() == (golem.FieldID{}) {
			t.Fatal("tag fixture has no post-tag relation")
		}
		foreignRelation := golem.GeneratedStampedToMany[p5social.Tag, p5social.PostTag](foreignGeneration(), relation.FieldID(), relation.RelationID(), relation.TargetModelID())
		if _, err := tags.ClassifyReadFields(UseProjection, golem.FrozenActionRead, foreignRelation); err == nil {
			t.Fatal("classification accepted a byte-identical relation field from another generation")
		} else {
			expectCode(t, err, ErrorGenerationMismatch, "foreign-generation relation field")
		}
	})

	t.Run("ModelRejectsAZeroDescriptorAndAnUnbuiltPolicySet", func(t *testing.T) {
		_, err := Model(policies, golem.ModelDescriptor[p5social.Tag]{})
		expectCode(t, err, ErrorInvalidInput, "zero descriptor")
		_, err = Model(PolicySet{}, p5social.GolemGeneratedTagDescriptor)
		expectCode(t, err, ErrorInvalidInput, "zero policy set")
	})
}
