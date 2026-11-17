package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type invalidationIncompleteCDCAdapter struct{}

func (invalidationIncompleteCDCAdapter) Identity() events.CDCIdentity {
	return events.CDCIdentity{Name: "invalidation-before-coverage", Version: "1", Provider: golem.SQLite}
}
func (invalidationIncompleteCDCAdapter) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool, error) {
	return false, nil
}
func (invalidationIncompleteCDCAdapter) Run(context.Context, events.CDCEmitter) error { return nil }

// A configured adapter may legitimately omit Updated.Before. The production
// encoder preserves that as an unverifiable snapshot, even when the active
// event schema declares every field. It is not evidence of old nonmembership.
func TestInvalidationMissingCDCBeforeDoesNotSilentlyLoseMembershipExit(t *testing.T) {
	fixture := schematest.NewSubscribedIndexedWithFullDeleteSnapshot(t)
	history, err := newEventSchemaHistory(fixture.Registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := newCDCEventEncoder(fixture.Registry, history, 0)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := providerSet(fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	app := &App[string, struct{}]{registry: fixture.Registry, eventSchemas: history, providers: providers}
	model := policyir.ModelID(fixture.Post)
	descriptor := golem.GeneratedModelDescriptor[invalidationRelationTarget](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	frozen, err := golem.GeneratedTextField[invalidationRelationTarget, string](fixture.PostTitle).Eq("WATCH").Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := policybind.Predicate(frozen, fixture.Registry, providers)
	if err != nil {
		t.Fatal(err)
	}
	rowDomain, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	makeRow := func(title string) golem.RuntimeModelRow {
		row, err := golem.RuntimeCDCModelRow(fixture.Post,
			golem.RuntimePresentReadCell(fixture.PostID, golem.UUID{15: 1}, nil),
			golem.RuntimePresentReadCell(fixture.AuthorID, golem.UUID{15: 2}, nil),
			golem.RuntimePresentReadCell(fixture.PostTitle, title, nil),
			golem.RuntimePresentReadCell(fixture.PostRowVersion, int64(1), nil))
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	before, after := makeRow("WATCH"), makeRow("OTHER")
	metadata, _ := fixture.Registry.Model(fixture.Post)
	fingerprint, _, _ := metadata.EventSchema()
	digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	member := &eventDependencyMember{
		metadata:   golem.GeneratedEventModelMetadata(fixture.Post, golem.EventSchemaDigest(digest), []golem.FieldID{fixture.PostID}, "PostEvent", "golem.UUID"),
		membership: true, watch: []golem.FieldID{fixture.PostTitle},
		judge: &eventJudgeSpec{row: rowDomain, filter: &filter, watch: []golem.FieldID{fixture.PostTitle}},
	}
	for _, hasBefore := range []bool{true, false} {
		name := "complete before"
		if !hasBefore {
			name = "missing before"
		}
		t.Run(name, func(t *testing.T) {
			input := eventcdc.EncodeInput{
				Adapter: (invalidationIncompleteCDCAdapter{}).Identity(), SourceTransactionID: "membership-exit", Cursor: []byte("cursor"),
				RecordedAt: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC), EventID: golem.EventID{15: 3}, CausationID: golem.CausationID{15: 4},
				Ordinal: 1, Version: 2, Model: fixture.Post, Action: golem.EventUpdated, After: &after,
			}
			if hasBefore {
				input.Before = &before
			}
			encoded, err := encoder.EncodeCDC(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := eventcodec.Decode(encoded.Notice.Encoded(), history, eventcodec.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if !hasBefore && envelope.Fact().DeleteSnapshotState() != mutationir.DeleteSnapshotUnverifiable {
				t.Fatal("valid CDC missing-before did not retain its evidence state")
			}
			current, err := decodeCDCExactImage(fixture.Registry, model, &after)
			if err != nil {
				t.Fatal(err)
			}
			identity, ok := envelope.Fact().AfterIdentity()
			if !ok {
				t.Fatal("CDC update has no identity")
			}
			evaluator, err := newDeleteNode(&deleteEvaluation{providers: providers, fromRecord: true}, model, current, identity, 0)
			if err != nil {
				t.Fatal(err)
			}
			currentSnapshot := &eventSnapshot{found: true, evaluator: evaluator}
			if currentSnapshot.judge(member.judge) != deleteFalse || evaluator.truth(rowDomain) != deleteTrue {
				t.Fatal("fixture must prove the current row domain but no current membership")
			}
			outside, err := policyir.NewConstant(model, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, authority := range []struct {
				name     string
				row      policyir.Condition
				snapshot *eventSnapshot
				oldMatch bool
			}{
				{name: "current domain true", row: rowDomain, snapshot: currentSnapshot, oldMatch: true},
				{name: "current outside domain", row: outside, snapshot: currentSnapshot},
				// readEventSnapshot returns nil,nil when its current snapshot plan
				// cannot be constructed. No invented record or predicate supplies
				// old authority in this case; complete wire before evidence still can.
				{name: "current authority unavailable", row: rowDomain, snapshot: nil, oldMatch: true},
			} {
				t.Run(authority.name, func(t *testing.T) {
					ownedMember := *member
					ownedJudge := *member.judge
					ownedJudge.row = authority.row
					ownedMember.judge = &ownedJudge
					shared := &eventNoticeShared{envelope: envelope, snapshot: authority.snapshot}
					changed, err := app.readableInvalidationChanged(&ownedMember, shared)
					if hasBefore {
						if changed != authority.oldMatch || err != nil {
							t.Fatalf("complete evidence lost exact membership/domain result: changed=%v want=%v err=%v", changed, authority.oldMatch, err)
						}
						return
					}
					if code, _ := events.CodeOf(err); changed || code != events.CodeSubscriptionSourceClosed {
						t.Fatalf("missing old membership evidence must terminate generically, never attribute or silently suppress: changed=%v err=%v", changed, err)
					}
					if reason, _ := events.ReasonOf(err); reason != "" {
						t.Fatalf("missing evidence acquired activity attribution: %v", err)
					}
				})
			}
		})
	}
	// Retained native facts can carry the legal unverifiable state for deletes,
	// or a stored-scalar inventory that does not satisfy today's complete source
	// contract. Use the actual fact/outbox/event codecs; never manufacture bytes.
	for _, action := range []golem.EventAction{golem.EventUpdated, golem.EventDeleted} {
		for _, partial := range []bool{false, true} {
			if action == golem.EventUpdated && !partial {
				continue // The genuine CDC case above already covers this shape.
			}
			name := string(action) + "/unverifiable"
			if partial {
				name = string(action) + "/partial stored scalars"
			}
			t.Run(name, func(t *testing.T) {
				state := mutationir.DeleteSnapshotUnverifiable
				var stored []policyir.FieldID
				ids := []policyir.FieldID{policyir.FieldID(fixture.PostID)}
				if partial {
					state = mutationir.DeleteSnapshotStoredScalars
					// Keep identity and author authority but omit the narrowed title.
					stored = []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID)}
				}
				requirement, err := mutationir.NewDeleteFactRequirement(ids, state, stored)
				if action == golem.EventUpdated {
					requirement, err = mutationir.NewUpdateFactRequirement(ids, ids, state, stored)
				}
				if err != nil {
					t.Fatal(err)
				}
				requirement, err = requirement.WithEventSchema([32]byte(digest))
				if err != nil {
					t.Fatal(err)
				}
				oldRow, err := decodeCDCExactImage(fixture.Registry, model, &before)
				if err != nil {
					t.Fatal(err)
				}
				newRow, err := decodeCDCExactImage(fixture.Registry, model, &after)
				if err != nil {
					t.Fatal(err)
				}
				if action == golem.EventDeleted {
					newRow = nil
				}
				fact, err := mutationfact.New(fixture.Registry, golem.SchemaDigest(digest), mutationfact.EventID{15: 8}, requirement, mutationfact.CausationID{15: 9}, 1, 2, oldRow, newRow)
				if err != nil {
					t.Fatal(err)
				}
				storedRow, err := fact.OutboxRow(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := eventcodec.EncodeStoredRow(storedRow, history, eventcodec.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				envelope, err := eventcodec.Decode(encoded.Encoded(), history, eventcodec.Limits{})
				if err != nil {
					t.Fatal(err)
				}
				if partial {
					snapshot, present := envelope.Fact().PrivateDeleteSnapshot()
					if !present || len(snapshot.Cells()) != 2 {
						t.Fatal("partial fixture lost its real stored scalar evidence")
					}
				}
				changed, err := app.readableInvalidationChanged(member, &eventNoticeShared{envelope: envelope})
				if code, _ := events.CodeOf(err); changed || code != events.CodeSubscriptionSourceClosed {
					t.Fatalf("incomplete before image accepted: changed=%v err=%v", changed, err)
				}
				if reason, _ := events.ReasonOf(err); reason != "" {
					t.Fatalf("operational evidence failure attributed activity: %v", err)
				}
			})
		}
	}

}
