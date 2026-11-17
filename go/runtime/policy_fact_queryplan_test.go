package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queryplan"
)

// Named facts remain explainable without disclosing their private source rows.
// The fixture requires the compiled historical writer and capture inventory.
func TestPolicyFactSQLiteExplainFindMany(t *testing.T) {
	for _, negative := range []bool{false, true} {
		name := "positive-exists"
		if negative {
			name = "negative-exists"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newHistoricalPolicyFactRuntimeFixture(t, negative)
			ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
			defer cancel()
			// Keep the dependency invisible through its ordinary Caller policy;
			// only the actor-scoped named fact authorizes the source User.
			fixture.ctx = ctx
			fixture.mutate(t, golem.EventCreated, 12, 1, "ready")
			caller := fixture.caller(t)
			id := golem.GeneratedEqualField[policyFactRuntimeUser, golem.UUID](fixture.schema.UserID)
			options := []golem.ReadOption[policyFactRuntimeUser]{
				golem.Select[policyFactRuntimeUser](id),
				golem.Take[policyFactRuntimeUser](100),
			}
			rows, err := CallerFindMany(ctx, caller, fixture.users, options...)
			want := 1
			if negative {
				want = 0
			}
			if err != nil || len(rows) != want {
				t.Fatalf("ordinary authorized read: rows=%d want=%d err=%v", len(rows), want, err)
			}
			if len(rows) != 0 {
				got, present := golem.Value(rows[0], id).Get()
				if !present || got != fixture.principal.ID {
					t.Fatal("ordinary read returned a row outside the actor scope")
				}
			}
			report, err := CallerExplainFindMany(ctx, caller, fixture.users, options...)
			if err != nil {
				t.Fatalf("public ExplainFindMany: %v (context=%v)", err, ctx.Err())
			}
			if report.Provider() != golem.SQLite || report.Operation() != queryplan.OperationFindMany || report.RootModelID() != fixture.schema.User || len(report.Statements()) == 0 || report.MinimumExecutionStatements() != 1 || report.MaximumExecutionStatements() < 1 {
				t.Fatal("public report does not describe the authorized SQLite user read")
			}
		})
	}
}
