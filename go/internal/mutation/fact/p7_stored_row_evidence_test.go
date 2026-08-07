package fact

import (
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestP7StoredFactCrossChecksEveryDuplicatedColumn(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	before := mustRow(t, fixture, postCells(t, fixture, [16]byte{7}, "private", 101))
	digest := golem.SchemaDigest{1, 2, 3, 4}
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
	envelope, err := NewV2(fixture.Registry, digest, EventID{9}, requirement, CausationID{8}, 1, &before, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := envelope.OutboxRow(time.Unix(3, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	resolver := testHistoricalResolver{registry: fixture.Registry, digest: digest, enabled: true}
	if _, err := ValidateStoredRow(row, resolver); err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*OutboxRow){
		"event_id":               func(value *OutboxRow) { value.EventID = "00000000-0000-4000-8000-000000000099" },
		"fact_version":           func(value *OutboxRow) { value.FactVersion = int64(FormatVersionV1) },
		"codec_identity":         func(value *OutboxRow) { value.CodecIdentity = CodecIdentityV1 },
		"generation_fingerprint": func(value *OutboxRow) { value.GenerationFingerprint = string(make([]byte, 64)) },
		"model_id":               func(value *OutboxRow) { value.ModelID = "00000000000000000000000000000099" },
		"action":                 func(value *OutboxRow) { value.Action = "created" },
		"before_identity":        func(value *OutboxRow) { value.BeforeIdentity[0] ^= 0xff },
		"after_identity":         func(value *OutboxRow) { value.AfterIdentity = []byte{1} },
		"causation_id":           func(value *OutboxRow) { value.CausationID = "00000000-0000-4000-8000-000000000098" },
		"transaction_ordinal":    func(value *OutboxRow) { value.TransactionOrdinal = 2 },
		"metadata":               func(value *OutboxRow) { value.Metadata[0] ^= 0xff },
		"delete_snapshot":        func(value *OutboxRow) { value.DeleteSnapshot[0] ^= 0xff },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := row
			candidate.BeforeIdentity = append([]byte(nil), row.BeforeIdentity...)
			candidate.AfterIdentity = append([]byte(nil), row.AfterIdentity...)
			candidate.Metadata = append([]byte(nil), row.Metadata...)
			candidate.DeleteSnapshot = append([]byte(nil), row.DeleteSnapshot...)
			mutate(&candidate)
			if _, err := ValidateStoredRow(candidate, resolver); err == nil {
				t.Fatal("tampered duplicated column was accepted")
			}
		})
	}
}
