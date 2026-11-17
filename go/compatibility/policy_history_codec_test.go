package compatibility

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type fixtureResolver struct {
	registry *schema.Registry
	digest   golem.SchemaDigest
}

func (r fixtureResolver) ResolveFactSchema(ref fact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	return r.registry, r.digest, ref.EventSchema == r.digest
}
func (r fixtureResolver) ResolvePolicyHistorySchema(generation, projection golem.SchemaDigest) (*schema.Registry, bool) {
	return r.registry, generation == r.registry.GenerationDigest() && projection == r.registry.PolicyCaptureFingerprint()
}

func TestReleasedV088AndCurrentFactCodecCompatibility(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	model, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, _ := model.EventSchema()
	digest, err := fact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	resolver := fixtureResolver{fixture.Registry, digest}
	row := func(model golem.ModelID, cells ...decode.Cell) decode.Row {
		t.Helper()
		value, err := decode.NewRow(fixture.Registry, policyir.ModelID(model), cells)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	text := func(value string) policyir.Value {
		result, err := policyir.StringValue(value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	userKey := decode.Value(policyir.FieldID(fixture.UserID), policyir.UUIDValue([16]byte{15: 1}))
	user := row(fixture.User, userKey, decode.Value(policyir.FieldID(fixture.UserName), text("source")))
	post := row(fixture.Post,
		decode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 7})),
		decode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{15: 1})),
		decode.Value(policyir.FieldID(fixture.PostTitle), text("ready")))
	graph, err := history.New(fixture.Registry, policyir.ProviderSQLite, history.Input{
		Rows:  []history.PhaseRow{{Phase: history.Before, Row: user}, {Phase: history.After, Row: user}, {Phase: history.After, Row: post}},
		Roots: []history.Root{{Identity: row(fixture.User, userKey), Before: history.Presence{Present: true, Row: 1}, After: history.Presence{Present: true, Row: 2}}},
		Extents: []history.Extent{
			{Phase: history.Before, Parent: 1, Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship)},
			{Phase: history.After, Parent: 2, Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship), Children: []history.RowRef{3}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := fact.New(fixture.Registry, digest, fact.EventID{1}, requirement, fact.CausationID{2}, 1, 41, nil, &post)
	if err != nil {
		t.Fatal(err)
	}
	base, err := envelope.OutboxRow(time.Unix(123, 0))
	if err != nil {
		t.Fatal(err)
	}
	sealed, _, err := fact.SealPolicyHistoryCausation([]fact.OutboxRow{base}, graph, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fact.DecodePolicyHistoryCausation(sealed, resolver); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		metadata []byte
		version  uint16
	}{{base.Metadata, 3}, {sealed[0].Metadata, 4}} {
		decoded, err := fact.DecodeWithResolver(test.metadata, resolver)
		if err != nil || decoded.FormatVersion() != test.version {
			t.Fatalf("current V%d reader: %v", test.version, err)
		}
	}

	dir := preparePriorRelease(t)
	for name, data := range map[string][]byte{"current-v3.bin": base.Metadata, "current-v4.bin": sealed[0].Metadata} {
		if len(data) > 64<<10 {
			t.Fatal("metadata fixture exceeded bound")
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	runPriorRelease(t, dir, "TestReleasedCodec")
	path := filepath.Join(dir, "released-v3.bin")
	info, err := os.Stat(path)
	if err != nil || info.Size() > 64<<10 {
		t.Fatalf("invalid released fixture: %v", err)
	}
	released, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := fact.DecodeWithResolver(released, resolver)
	if err != nil || decoded.FormatVersion() != 3 {
		t.Fatalf("current reader refused released V3: %v", err)
	}
	encoded, err := fact.Encode(decoded)
	if err != nil || !bytes.Equal(encoded, released) {
		t.Fatalf("released V3 bytes changed: %v", err)
	}
}
