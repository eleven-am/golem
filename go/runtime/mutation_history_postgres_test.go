package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jackc/pgx/v5/pgconn"
)

func mutationHistoryPostgresFixture(t *testing.T) policyFactRuntimeFixture {
	t.Helper()
	requireMutationHistoryWriter(t)
	root, dsn := os.Getenv("GOLEM_POLICY_HISTORY_PG_ROOT"), os.Getenv("GOLEM_POLICY_HISTORY_PG_DSN")
	if root == "" && dsn == "" {
		testenv.SkipMissingPostgreSQL(t, "requires the root-owned disposable Unix-socket PostgreSQL cluster")
	}
	disposable := testenv.DisposablePostgreSQLInRoot(t, root, dsn)
	return newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true,
		postgresDSN: disposable, userNameScope: true,
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
	})
}

func mutationHistoryPostgresRows(t *testing.T, fixture policyFactRuntimeFixture, causal string) []mutationfact.OutboxRow {
	t.Helper()
	rows, err := fixture.app.database.QueryxContext(fixture.ctx, `SELECT 0,`+runtimeOutboxFactColumns+` FROM "_golem"."_golem_outbox" WHERE causation_id=$1 ORDER BY transaction_ordinal`, causal)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []mutationfact.OutboxRow
	for rows.Next() {
		_, row, err := fixture.app.scanOutboxFact(rows)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMutationHistoryPostgreSQLDefaultSerializableAndCommittedReparent(t *testing.T) {
	fixture := mutationHistoryPostgresFixture(t)
	fixture.mutate(t, golem.EventCreated, 151, 1, "before")
	var causal string
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		var isolation string
		binding := transaction.system.executor
		if err := binding.transaction.GetContext(fixture.ctx, &isolation, `SHOW transaction_isolation`); err != nil {
			return err
		}
		if isolation != "serializable" || !binding.historySerializable {
			return fmt.Errorf("default PostgreSQL history transaction has isolation=%s certificate=%v", isolation, binding.historySerializable)
		}
		if _, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 151), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after"))); err != nil {
			return err
		}
		state, err := binding.mutationState()
		if err != nil {
			return err
		}
		causal = state.facts[0].CausationID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := mutationHistoryPostgresRows(t, fixture, causal)
	value, err := mutationfact.DecodePolicyHistoryCausation(rows, fixture.app.eventSchemas)
	if err != nil || len(rows) != 1 || rows[0].FactVersion != 4 {
		t.Fatalf("automatic PostgreSQL history readback: rows=%d err=%v", len(rows), err)
	}
	graph := value.Graph()
	if graph.Provider() != policyir.ProviderPostgreSQL || len(graph.Roots()) != 2 {
		t.Fatal("reparent graph lost its provider or one owner")
	}
	for index, root := range graph.Roots() {
		owner := mutationHistoryGraphRowID(t, root.Identity, fixture.schema.UserID)
		for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
			children, complete := graph.Children(phase, graph.RootKeys()[index], policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
			want := 0
			if phase == policyhistory.Before && owner == 1 || phase == policyhistory.After && owner == 2 {
				want = 1
			}
			if !complete || len(children) != want {
				t.Fatalf("owner=%d phase=%d complete=%v rows=%d want=%d", owner, phase, complete, len(children), want)
			}
			if want == 1 && mutationHistoryGraphRowID(t, children[0], fixture.schema.AuthorID) != owner {
				t.Fatal("phase graph substituted the other owner")
			}
		}
	}
	// A later independent transaction cannot rewrite the earlier causal graph.
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 151), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}))); err != nil {
		t.Fatal(err)
	}
	var author string
	if err := fixture.app.database.GetContext(fixture.ctx, &author, `SELECT author_id FROM public.posts WHERE id=$1`, (golem.UUID{15: 151}).String()); err != nil || author != (golem.UUID{15: 1}).String() {
		t.Fatalf("later current reparent: author=%q err=%v", author, err)
	}
	again, err := mutationfact.DecodePolicyHistoryCausation(mutationHistoryPostgresRows(t, fixture, causal), fixture.app.eventSchemas)
	if err != nil || again.Digest() != value.Digest() {
		t.Fatalf("later transaction altered committed history: %v", err)
	}
	called := false
	err = SystemTransaction(fixture.ctx, fixture.app.System(), func(*SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error { called = true; return nil }, golem.Isolation(golem.ReadCommitted), golem.RetryConflicts(2))
	if err == nil || called || !strings.Contains(err.Error(), "requires Serializable") {
		t.Fatalf("explicit ReadCommitted was not refused before callback: called=%v err=%v", called, err)
	}
}

func TestMutationHistoryPostgreSQLConflictRetryDiscardsFirstCausation(t *testing.T) {
	fixture := mutationHistoryPostgresFixture(t)
	fixture.mutate(t, golem.EventCreated, 151, 1, "initial")
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	userTarget := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
	userName := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
	userUpdate := golem.GeneratedUpdateInput[policyFactRuntimeUser](fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, userName, "retry-owner"))
	postUpdate := mutationHistoryRuntimeUpdate(fixture, "retried-final")
	type attemptReady struct {
		causal string
		pid    int
	}
	ready, release := make(chan attemptReady, 1), make(chan struct{})
	done := make(chan error, 1)
	exited := make(chan struct{})
	defer func() {
		cancel()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		select {
		case <-exited:
		case <-deadline.C:
			t.Error("PostgreSQL retry transaction did not finish after cancellation")
		}
	}()
	var attempts atomic.Int32
	var committed string
	go func() {
		defer close(exited)
		done <- SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
			attempt := attempts.Add(1)
			if attempt > 2 {
				return fmt.Errorf("unexpected third callback")
			}
			var title string
			binding := transaction.system.executor
			// Establish A's real PostgreSQL snapshot before B changes this row.
			if err := binding.transaction.GetContext(ctx, &title, `SELECT title FROM public.posts WHERE id=$1`, (golem.UUID{15: 151}).String()); err != nil {
				return err
			}
			if attempt == 1 && title != "initial" || attempt == 2 && title != "concurrent" {
				return fmt.Errorf("attempt %d saw unexpected title %q", attempt, title)
			}
			// Stage a real captured write and fact before interference. User2 is
			// outside B's owner1 graph, avoiding a second manufactured conflict.
			if _, err := SystemTxUpdate(ctx, transaction, fixture.users, userTarget, userUpdate); err != nil {
				return err
			}
			state, err := binding.mutationState()
			if err != nil {
				return err
			}
			causal := state.facts[0].CausationID
			if attempt == 1 {
				var pid int
				if err := binding.transaction.GetContext(ctx, &pid, `SELECT pg_backend_pid()`); err != nil {
					return err
				}
				ready <- attemptReady{causal, pid}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if _, err := SystemTxUpdate(ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 151), postUpdate); err != nil {
				return err
			}
			committed = causal
			return nil
		}, golem.RetryConflicts(2))
	}()
	var first attemptReady
	select {
	case first = <-ready:
	case err := <-done:
		t.Fatalf("first transaction ended before barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var competingPID int
	err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if err := transaction.system.executor.transaction.GetContext(ctx, &competingPID, `SELECT pg_backend_pid()`); err != nil {
			return err
		}
		_, err := SystemTxUpdate(ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 151), mutationHistoryRuntimeUpdate(fixture, "concurrent"))
		return err
	})
	close(release)
	if err != nil {
		cancel()
		t.Fatalf("competing transaction did not commit: %v", err)
	}
	if competingPID == first.pid {
		t.Fatal("interference reused A's PostgreSQL connection")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if attempts.Load() != 2 || committed == "" || committed == first.causal {
		t.Fatalf("retry attempts=%d did not replace failed causation", attempts.Load())
	}
	if rows := mutationHistoryPostgresRows(t, fixture, first.causal); len(rows) != 0 {
		t.Fatal("failed attempt retained outbox facts or anchor")
	}
	var deliveries int
	if err := fixture.app.database.GetContext(ctx, &deliveries, `SELECT count(*) FROM "_golem"."_golem_outbox_delivery" WHERE causation_id=$1`, first.causal); err != nil || deliveries != 0 {
		t.Fatalf("failed attempt retained delivery: count=%d err=%v", deliveries, err)
	}
	rows := mutationHistoryPostgresRows(t, fixture, committed)
	value, err := mutationfact.DecodePolicyHistoryCausation(rows, fixture.app.eventSchemas)
	if err != nil || len(rows) != 2 {
		t.Fatalf("retried committed group: facts=%d err=%v", len(rows), err)
	}
	for index, row := range rows {
		envelope, err := mutationfact.ValidateStoredRow(row, fixture.app.eventSchemas)
		if err != nil {
			t.Fatal(err)
		}
		_, anchor := envelope.PolicyHistoryCompanion()
		if anchor != (index == 0) || row.TransactionOrdinal != int64(index+1) {
			t.Fatal("retried group has duplicate anchor or noncontiguous facts")
		}
	}
	if value.Graph().Provider() != policyir.ProviderPostgreSQL {
		t.Fatal("retried evidence lost PostgreSQL provenance")
	}
	var ownerBefore, postBefore bool
	for _, phaseRow := range value.Graph().Rows() {
		if phaseRow.Phase != policyhistory.Before {
			continue
		}
		switch phaseRow.Row.ModelID() {
		case policyir.ModelID(fixture.schema.User):
			if mutationHistoryGraphRowID(t, phaseRow.Row, fixture.schema.UserID) == 2 {
				mutationHistoryText(t, &phaseRow.Row, fixture.schema.UserName, "source")
				ownerBefore = true
			}
		case policyir.ModelID(fixture.schema.Post):
			if mutationHistoryGraphRowID(t, phaseRow.Row, fixture.schema.PostID) == 151 {
				mutationHistoryText(t, &phaseRow.Row, fixture.schema.PostTitle, "concurrent")
				postBefore = true
			}
		}
	}
	if !ownerBefore || !postBefore {
		t.Fatal("retry omitted the fresh boundary rows")
	}
	var title, ownerName string
	if err := fixture.app.database.GetContext(ctx, &title, `SELECT title FROM public.posts WHERE id=$1`, (golem.UUID{15: 151}).String()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.database.GetContext(ctx, &ownerName, `SELECT name FROM public.users WHERE id=$1`, (golem.UUID{15: 2}).String()); err != nil {
		t.Fatal(err)
	}
	if title != "retried-final" || ownerName != "retry-owner" {
		t.Fatalf("retry final rows title=%q owner=%q", title, ownerName)
	}
}

func TestMutationHistoryPostgreSQLRelationPhantomRefusesWriteSkew(t *testing.T) {
	fixture := mutationHistoryPostgresFixture(t)
	fixture.mutate(t, golem.EventCreated, 151, 1, "initial")
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	type extentReady struct {
		causal string
		pid    int
	}
	ready, release := make(chan extentReady, 1), make(chan struct{})
	done, exited := make(chan error, 1), make(chan struct{})
	defer func() {
		cancel()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		select {
		case <-exited:
		case <-deadline.C:
			t.Error("PostgreSQL phantom transaction did not finish after cancellation")
		}
	}()
	var aCausal string
	runA := func(wait bool, expectedChildren int) error {
		return SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
			binding := transaction.system.executor
			state, err := binding.mutationState()
			if err != nil {
				return err
			}
			aCausal = formatMutationUUID(state.causation)
			if _, err := SystemTxUpdate(ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 151), mutationHistoryRuntimeUpdate(fixture, "changed")); err != nil {
				return err
			}
			// Read the real capture owner's complete relation extent while A's
			// write is still private. This is only a read: automatic finalization
			// rebuilds and seals its own graph after the callback has finished.
			snapshot, err := newMutationHistorySnapshot(binding, fixture.app.registry)
			if err != nil {
				return err
			}
			graph, err := newMutationHistoryGraph(ctx, snapshot)
			if err != nil {
				return err
			}
			keys := graph.value.RootKeys()
			if len(keys) != 1 {
				return fmt.Errorf("A captured %d roots, want owner1 only", len(keys))
			}
			for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
				children, complete := graph.value.Children(phase, keys[0], policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
				if !complete || len(children) != expectedChildren {
					return fmt.Errorf("A phase %d extent complete=%v children=%d want=%d", phase, complete, len(children), expectedChildren)
				}
			}
			if wait {
				var pid int
				if err := binding.transaction.GetContext(ctx, &pid, `SELECT pg_backend_pid()`); err != nil {
					return err
				}
				ready <- extentReady{aCausal, pid}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}, golem.RetryConflicts(1))
	}
	go func() {
		defer close(exited)
		done <- runA(true, 1)
	}()
	var first extentReady
	select {
	case first = <-ready:
	case err := <-done:
		t.Fatalf("A ended before its captured extent barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	var bCausal string
	var bPID int
	runB := func(expectedTitle string) error {
		return SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
			binding := transaction.system.executor
			state, err := binding.mutationState()
			if err != nil {
				return err
			}
			bCausal = formatMutationUUID(state.causation)
			if err := binding.transaction.GetContext(ctx, &bPID, `SELECT pg_backend_pid()`); err != nil {
				return err
			}
			var title string
			if err := binding.transaction.GetContext(ctx, &title, `SELECT title FROM public.posts WHERE id=$1`, (golem.UUID{15: 151}).String()); err != nil {
				return err
			}
			if title != expectedTitle {
				return fmt.Errorf("B read title %q, want %q", title, expectedTitle)
			}
			// B reads A's old row but writes a different identity. A's earlier
			// captured extent excludes this insert: B->A and A->B form an SSI
			// cycle, without any common row being updated by both transactions.
			_, err = SystemTxCreate(ctx, transaction, fixture.posts, golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 152}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "phantom")))
			return err
		}, golem.RetryConflicts(1))
	}
	bErr := runB("initial")
	firstB := bCausal
	close(release)
	var aErr error
	select {
	case aErr = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if bPID == first.pid || bPID == 0 {
		t.Fatal("phantom interference did not use distinct PostgreSQL connections")
	}
	if (aErr == nil) == (bErr == nil) {
		t.Fatalf("SSI cycle must commit one transaction and refuse the other: A=%v B=%v", aErr, bErr)
	}
	failedCausal, refused := first.causal, aErr
	if bErr != nil {
		failedCausal, refused = firstB, bErr
	}
	var providerFailure *pgconn.PgError
	if !isConflict(refused) || !errors.As(refused, &providerFailure) || providerFailure.Code != "40001" {
		t.Fatalf("expected genuine PostgreSQL serialization refusal, got %v", refused)
	}
	assertDiscarded := func() {
		t.Helper()
		if rows := mutationHistoryPostgresRows(t, fixture, failedCausal); len(rows) != 0 {
			t.Fatal("refused write-skew transaction retained facts or a graph anchor")
		}
		var deliveries int
		if err := fixture.app.database.GetContext(ctx, &deliveries, `SELECT count(*) FROM "_golem"."_golem_outbox_delivery" WHERE causation_id=$1`, failedCausal); err != nil || deliveries != 0 {
			t.Fatalf("refused write-skew delivery count=%d err=%v", deliveries, err)
		}
	}
	assertDiscarded()
	assertHistory := func(causal string, before, after map[byte]string) {
		t.Helper()
		rows := mutationHistoryPostgresRows(t, fixture, causal)
		value, err := mutationfact.DecodePolicyHistoryCausation(rows, fixture.app.eventSchemas)
		if err != nil || len(rows) != 1 || rows[0].FactVersion != int64(mutationfact.HistoryFactVersion) {
			t.Fatalf("committed phantom history: facts=%d err=%v", len(rows), err)
		}
		graph := value.Graph()
		if graph.Provider() != policyir.ProviderPostgreSQL || len(graph.Roots()) != 1 || mutationHistoryGraphRowID(t, graph.Roots()[0].Identity, fixture.schema.UserID) != 1 {
			t.Fatal("phantom history lost its exact PostgreSQL owner")
		}
		for _, phase := range []policyhistory.Phase{policyhistory.Before, policyhistory.After} {
			want := before
			if phase == policyhistory.After {
				want = after
			}
			children, complete := graph.Children(phase, graph.RootKeys()[0], policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
			if !complete || len(children) != len(want) {
				t.Fatalf("committed phase %d extent complete=%v children=%d want=%d", phase, complete, len(children), len(want))
			}
			for _, child := range children {
				id := mutationHistoryGraphRowID(t, child, fixture.schema.PostID)
				title, exists := want[id]
				if !exists || mutationHistoryGraphRowID(t, child, fixture.schema.AuthorID) != 1 {
					t.Fatalf("unexpected phase %d child %d or owner", phase, id)
				}
				mutationHistoryText(t, &child, fixture.schema.PostTitle, title)
			}
		}
	}
	// PostgreSQL chooses the victim. Replay only that refused transaction after
	// the winner commits, then require history consistent with that serial order.
	if aErr == nil {
		assertHistory(first.causal, map[byte]string{151: "initial"}, map[byte]string{151: "changed"})
		if err := runB("changed"); err != nil {
			t.Fatalf("fresh B replay failed: %v", err)
		}
		if bCausal == failedCausal {
			t.Fatal("B replay reused the discarded causation")
		}
		assertHistory(bCausal, map[byte]string{151: "changed"}, map[byte]string{151: "changed", 152: "phantom"})
	} else {
		assertHistory(firstB, map[byte]string{151: "initial"}, map[byte]string{151: "initial", 152: "phantom"})
		if err := runA(false, 2); err != nil {
			t.Fatalf("fresh A replay failed: %v", err)
		}
		if aCausal == failedCausal {
			t.Fatal("A replay reused the discarded causation")
		}
		assertHistory(aCausal, map[byte]string{151: "initial", 152: "phantom"}, map[byte]string{151: "changed", 152: "phantom"})
	}
	assertDiscarded()
	var currentCount int
	if err := fixture.app.database.GetContext(ctx, &currentCount, `SELECT count(*) FROM public.posts WHERE author_id=$1`, (golem.UUID{15: 1}).String()); err != nil || currentCount != 2 {
		t.Fatalf("serial replay current extent count=%d err=%v", currentCount, err)
	}
	for id, expectedTitle := range map[byte]string{151: "changed", 152: "phantom"} {
		var title string
		if err := fixture.app.database.GetContext(ctx, &title, `SELECT title FROM public.posts WHERE id=$1`, (golem.UUID{15: id}).String()); err != nil || title != expectedTitle {
			t.Fatalf("serial replay current post %d title=%q want=%q err=%v", id, title, expectedTitle, err)
		}
	}
}
