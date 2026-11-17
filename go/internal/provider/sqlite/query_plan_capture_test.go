package sqlite_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queryplancapture "github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/eleven-am/golem/go/internal/queryplanreport"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func TestQueryPlanSQLiteMapsFullPrimaryAndOrdinaryIndexWithoutRawDetail(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	table := physicalTable(t, fixture.SQLite, fixture.Post)
	idColumn := physicalColumn(t, table, fixture.PostID)
	authorColumn := physicalColumn(t, table, fixture.AuthorID)
	titleColumn := physicalColumn(t, table, fixture.PostTitle)
	index := physicalIndex(t, table, fixture.AuthorID)

	database := sqlx.MustOpen("sqlite", ":memory:")
	t.Cleanup(func() { _ = database.Close() })
	database.MustExec(fmt.Sprintf("CREATE TABLE %q (%q BLOB PRIMARY KEY, %q BLOB NOT NULL, %q TEXT NOT NULL)", table.Name, idColumn.Name, authorColumn.Name, titleColumn.Name))
	database.MustExec(fmt.Sprintf("CREATE INDEX %q ON %q (%q)", index.Name, table.Name, authorColumn.Name))

	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	fact, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == "plan_root" }, fixture.Post, golem.RelationID{}, []golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, queryplancapture.AliasPhysicalAccess)
	if err != nil {
		t.Fatal(err)
	}
	aliases := queryplancapture.NewAliasMap(fact)

	tests := []struct {
		name       string
		statement  string
		arguments  []any
		wantKind   queryplancapture.NodeKind
		wantAccess queryplancapture.AccessKind
	}{
		{name: "full scan", statement: fmt.Sprintf("SELECT %q FROM %q AS plan_root WHERE %q LIKE ?", titleColumn.Name, table.Name, titleColumn.Name), arguments: []any{"never-executed-value%"}, wantKind: queryplancapture.NodeAccess, wantAccess: queryplancapture.AccessFullScan},
		{name: "name-free primary key", statement: fmt.Sprintf("SELECT %q FROM %q AS plan_root WHERE %q = ?", titleColumn.Name, table.Name, idColumn.Name), arguments: []any{[]byte{1}}, wantKind: queryplancapture.NodeAccess, wantAccess: queryplancapture.AccessPrimaryKey},
		{name: "ordinary index", statement: fmt.Sprintf("SELECT %q FROM %q AS plan_root WHERE %q = ?", titleColumn.Name, table.Name, authorColumn.Name), arguments: []any{[]byte{2}}, wantKind: queryplancapture.NodeAccess, wantAccess: queryplancapture.AccessIndex},
		{name: "temporary sort", statement: fmt.Sprintf("SELECT %q FROM %q AS plan_root ORDER BY %q", titleColumn.Name, table.Name, titleColumn.Name), wantKind: queryplancapture.NodeSort, wantAccess: queryplancapture.AccessNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := sqliteprovider.CaptureQueryPlan(context.Background(), connection, test.statement, test.arguments, fixture.Registry, aliases)
			if err != nil {
				t.Fatal(err)
			}
			root := plan.Root()
			if root.Kind() != test.wantKind || root.AccessKind() != test.wantAccess {
				t.Fatalf("root=(kind=%v access=%v), want (%v,%v)", root.Kind(), root.AccessKind(), test.wantKind, test.wantAccess)
			}
			if test.wantKind == queryplancapture.NodeAccess {
				if model, ok := root.ModelID(); !ok || model != fixture.Post {
					t.Fatalf("model=(%v,%v), want Post", model, ok)
				}
			}
			if test.wantAccess == queryplancapture.AccessPrimaryKey || test.wantAccess == queryplancapture.AccessIndex {
				if _, ok := root.IndexID(); !ok {
					t.Fatal("stable access identity absent")
				}
			}
		})
	}
}

func TestQueryPlanSQLiteUnknownAliasAndOversizeFailClosed(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	table := physicalTable(t, fixture.SQLite, fixture.Post)
	idColumn := physicalColumn(t, table, fixture.PostID)
	database := sqlx.MustOpen("sqlite", ":memory:")
	t.Cleanup(func() { _ = database.Close() })
	database.MustExec(fmt.Sprintf("CREATE TABLE %q (%q BLOB PRIMARY KEY)", table.Name, idColumn.Name))
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	plan, err := sqliteprovider.CaptureQueryPlan(context.Background(), connection, fmt.Sprintf("SELECT %q FROM %q AS unregistered_alias", idColumn.Name, table.Name), nil, fixture.Registry, queryplancapture.AliasMap{})
	if err != nil {
		t.Fatal(err)
	}
	if root := plan.Root(); root.Kind() != queryplancapture.NodeUnknown || root.AccessKind() != queryplancapture.AccessUnknown {
		t.Fatalf("unknown alias guessed: kind=%v access=%v", root.Kind(), root.AccessKind())
	}
}

func TestQueryPlanSQLiteCorrelatedWrappersPreserveProvenanceWithoutDuplicateWarnings(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	users := physicalTable(t, fixture.SQLite, fixture.User)
	posts := physicalTable(t, fixture.SQLite, fixture.Post)
	userID := physicalColumn(t, users, fixture.UserID)
	postID := physicalColumn(t, posts, fixture.PostID)
	authorID := physicalColumn(t, posts, fixture.AuthorID)
	authorIndex := physicalIndex(t, posts, fixture.AuthorID)
	database := sqlx.MustOpen("sqlite", ":memory:")
	t.Cleanup(func() { _ = database.Close() })
	database.MustExec(fmt.Sprintf("CREATE TABLE %q (%q BLOB PRIMARY KEY)", users.Name, userID.Name))
	database.MustExec(fmt.Sprintf("CREATE TABLE %q (%q BLOB PRIMARY KEY, %q BLOB NOT NULL)", posts.Name, postID.Name, authorID.Name))
	database.MustExec(fmt.Sprintf("CREATE INDEX %q ON %q (%q)", authorIndex.Name, posts.Name, authorID.Name))
	connection, err := database.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	for _, test := range []struct {
		name     string
		branches int
		proof    string
	}{
		{"known-single", 1, "exact"},
		{"known-many", 64, "exact"},
		{"unregistered", 1, "missing"},
		{"ambiguous", 1, "ambiguous"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == "plan_root" }, fixture.User, golem.RelationID{}, []golem.FieldID{fixture.UserID}, queryplancapture.AliasPhysicalAccess)
			if err != nil {
				t.Fatal(err)
			}
			facts := []queryplancapture.AliasFact{root}
			branches := make([]string, test.branches)
			for index := range branches {
				alias := fmt.Sprintf("plan_child_%d", index)
				branches[index] = fmt.Sprintf("EXISTS (SELECT 1 FROM %q AS %s WHERE %s.%q = plan_root.%q)", posts.Name, alias, alias, authorID.Name, userID.Name)
				if test.proof == "missing" {
					continue
				}
				child, err := queryplancapture.NewAliasFact(func(candidate string) bool { return candidate == alias }, fixture.Post, fixture.Authorship, []golem.FieldID{fixture.AuthorID}, queryplancapture.AliasCorrelatedRelation)
				if err != nil {
					t.Fatal(err)
				}
				facts = append(facts, child)
				if test.proof == "ambiguous" {
					facts = append(facts, child)
				}
			}
			statement := fmt.Sprintf("SELECT plan_root.%q FROM %q AS plan_root WHERE plan_root.%q = ? AND (%s)", userID.Name, users.Name, userID.Name, strings.Join(branches, " OR "))
			plan, err := sqliteprovider.CaptureQueryPlan(context.Background(), connection, statement, []any{[]byte{1}}, fixture.Registry, queryplancapture.NewAliasMap(facts...))
			if err != nil {
				t.Fatal(err)
			}
			unknown, correlated := 0, 0
			var walk func(queryplancapture.Node)
			walk = func(node queryplancapture.Node) {
				if node.Kind() == queryplancapture.NodeUnknown {
					unknown++
				}
				if node.Kind() == queryplancapture.NodeCorrelatedRelation {
					correlated++
					model, hasModel := node.ModelID()
					relation, hasRelation := node.RelationID()
					if !hasModel || model != fixture.Post || !hasRelation || relation != fixture.Authorship || len(node.Children()) != 1 || node.Children()[0].AccessKind() != queryplancapture.AccessIndex {
						t.Fatal("correlated access lost its renderer-proven identity or indexed child")
					}
				}
				for _, child := range node.Children() {
					walk(child)
				}
			}
			walk(plan.Root())
			if test.proof != "exact" {
				if unknown == 0 || correlated != 0 {
					t.Fatalf("unproven aliases acquired authority: unknown=%d correlated=%d", unknown, correlated)
				}
				return
			}
			report, err := queryplanreport.Build(queryplanreport.Input{Provider: golem.SQLite, Operation: "findMany", RootModelID: fixture.User,
				Statements: []queryplanreport.StatementInput{{Purpose: "root", Root: plan.RootInput()}}, MinimumExecutionStatements: 1, MaximumExecutionStatements: 1})
			if err != nil {
				t.Fatalf("report for bounded, known correlated plan: %v (nodes=%d unknown=%d)", err, plan.NodeCount(), unknown)
			}
			if unknown != 0 || correlated != test.branches || len(report.Warnings()) != 0 {
				t.Fatalf("redundant provider wrappers: unknown=%d correlated=%d want=%d warnings=%v", unknown, correlated, test.branches, report.Warnings())
			}
		})
	}
}

func physicalTable(t *testing.T, value physical.PhysicalSchema, model golem.ModelID) physical.PhysicalTable {
	t.Helper()
	for _, table := range value.Tables {
		if string(table.ID) == hex.EncodeToString(model[:]) {
			return table
		}
	}
	t.Fatalf("physical table for model %x not found", model)
	return physical.PhysicalTable{}
}

func physicalColumn(t *testing.T, table physical.PhysicalTable, field golem.FieldID) physical.PhysicalColumn {
	t.Helper()
	for _, column := range table.Columns {
		if string(column.ID) == hex.EncodeToString(field[:]) {
			return column
		}
	}
	t.Fatalf("physical column for field %x not found", field)
	return physical.PhysicalColumn{}
}

func physicalIndex(t *testing.T, table physical.PhysicalTable, field golem.FieldID) physical.PhysicalIndex {
	t.Helper()
	for _, index := range table.Indexes {
		if len(index.Keys) == 1 && index.Keys[0].Column != nil && string(*index.Keys[0].Column) == hex.EncodeToString(field[:]) {
			return index
		}
	}
	t.Fatalf("physical index for field %x not found", field)
	return physical.PhysicalIndex{}
}
