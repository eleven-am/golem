package batch

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func identityDepthUUID(number uint32) policyir.Value {
	var id [16]byte
	binary.BigEndian.PutUint32(id[12:], number)
	return policyir.UUIDValue(id)
}

// These are actual SQLite preparations/executions, not SQL-length assertions.
// A legal parameter chunk must not hit SQLite's expression-tree ceiling before
// the cascade owner can inspect its bounded sentinel and enforce touched rows.
func TestBatchIdentityDepthSingleKeySQLite(t *testing.T) {
	fixture := schematest.NewSubscribedRowVersionedOnDelete(t, compilerir.ActionCascade)
	ctx := context.Background()
	database, _, err := sqliteprovider.New().Open(ctx, filepath.Join(t.TempDir(), "depth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqliteprovider.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	name, _ := policyir.StringValue("owner")
	title, _ := policyir.StringValue("leaf")
	parents := make([]mutationdecode.Row, 1000)
	for index := range parents {
		cells := []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(fixture.UserID), identityDepthUUID(uint32(index+1))),
			mutationdecode.Value(policyir.FieldID(fixture.UserName), name),
		}
		parents[index], err = mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.User), cells)
		if err != nil {
			t.Fatal(err)
		}
	}
	for index, number := range []int{1, 511, 999, 1000} {
		insertCaptureCells(t, ctx, database, fixture.Registry, policyir.ProviderSQLite, policyir.ModelID(fixture.User), parents[number-1].Cells())
		insertCaptureCells(t, ctx, database, fixture.Registry, policyir.ProviderSQLite, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(fixture.PostID), identityDepthUUID(uint32(index+1))),
			mutationdecode.Value(policyir.FieldID(fixture.AuthorID), identityDepthUUID(uint32(number))),
			mutationdecode.Value(policyir.FieldID(fixture.PostTitle), title),
		})
	}
	effects := fixture.Registry.DeleteEffects(fixture.User)
	if len(effects) != 1 {
		t.Fatalf("expected one real cascade endpoint, got %d", len(effects))
	}
	for _, maximum := range []int{511, 999} {
		t.Run(fmt.Sprintf("parents=%d", maximum), func(t *testing.T) {
			want := 2
			if maximum == 999 {
				want = 3
			}
			for _, renderRows := range []bool{false, true} {
				t.Run(fmt.Sprintf("rowLookup=%v", renderRows), func(t *testing.T) {
					var statements []Statement
					var err error
					if renderRows {
						statements, err = RenderRows(RowsRequest{Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Model: policyir.ModelID(fixture.User), Rows: parents[:maximum], MaxParameters: 999})
					} else {
						statements, err = RenderDependents(DependentsRequest{Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Parent: policyir.ModelID(fixture.User), Parents: parents[:maximum], Effect: effects[0], MaxRows: 4, MaxParameters: 999})
					}
					if err != nil {
						t.Fatal(err)
					}
					if len(statements) != 1 || len(statements[0].Bindings()) != maximum {
						t.Fatalf("legal scalar chunk changed: statements=%d", len(statements))
					}
					if count := executeIdentityDepthStatements(t, database, statements); count != want {
						t.Fatalf("identity set changed: rows=%d want=%d", count, want)
					}
				})
			}
		})
	}
	t.Run("sentinel remains visible", func(t *testing.T) {
		statements, err := RenderDependents(DependentsRequest{Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Parent: policyir.ModelID(fixture.User), Parents: parents[:999], Effect: effects[0], MaxRows: 1, MaxParameters: 999})
		if err != nil {
			t.Fatal(err)
		}
		if count := executeIdentityDepthStatements(t, database, statements); count != 2 {
			t.Fatalf("overflow sentinel was truncated: rows=%d", count)
		}
	})
}

func TestBatchIdentityDepthCompositeTuplesSQLite(t *testing.T) {
	fixture := schematest.NewCompositeRelation(t)
	ctx := context.Background()
	database, _, err := sqliteprovider.New().Open(ctx, filepath.Join(t.TempDir(), "tuples.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqliteprovider.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	makeCells := func(region, id uint32) []mutationdecode.Cell {
		return []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(fixture.TenantRegion), identityDepthUUID(region)),
			mutationdecode.Value(policyir.FieldID(fixture.TenantID), identityDepthUUID(id)),
		}
	}
	parents := make([]mutationdecode.Row, 511)
	for index := range parents {
		parents[index], err = mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Tenant), makeCells(uint32(index+1), uint32(index+1)))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Crossed pairs exist physically but belong to neither requested identity.
	for _, pair := range [][2]uint32{{1, 1}, {2, 2}, {511, 511}, {1, 2}, {2, 1}} {
		insertCaptureCells(t, ctx, database, fixture.Registry, policyir.ProviderSQLite, policyir.ModelID(fixture.Tenant), makeCells(pair[0], pair[1]))
	}
	request := RowsRequest{Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Model: policyir.ModelID(fixture.Tenant), Rows: parents, MaxParameters: 999}
	statements, err := RenderRows(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 || len(statements[0].Bindings()) != 998 || len(statements[1].Bindings()) != 24 {
		t.Fatal("composite identities no longer obey the existing parameter chunk budget")
	}
	if count := executeIdentityDepthStatements(t, database, statements); count != 3 {
		t.Fatalf("composite tuple correlation lost: rows=%d want=3 (cross pairs must not match)", count)
	}
	request.MaxParameters = 1
	_, err = RenderRows(request)
	var bound *Error
	if !errors.As(err, &bound) || bound.Code != CodeLimit || bound.Limit != golem.LimitStatementParameters {
		t.Fatalf("one tuple wider than parameter budget reached SQL: %v", err)
	}
}

func executeIdentityDepthStatements(t *testing.T, database *sqlx.DB, statements []Statement) int {
	t.Helper()
	count := 0
	for index, statement := range statements {
		bindings := statement.Bindings()
		if len(bindings) > 999 {
			t.Fatal("statement exceeded the portable parameter ceiling")
		}
		arguments := make([]any, len(bindings))
		for index, binding := range bindings {
			arguments[index] = binding.Value()
		}
		rows, err := database.QueryxContext(context.Background(), statement.SQL(), arguments...)
		if err != nil {
			t.Fatalf("statement %d params=%d SQLbytes=%d failed within budget: %v", index, len(arguments), len(statement.SQL()), err)
		}
		for rows.Next() {
			if _, err := rows.SliceScan(); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			count++
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			t.Fatalf("statement %d execution failed: %v", index, err)
		}
	}
	return count
}
