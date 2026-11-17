package postgresql

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestPostgreSQLDataCorrectionClosedExpressionGrammar(t *testing.T) {
	table := physical.PhysicalTable{Name: "items", Columns: []physical.PhysicalColumn{{Name: "title"}, {Name: "count"}, {Name: "ready"}}}
	target := table.Columns[0]
	for _, command := range []string{
		`UPDATE "reviewed"."items" SET "title" = CASE WHEN "count" + 1 >= 3 AND NOT "ready" THEN 'a''b\c' ELSE NULL END WHERE "title" IS NOT NULL;`,
		`update reviewed.items set title = 'fixed' where count=2;`,
		`UPDATE reviewed.items SET title = 'literal -- /* */';`,
		`UPDATE reviewed.items SET title = CASE WHEN count = 1 THEN 'one' WHEN count = 2 THEN 'two' ELSE title END;`,
	} {
		generated, err := renderPostgreSQLCorrection([]byte(command+"\n"), "reviewed", table, target)
		if err != nil {
			t.Fatalf("closed expression rejected: %v", err)
		}
		if !strings.HasPrefix(generated, `UPDATE "reviewed"."items" SET "title" = `) {
			t.Fatalf("unbound rendering: %s", generated)
		}
	}
	for _, fragment := range []string{
		`lower(title)`, `pg_catalog.lower(title)`, `title::text`, `(SELECT title FROM reviewed.items)`,
		`$1`, `E'escape'`, `title COLLATE "C"`, `title; DELETE FROM reviewed.items`,
		`title, count=9`, `unknown`, `"unknown"`, `1e+`, `1/*hidden*/+2`, `count--count`, `count -- comment`, `count/*comment*/`, `count*/count`, `CASE title WHEN 'old' THEN 'new' END`,
		strings.Repeat("(", scopecontract.MaxDepth+1) + "title" + strings.Repeat(")", scopecontract.MaxDepth+1),
	} {
		if generated, err := renderPostgreSQLCorrection([]byte(`UPDATE reviewed.items SET title = `+fragment+";\n"), "reviewed", table, target); err == nil {
			t.Fatalf("unsupported syntax rendered: %s", generated)
		}
	}
	// A quoted physical name is a column, even when it resembles a keyword.
	table.Columns = append(table.Columns, physical.PhysicalColumn{Name: "NULL"})
	generated, err := renderPostgreSQLCorrection([]byte("UPDATE reviewed.items SET title = \"NULL\";\n"), "reviewed", table, target)
	if err != nil || !strings.HasSuffix(generated, ` = "NULL"`) {
		t.Fatalf("quoted column binding: %s %v", generated, err)
	}
}
