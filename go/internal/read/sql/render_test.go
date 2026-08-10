package sql

import (
	"context"
	"fmt"
	"path/filepath"
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

type renderUser struct{}
type renderPost struct{}
type renderPolicySet map[policyir.ModelID]policyir.Policy

func (set renderPolicySet) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	value, ok := set[model]
	return value, ok
}

func TestRenderIsDeterministicAcrossProvidersAndBindsPaging(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	frozen, err := golem.FreezeFindMany(descriptor,
		golem.Where(name.Contains("roy")), golem.OrderBy(name.Desc()), golem.Take[renderUser](-2),
		golem.Distinct[renderUser](name), golem.Select[renderUser](name),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Fields()) != 2 || planned.Fields()[1].Public() {
		t.Fatalf("physical projection=%#v", planned.Fields())
	}

	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		proof, proofErr := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
		if proofErr != nil {
			t.Fatal(proofErr)
		}
		first, renderErr := Render(planned, fixture.Registry, provider, proof)
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		second, renderErr := Render(planned, fixture.Registry, provider, proof)
		if renderErr != nil || first.SQL() != second.SQL() {
			t.Fatalf("provider %d nondeterministic SQL: %v", provider, renderErr)
		}
		if !first.ReverseResult() || len(first.Columns()) != 2 || len(first.Args()) != 2 {
			t.Fatalf("provider %d statement=%#v args=%#v", provider, first, first.Args())
		}
		for _, fragment := range []string{"ROW_NUMBER() OVER (PARTITION BY", "golem_rank", "LIMIT"} {
			if !strings.Contains(first.SQL(), fragment) {
				t.Errorf("provider %d SQL lacks %q: %s", provider, fragment, first.SQL())
			}
		}
		if provider == policyir.ProviderSQLite && (!strings.Contains(first.SQL(), "?1") || !strings.Contains(first.SQL(), "?2")) {
			t.Errorf("SQLite placeholders: %s", first.SQL())
		}
		if provider == policyir.ProviderSQLite && strings.Count(first.SQL(), "COLLATE BINARY") < 3 {
			t.Errorf("SQLite distinct/order did not fence binary collation: %s", first.SQL())
		}
		if provider == policyir.ProviderPostgreSQL && (!strings.Contains(first.SQL(), "$1") || !strings.Contains(first.SQL(), "$2")) {
			t.Errorf("PostgreSQL placeholders: %s", first.SQL())
		}
		if provider == policyir.ProviderPostgreSQL && strings.Count(first.SQL(), `COLLATE "C"`) < 3 {
			t.Errorf("PostgreSQL distinct/order did not fence C collation: %s", first.SQL())
		}
	}
}

func TestPortableOrderOperandCoversTextAndEnumOnly(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider policyir.Provider
		typ      compilerir.LogicalTypeIR
		want     string
	}{
		{name: "postgres string", provider: policyir.ProviderPostgreSQL, typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, want: `("value" COLLATE "C")`},
		{name: "postgres enum", provider: policyir.ProviderPostgreSQL, typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeEnum}, want: `(CAST("value" AS TEXT) COLLATE "C")`},
		{name: "sqlite string", provider: policyir.ProviderSQLite, typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, want: `("value" COLLATE BINARY)`},
		{name: "sqlite enum", provider: policyir.ProviderSQLite, typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeEnum}, want: `("value" COLLATE BINARY)`},
		{name: "uuid unchanged", provider: policyir.ProviderPostgreSQL, typ: compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, want: `"value"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := portableOrderOperand(test.provider, test.typ, `"value"`); got != test.want {
				t.Fatalf("operand=%s want=%s", got, test.want)
			}
		})
	}
}

func TestBoundedBatchSQLIsDeterministicPortableAndPerParent(t *testing.T) {
	fixture := schematest.New(t)
	userDescriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	title := golem.GeneratedTextField[renderPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[renderUser, renderPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	cursorID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000011")
	cursor := golem.GeneratedUniqueSelectorValue[renderPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, cursorID))
	frozen, err := golem.FreezeFindMany(userDescriptor,
		golem.Select[renderUser](posts.Args(
			golem.OrderBy(title.Asc()), golem.Skip[renderPost](1), golem.Take[renderPost](-2),
			golem.Distinct[renderPost](title), golem.Cursor(cursor), golem.Select[renderPost](title),
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	child := planned.Relations()[0].Child()
	endpoint, ok := fixture.Registry.RelationEndpoint(fixture.User, fixture.UserPosts, fixture.Authorship)
	if !ok {
		t.Fatal("relation endpoint is absent")
	}
	uuid1, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	uuid2, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000002")
	keys := [][]policyir.Value{{policyir.UUIDValue(uuid1.Bytes())}, {policyir.UUIDValue(uuid2.Bytes())}}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		proof, proofErr := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
		if proofErr != nil {
			t.Fatal(proofErr)
		}
		first, renderErr := RenderBatch(child, endpoint, keys, fixture.Registry, provider, proof)
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		second, renderErr := RenderBatch(child, endpoint, keys, fixture.Registry, provider, proof)
		if renderErr != nil || first.SQL() != second.SQL() || fmt.Sprint(first.Args()) != fmt.Sprint(second.Args()) {
			t.Fatalf("provider %d batch SQL is nondeterministic: %v", provider, renderErr)
		}
		for _, fragment := range []string{"ROW_NUMBER() OVER (PARTITION BY", "golem_distinct_rank", "golem_page_rank", "> 1", "<= 3"} {
			if !strings.Contains(first.SQL(), fragment) {
				t.Errorf("provider %d batch SQL lacks %q: %s", provider, fragment, first.SQL())
			}
		}
		if !strings.Contains(first.SQL(), `"golem_cp0"."author_id"`) || !strings.Contains(first.SQL(), `"golem_br0"."author_id"`) {
			t.Errorf("provider %d cursor anchor was not scoped by trusted parent keys: %s", provider, first.SQL())
		}
		if len(first.Args()) != 3 || !first.ReverseBuckets() {
			t.Errorf("provider %d batch shape args=%#v reverse=%t SQL=%s", provider, first.Args(), first.ReverseBuckets(), first.SQL())
		}
		collation := "COLLATE BINARY"
		if provider == policyir.ProviderPostgreSQL {
			collation = `COLLATE "C"`
		}
		if strings.Count(first.SQL(), collation) < 2 {
			t.Errorf("provider %d batch order is not portable: %s", provider, first.SQL())
		}
	}
}

func TestRootStatementRefusesProviderNeutralParameterOverflow(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	values := make([]string, MaxStatementParameters+1)
	for index := range values {
		values[index] = fmt.Sprintf("value-%04d", index)
	}
	frozen, err := golem.FreezeFindMany(descriptor, golem.Where(name.In(values...)), golem.Select[renderUser](name))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if _, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof); err == nil || !strings.Contains(err.Error(), "parameter ceiling") {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestRenderedSQLiteStatementExecutesPolicyBeforePage(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "render.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"00000000-0000-0000-0000-000000000001", "roy"}, {"00000000-0000-0000-0000-000000000002", "other"}, {"00000000-0000-0000-0000-000000000003", "royal"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	frozen, _ := golem.FreezeFindMany(descriptor, golem.Where(name.Contains("roy")), golem.OrderBy(name.Asc()), golem.Take[renderUser](1), golem.Select[renderUser](name))
	request, _ := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	planned, _ := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	proof, err := sqlite.New().PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := database.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		t.Fatalf("execute %s: %v", statement.SQL(), err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows=%d want=1", count)
	}
}

func TestRelationCountRendersDeterministicallyAndExecutesAuthorizedTargetBeforeCount(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "relation-count.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "alice"},
		{"00000000-0000-0000-0000-000000000002", "bob"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][3]string{
		{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "allowed-match"},
		{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "allowed-other"},
		{"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000001", "denied-match"},
		{"00000000-0000-0000-0000-000000000014", "00000000-0000-0000-0000-000000000002", "denied-match"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}

	userDescriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[renderPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[renderUser, renderPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	frozen, err := golem.FreezeFindMany(userDescriptor,
		golem.OrderBy(name.Asc()),
		golem.Select[renderUser](name, posts.Count(golem.Where(postTitle.EndsWith("match")))),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	bindPolicy := func(model golem.ModelID, frozen golem.FrozenPolicy) policyir.Policy {
		t.Helper()
		bound, bindErr := policybind.Policy(frozen, fixture.Registry, policyir.PortableProviders())
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		bound, normalizeErr := normalize.Policy(bound)
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		if bound.ModelID() != policyir.ModelID(model) {
			t.Fatalf("policy model=%x want=%x", bound.ModelID(), model)
		}
		return bound
	}
	userRules := golem.NewRules[renderUser]()
	userRules.CanRead(golem.All[renderUser]())
	frozenUser, _ := userRules.Freeze(fixture.User)
	postRules := golem.NewRules[renderPost]()
	postRules.CanRead(postTitle.StartsWith("allowed"))
	frozenPost, _ := postRules.Freeze(fixture.Post)
	planned, err := readplan.Caller(request, fixture.Registry, renderPolicySet{
		policyir.ModelID(fixture.User): bindPolicy(fixture.User, frozenUser),
		policyir.ModelID(fixture.Post): bindPolicy(fixture.Post, frozenPost),
	}, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if counts := planned.RelationCounts(); len(counts) != 1 || counts[0].Child().Operation() != readir.Count {
		t.Fatalf("planned counts=%#v", counts)
	}

	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		proof, proofErr := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
		if proofErr != nil {
			t.Fatal(proofErr)
		}
		first, renderErr := Render(planned, fixture.Registry, provider, proof)
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		second, renderErr := Render(planned, fixture.Registry, provider, proof)
		if renderErr != nil || first.SQL() != second.SQL() {
			t.Fatalf("provider %d relation-count render is not deterministic: %v", provider, renderErr)
		}
		if len(first.CountColumns()) != 1 || len(first.Args()) != 2 || !strings.Contains(first.SQL(), "SELECT COUNT(*) FROM") || !strings.Contains(first.SQL(), "author_id") {
			t.Fatalf("provider %d SQL=%s args=%#v counts=%#v", provider, first.SQL(), first.Args(), first.CountColumns())
		}
		if provider == policyir.ProviderPostgreSQL && (!strings.Contains(first.SQL(), "$1") || !strings.Contains(first.SQL(), "$2")) {
			t.Fatalf("PostgreSQL count placeholders=%s", first.SQL())
		}
		if provider != policyir.ProviderSQLite {
			continue
		}
		rows, queryErr := database.QueryxContext(ctx, first.SQL(), first.Args()...)
		if queryErr != nil {
			t.Fatalf("execute %s args=%#v: %v", first.SQL(), first.Args(), queryErr)
		}
		defer rows.Close()
		got := map[string]int64{}
		for rows.Next() {
			var userName, ignoredID string
			var count int64
			if err := rows.Scan(&userName, &ignoredID, &count); err != nil {
				t.Fatal(err)
			}
			got[userName] = count
		}
		if got["alice"] != 1 || got["bob"] != 0 {
			t.Fatalf("authorized relation counts=%v", got)
		}
	}
}

func TestRenderedCursorIsInclusiveOrderedAndAuthorized(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "render-cursor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"00000000-0000-0000-0000-000000000001", "a"}, {"00000000-0000-0000-0000-000000000002", "b"}, {"00000000-0000-0000-0000-000000000003", "b"}, {"00000000-0000-0000-0000-000000000004", "c"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	id := golem.GeneratedEqualField[renderUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	cursorID, err := golem.ParseUUID("00000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	selector := golem.GeneratedUniqueSelectorValue[renderUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, cursorID))
	frozen, err := golem.FreezeFindMany(descriptor,
		golem.OrderBy(name.Asc()), golem.Cursor(selector), golem.Skip[renderUser](1), golem.Take[renderUser](2), golem.Select[renderUser](id),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := sqlite.New().PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statement.SQL(), `WITH "golem_cursor0"`) || !strings.Contains(statement.SQL(), `CROSS JOIN "golem_cursor0"`) {
		t.Fatalf("cursor SQL=%s", statement.SQL())
	}
	rows, err := database.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		t.Fatalf("execute %s args=%#v: %v", statement.SQL(), statement.Args(), err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var gotID, ignoredName string
		if err := rows.Scan(&gotID, &ignoredName); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, gotID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "00000000-0000-0000-0000-000000000003" || ids[1] != "00000000-0000-0000-0000-000000000004" {
		t.Fatalf("cursor page=%v", ids)
	}

	// Cursor lookup is authorized independently of the caller's narrowing
	// filter. The cursor row is "b" and therefore does not match this Where,
	// but it still defines the boundary from which the matching "c" row is
	// returned.
	narrowedFrozen, err := golem.FreezeFindMany(descriptor,
		golem.Where(name.StartsWith("c")), golem.OrderBy(name.Asc()),
		golem.Cursor(selector), golem.Select[renderUser](id),
	)
	if err != nil {
		t.Fatal(err)
	}
	narrowedRequest, err := readbind.Request(narrowedFrozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	narrowedPlan, err := readplan.System(narrowedRequest, fixture.Registry, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	narrowedStatement, err := Render(narrowedPlan, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	narrowedRows, err := database.QueryxContext(ctx, narrowedStatement.SQL(), narrowedStatement.Args()...)
	if err != nil {
		t.Fatal(err)
	}
	defer narrowedRows.Close()
	if !narrowedRows.Next() {
		t.Fatal("caller filter incorrectly removed the cursor anchor")
	}
	var narrowedID, narrowedName string
	if err := narrowedRows.Scan(&narrowedID, &narrowedName); err != nil {
		t.Fatal(err)
	}
	if narrowedID != "00000000-0000-0000-0000-000000000004" {
		t.Fatalf("narrowed cursor row=%s", narrowedID)
	}

	policyFor := func(prefix string) policyir.Policy {
		t.Helper()
		rules := golem.NewRules[renderUser]()
		rules.CanRead(name.StartsWith(prefix))
		frozenPolicy, freezeErr := rules.Freeze(fixture.User)
		if freezeErr != nil {
			t.Fatal(freezeErr)
		}
		bound, bindErr := policybind.Policy(frozenPolicy, fixture.Registry, policyir.PortableProviders())
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		bound, normalizeErr := normalize.Policy(bound)
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		return bound
	}

	// The policy predicate is rendered independently in the cursor CTE and
	// root query. Numbered SQLite parameters must be rebased so both retain
	// their own values.
	authorized, err := readplan.Caller(request, fixture.Registry, renderPolicySet{policyir.ModelID(fixture.User): policyFor("b")}, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	authorizedStatement, err := Render(authorized, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	authorizedRows, err := database.QueryxContext(ctx, authorizedStatement.SQL(), authorizedStatement.Args()...)
	if err != nil {
		t.Fatal(err)
	}
	defer authorizedRows.Close()
	if !authorizedRows.Next() {
		t.Fatal("policy-visible cursor did not return its authorized successor")
	}
	var authorizedID, authorizedName string
	if err := authorizedRows.Scan(&authorizedID, &authorizedName); err != nil {
		t.Fatal(err)
	}
	if authorizedID != "00000000-0000-0000-0000-000000000003" {
		t.Fatalf("authorized cursor row=%s", authorizedID)
	}

	// A cursor row outside the caller's policy produces an empty page. The CTE
	// is authorized by the same row constraint and therefore does not disclose
	// whether that cursor exists.
	denied, err := readplan.Caller(request, fixture.Registry, renderPolicySet{policyir.ModelID(fixture.User): policyFor("c")}, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	deniedStatement, err := Render(denied, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	deniedRows, err := database.QueryxContext(ctx, deniedStatement.SQL(), deniedStatement.Args()...)
	if err != nil {
		t.Fatal(err)
	}
	defer deniedRows.Close()
	if deniedRows.Next() {
		t.Fatal("policy-invisible cursor returned a page")
	}
}

func TestPostgreSQLCursorPlaceholdersAreDeterministicAndRebased(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	id := golem.GeneratedEqualField[renderUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	cursorID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000002")
	selector := golem.GeneratedUniqueSelectorValue[renderUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, cursorID))
	frozen, _ := golem.FreezeFindMany(descriptor, golem.Where(name.Contains("b")), golem.OrderBy(name.Asc()), golem.Cursor(selector), golem.Take[renderUser](2), golem.Select[renderUser](id))
	request, _ := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	planned, _ := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	proof, err := policysql.NewCapabilityProof(policyir.ProviderPostgreSQL, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Render(planned, fixture.Registry, policyir.ProviderPostgreSQL, proof)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(planned, fixture.Registry, policyir.ProviderPostgreSQL, proof)
	if err != nil || first.SQL() != second.SQL() {
		t.Fatalf("nondeterministic cursor SQL: %v", err)
	}
	// The cursor anchor contains authorization reach plus selector only. The
	// unrelated caller Where belongs to the outer query and is bound once.
	if len(first.Args()) != 3 {
		t.Fatalf("args=%#v", first.Args())
	}
	if strings.Count(first.SQL(), `COLLATE "C"`) < 6 {
		t.Fatalf("cursor/order comparison did not normalize both sides: %s", first.SQL())
	}
	for index := 1; index <= 3; index++ {
		if !strings.Contains(first.SQL(), fmt.Sprintf("$%d", index)) {
			t.Fatalf("SQL lacks rebased placeholder $%d: %s", index, first.SQL())
		}
	}
}

func TestRenderedSQLiteSkipWithoutTakeUsesNoLimitSentinel(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "render-skip.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"00000000-0000-0000-0000-000000000001", "a"}, {"00000000-0000-0000-0000-000000000002", "b"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	frozen, err := golem.FreezeFindMany(descriptor, golem.OrderBy(name.Asc()), golem.Skip[renderUser](1), golem.Select[renderUser](name))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.System(request, fixture.Registry, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := sqlite.New().PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statement.SQL(), "LIMIT -1 OFFSET") {
		t.Fatalf("SQLite skip SQL=%s", statement.SQL())
	}
	rows, err := database.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows=%d want=1", count)
	}
}
