package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestDefaultProjectionExcludesNonPublicFieldsAtRuntime(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{
		UserName: []compilerir.FieldMode{compilerir.ModeHidden},
	})
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "runtime-visibility.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000001", "secret"); err != nil {
		t.Fatal(err)
	}

	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userBinding := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userBinding, postBinding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{Allow: true}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	id := golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedTextField[testUser, string](fixture.UserName)
	rows, err := SystemFindMany(ctx, app.System(), userDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if _, present := golem.Value(rows[0], id).Get(); !present {
		t.Fatal("visible ID is absent from the default projection")
	}
	if value, present := golem.Value(rows[0], name).Get(); present {
		t.Fatalf("hidden name escaped the default projection: %q", value)
	}

	_, err = SystemFindMany(ctx, app.System(), userDescriptor, golem.Select[testUser](name))
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("explicit hidden selection error=%v", err)
	}
}
