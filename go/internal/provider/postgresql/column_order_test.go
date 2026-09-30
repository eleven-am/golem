package postgresql

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	providerdrift "github.com/eleven-am/golem/go/internal/provider/drift"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func reorderedPostsFixture(t *testing.T, namespace physical.PhysicalName) (physical.PhysicalSchema, physical.PhysicalSchema) {
	t.Helper()
	before, err := New().Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.Tables = append([]physical.PhysicalTable(nil), before.Tables...)
	for index := range after.Tables {
		if after.Tables[index].Name != "posts" {
			continue
		}
		columns := append([]physical.PhysicalColumn(nil), after.Tables[index].Columns...)
		var day, clock int
		for columnIndex, column := range columns {
			switch column.Name {
			case "day":
				day = columnIndex
			case "clock":
				clock = columnIndex
			}
		}
		columns[day].Ordinal, columns[clock].Ordinal = columns[clock].Ordinal, columns[day].Ordinal
		after.Tables[index].Columns = columns
	}
	after, err = physical.Normalize(after)
	if err != nil {
		t.Fatal(err)
	}
	return before, after
}

func TestPlanIncrementalRecordsAFieldReorderWithoutARebuild(t *testing.T) {
	before, after := reorderedPostsFixture(t, "reviewed")
	plan, err := New().PlanIncremental(reviewedPostgreSQLEntry(t, "002_reorder", before, after, nil))
	if err != nil {
		t.Fatal(err)
	}
	if sql := plan.SQL(); sql != "" {
		t.Fatalf("a field reorder rendered DDL:\n%s", sql)
	}
}

func TestLiveVerifyAcceptsPhysicalColumnOrderButNotAMissingColumn(t *testing.T) {
	dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
	provider := New()
	db, _, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, after := reorderedPostsFixture(t, "golem_column_order")
	if err = provider.ApplyInitial(context.Background(), db, before); err != nil {
		t.Fatal(err)
	}
	if err = provider.Verify(context.Background(), db, after); err != nil {
		t.Fatalf("physical column order was treated as drift: %v", err)
	}
	if _, err = db.Exec(`ALTER TABLE "golem_column_order"."posts" DROP COLUMN "clock"`); err != nil {
		t.Fatal(err)
	}
	err = provider.Verify(context.Background(), db, after)
	object, isDrift := providerdrift.Inspect(err)
	if err == nil || !isDrift || object.Name != "clock" || object.Table != "posts" {
		t.Fatalf("missing column verification = %v (%#v)", err, object)
	}
}
