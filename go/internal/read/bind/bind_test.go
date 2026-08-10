package bind

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type user struct{}
type post struct{}

func TestRequestBindsNestedSchemaOwnedRead(t *testing.T) {
	fixture := schematest.New(t)
	userDescriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userID := golem.GeneratedEqualField[user, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[user, string](fixture.UserName)
	postID := golem.GeneratedEqualField[post, golem.UUID](fixture.PostID)
	postTitle := golem.GeneratedTextField[post, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[user, post](fixture.UserPosts, fixture.Authorship, fixture.Post)

	frozen, err := golem.FreezeFindMany(userDescriptor,
		golem.Where(userName.Contains("roy")),
		golem.OrderBy(userID.Asc()),
		golem.Take[user](10),
		golem.Select[user](userID, userName, posts.Args(
			golem.OrderBy(postID.Desc()),
			golem.Select[post](postID, postTitle),
		)),
	)
	if err != nil {
		t.Fatal(err)
	}

	bound, err := Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if bound.Operation() != readir.FindMany || bound.ModelID() != policyir.ModelID(fixture.User) {
		t.Fatalf("root = %#v", bound)
	}
	if where, ok := bound.Where(); !ok || where.ModelID() != policyir.ModelID(fixture.User) {
		t.Fatal("root where was not bound")
	}
	selections := bound.Selection()
	if len(selections) != 3 || selections[2].Kind() != readir.SelectRelation {
		t.Fatalf("selections = %#v", selections)
	}
	child, ok := selections[2].Request()
	if !ok || child.ModelID() != policyir.ModelID(fixture.Post) || len(child.OrderBy()) != 1 || len(child.Selection()) != 2 {
		t.Fatalf("child = %#v", child)
	}
}

func TestRequestBindsToManyRelationCountWithWhere(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	postTitle := golem.GeneratedTextField[post, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[user, post](fixture.UserPosts, fixture.Authorship, fixture.Post)
	frozen, err := golem.FreezeFindMany(descriptor,
		golem.Select[user](posts.Count(golem.Where(postTitle.Contains("go")))),
	)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	selections := bound.Selection()
	if len(selections) != 1 || selections[0].Kind() != readir.SelectRelationCount {
		t.Fatalf("selections=%#v", selections)
	}
	child, ok := selections[0].Request()
	if !ok || child.Operation() != readir.Count || child.ModelID() != policyir.ModelID(fixture.Post) {
		t.Fatalf("child=%#v present=%t", child, ok)
	}
	if _, ok := child.Where(); !ok {
		t.Fatal("relation-count where was not bound")
	}
}

func TestRequestBindsRelationCountInsideNestedRelationProjection(t *testing.T) {
	fixture := schematest.New(t)
	postDescriptor := golem.GeneratedModelDescriptor[post](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	author := golem.GeneratedToOne[post, user](fixture.PostAuthor, fixture.Authorship, fixture.User)
	posts := golem.GeneratedToMany[user, post](fixture.UserPosts, fixture.Authorship, fixture.Post)
	postTitle := golem.GeneratedTextField[post, string](fixture.PostTitle)
	frozen, err := golem.FreezeFindMany(postDescriptor,
		golem.Select[post](author.Select(posts.Count(golem.Where(postTitle.Contains("go"))))),
	)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	root := bound.Selection()
	if len(root) != 1 || root[0].Kind() != readir.SelectRelation {
		t.Fatalf("root=%#v", root)
	}
	userChild, ok := root[0].Request()
	if !ok {
		t.Fatal("author child is absent")
	}
	nested := userChild.Selection()
	if len(nested) != 1 || nested[0].Kind() != readir.SelectRelationCount {
		t.Fatalf("nested=%#v", nested)
	}
	countChild, ok := nested[0].Request()
	if !ok || countChild.Operation() != readir.Count || countChild.ModelID() != policyir.ModelID(fixture.Post) {
		t.Fatalf("count child=%#v present=%t", countChild, ok)
	}
}

func TestRequestBindsCursorIncludeAndOmit(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userID := golem.GeneratedEqualField[user, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[user, string](fixture.UserName)
	postID := golem.GeneratedEqualField[post, golem.UUID](fixture.PostID)
	posts := golem.GeneratedToMany[user, post](fixture.UserPosts, fixture.Authorship, fixture.Post)
	selector := golem.GeneratedUniqueSelectorValue[user](fixture.User, fixture.UserKey,
		golem.GeneratedSelectorComponent(fixture.UserID, golem.UUID{1}),
	)
	frozen, err := golem.FreezeFindMany(descriptor,
		golem.Cursor(selector),
		golem.Include[user](posts.Select(postID)),
		golem.Omit[user](userName),
	)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if bound.ProjectionMode() != readir.ProjectionInclude {
		t.Fatalf("projection mode=%d", bound.ProjectionMode())
	}
	if omitted := bound.Omitted(); len(omitted) != 1 || omitted[0] != policyir.FieldID(fixture.UserName) {
		t.Fatalf("omitted=%x", omitted)
	}
	cursor, ok := bound.Cursor()
	if !ok || cursor.Selector().KeyID() != fixture.UserKey || len(cursor.Selector().Fields()) != 1 {
		t.Fatalf("cursor=%#v present=%t", cursor, ok)
	}
	if cursor.Predicate().ModelID() != policyir.ModelID(fixture.User) {
		t.Fatalf("cursor predicate model=%x", cursor.Predicate().ModelID())
	}
	if selections := bound.Selection(); len(selections) != 1 || selections[0].Kind() != readir.SelectRelation {
		t.Fatalf("selections=%#v", selections)
	}
	_ = userID // The selector field is deliberately represented by its generated identity value.
}

func TestRequestRejectsForgedAndCrossModelFacts(t *testing.T) {
	fixture := schematest.New(t)
	tests := []struct {
		name string
		make func() golem.FrozenReadRequest
		code ErrorCode
	}{
		{name: "cross-model scalar", make: func() golem.FrozenReadRequest {
			field := golem.GeneratedEqualField[user, string](fixture.PostTitle)
			descriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
			request, err := golem.FreezeFindMany(descriptor, golem.Select[user](field))
			if err != nil {
				t.Fatal(err)
			}
			return request
		}, code: CodeField},
		{name: "forged relation target", make: func() golem.FrozenReadRequest {
			relation := golem.GeneratedToMany[user, post](fixture.UserPosts, fixture.Authorship, fixture.User)
			field := golem.GeneratedEqualField[post, golem.UUID](fixture.PostID)
			descriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
			request, err := golem.FreezeFindMany(descriptor, golem.Select[user](relation.Select(field)))
			if err != nil {
				t.Fatal(err)
			}
			return request
		}, code: CodeRelation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Request(test.make(), fixture.Registry, policyir.PortableProviders())
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestRequestRejectsNonPublicFieldsInEveryExplicitPosition(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{
		UserID:    []compilerir.FieldMode{compilerir.ModeHidden},
		UserName:  []compilerir.FieldMode{compilerir.ModeWriteOnly},
		UserPosts: []compilerir.FieldMode{compilerir.ModeHidden},
	})
	descriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userID := golem.GeneratedEqualField[user, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[user, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[post, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[user, post](fixture.UserPosts, fixture.Authorship, fixture.Post)
	selector := golem.GeneratedUniqueSelectorValue[user](fixture.User, fixture.UserKey,
		golem.GeneratedSelectorComponent(fixture.UserID, golem.UUID{1}),
	)
	tests := []struct {
		name  string
		field golem.FieldID
		make  func() (golem.FrozenReadRequest, error)
	}{
		{name: "select", field: fixture.UserName, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Select[user](userName))
		}},
		{name: "where", field: fixture.UserName, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Where(userName.Contains("private")))
		}},
		{name: "order", field: fixture.UserName, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.OrderBy(userName.Asc()))
		}},
		{name: "distinct", field: fixture.UserName, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Distinct[user](userName))
		}},
		{name: "selector", field: fixture.UserID, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindUnique(descriptor, selector)
		}},
		{name: "cursor", field: fixture.UserID, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Cursor(selector))
		}},
		{name: "include", field: fixture.UserPosts, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Include[user](posts))
		}},
		{name: "omit", field: fixture.UserName, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Omit[user](userName))
		}},
		{name: "relation filter", field: fixture.UserPosts, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Where(posts.Some(postTitle.Contains("private"))))
		}},
		{name: "relation count", field: fixture.UserPosts, make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Select[user](posts.Count()))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, err := test.make()
			if err != nil {
				t.Fatal(err)
			}
			_, err = Request(frozen, fixture.Registry, policyir.PortableProviders())
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != CodeField || failure.Field != test.field {
				t.Fatalf("error=%v, want %s for field %x", err, CodeField, test.field)
			}
		})
	}
	_ = userID
}

func TestRequestRejectsNonPublicFieldsThroughVisibleRelations(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{
		PostTitle: []compilerir.FieldMode{compilerir.ModeWriteOnly},
	})
	descriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	postTitle := golem.GeneratedTextField[post, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[user, post](fixture.UserPosts, fixture.Authorship, fixture.Post)
	tests := []struct {
		name string
		make func() (golem.FrozenReadRequest, error)
	}{
		{name: "nested filter", make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Where(posts.Some(postTitle.Contains("private"))))
		}},
		{name: "nested selection", make: func() (golem.FrozenReadRequest, error) {
			return golem.FreezeFindMany(descriptor, golem.Select[user](posts.Select(postTitle)))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, err := test.make()
			if err != nil {
				t.Fatal(err)
			}
			_, err = Request(frozen, fixture.Registry, policyir.PortableProviders())
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != CodeField || failure.Field != fixture.PostTitle {
				t.Fatalf("error=%v, want %s for field %x", err, CodeField, fixture.PostTitle)
			}
		})
	}
}

func TestRequestDefaultProjectionAllowsSchemaToExcludeNonPublicFields(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{
		UserName: []compilerir.FieldMode{compilerir.ModeHidden},
	})
	descriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	frozen, err := golem.FreezeFindMany(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if bound.ProjectionMode() != readir.ProjectionDefault || len(bound.Selection()) != 0 {
		t.Fatalf("default projection=%d selections=%#v", bound.ProjectionMode(), bound.Selection())
	}
}

func TestInternalPolicyBinderStillAllowsNonPublicFields(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{
		UserName: []compilerir.FieldMode{compilerir.ModeHidden},
	})
	userName := golem.GeneratedTextField[user, string](fixture.UserName)
	descriptor := golem.GeneratedModelDescriptor[user](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	frozen, err := userName.Contains("internal").Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policybind.Predicate(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if field, ok := condition.Field(); !ok || field != policyir.FieldID(fixture.UserName) {
		t.Fatalf("internal condition field=%x present=%t", field, ok)
	}
}
