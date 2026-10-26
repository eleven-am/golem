package runtime

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/jackc/pgx/v5/pgconn"
)

type optionCallerTx = CallerTx[graphMutationPrincipal, graphMutationActor]

func retryConflictFailure() error {
	return golem.RuntimeOperationError(golem.CodeConflict, "test", golem.ModelID{}, golem.FieldID{}, "attempt conflicted", nil)
}

func requireTransactionCode(t testing.TB, err error, code golem.ErrorCode) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("err=%v; want %s", err, code)
	}
}

func TestTransactionBeginOptionsSendALevelOnlyForPostgreSQLSerializable(t *testing.T) {
	if options := transactionBeginOptions(policyir.ProviderPostgreSQL, golem.Serializable); options == nil || options.Isolation != sql.LevelSerializable || options.ReadOnly {
		t.Fatalf("postgresql serializable options=%#v; want LevelSerializable", options)
	}
	for _, provider := range []policyir.Provider{policyir.ProviderPostgreSQL, policyir.ProviderSQLite} {
		if options := transactionBeginOptions(provider, golem.ReadCommitted); options != nil {
			t.Fatalf("%v read committed options=%#v; want the driver default", provider, options)
		}
	}
	if options := transactionBeginOptions(policyir.ProviderSQLite, golem.Serializable); options != nil {
		t.Fatalf("sqlite serializable options=%#v; want no level sent", options)
	}
}

func TestSQLiteSerializableTransactionCommits(t *testing.T) {
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	ctx := context.Background()
	if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *lockKeyTx) error {
		_, err := SystemTxCreate(ctx, transaction, fixture.userDescriptor, fixture.userCreate(1, "serializable"))
		return err
	}, golem.Isolation(golem.Serializable)); err != nil {
		t.Fatal(err)
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if err := CallerTransaction(ctx, caller, func(transaction *optionCallerTx) error {
		_, err := CallerTxCreate(ctx, transaction, fixture.userDescriptor, fixture.userCreate(2, "serializable"))
		return err
	}, golem.Isolation(golem.Serializable), golem.RetryConflicts(2)); err != nil {
		t.Fatal(err)
	}
	if got := socialCount(t, fixture, fixture.schema.User); got != 2 {
		t.Fatalf("users=%d; want both serializable transactions committed", got)
	}
}

func TestRetryConflictsRunsTheCallbackUntilItCommitsAndAfterCommitHooksOnce(t *testing.T) {
	log := &socialHookLog{}
	fixture := newSocialMutationFixture(t, golem.ModelID{}, log)
	ctx := context.Background()
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := CallerTransaction(ctx, caller, func(transaction *optionCallerTx) error {
		calls++
		if _, err := CallerTxCreate(ctx, transaction, fixture.userDescriptor, fixture.userCreate(byte(calls), "attempt")); err != nil {
			return err
		}
		if calls < 3 {
			return retryConflictFailure()
		}
		return nil
	}, golem.RetryConflicts(3)); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d; want 3", calls)
	}
	if len(log.before) != 3 || len(log.afterCommit) != 1 {
		t.Fatalf("before=%v afterCommit=%v; want three attempts and one after-commit hook", log.before, log.afterCommit)
	}
	if got := socialCount(t, fixture, fixture.schema.User); got != 1 {
		t.Fatalf("users=%d; want only the committed attempt's row", got)
	}
}

func TestRetryConflictsStopsAfterTheLastAttempt(t *testing.T) {
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	ctx := context.Background()
	for _, attempts := range []int{1, 3, 10} {
		calls := 0
		err := SystemTransaction(ctx, fixture.app.System(), func(*lockKeyTx) error {
			calls++
			return retryConflictFailure()
		}, golem.RetryConflicts(attempts))
		requireTransactionCode(t, err, golem.CodeConflict)
		if calls != attempts {
			t.Fatalf("attempts=%d calls=%d; want every attempt used", attempts, calls)
		}
	}
}

func TestTransactionsWithoutRetryNeverReplayTheCallback(t *testing.T) {
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	ctx := context.Background()
	calls := 0
	err := SystemTransaction(ctx, fixture.app.System(), func(*lockKeyTx) error {
		calls++
		return retryConflictFailure()
	}, golem.Isolation(golem.Serializable))
	requireTransactionCode(t, err, golem.CodeConflict)
	if calls != 1 {
		t.Fatalf("calls=%d; want the default contract to run the callback once", calls)
	}
}

func TestRetryConflictsDoesNotRetryOtherFailures(t *testing.T) {
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	ctx := context.Background()
	plain := errors.New("application failure")
	for name, failure := range map[string]error{
		"plain":         plain,
		"bad input":     golem.RuntimeOperationError(golem.CodeBadUserInput, "test", golem.ModelID{}, golem.FieldID{}, "bad input", nil),
		"forbidden":     golem.RuntimeOperationError(golem.CodeForbidden, "test", golem.ModelID{}, golem.FieldID{}, "forbidden", nil),
		"raw provider":  &pgconn.PgError{Code: "40001"},
		"context error": context.Canceled,
	} {
		calls := 0
		err := SystemTransaction(ctx, fixture.app.System(), func(*lockKeyTx) error {
			calls++
			return failure
		}, golem.RetryConflicts(5))
		if !errors.Is(err, failure) {
			t.Fatalf("%s: err=%v; want the callback's failure", name, err)
		}
		if calls != 1 {
			t.Fatalf("%s: calls=%d; want no retry", name, calls)
		}
	}
}

func TestRetryConflictsStopsWhenTheContextEnds(t *testing.T) {
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := SystemTransaction(ctx, fixture.app.System(), func(*lockKeyTx) error {
		calls++
		cancel()
		return retryConflictFailure()
	}, golem.RetryConflicts(5))
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d; want the cancellation after one attempt", err, calls)
	}
}

func TestTransactionOptionsAreValidatedBeforeTheCallbackRuns(t *testing.T) {
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	ctx := context.Background()
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	for name, options := range map[string][]golem.TransactionOption{
		"zero attempts":   {golem.RetryConflicts(0)},
		"eleven attempts": {golem.RetryConflicts(11)},
		"repeatable read": {golem.Isolation("repeatable_read")},
	} {
		calls := 0
		if err := SystemTransaction(ctx, fixture.app.System(), func(*lockKeyTx) error { calls++; return nil }, options...); err == nil {
			t.Fatalf("%s: system transaction accepted invalid options", name)
		}
		if err := CallerTransaction(ctx, caller, func(*optionCallerTx) error { calls++; return nil }, options...); err == nil {
			t.Fatalf("%s: caller transaction accepted invalid options", name)
		}
		if calls != 0 {
			t.Fatalf("%s: calls=%d; want the callback never run", name, calls)
		}
	}
}

func TestReadFaultCodeClassifiesProviderInterferenceAsConflict(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		if got := readFaultCode(&pgconn.PgError{Code: code}); got != golem.CodeConflict {
			t.Fatalf("%s read fault=%s; want CONFLICT", code, got)
		}
	}
	if got := readFaultCode(&pgconn.PgError{Code: "42P01"}); got != golem.CodeBadUserInput {
		t.Fatalf("undefined table read fault=%s; want BAD_USER_INPUT", got)
	}
	if got := readFaultCode(errors.New("opaque")); got != golem.CodeBadUserInput {
		t.Fatalf("opaque read fault=%s; want BAD_USER_INPUT", got)
	}
}
