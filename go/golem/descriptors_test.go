package golem

import "testing"

type descriptorFixture struct{}

func TestGeneratedDescriptorsPreserveCompositeOrderAndAreImmutable(t *testing.T) {
	model := ModelID{1}
	first, second, generated := FieldID{1}, FieldID{2}, FieldID{3}
	key := KeyID{9}
	identity := GeneratedIdentityMetadata(model, key, PrimaryIdentity, first, second)
	descriptor := GeneratedModelDescriptor[descriptorFixture](model, GeneratedDescriptorShape(
		[]FieldID{first, second, generated},
		[]FieldID{first, second},
		[]IdentityMetadata{identity},
		nil,
	))
	metadata := descriptor.Metadata()
	if got := metadata.Identities()[0].Fields(); len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("composite identity order=%v", got)
	}
	metadata.ScanFields()[0] = FieldID{99}
	metadata.WriteFields()[0] = FieldID{99}
	metadata.Identities()[0].fields[0] = FieldID{99}
	fresh := descriptor.Metadata()
	if fresh.ScanFields()[0] != first || fresh.WriteFields()[0] != first || fresh.Identities()[0].Fields()[0] != first {
		t.Fatal("public descriptor projection mutated generated metadata")
	}
	selector := GeneratedIdentitySelector[descriptorFixture](model, key, PrimaryIdentity, first, second)
	if got := selector.Metadata().Fields(); len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("typed selector lost composite order=%v", got)
	}
}

func TestApplicationDescriptorRegistryResolvesRelationIDsWithoutPointers(t *testing.T) {
	model, target := ModelID{1}, ModelID{2}
	field, relation := FieldID{3}, RelationID{4}
	descriptor := GeneratedModelDescriptor[descriptorFixture](model, GeneratedDescriptorShape(nil, nil, nil, []RelationMetadata{
		GeneratedRelationMetadata(model, target, field, relation, RelationSource, RelationToOne),
	}))
	digest := SchemaDigest{8}
	registry, err := GeneratedApplicationDescriptors(digest, GeneratedStampedPackageDescriptors(digest, descriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	metadata, ok := registry.Lookup(model)
	if !ok {
		t.Fatal("model not found")
	}
	relations := metadata.Relations()
	if len(relations) != 1 || relations[0].FieldID() != field || relations[0].RelationID() != relation || relations[0].TargetModelID() != target || relations[0].Role() != RelationSource || relations[0].Cardinality() != RelationToOne {
		t.Fatalf("relation metadata=%#v", relations)
	}
	if registry.GenerationDigest() != digest {
		t.Fatal("application descriptor generation digest was not preserved")
	}
}

func TestGeneratedApplicationDescriptorsRejectMixedAndUnstampedPackages(t *testing.T) {
	expected := SchemaDigest{1}
	for _, pkg := range []PackageDescriptors{
		GeneratedStampedPackageDescriptors(SchemaDigest{2}),
		GeneratedPackageDescriptors(),
	} {
		registry, err := GeneratedApplicationDescriptors(expected, GeneratedStampedPackageDescriptors(expected), pkg)
		if registry.GenerationDigest() != (SchemaDigest{}) {
			t.Fatal("rejected descriptor registry retained a generation stamp")
		}
		mismatch, ok := err.(*GenerationDigestError)
		if !ok || mismatch.PackageIndex != 1 || mismatch.Expected != expected || mismatch.Actual != pkg.GenerationDigest() {
			t.Fatalf("mixed package error=%#v", err)
		}
	}
	if _, err := GeneratedApplicationDescriptors(SchemaDigest{}); err == nil {
		t.Fatal("unstamped expected descriptor generation was accepted")
	}
}
