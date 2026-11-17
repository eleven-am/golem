package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/mutation/rowlock"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestMutationHistoryKeepsFirstBeforeAndActualFinalAfter(t *testing.T) {
	fixture := mutationHistoryFixture(t)
	state := mutationHistoryState(t)
	ctx := context.Background()
	before := mutationHistoryPost(t, fixture, 1, "before")
	hook := mutationHistoryPost(t, fixture, 1, "hook")
	after := mutationHistoryPost(t, fixture, 1, "final")
	mutationHistoryRecord(t, state, fixture.Registry, &before, &hook, false)
	// The outer operation's locked preimage predates a hook write. The
	// journal must use the latest surviving application as this before image.
	mutationHistoryRecord(t, state, fixture.Registry, &before, &after, false)
	mutationHistoryText(t, state.history[1].before, fixture.PostTitle, "hook")
	images := state.historyImages()
	if len(images) != 1 || images[0].key == "" {
		t.Fatalf("repeated applications did not collapse to one known row: %+v", images)
	}
	mutationHistoryText(t, images[0].before, fixture.PostTitle, "before")
	mutationHistoryText(t, images[0].after, fixture.PostTitle, "final")
	if err := state.recordHistory(ctx, fixture.Registry, policyir.ProviderSQLite, &after, nil, false); err != nil {
		t.Fatal(err)
	}
	recreated := mutationHistoryPost(t, fixture, 1, "recreated")
	mutationHistoryRecord(t, state, fixture.Registry, nil, &recreated, false)
	images = state.historyImages()
	mutationHistoryText(t, images[0].before, fixture.PostTitle, "before")
	mutationHistoryText(t, images[0].after, fixture.PostTitle, "recreated")
	if state.history[3].before != nil {
		t.Fatal("recreation acquired a before-image instead of known absence")
	}
}

func TestMutationHistoryCreateDeleteRetainsKnownAbsence(t *testing.T) {
	fixture := mutationHistoryFixture(t)
	state := mutationHistoryState(t)
	created := mutationHistoryPost(t, fixture, 2, "created")
	mutationHistoryRecord(t, state, fixture.Registry, nil, &created, false)
	mutationHistoryRecord(t, state, fixture.Registry, &created, nil, false)
	images := state.historyImages()
	if len(images) != 1 || images[0].key == "" || images[0].before != nil || images[0].after != nil {
		t.Fatalf("create/delete must retain a keyed absent-to-absent transition: %+v", images)
	}
}

func TestMutationHistoryReleasedDescendantIsRemovedByParentRollback(t *testing.T) {
	fixture := mutationHistoryFixture(t)
	state := mutationHistoryState(t)
	ctx := context.Background()
	baseline := mutationHistoryPost(t, fixture, 1, "outside")
	mutationHistoryRecord(t, state, fixture.Registry, nil, &baseline, false)
	baselineBytes := state.historyBytes
	parent := mutationHistoryScope(t, state)
	parentRow := mutationHistoryPost(t, fixture, 2, "parent")
	mutationHistoryRecord(t, state, fixture.Registry, nil, &parentRow, false)
	child := mutationHistoryScope(t, state)
	childRow := mutationHistoryPost(t, fixture, 3, "child")
	mutationHistoryRecord(t, state, fixture.Registry, nil, &childRow, false)
	if err := child.release(); err != nil {
		t.Fatal(err)
	}
	grandchild := mutationHistoryScope(t, state)
	changed := mutationHistoryPost(t, fixture, 3, "released-again")
	mutationHistoryRecord(t, state, fixture.Registry, &childRow, &changed, false)
	if err := grandchild.release(); err != nil {
		t.Fatal(err)
	}
	if err := parent.rollback(); err != nil {
		t.Fatal(err)
	}
	images := state.historyImages()
	if len(images) != 1 || len(state.history) != 1 || state.historyBytes != baselineBytes {
		t.Fatalf("parent rollback retained released descendants: images=%d entries=%d bytes=%d want=%d", len(images), len(state.history), state.historyBytes, baselineBytes)
	}
	mutationHistoryText(t, images[0].after, fixture.PostTitle, "outside")
	// A row stamp cannot continue a rolled-back descendant application.
	if err := state.recordHistory(ctx, fixture.Registry, policyir.ProviderSQLite, nil, &changed, true); err == nil {
		t.Fatal("continuation accepted an application removed by ancestor rollback")
	}
}

func TestMutationHistoryChildRollbackRestoresSameRowAndCapacity(t *testing.T) {
	fixture := mutationHistoryFixture(t)
	state := mutationHistoryState(t)
	outer := mutationHistoryScope(t, state)
	before := mutationHistoryPost(t, fixture, 1, "before")
	after := mutationHistoryPost(t, fixture, 1, "outer")
	mutationHistoryRecord(t, state, fixture.Registry, &before, &after, false)
	outerBytes := state.historyBytes
	child := mutationHistoryScope(t, state)
	large := mutationHistoryPost(t, fixture, 1, strings.Repeat("x", 400_000))
	mutationHistoryRecord(t, state, fixture.Registry, &before, &large, false)
	childBytes := state.historyBytes
	err := state.recordHistory(context.Background(), fixture.Registry, policyir.ProviderSQLite, &large, &large, false)
	mutationHistoryLimit(t, err, golem.LimitPolicyHistoryBytes)
	if state.historyBytes != childBytes || len(state.history) != 2 {
		t.Fatal("failed capacity reservation changed journal state")
	}
	if err := child.rollback(); err != nil {
		t.Fatal(err)
	}
	if state.historyBytes != outerBytes || len(state.history) != 1 {
		t.Fatal("child rollback did not restore outer capacity")
	}
	mutationHistoryText(t, state.historyImages()[0].after, fixture.PostTitle, "outer")
	// The same large write now fits and starts from the surviving outer
	// image, even when supplied the obsolete pre-hook locked row.
	mutationHistoryRecord(t, state, fixture.Registry, &before, &large, false)
	mutationHistoryText(t, state.history[1].before, fixture.PostTitle, "outer")
	if err := outer.release(); err != nil {
		t.Fatal(err)
	}
}

func TestMutationHistoryRejectsIncompleteImagesAndInvalidTransitions(t *testing.T) {
	fixture := mutationHistoryFixture(t)
	state := mutationHistoryState(t)
	complete := mutationHistoryPost(t, fixture, 1, "ready")
	cells := complete.Cells()
	for _, missing := range []golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle} {
		var partial []mutationdecode.Cell
		for _, cell := range cells {
			if cell.FieldID() != policyir.FieldID(missing) {
				partial = append(partial, cell)
			}
		}
		row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), partial)
		if err != nil {
			t.Fatal(err)
		}
		if err := state.recordHistory(context.Background(), fixture.Registry, policyir.ProviderSQLite, nil, &row, false); err == nil {
			t.Fatalf("missing required capture/identity field %x accepted", missing)
		}
	}
	// Wrong types cannot be fabricated as an immutable decode.Row: the real
	// scalar owner rejects them before the journal accepts any transition.
	if _, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostTitle), policyir.BoolValue(true)),
	}); err == nil {
		t.Fatal("wrong persisted scalar type accepted")
	}
	other := mutationHistoryPost(t, fixture, 2, "other")
	if err := state.recordHistory(context.Background(), fixture.Registry, policyir.ProviderSQLite, &complete, &other, false); err == nil {
		t.Fatal("primary-key-changing transition accepted")
	}
	name, err := policyir.StringValue("owner")
	if err != nil {
		t.Fatal(err)
	}
	user, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.User), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.UserID), policyir.UUIDValue([16]byte{1})),
		mutationdecode.Value(policyir.FieldID(fixture.UserName), name),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.recordHistory(context.Background(), fixture.Registry, policyir.ProviderSQLite, &user, &complete, false); err == nil {
		t.Fatal("cross-model transition accepted")
	}
	if err := state.recordHistory(context.Background(), fixture.Registry, policyir.ProviderSQLite, nil, &complete, true); err == nil {
		t.Fatal("row stamp accepted without a preceding application")
	}
	if len(state.historyImages()) != 0 || state.historyBytes != 0 {
		t.Fatal("rejected image consumed history capacity")
	}
	mutationHistoryRecord(t, state, fixture.Registry, nil, &complete, false)
	stamp := mutationHistoryPost(t, fixture, 1, "stamped")
	mutationHistoryRecord(t, state, fixture.Registry, &complete, &stamp, true)
	mutationHistoryText(t, state.historyImages()[0].after, fixture.PostTitle, "stamped")
}

func TestMutationHistoryNoTemplatesDoesNotRetainRows(t *testing.T) {
	fixture := schematest.NewSubscribedGraph(t)
	state := mutationHistoryState(t)
	row := mutationHistoryPost(t, fixture, 1, "ready")
	mutationHistoryRecord(t, state, fixture.Registry, nil, &row, false)
	if len(state.historyImages()) != 0 || state.historyBytes != 0 {
		t.Fatal("schema with no templates incurred history payload")
	}
}

func TestMutationHistoryBoundsPayloadAndActualApplicationCount(t *testing.T) {
	fixture := mutationHistoryFixture(t)
	t.Run("payload", func(t *testing.T) {
		state := mutationHistoryState(t)
		row := mutationHistoryPost(t, fixture, 1, strings.Repeat("x", maxMutationHistoryBytes+1))
		err := state.recordHistory(context.Background(), fixture.Registry, policyir.ProviderSQLite, nil, &row, false)
		mutationHistoryLimit(t, err, golem.LimitPolicyHistoryBytes)
		if len(state.historyImages()) != 0 || state.historyBytes != 0 {
			t.Fatal("oversized payload mutated history")
		}
	})
	t.Run("applications", func(t *testing.T) {
		state := mutationHistoryState(t)
		row := mutationHistoryPost(t, fixture, 1, "x")
		for index := 0; index < 2*MaxMutationTouchedRows; index++ {
			mutationHistoryRecord(t, state, fixture.Registry, nil, &row, false)
		}
		reserved := state.historyBytes
		err := state.recordHistory(context.Background(), fixture.Registry, policyir.ProviderSQLite, nil, &row, false)
		mutationHistoryLimit(t, err, golem.LimitTouchedRows)
		if len(state.history) != 2*MaxMutationTouchedRows || len(state.historyImages()) != 1 || state.historyBytes != reserved {
			t.Fatal("transition ceiling lost actual applications or consumed failed reservation")
		}
	})
}

func TestMutationHistorySnapshotsAndFinalizationAreOwned(t *testing.T) {
	fixture := mutationHistoryFixture(t)
	for _, commit := range []bool{false, true} {
		state := mutationHistoryState(t)
		before := mutationHistoryPost(t, fixture, 1, "before")
		after := mutationHistoryPost(t, fixture, 1, "after")
		mutationHistoryRecord(t, state, fixture.Registry, &before, &after, false)
		first := state.historyImages()
		first[0].key = "caller mutation"
		*first[0].before = after
		*first[0].after = before
		second := state.historyImages()
		mutationHistoryText(t, second[0].before, fixture.PostTitle, "before")
		mutationHistoryText(t, second[0].after, fixture.PostTitle, "after")
		if second[0].key == "caller mutation" {
			t.Fatal("snapshot key aliased journal state")
		}
		cells := second[0].after.Cells()
		cells[0] = mutationdecode.Null(cells[0].FieldID())
		mutationHistoryText(t, state.historyImages()[0].after, fixture.PostTitle, "after")
		final := mutationHistoryPost(t, fixture, 1, "final")
		mutationHistoryRecord(t, state, fixture.Registry, &after, &final, false)
		mutationHistoryText(t, second[0].after, fixture.PostTitle, "after")
		if commit {
			state.committed(context.Background(), nil)
		} else {
			state.discarded()
		}
		if len(state.historyImages()) != 0 || len(state.history) != 0 || state.historyBytes != 0 {
			t.Fatal("completion retained history payload")
		}
		if err := state.recordHistory(context.Background(), fixture.Registry, policyir.ProviderSQLite, &before, &after, false); err == nil {
			t.Fatal("finalized state accepted another application")
		}
	}
}

func mutationHistoryFixture(t *testing.T) schematest.GraphFixture {
	return schematest.NewPolicyCaptureGraph(t)
}

func mutationHistoryState(t *testing.T) *mutationState {
	t.Helper()
	limits, err := normalizeMutationLimits(MutationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := newMutationState(limits, mutationfact.CausationID{1})
	if err != nil {
		t.Fatal(err)
	}
	state.binding = &executionBinding{locks: rowlock.NewLedger()}
	return state
}

func mutationHistoryScope(t *testing.T, state *mutationState) *mutationScope {
	t.Helper()
	scope, err := state.beginScope(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func mutationHistoryPost(t *testing.T, fixture schematest.GraphFixture, id byte, title string) mutationdecode.Row {
	t.Helper()
	value, err := policyir.StringValue(title)
	if err != nil {
		t.Fatal(err)
	}
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{id})),
		mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{99})),
		mutationdecode.Value(policyir.FieldID(fixture.PostTitle), value),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func mutationHistoryRecord(t *testing.T, state *mutationState, registry *schema.Registry, before, after *mutationdecode.Row, continuation bool) {
	t.Helper()
	if err := state.recordHistory(context.Background(), registry, policyir.ProviderSQLite, before, after, continuation); err != nil {
		t.Fatal(err)
	}
}

func mutationHistoryText(t *testing.T, row *mutationdecode.Row, field golem.FieldID, expected string) {
	t.Helper()
	if row == nil {
		t.Fatal("expected a known persisted row")
	}
	cell, ok := row.Cell(policyir.FieldID(field))
	if !ok {
		t.Fatalf("field %x absent from persisted image", field)
	}
	value, ok := cell.PolicyValue()
	if !ok {
		t.Fatal("expected non-null persisted scalar")
	}
	text, ok := value.Text()
	if !ok || text != expected {
		t.Fatalf("persisted text=%q want=%q", text, expected)
	}
}

func mutationHistoryLimit(t *testing.T, err error, expected golem.Limit) {
	t.Helper()
	var failure *mutationLimitError
	if !errors.As(err, &failure) || failure.limit != expected {
		t.Fatalf("limit error=%v want=%s", err, expected)
	}
}
