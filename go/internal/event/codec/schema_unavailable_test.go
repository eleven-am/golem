package codec

import (
	"errors"
	"testing"
	"time"

	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestResolveEventSchemaWithoutASchemaIsTyped(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	row := postRow(t, fixture, [16]byte{7})
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := mutationfact.New(fixture.Registry, mutationfact.EventID{1}, requirement, mutationfact.CausationID{2}, 1, nil, &row)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := created.OutboxRow(time.Unix(1_700_000_000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := EncodeStoredRow(stored, testResolver{registry: fixture.Registry}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveEventSchema(envelope.Fact(), testResolver{})
	var failure *mutationfact.SchemaUnavailableError
	if !errors.As(err, &failure) {
		t.Fatalf("error is not *SchemaUnavailableError: %v", err)
	}
	want := mutationfact.SchemaReference{FormatVersion: mutationfact.FormatVersionV1, CodecIdentity: mutationfact.CodecIdentityV1, Generation: fixture.Registry.GenerationDigest()}
	if failure.Reference() != want {
		t.Fatalf("reference = %#v, want %#v", failure.Reference(), want)
	}
	if _, err := resolveEventSchema(envelope.Fact(), nil); err == nil {
		t.Fatal("a missing resolver was accepted")
	}
}
