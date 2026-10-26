package golem

import "testing"

func TestRuntimeTransactionOptionsDefaultToReadCommittedAndOneAttempt(t *testing.T) {
	level, attempts, err := RuntimeTransactionOptions(nil)
	if err != nil || level != ReadCommitted || attempts != 1 {
		t.Fatalf("level=%q attempts=%d err=%v; want read committed, one attempt", level, attempts, err)
	}
}

func TestRuntimeTransactionOptionsAcceptBothLevelsAndTheAttemptBounds(t *testing.T) {
	for _, want := range []IsolationLevel{ReadCommitted, Serializable} {
		for _, wantAttempts := range []int{1, 10} {
			level, attempts, err := RuntimeTransactionOptions([]TransactionOption{Isolation(want), RetryConflicts(wantAttempts)})
			if err != nil || level != want || attempts != wantAttempts {
				t.Fatalf("level=%q attempts=%d err=%v; want %q and %d", level, attempts, err, want, wantAttempts)
			}
		}
	}
}

func TestRuntimeTransactionOptionsRefuseInvalidOptions(t *testing.T) {
	refused := map[string][]TransactionOption{
		"zero attempts":        {RetryConflicts(0)},
		"negative attempts":    {RetryConflicts(-1)},
		"eleven attempts":      {RetryConflicts(11)},
		"empty level":          {Isolation("")},
		"repeatable read":      {Isolation("repeatable_read")},
		"nil option":           {nil},
		"isolation twice":      {Isolation(Serializable), Isolation(Serializable)},
		"retry attempts twice": {RetryConflicts(2), RetryConflicts(3)},
	}
	for name, options := range refused {
		if level, attempts, err := RuntimeTransactionOptions(options); err == nil || level != "" || attempts != 0 {
			t.Errorf("%s: level=%q attempts=%d err=%v; want a refusal", name, level, attempts, err)
		}
	}
}
