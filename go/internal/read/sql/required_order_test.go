package sql

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestRequiredLocalOrderUsesSQLitePrimaryIndex(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, filepath.Join(t.TempDir(), "order.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := 1; i <= 32; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		ids = append(ids, id)
		if _, err := database.ExecContext(ctx, `INSERT INTO users(id,name) VALUES (?,?)`, id, fmt.Sprintf("name-%02d-%s", i, map[bool]string{true: "open", false: "closed"}[i%2 == 0])); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	id := golem.GeneratedEqualField[renderUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	rules := golem.NewRules[renderUser]()
	rules.CanRead(golem.All[renderUser]())
	rules.CannotReadFields(golem.All[renderUser](), id)
	rules.CanReadFields(name.EndsWith("-open"), id)
	frozenPolicy, err := rules.Freeze(fixture.User)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policybind.Policy(frozenPolicy, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	policy, err = normalize.Policy(policy)
	if err != nil {
		t.Fatal(err)
	}
	policies := renderPolicySet{policyir.ModelID(fixture.User): policy}
	for _, caller := range []bool{false, true} {
		for _, descending := range []bool{false, true} {
			order := id.Asc()
			if descending {
				order = id.Desc()
			}
			options := []golem.ReadOption[renderUser]{golem.Select[renderUser](id), golem.OrderBy(order), golem.Take[renderUser](3)}
			if caller {
				options = append(options, golem.Where(name.EndsWith("-open")))
			}
			frozen, err := golem.FreezeFindMany(descriptor, options...)
			if err != nil {
				t.Fatal(err)
			}
			request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
			if err != nil {
				t.Fatal(err)
			}
			plan, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
			if caller {
				plan, err = readplan.Caller(request, fixture.Registry, policies, readplan.DefaultLimits())
			}
			if err != nil {
				t.Fatal(err)
			}
			if caller {
				field := plan.Fields()[0]
				_, masked := field.Mask()
				_, sqlMasked := field.SQLMask()
				if field.FieldID() != policyir.FieldID(fixture.UserID) || field.Nullable() || !field.Conditional() || !field.DischargedByConstraint() || !masked || !sqlMasked {
					t.Fatalf("caller required key has no actual discharging proof: %+v", field)
				}
			}
			for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
				proof, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
				if err != nil {
					t.Fatal(err)
				}
				statement, err := Render(plan, fixture.Registry, provider, proof)
				if err != nil {
					t.Fatal(err)
				}
				if provider != policyir.ProviderSQLite {
					// PostgreSQL coverage here is rendering only. SQLite must reach
					// actual execution and EQP before any SQL-shape assertion.
					orderClause := statement.SQL()[strings.LastIndex(statement.SQL(), " ORDER BY "):]
					if strings.Contains(orderClause, " IS NULL") || strings.Contains(orderClause, " IS NOT NULL") {
						t.Fatalf("required local order retained null rank: %s", statement.SQL())
					}
					continue
				}
				var got []string
				dataRows, err := database.QueryxContext(ctx, statement.SQL(), statement.Args()...)
				if err != nil {
					t.Fatal(err)
				}
				for dataRows.Next() {
					values, err := dataRows.SliceScan()
					if err != nil {
						_ = dataRows.Close()
						t.Fatal(err)
					}
					got = append(got, fmt.Sprintf("%s", values[0]))
				}
				dataErr := dataRows.Err()
				closeDataErr := dataRows.Close()
				if dataErr != nil || closeDataErr != nil {
					t.Fatalf("ordered rows: %v %v", dataErr, closeDataErr)
				}
				want := ids[:3]
				if descending {
					want = []string{ids[31], ids[30], ids[29]}
				}
				if caller {
					want = []string{ids[1], ids[3], ids[5]}
					if descending {
						want = []string{ids[31], ids[29], ids[27]}
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("ordered IDs=%v want=%v", got, want)
				}
				rows, err := database.QueryxContext(ctx, "EXPLAIN QUERY PLAN "+statement.SQL(), statement.Args()...)
				if err != nil {
					t.Fatal(err)
				}
				indexed := false
				for rows.Next() {
					var node, parent, unused int
					var detail string
					if err := rows.Scan(&node, &parent, &unused, &detail); err != nil {
						rows.Close()
						t.Fatal(err)
					}
					if strings.Contains(detail, "TEMP B-TREE") {
						rows.Close()
						t.Fatalf("required primary order sorted (caller=%t descending=%t): %s", caller, descending, detail)
					}
					indexed = indexed || strings.Contains(detail, "USING COVERING INDEX sqlite_autoindex_users_") || strings.Contains(detail, "USING INDEX sqlite_autoindex_users_")
				}
				readErr := rows.Err()
				closeErr := rows.Close()
				if readErr != nil || closeErr != nil {
					t.Fatalf("plan rows: %v %v", readErr, closeErr)
				}
				if !indexed {
					t.Fatal("primary ordered index access absent")
				}
			}
		}
	}
}

func TestNullableLocalTypedOrderRetainsNullRank(t *testing.T) {
	fixture := schematest.NewSubscribedIndexedOptionalSource(t)
	descriptor := golem.GeneratedModelDescriptor[renderPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	field := golem.GeneratedEqualField[renderPost, golem.UUID](fixture.AuthorID)
	for _, descending := range []bool{false, true} {
		order := field.Asc()
		if descending {
			order = field.Desc()
		}
		frozen, err := golem.FreezeFindMany(descriptor, golem.Select[renderPost](field), golem.OrderBy(order))
		if err != nil {
			t.Fatal(err)
		}
		request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		plan, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
			proof, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
			if err != nil {
				t.Fatal(err)
			}
			statement, err := Render(plan, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			want := `"golem_r0"."author_id" IS NOT NULL ASC`
			if descending {
				want = `"golem_r0"."author_id" IS NULL ASC`
			}
			if !strings.Contains(statement.SQL(), want) {
				t.Fatalf("nullable local rank absent: %s", statement.SQL())
			}
		}
	}
}

func TestSQLiteLeftJoinRequiredTargetRetainsNullOrdering(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewSubscribedIndexedOptionalSource(t)
	database, _, err := sqlite.New().Open(ctx, filepath.Join(t.TempDir(), "left-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	user := "00000000-0000-0000-0000-000000000001"
	absent := "00000000-0000-0000-0000-000000000002"
	present := "00000000-0000-0000-0000-000000000003"
	if _, err := database.ExecContext(ctx, `INSERT INTO users(id,name) VALUES (?,?)`, user, "name"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO posts(id,author_id,title) VALUES (?,NULL,?),(?,?,?)`, absent, "absent", present, user, "present"); err != nil {
		t.Fatal(err)
	}
	for _, descending := range []bool{false, true} {
		term := OrderTerm(policyir.ProviderSQLite, compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, `"joined"."id"`, descending)
		var got []string
		if err := database.SelectContext(ctx, &got, `SELECT root.id FROM posts AS root LEFT JOIN users AS joined ON joined.id=root.author_id ORDER BY `+term); err != nil {
			t.Fatal(err)
		}
		want := []string{absent, present}
		if descending {
			want = []string{present, absent}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("left join ordering=%v want=%v", got, want)
		}
	}
}

// A row-dependent field policy cannot currently be used as an ordering key
// without discharge. Obtain real masked metadata through an unordered projection,
// then exercise the local renderer guard separately to preserve this boundary if
// a future planner admits masked order expressions.
func TestRequiredMaskedLocalOrderIsRejectedAndRetainsConservativeRank(t *testing.T) {
	fixture := schematest.New(t)
	key := golem.GeneratedEqualField[renderPost, golem.UUID](fixture.PostID)
	title := golem.GeneratedTextField[renderPost, string](fixture.PostTitle)
	rules := golem.NewRules[renderPost]()
	rules.CanRead(golem.All[renderPost]())
	rules.CannotReadFields(golem.All[renderPost](), key)
	rules.CanReadFields(title.EndsWith("-open"), key)
	frozen, err := rules.Freeze(fixture.Post)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := policybind.Policy(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	policy, err = normalize.Policy(policy)
	if err != nil {
		t.Fatal(err)
	}
	policies := renderPolicySet{policyir.ModelID(fixture.Post): policy}
	selection, err := readir.NewScalarSelection(policyir.FieldID(fixture.PostID))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.Post), Projection: readir.ProjectionSelect, Selection: []readir.Selection{selection}, Unordered: true})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.Caller(request, fixture.Registry, policies, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, field := range planned.Fields() {
		if field.FieldID() != policyir.FieldID(fixture.PostID) {
			continue
		}
		_, mask := field.Mask()
		_, sqlMask := field.SQLMask()
		if field.Nullable() || !field.Conditional() || !mask || !sqlMask || field.DischargedByConstraint() {
			t.Fatalf("required conditional key lost actual mask metadata: %+v", field)
		}
		found = true
	}
	if !found {
		t.Fatal("conditional key absent")
	}
	for _, direction := range []readir.SortDirection{readir.Ascending, readir.Descending} {
		order, err := readir.NewOrder(policyir.FieldID(fixture.PostID), direction)
		if err != nil {
			t.Fatal(err)
		}
		ordered, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.Post), Projection: readir.ProjectionSelect, Selection: []readir.Selection{selection}, OrderBy: []readir.Order{order}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = readplan.Caller(ordered, fixture.Registry, policies, readplan.DefaultLimits())
		var refusal *readplan.Error
		if !errors.As(err, &refusal) || refusal.Code != readplan.CodeField || refusal.Field != policyir.FieldID(fixture.PostID) {
			t.Fatalf("conditional ordering was not refused by owner: %v", err)
		}
		for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
			dialect, err := providerDialect(provider)
			if err != nil {
				t.Fatal(err)
			}
			columns := map[policyir.FieldID]string{policyir.FieldID(fixture.PostID): `"golem_r0"."id"`}
			root, outer, err := renderOrders([]readir.Order{order}, planned.Fields(), columns, dialect, provider, false)
			if err != nil {
				t.Fatal(err)
			}
			rank := " IS NOT NULL ASC"
			if direction == readir.Descending {
				rank = " IS NULL ASC"
			}
			if !strings.Contains(root, rank) || !strings.Contains(outer, rank) {
				t.Fatalf("masked local operand lost null rank: root=%s outer=%s", root, outer)
			}
			// An operand with no selected-field metadata must refuse rather than
			// infer non-nullability from its textual name or storage spelling.
			if _, _, err := renderOrders([]readir.Order{order}, nil, columns, dialect, provider, false); err == nil {
				t.Fatal("missing metadata admitted an ordering operand")
			}
		}
	}
}
