package typedvalue

import (
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

func TestValidatedEventOwnsIdentityAndBindsResolvedSchema(t *testing.T) {
	schema := golem.EventSchemaDigest{5}
	identity := []byte{7}
	event, err := New(Metadata{EventID: golem.EventID{1}, Action: golem.EventCreated, CausationID: golem.CausationID{2}, Ordinal: 1, RecordedAt: time.Unix(3, 0), Generation: golem.SchemaDigest{4}, EventSchema: schema, HasEventSchema: true, ResolvedEventSchema: schema, ModelID: golem.ModelID{6}}, []any{identity}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity[0] = 9
	first := event.IdentityValues()[0].([]byte)
	first[0] = 8
	if event.IdentityValues()[0].([]byte)[0] != 7 || event.ResolvedEventSchemaDigest() != schema {
		t.Fatal("validated event exposed aliased identity or changed schema")
	}
	if _, err := New(Metadata{Action: golem.EventCreated, ResolvedEventSchema: schema}, nil, nil); err == nil {
		t.Fatal("empty identity accepted")
	}
}
