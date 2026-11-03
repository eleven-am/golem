package fact

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

const schemaUnavailableRemedy = "which no active or historical bundle provides: deliver or drain events written before the schema changed, or supply the bundle that wrote them"

func requireSchemaUnavailable(t *testing.T, err error, want SchemaReference, message string) {
	t.Helper()
	var failure *SchemaUnavailableError
	if !errors.As(err, &failure) {
		t.Fatalf("error is not *SchemaUnavailableError: %v", err)
	}
	if failure.Reference() != want {
		t.Fatalf("reference = %#v, want %#v", failure.Reference(), want)
	}
	if failure.Error() != message {
		t.Fatalf("message = %q, want %q", failure.Error(), message)
	}
}

func deleteSnapshotRow(t *testing.T, fixture schematest.Fixture, digest golem.SchemaDigest) OutboxRow {
	t.Helper()
	before := mustRow(t, fixture, postCells(t, fixture, [16]byte{7}, "private", 101))
	requirement, err := mutationir.NewDeleteFactRequirement(
		[]policyir.FieldID{policyir.FieldID(fixture.PostID)},
		mutationir.DeleteSnapshotStoredScalars,
		[]policyir.FieldID{policyir.FieldID(fixture.PostTitle)},
	)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := New(fixture.Registry, digest, EventID{9}, requirement, CausationID{8}, 1, testRowVersion, &before, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := envelope.OutboxRow(time.Unix(3, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestUnresolvedFactFailsTyped(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	digest := golem.SchemaDigest{1, 2, 3, 4}
	row := deleteSnapshotRow(t, fixture, digest)
	_, err := DecodeWithResolver(row.Metadata, testHistoricalResolver{registry: fixture.Registry, digest: digest})
	reference := SchemaReference{FormatVersion: FormatVersion, CodecIdentity: CodecIdentity, Generation: fixture.Registry.GenerationDigest(), EventSchema: digest}
	requireSchemaUnavailable(t, err, reference, fmt.Sprintf("GOLEM_EVENT_SCHEMA_UNAVAILABLE: stored event fact (format V3) needs event schema %s, %s", digest, schemaUnavailableRemedy))
}

func TestDeleteSnapshotWithUnavailableSchemaFailsTyped(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	digest := golem.SchemaDigest{1, 2, 3, 4}
	row := deleteSnapshotRow(t, fixture, digest)
	if len(row.DeleteSnapshot) == 0 {
		t.Fatal("fixture has no delete snapshot")
	}
	_, err := DecodeOutboxWithResolver(row.Metadata, row.DeleteSnapshot, testHistoricalResolver{registry: fixture.Registry, digest: digest})
	reference := SchemaReference{FormatVersion: FormatVersion, CodecIdentity: CodecIdentity, Generation: fixture.Registry.GenerationDigest(), EventSchema: digest}
	requireSchemaUnavailable(t, err, reference, fmt.Sprintf("GOLEM_EVENT_SCHEMA_UNAVAILABLE: stored event fact (format V3) needs event schema %s, %s", digest, schemaUnavailableRemedy))
}

type countingResolver struct {
	testHistoricalResolver
	calls int
}

func (resolver *countingResolver) ResolveFactSchema(reference SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	resolver.calls++
	return resolver.testHistoricalResolver.ResolveFactSchema(reference)
}

func TestDecodingADeleteSnapshotFactResolvesItsSchemaOnce(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	digest := golem.SchemaDigest{1, 2, 3, 4}
	row := deleteSnapshotRow(t, fixture, digest)
	resolver := &countingResolver{testHistoricalResolver: testHistoricalResolver{registry: fixture.Registry, digest: digest, enabled: true}}
	if _, err := DecodeOutboxWithResolver(row.Metadata, row.DeleteSnapshot, resolver); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 {
		t.Fatalf("the resolver was asked %d times for one fact; want 1", resolver.calls)
	}
}
