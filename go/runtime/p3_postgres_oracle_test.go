package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
)

func TestPostgreSQLLiveAuthorizedReadGraph(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()
	fixture := schematest.New(t)
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p3_runtime_%d", time.Now().UnixNano()))
	postgresSchema := fixture.PostgreSQL
	postgresSchema.Namespace.Name = namespace
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "_golem" CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	})
	if err := provider.ApplyInitial(ctx, database, postgresSchema); err != nil {
		t.Fatal(err)
	}
	bundle := postgresRuntimeBundle(t, fixture, postgresSchema)

	users := golem.GeneratedModelDescriptor[oracleUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	posts := golem.GeneratedModelDescriptor[oraclePost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[oracleUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[oraclePost, string](fixture.PostTitle)
	userBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		rules.CanRead(userName.StartsWith(actor.UserPrefix))
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		rules.CanRead(postTitle.StartsWith(actor.PostPrefix))
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{userBinding, postBinding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[oraclePrincipal, oracleActor]{
		Database: p8RuntimeTestDatabase(database, golem.PostgreSQL), Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	qualifiedUsers := `"` + string(namespace) + `"."users"`
	qualifiedPosts := `"` + string(namespace) + `"."posts"`
	for _, row := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "hidden-a"},
		{"00000000-0000-0000-0000-000000000002", "visible-a"},
		{"00000000-0000-0000-0000-000000000003", "visible-b"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO `+qualifiedUsers+`("id","name") VALUES ($1,$2)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][3]string{
		{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000002", "allowed-a"},
		{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000002", "allowed-z"},
		{"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000002", "zzzz-denied"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO `+qualifiedPosts+`("id","author_id","title") VALUES ($1,$2,$3)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}

	caller, err := app.ForPrincipal(ctx, oraclePrincipal{UserPrefix: "visible-", PostPrefix: "allowed-"})
	if err != nil {
		t.Fatal(err)
	}
	userID := golem.GeneratedEqualField[oracleUser, golem.UUID](fixture.UserID)
	postID := golem.GeneratedEqualField[oraclePost, golem.UUID](fixture.PostID)
	userPosts := golem.GeneratedToMany[oracleUser, oraclePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	rows, err := CallerFindMany(ctx, caller, users,
		golem.OrderBy(userName.Asc()), golem.Take[oracleUser](1),
		golem.Select[oracleUser](userID, userName, userPosts.Args(golem.OrderBy(postTitle.Desc()), golem.Take[oraclePost](1), golem.Select[oraclePost](postID, postTitle))),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("root rows=%d want=1", len(rows))
	}
	if name, ok := golem.Value(rows[0], userName).Get(); !ok || name != "visible-a" {
		t.Fatalf("root name=%q present=%t", name, ok)
	}
	children, ok := golem.Many(rows[0], userPosts).Get()
	if !ok || len(children) != 1 {
		t.Fatalf("children=%d present=%t", len(children), ok)
	}
	if title, present := golem.Value(children[0], postTitle).Get(); !present || title != "allowed-z" {
		t.Fatalf("authorized child title=%q present=%t", title, present)
	}

	count, err := CallerCount(ctx, caller, users)
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v want=2", count, err)
	}
	visibleID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000003")
	visibleSelector := golem.GeneratedUniqueSelectorValue[oracleUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, visibleID))
	unique, err := CallerFindUnique(ctx, caller, users, visibleSelector, golem.Select[oracleUser](userName))
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := golem.Value(unique, userName).Get(); !ok || name != "visible-b" {
		t.Fatalf("unique name=%q present=%t", name, ok)
	}
	hiddenID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	hiddenSelector := golem.GeneratedUniqueSelectorValue[oracleUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, hiddenID))
	_, err = CallerFindUnique(ctx, caller, users, hiddenSelector, golem.Select[oracleUser](userName))
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeNotFound {
		t.Fatalf("hidden unique error=%v", err)
	}

	cursorID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000002")
	cursor := golem.GeneratedUniqueSelectorValue[oracleUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, cursorID))
	page, err := CallerFindMany(ctx, caller, users, golem.OrderBy(userName.Asc()), golem.Cursor(cursor), golem.Take[oracleUser](2), golem.Select[oracleUser](userID, userName))
	if err != nil {
		t.Fatal(err)
	}
	if got := oracleUserIDs(t, page, userID); len(got) != 2 || got[0] != "00000000-0000-0000-0000-000000000002" || got[1] != "00000000-0000-0000-0000-000000000003" {
		t.Fatalf("cursor page=%v", got)
	}
	invisiblePage, err := CallerFindMany(ctx, caller, users, golem.OrderBy(userName.Asc()), golem.Cursor(hiddenSelector), golem.Take[oracleUser](2), golem.Select[oracleUser](userID))
	if err != nil || len(invisiblePage) != 0 {
		t.Fatalf("invisible cursor rows=%d err=%v", len(invisiblePage), err)
	}
}

func TestPostgreSQLLiveOrderingAgreesAcrossCollationProfiles(t *testing.T) {
	cDefault := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))
	linguistic := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))
	if cDefault == "" || linguistic == "" {
		t.Skip("both GOLEM_TEST_POSTGRES_DSN and GOLEM_TEST_POSTGRES_LINGUISTIC_DSN are required")
	}
	cOrder := postgresLiveSystemNameOrder(t, cDefault, "c")
	linguisticOrder := postgresLiveSystemNameOrder(t, linguistic, "linguistic")
	want := []string{"Z", "a", "é", "É"}
	if !reflect.DeepEqual(cOrder, want) || !reflect.DeepEqual(linguisticOrder, want) {
		t.Fatalf("forced portable order: C=%q linguistic=%q want=%q", cOrder, linguisticOrder, want)
	}
}

func postgresLiveSystemNameOrder(t *testing.T, dsn, label string) []string {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.New(t)
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p3_order_%s_%d", label, time.Now().UnixNano()))
	postgresSchema := fixture.PostgreSQL
	postgresSchema.Namespace.Name = namespace
	_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
	_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	})
	if err := provider.ApplyInitial(ctx, database, postgresSchema); err != nil {
		t.Fatal(err)
	}
	bundle := postgresRuntimeBundle(t, fixture, postgresSchema)
	users := golem.GeneratedModelDescriptor[oracleUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	posts := golem.GeneratedModelDescriptor[oraclePost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	allowUser := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		rules.CanRead(golem.All[oracleUser]())
		return rules.Freeze(fixture.User)
	})
	allowPost := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		rules.CanRead(golem.All[oraclePost]())
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{allowUser, allowPost}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[oraclePrincipal, oracleActor]{Database: p8RuntimeTestDatabase(database, golem.PostgreSQL), Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, oraclePrincipal) (oracleActor, error) { return oracleActor{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	qualifiedUsers := `"` + string(namespace) + `"."users"`
	for index, name := range []string{"É", "a", "Z", "é"} {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1)
		if _, err := database.ExecContext(ctx, `INSERT INTO `+qualifiedUsers+`("id","name") VALUES ($1,$2)`, id, name); err != nil {
			t.Fatal(err)
		}
	}
	name := golem.GeneratedTextField[oracleUser, string](fixture.UserName)
	rows, err := SystemFindMany(ctx, app.System(), users, golem.OrderBy(name.Asc()), golem.Select[oracleUser](name))
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, len(rows))
	for index, row := range rows {
		value, ok := golem.Value(row, name).Get()
		if !ok {
			t.Fatalf("row %d name is absent", index)
		}
		result[index] = value
	}
	cursorID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000003")
	cursor := golem.GeneratedUniqueSelectorValue[oracleUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, cursorID))
	cursorRows, err := SystemFindMany(ctx, app.System(), users, golem.OrderBy(name.Asc()), golem.Cursor(cursor), golem.Take[oracleUser](4), golem.Select[oracleUser](name))
	if err != nil {
		t.Fatal(err)
	}
	cursorOrder := make([]string, len(cursorRows))
	for index, row := range cursorRows {
		value, ok := golem.Value(row, name).Get()
		if !ok {
			t.Fatalf("cursor row %d name is absent", index)
		}
		cursorOrder[index] = value
	}
	wantCursor := []string{"Z", "a", "é", "É"}
	if !reflect.DeepEqual(cursorOrder, wantCursor) {
		t.Fatalf("%s cursor order=%q want=%q", label, cursorOrder, wantCursor)
	}
	return result
}

func postgresRuntimeBundle(t *testing.T, fixture schematest.Fixture, postgresSchema physical.PhysicalSchema) golem.SchemaBundle {
	t.Helper()
	payload, err := physical.CanonicalEncode(postgresSchema)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := physical.PhysicalFingerprint(postgresSchema)
	if err != nil {
		t.Fatal(err)
	}
	system, err := physical.SystemFingerprint(postgresSchema.Provider, postgresSchema.System)
	if err != nil {
		t.Fatal(err)
	}
	postgresDocument := golem.GeneratedProviderSchemaDocument(golem.PostgreSQL, golem.SchemaDigest(system), golem.GeneratedSchemaDocument(postgresSchema.Version, postgresSchema.CanonicalVersion, golem.SchemaDigest(fingerprint), payload))
	providers := fixture.Bundle.Providers()
	for index := range providers {
		if providers[index].Provider() == golem.PostgreSQL {
			providers[index] = postgresDocument
		}
	}
	return golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), fixture.Bundle.Contract(), providers...)
}
