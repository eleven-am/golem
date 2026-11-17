package runtime

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestMutationHistorySnapshotRewindsChangedCorrelationsAndDeletedRows(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	fixture.mutate(t, golem.EventCreated, 51, 1, "before")
	target := golem.GeneratedUniqueSelectorValue[policyFactRuntimePost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 51}))
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		_, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, target, golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2})))
		if err != nil {
			return err
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		filter := func(id byte) []mutationdecode.Cell {
			return []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue(golem.UUID{15: id}.Bytes()))}
		}
		assertRows := func(phase mutationHistoryPhase, author byte, expected ...byte) error {
			rows, err := snapshot.read(fixture.ctx, policyir.ModelID(fixture.schema.Post), filter(author), phase, 10)
			if err != nil {
				return err
			}
			if len(rows) != len(expected) {
				t.Fatalf("phase=%d author=%d rows=%d want=%v", phase, author, len(rows), expected)
			}
			seen := map[[16]byte]bool{}
			for _, row := range rows {
				cell, _ := row.Cell(policyir.FieldID(fixture.schema.PostID))
				value, _ := cell.PolicyValue()
				id, _ := value.UUID()
				seen[id] = true
			}
			for _, id := range expected {
				if !seen[golem.UUID{15: id}.Bytes()] {
					t.Fatalf("phase=%d missing old/new row %d", phase, id)
				}
			}
			return nil
		}
		if err := assertRows(mutationHistoryBefore, 1, 51); err != nil {
			return err
		}
		if err := assertRows(mutationHistoryBefore, 2); err != nil {
			return err
		}
		if err := assertRows(mutationHistoryAfter, 1); err != nil {
			return err
		}
		if err := assertRows(mutationHistoryAfter, 2, 51); err != nil {
			return err
		}
		// Text correlations and filters carry the ordinary binary-text
		// capability requirement, including for overlaid old images.
		for _, text := range []string{"before", "absent"} {
			value, err := policyir.StringValue(text)
			if err != nil {
				return err
			}
			for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
				rows, err := snapshot.read(fixture.ctx, policyir.ModelID(fixture.schema.Post), []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), value)}, phase, 10)
				if err != nil {
					return err
				}
				want := 0
				if text == "before" {
					want = 1
				}
				if len(rows) != want {
					t.Fatalf("text filter %q phase=%d rows=%d want=%d", text, phase, len(rows), want)
				}
			}
		}
		if _, err := SystemTxDelete(fixture.ctx, transaction, fixture.posts, target); err != nil {
			return err
		}
		if _, err := snapshot.read(fixture.ctx, policyir.ModelID(fixture.schema.Post), filter(1), mutationHistoryBefore, 10); err == nil {
			t.Fatal("snapshot accepted a later write epoch")
		}
		_, err = SystemTxCreate(fixture.ctx, transaction, fixture.posts, golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 52}), golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}), golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "created")))
		if err != nil {
			return err
		}
		snapshot, err = newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		if err := assertRows(mutationHistoryBefore, 1, 51); err != nil {
			return err
		}
		if err := assertRows(mutationHistoryBefore, 2); err != nil {
			return err
		}
		if err := assertRows(mutationHistoryAfter, 1, 52); err != nil {
			return err
		}
		return assertRows(mutationHistoryAfter, 2)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMutationHistorySnapshotRejectsOverflowAndUncapturedEquality(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry); err == nil {
			t.Fatal("read-only transaction acquired write snapshot authority")
		}
		for _, id := range []byte{51, 52} {
			_, err := SystemTxCreate(fixture.ctx, transaction, fixture.posts, golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}), golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}), golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "ready")))
			if err != nil {
				return err
			}
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		_, err = snapshot.read(fixture.ctx, policyir.ModelID(fixture.schema.Post), nil, mutationHistoryAfter, 1)
		mutationHistoryLimit(t, err, golem.LimitPolicyHistoryNodes)
		if _, err := snapshot.read(fixture.ctx, policyir.ModelID(fixture.schema.Post), nil, mutationHistoryBefore, 10); err == nil {
			t.Fatal("overflow was reused as complete evidence")
		}
		snapshot, err = newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		_, err = snapshot.read(fixture.ctx, policyir.ModelID(fixture.schema.Post), []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.schema.PostRowVersion), mustHistoryVersion(t))}, mutationHistoryAfter, 10)
		if err == nil {
			t.Fatal("uncaptured equality crossed the overlay")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustHistoryVersion(t *testing.T) policyir.Value {
	t.Helper()
	value, err := policyir.SignedValue(policyir.ValueInt64, 1)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
