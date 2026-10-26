package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

type p11Tx = p10operations.CallerTx[p10operations.Principal]

func (fixture *p10OperationFixture) txCreate(ctx context.Context, tx *p11Tx, id golem.UUID) error {
	_, err := tx.Invites.Create(ctx, p10operations.Invites.Create(
		p10operations.Invites.ID.Create(id), p10operations.Invites.TeamID.Create(fixture.alpha),
		p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("tx@example.test"),
		p10operations.Invites.Status.Create("pending"),
	))
	return err
}

func (fixture *p10OperationFixture) refusedOutsideRun(t *testing.T, id golem.UUID) error {
	t.Helper()
	var refusal error
	err := fixture.caller(t, "alpha").Transaction(context.Background(), func(tx *p11Tx) error {
		refusal = fixture.txCreate(context.Background(), tx, id)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil {
		t.Fatal("the caller transaction created a model its policy closes")
	}
	return refusal
}

func assertP11OperationEnded(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "P5_CUSTOM_OPERATION: operation has ended") {
		t.Fatalf("%s: err = %v, want the operation-ended refusal", label, err)
	}
}

func (fixture *p10OperationFixture) operationRecords() []p10OperationRecord {
	var result []p10OperationRecord
	for _, record := range fixture.observer.snapshot("") {
		if record.kind == observe.KindMutation && record.operation == observe.OperationMutationOperation {
			result = append(result, record)
		}
	}
	return result
}

func TestTxOperationWithinCreateInsideCallerTxCommitsAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		fixture.observer.reset()
		var created string
		err := caller.Transaction(ctx, func(tx *p11Tx) error {
			var runErr error
			created, runErr = p10operations.Run(ctx, tx, p10operations.RecordInvite, fixture.inviteArgs(p10OperationID(t, 1100), " Record@Example.TEST "))
			return runErr
		})
		if err != nil || created != p10OperationID(t, 1100).String() {
			t.Fatalf("Run inside a caller transaction = %q, %v", created, err)
		}
		if exists, email, _ := fixture.inviteExists(t, p10OperationID(t, 1100)); !exists || email != "record@example.test" {
			t.Fatalf("committed invite exists=%t email=%q", exists, email)
		}
		records := fixture.operationRecords()
		if len(records) != 1 || records[0].outcome != observe.OutcomeSuccess {
			t.Fatalf("mutation.operation records = %+v", records)
		}
		rollback := errors.New("requested rollback")
		err = caller.Transaction(ctx, func(tx *p11Tx) error {
			if _, runErr := p10operations.Run(ctx, tx, p10operations.RecordInvite, fixture.inviteArgs(p10OperationID(t, 1101), "rollback@example.test")); runErr != nil {
				return runErr
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("rolled-back transaction = %v", err)
		}
		if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, 1101)); exists {
			t.Fatal("a rolled-back operation write persisted")
		}
	})
}

func TestTxOperationGrantIsRefusedOutsideRunAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		generic := fixture.genericCreate(ctx, fixture.caller(t, "alpha"), p10OperationID(t, 1200))
		if generic == nil {
			t.Fatal("the caller created a model its policy closes")
		}
		assertP10SameRefusal(t, "caller transaction outside Run", generic, fixture.refusedOutsideRun(t, p10OperationID(t, 1201)))
		var direct error
		if err := fixture.caller(t, "alpha").Transaction(ctx, func(tx *p11Tx) error {
			_, direct = p10operations.RecordInvite(ctx, tx, fixture.inviteArgs(p10OperationID(t, 1202), "direct@example.test"))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		assertP10SameRefusal(t, "operation function called without Run", generic, direct)
		for _, id := range []int{1200, 1201, 1202} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists {
				t.Fatalf("a write outside Run was committed: %d", id)
			}
		}
	})
}

func TestTxOperationGrantEndsWhenTheOperationReturnsAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		var retained *p11Tx
		var retainedCtx context.Context
		var waitedErr error
		outlive := make(chan struct{})
		outlived := make(chan error, 1)
		p10operations.SetTxProbe(func(ctx context.Context, _ string, scoped *p11Tx) error {
			retained, retainedCtx = scoped, ctx
			var wait sync.WaitGroup
			wait.Add(1)
			go func() {
				defer wait.Done()
				waitedErr = fixture.txCreate(ctx, scoped, p10OperationID(t, 1300))
			}()
			wait.Wait()
			go func() {
				<-outlive
				outlived <- fixture.txCreate(ctx, scoped, p10OperationID(t, 1301))
			}()
			return nil
		})
		var afterReturn, afterReturnBackground error
		err := fixture.caller(t, "alpha").Transaction(ctx, func(tx *p11Tx) error {
			if _, runErr := p10operations.Run(ctx, tx, p10operations.RecordInvite, fixture.inviteArgs(p10OperationID(t, 1302), "owner@example.test")); runErr != nil {
				return runErr
			}
			afterReturn = fixture.txCreate(retainedCtx, retained, p10OperationID(t, 1303))
			afterReturnBackground = fixture.txCreate(context.Background(), retained, p10OperationID(t, 1304))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if waitedErr != nil {
			t.Fatalf("a goroutine the operation waited for lost the grant: %v", waitedErr)
		}
		assertP11OperationEnded(t, "retained scoped transaction inside the outer callback", afterReturn)
		assertP11OperationEnded(t, "retained scoped transaction with a fresh context", afterReturnBackground)
		assertP11OperationEnded(t, "retained scoped transaction after commit", fixture.txCreate(context.Background(), retained, p10OperationID(t, 1305)))
		close(outlive)
		assertP11OperationEnded(t, "goroutine that outlived the operation", <-outlived)
		for id, want := range map[int]bool{1300: true, 1302: true, 1301: false, 1303: false, 1304: false, 1305: false} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists != want {
				t.Fatalf("invite %d exists=%t want %t", id, exists, want)
			}
		}
	})
}

func TestTxOperationOuterTransactionNeverGainsTheGrantAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		refusal := fixture.refusedOutsideRun(t, p10OperationID(t, 1400))
		var outer *p11Tx
		var hooked, hookedRan bool
		var hookedErr error
		p10operations.SetInviteHook(func(ctx context.Context) error {
			if hooked {
				return nil
			}
			hooked = true
			hookedErr = fixture.txCreate(ctx, outer, p10OperationID(t, 1401))
			hookedRan = true
			return nil
		})
		var during, after error
		err := fixture.caller(t, "alpha").Transaction(ctx, func(tx *p11Tx) error {
			outer = tx
			p10operations.SetTxProbe(func(operationCtx context.Context, _ string, _ *p11Tx) error {
				during = fixture.txCreate(operationCtx, tx, p10OperationID(t, 1402))
				return nil
			})
			if _, runErr := p10operations.Run(ctx, tx, p10operations.RecordInvite, fixture.inviteArgs(p10OperationID(t, 1403), "owner@example.test")); runErr != nil {
				return runErr
			}
			after = fixture.txCreate(ctx, tx, p10OperationID(t, 1404))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !hookedRan {
			t.Fatal("the create hook did not run")
		}
		assertP10SameRefusal(t, "outer transaction under the operation's write lease", refusal, hookedErr)
		assertP10SameRefusal(t, "outer transaction inside the operation's call chain", refusal, during)
		assertP10SameRefusal(t, "outer transaction after the operation", refusal, after)
		for id, want := range map[int]bool{1401: false, 1402: false, 1403: true, 1404: false} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists != want {
				t.Fatalf("invite %d exists=%t want %t", id, exists, want)
			}
		}
	})
}

func TestTxOperationNestedRunDoesNotAccumulateGrantsAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		seeded := p10OperationID(t, 1500)
		fixture.seedInvite(t, seeded)
		refusal := fixture.refusedOutsideRun(t, p10OperationID(t, 1501))
		var nestedStatus string
		var nestedRunErr, nestedCreateErr error
		p10operations.SetTxProbe(func(ctx context.Context, operation string, scoped *p11Tx) error {
			switch operation {
			case "recordInvite":
				nestedStatus, nestedRunErr = p10operations.Run(ctx, scoped, p10operations.MarkInvite, p10operations.AcceptArgs{ID: seeded})
			case "markInvite":
				nestedCreateErr = fixture.txCreate(ctx, scoped, p10OperationID(t, 1502))
			}
			return nil
		})
		err := fixture.caller(t, "alpha").Transaction(ctx, func(tx *p11Tx) error {
			_, runErr := p10operations.Run(ctx, tx, p10operations.RecordInvite, fixture.inviteArgs(p10OperationID(t, 1503), "outer@example.test"))
			return runErr
		})
		if err != nil {
			t.Fatal(err)
		}
		if nestedRunErr != nil || nestedStatus != "marked" {
			t.Fatalf("nested Run = %q, %v", nestedStatus, nestedRunErr)
		}
		assertP10SameRefusal(t, "nested operation using the outer operation's grant", refusal, nestedCreateErr)
		if _, _, status := fixture.inviteExists(t, seeded); status != "marked" {
			t.Fatalf("nested operation status=%q", status)
		}
		for id, want := range map[int]bool{1502: false, 1503: true} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists != want {
				t.Fatalf("invite %d exists=%t want %t", id, exists, want)
			}
		}
		p10operations.SetTxProbe(nil)
		var markErr error
		err = fixture.caller(t, "alpha").Transaction(ctx, func(tx *p11Tx) error {
			_, markErr = tx.Invites.Update(ctx, p10operations.Invites.ByID.Value(seeded), p10operations.Invites.Update(p10operations.Invites.Status.Set("outside")))
			return markErr
		})
		if markErr == nil || err == nil {
			t.Fatalf("the status grant leaked out of the nested operation: update=%v transaction=%v", markErr, err)
		}
		if _, _, status := fixture.inviteExists(t, seeded); status != "marked" {
			t.Fatalf("a refused status update changed status to %q", status)
		}
	})
}

func TestTxOperationPanicClosesTheGateAndReraisesAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		var retained *p11Tx
		var retainedCtx context.Context
		p10operations.SetTxProbe(func(ctx context.Context, _ string, scoped *p11Tx) error {
			retained, retainedCtx = scoped, ctx
			if err := fixture.txCreate(ctx, scoped, p10OperationID(t, 1600)); err != nil {
				return err
			}
			panic("tx operation panic")
		})
		fixture.observer.reset()
		recovered := func() (value any) {
			defer func() { value = recover() }()
			_ = fixture.caller(t, "alpha").Transaction(ctx, func(tx *p11Tx) error {
				_, err := p10operations.Run(ctx, tx, p10operations.RecordInvite, fixture.inviteArgs(p10OperationID(t, 1601), "panic@example.test"))
				return err
			})
			return nil
		}()
		if recovered != "tx operation panic" {
			t.Fatalf("recovered %v, want the operation's own panic", recovered)
		}
		assertP11OperationEnded(t, "scoped transaction after a panic", fixture.txCreate(retainedCtx, retained, p10OperationID(t, 1602)))
		records := fixture.operationRecords()
		if len(records) != 1 || records[0].outcome != observe.OutcomeFailure {
			t.Fatalf("mutation.operation records after a panic = %+v", records)
		}
		for _, id := range []int{1600, 1601, 1602} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists {
				t.Fatalf("a write of the panicked transaction persisted: %d", id)
			}
		}
	})
}

func TestRunAndMutateRefuseTheOtherOperationKindAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		wrapped := func(ctx context.Context, tx *p11Tx, arguments p10operations.InviteArgs) (string, error) {
			return p10operations.RecordInvite(ctx, tx, arguments)
		}
		fixture.observer.reset()
		p10operations.Reset(nil)
		refusals := map[string]error{}
		_, refusals["Mutate with a transaction operation"] = p10operations.MutateTxOperation(ctx, caller, fixture.inviteArgs(p10OperationID(t, 1700), "mutate@example.test"))
		if err := caller.Transaction(ctx, func(tx *p11Tx) error {
			_, refusals["Run with a custom mutation"] = p10operations.RunCustomMutationAsTxOperation(ctx, tx, fixture.inviteArgs(p10OperationID(t, 1701), "run@example.test"))
			_, refusals["Run with an unregistered function"] = p10operations.Run(ctx, tx, wrapped, fixture.inviteArgs(p10OperationID(t, 1702), "wrapped@example.test"))
			_, refusals["Run without a transaction"] = p10operations.Run(ctx, nil, p10operations.RecordInvite, fixture.inviteArgs(p10OperationID(t, 1703), "nil@example.test"))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for name, err := range refusals {
			if err == nil || !strings.Contains(err.Error(), "P5_CUSTOM_OPERATION") {
				t.Fatalf("%s: %v", name, err)
			}
		}
		for _, record := range fixture.observer.snapshot("") {
			if record.kind == observe.KindMutation {
				t.Fatalf("a refused operation was observed: %+v", record)
			}
		}
		if record := p10operations.Snapshot(); len(record.Resolvers) != 0 || len(record.Hooks) != 0 {
			t.Fatalf("refused operations ran application code: %+v", record)
		}
		for _, id := range []int{1700, 1701, 1702, 1703} {
			if exists, _, _ := fixture.inviteExists(t, p10OperationID(t, id)); exists {
				t.Fatalf("a refused operation wrote invite %d", id)
			}
		}
	})
}
