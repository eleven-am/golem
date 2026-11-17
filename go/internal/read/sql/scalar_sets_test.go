package sql

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

func TestCallerReadAndCountWithTwoThousandActorIDsPreserveRelationsAndMaskPlans(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqliteprovider.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "actor-sets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqliteprovider.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	grants := make([]golem.UUID, 2000)
	for i := range grants {
		binary.BigEndian.PutUint32(grants[i][12:], uint32(i+1))
	}
	first, last, filtered := grants[0], grants[1999], grants[1998]
	var outside golem.UUID
	binary.BigEndian.PutUint32(outside[12:], 2001)
	for _, id := range []golem.UUID{first, last, filtered, outside} {
		if _, err := database.Exec(`INSERT INTO users(id,name) VALUES(?,?)`, id.String(), "visible-name"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO posts(id,author_id,title) VALUES(?,?,?)`, id.String(), id.String(), "post"); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := golem.GeneratedModelDescriptor[renderUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	id := golem.GeneratedEqualField[renderUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedTextField[renderUser, string](fixture.UserName)
	posts := golem.GeneratedToMany[renderUser, renderPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	author := golem.GeneratedEqualField[renderPost, golem.UUID](fixture.AuthorID)
	postScope := author.In(grants...)
	postRules := golem.NewRules[renderPost]()
	postRules.CanRead(postScope)
	frozenPost, err := postRules.Freeze(fixture.Post)
	if err != nil {
		t.Fatal(err)
	}
	rules := golem.NewRules[renderUser]()
	rowScope := id.In(grants...)
	rules.CanRead(rowScope)
	rules.CannotReadFields(golem.All[renderUser](), name)
	// Field grants also contribute to row visibility. Keep their scope
	// explicitly inside the actor's row grant.
	rules.CanReadFields(rowScope.And(id.Eq(first)), name)
	frozenPolicy, err := rules.Freeze(fixture.User)
	if err != nil {
		t.Fatal(err)
	}
	// Freezing must sever the mutable actor-owned slice before packing it.
	grants[0] = outside
	policy, err := policybind.Policy(frozenPolicy, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	policy, err = normalize.Policy(policy)
	if err != nil {
		t.Fatal(err)
	}
	policies := renderPolicySet{policyir.ModelID(fixture.User): policy}
	postPolicy, err := policybind.Policy(frozenPost, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	postPolicy, err = normalize.Policy(postPolicy)
	if err != nil {
		t.Fatal(err)
	}
	policies[policyir.ModelID(fixture.Post)] = postPolicy
	proof, err := sqliteprovider.New().PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	where := id.NotIn(filtered).And(posts.Some(postScope))
	frozen, err := golem.FreezeFindMany(descriptor, golem.Where(where), golem.Select[renderUser](id, name))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := readplan.Caller(request, fixture.Registry, policies, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	var idAlias, nameAlias string
	for _, column := range statement.Columns() {
		switch column.FieldID() {
		case policyir.FieldID(fixture.UserID):
			idAlias = column.Alias()
		case policyir.FieldID(fixture.UserName):
			nameAlias = column.Alias()
		}
	}
	masked := false
	for _, field := range planned.Fields() {
		if field.FieldID() == policyir.FieldID(fixture.UserName) {
			_, hasMask := field.Mask()
			_, hasSQLMask := field.SQLMask()
			masked = field.Conditional() && hasMask && hasSQLMask
		}
	}
	if !masked {
		t.Fatal("actor-scoped field mask was lost from the plan")
	}
	if idAlias == "" || nameAlias == "" {
		t.Fatal("selected aliases absent")
	}
	rows, err := database.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for rows.Next() {
		row := map[string]any{}
		if err := rows.MapScan(row); err != nil {
			t.Fatal(err)
		}
		got[row[idAlias].(string)] = row[nameAlias]
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[first.String()] != "visible-name" {
		t.Fatalf("actor set/filter/mask results=%#v", got)
	}
	if _, present := got[last.String()]; !present {
		t.Fatalf("last actor grant was lost: %#v", got)
	}
	countRequest, err := golem.FreezeCount(descriptor, golem.Where(where))
	if err != nil {
		t.Fatal(err)
	}
	boundCount, err := readbind.Request(countRequest, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	countPlan, err := readplan.Caller(boundCount, fixture.Registry, policies, readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	countStatement, err := Render(countPlan, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.Get(&count, countStatement.SQL(), countStatement.Args()...); err != nil || count != 2 {
		t.Fatalf("authorized count=%d err=%v", count, err)
	}
}
