package postgresql

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
)

func renamedKeyFixture(t *testing.T, table, renamedTable, renamedKey physical.PhysicalName) (physical.PhysicalSchema, physical.PhysicalSchema) {
	t.Helper()
	provider := New()
	before, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.Tables = append([]physical.PhysicalTable(nil), before.Tables...)
	for index := range after.Tables {
		if after.Tables[index].Name != table {
			continue
		}
		key := *after.Tables[index].PrimaryKey
		key.Name = renamedKey
		after.Tables[index].Name = renamedTable
		after.Tables[index].PrimaryKey = &key
	}
	after, err = physical.Normalize(after)
	if err != nil {
		t.Fatal(err)
	}
	return before, after
}

func TestPlanIncrementalRenamesAReferencedKeyInsteadOfDroppingIt(t *testing.T) {
	before, after := renamedKeyFixture(t, "users", "members", "pk_members")
	plan, err := New().PlanIncremental(reviewedPostgreSQLEntry(t, "002_rename_users", before, after, nil))
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	if !strings.Contains(sql, `ALTER TABLE "reviewed"."members" RENAME CONSTRAINT "pk_users" TO "pk_members";`) {
		t.Fatalf("referenced key was not renamed:\n%s", sql)
	}
	if strings.Contains(sql, `DROP CONSTRAINT "pk_users"`) || strings.Contains(sql, `ADD CONSTRAINT "pk_members"`) {
		t.Fatalf("referenced key is still dropped and recreated:\n%s", sql)
	}
}

func TestPlanIncrementalKeepsReleasedDropAndAddForAnUnreferencedKeyRename(t *testing.T) {
	before, after := renamedKeyFixture(t, "posts", "articles", "pk_articles")
	plan, err := New().PlanIncremental(reviewedPostgreSQLEntry(t, "002_rename_posts", before, after, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "reviewed"."posts" RENAME TO "articles";
ALTER TABLE "reviewed"."articles" DROP CONSTRAINT "pk_posts";
ALTER TABLE "reviewed"."articles" ADD CONSTRAINT "pk_articles" PRIMARY KEY ("id");
`
	if !strings.Contains(plan.SQL(), want) {
		t.Fatalf("unreferenced key rename changed its released rendering:\n%s", plan.SQL())
	}
}
