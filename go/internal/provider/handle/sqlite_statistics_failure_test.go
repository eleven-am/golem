package handle

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

type maintenanceRecorder struct {
	mu      sync.Mutex
	records []observe.Observation
}

func (recorder *maintenanceRecorder) ObserveGolem(_ context.Context, value observe.Observation) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.records = append(recorder.records, value)
}

func (recorder *maintenanceRecorder) failures() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	count := 0
	for _, record := range recorder.records {
		if record.Kind() == observe.KindRuntime && record.Operation() == observe.OperationRuntimeMaintenance && record.Phase() == observe.PhaseApply &&
			record.Outcome() == observe.OutcomeFailure && record.Reason() == observe.ReasonProvider && record.Provider() == golem.SQLite {
			count++
		}
	}
	return count
}

func TestSQLiteStatisticsRefreshFailureIsObservedAndTheScheduleContinues(t *testing.T) {
	var calls, failing atomic.Int64
	withSQLiteStatisticsSchedule(t, time.Millisecond, func(context.Context, *sqlx.DB) error {
		calls.Add(1)
		if failing.Add(-1) >= 0 {
			return errors.New("database is locked")
		}
		return nil
	})
	database, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "failing-statistics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	recorder := &maintenanceRecorder{}
	AttachMaintenanceObserver(database, recorder)
	calls.Store(0)
	failing.Store(2)
	deadline := time.Now().Add(10 * time.Second)
	for calls.Load() < 5 || recorder.failures() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("refresh calls=%d observed failures=%d; a failing refresh must be reported and the schedule must keep running", calls.Load(), recorder.failures())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := recorder.failures(); got != 2 {
		t.Fatalf("observed %d refresh failures, want exactly the 2 that failed", got)
	}
}
