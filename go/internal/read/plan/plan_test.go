package plan

import (
	"errors"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type planUser struct{}
type planPost struct{}
type policyMap map[policyir.ModelID]policyir.Policy

func (policies policyMap) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	value, ok := policies[model]
	return value, ok
}

func TestCallerMergesPolicyBeforePagingAndPlansNestedProjection(t *testing.T) {
	fixture := schematest.New(t)
	userDescriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userName := golem.GeneratedTextField[planUser, string](fixture.UserName)
	postID := golem.GeneratedEqualField[planPost, golem.UUID](fixture.PostID)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	frozen, err := golem.FreezeFindMany(userDescriptor,
		golem.Where(userName.Contains("r")),
		golem.OrderBy(userName.Desc()),
		golem.Skip[planUser](2), golem.Take[planUser](5),
		golem.Select[planUser](userName, posts.Args(golem.OrderBy(postID.Asc()), golem.Take[planPost](3), golem.Select[planPost](postID, postTitle))),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	policies := policyMap{policyir.ModelID(fixture.User): allowPolicy(t, fixture.User), policyir.ModelID(fixture.Post): allowPolicy(t, fixture.Post)}
	planned, err := Caller(request, fixture.Registry, policies, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if take, ok := planned.Take(); !ok || take != 5 {
		t.Fatalf("take=%d present=%t", take, ok)
	}
	if skip, ok := planned.Skip(); !ok || skip != 2 {
		t.Fatalf("skip=%d present=%t", skip, ok)
	}
	if len(planned.Fields()) != 2 || !planned.Fields()[0].Public() || planned.Fields()[1].Public() || len(planned.Relations()) != 1 {
		t.Fatalf("fields=%#v relations=%#v", planned.Fields(), planned.Relations())
	}
	child := planned.Relations()[0].Child()
	if take, ok := child.Take(); !ok || take != 3 || len(child.Fields()) != 2 {
		t.Fatalf("child=%#v", child)
	}
	// The normalized WHERE still contains the caller leaf. Paging lives only on
	// the plan and therefore cannot precede this authorized predicate.
	if planned.Where().Kind() != policyir.ConditionScalar {
		t.Fatalf("where kind=%d", planned.Where().Kind())
	}
}

func TestPlannerPreservesAuthorizedRelationCountInsideNestedProjection(t *testing.T) {
	fixture := schematest.New(t)
	postDescriptor := golem.GeneratedModelDescriptor[planPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	author := golem.GeneratedToOne[planPost, planUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	frozen, err := golem.FreezeFindMany(postDescriptor,
		golem.Select[planPost](author.Select(posts.Count(golem.Where(postTitle.Contains("go"))))),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := Caller(request, fixture.Registry, policyMap{
		policyir.ModelID(fixture.User): allowPolicy(t, fixture.User),
		policyir.ModelID(fixture.Post): allowPolicy(t, fixture.Post),
	}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Relations()) != 1 {
		t.Fatalf("root relations=%#v", planned.Relations())
	}
	child := planned.Relations()[0].Child()
	counts := child.RelationCounts()
	if len(counts) != 1 || counts[0].TargetModelID() != policyir.ModelID(fixture.Post) || counts[0].Child().Operation() != readir.Count {
		t.Fatalf("nested counts=%#v", counts)
	}
	if counts[0].Child().Where().ModelID() != policyir.ModelID(fixture.Post) {
		t.Fatal("nested count lost its independently authorized target predicate")
	}
}

func TestSystemExpandsIncludeOmitAndPreservesCursor(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userName := golem.GeneratedTextField[planUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	selector := golem.GeneratedUniqueSelectorValue[planUser](fixture.User, fixture.UserKey,
		golem.GeneratedSelectorComponent(fixture.UserID, golem.UUID{1}),
	)
	frozen, err := golem.FreezeFindMany(descriptor,
		golem.Cursor(selector),
		golem.Include[planUser](posts.Args(golem.Omit[planPost](postTitle))),
		golem.Omit[planUser](userName),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(request, fixture.Registry, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := planned.Cursor(); !ok {
		t.Fatal("cursor was not preserved in the provider-neutral plan")
	}
	fields := planned.Fields()
	if len(fields) != 1 || fields[0].FieldID() != policyir.FieldID(fixture.UserID) || !fields[0].Public() {
		t.Fatalf("root fields=%#v", fields)
	}
	relations := planned.Relations()
	if len(relations) != 1 || !relations[0].Public() {
		t.Fatalf("relations=%#v", relations)
	}
	childFields := relations[0].Child().Fields()
	if len(childFields) != 2 || childFields[0].FieldID() != policyir.FieldID(fixture.PostID) || childFields[1].FieldID() != policyir.FieldID(fixture.AuthorID) {
		t.Fatalf("child fields=%#v", childFields)
	}
}

func TestRelationEveryIsRewrittenAsNoAuthorizedCounterexample(t *testing.T) {
	fixture := schematest.New(t)
	userDescriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	frozen, err := golem.FreezeFindMany(userDescriptor, golem.Where(posts.Every(postTitle.Contains("safe"))))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := Caller(request, fixture.Registry, policyMap{policyir.ModelID(fixture.User): allowPolicy(t, fixture.User), policyir.ModelID(fixture.Post): allowPolicy(t, fixture.Post)}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	condition := planned.Where()
	if condition.Kind() != policyir.ConditionRelation {
		t.Fatalf("where kind=%d", condition.Kind())
	}
	operator, _ := condition.Operator()
	_, _, _, _, child, _ := condition.Relation()
	if operator != policyir.OperatorRelationNone || child == nil {
		t.Fatalf("operator=%d child=%v", operator, child)
	}
	logical, children, ok := child.Logical()
	if !ok || logical != policyir.LogicalNot || len(children) != 1 {
		t.Fatalf("counterexample child was not NOT(caller predicate): %#v", child)
	}
}

func TestRelationNullUsesVisibilityAwareExistence(t *testing.T) {
	fixture := schematest.New(t)
	postDescriptor := golem.GeneratedModelDescriptor[planPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	author := golem.GeneratedToOne[planPost, planUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	frozen, err := golem.FreezeFindMany(postDescriptor, golem.Where(author.IsNull()))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := Caller(request, fixture.Registry, policyMap{policyir.ModelID(fixture.User): denyPolicy(t, fixture.User), policyir.ModelID(fixture.Post): allowPolicy(t, fixture.Post)}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	logical, children, ok := planned.Where().Logical()
	if !ok || logical != policyir.LogicalNot || len(children) != 1 {
		t.Fatalf("visibility-aware null condition=%#v", planned.Where())
	}
	op, _ := children[0].Operator()
	if op != policyir.OperatorRelationIs {
		t.Fatalf("existence operator=%d", op)
	}
}

func TestPlannerRefusesMissingTargetPolicyAndLimits(t *testing.T) {
	fixture := schematest.New(t)
	userDescriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	frozen, err := golem.FreezeFindMany(userDescriptor, golem.Where(posts.Some(postTitle.Eq("x"))), golem.Take[planUser](10))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	_, err = Caller(request, fixture.Registry, policyMap{policyir.ModelID(fixture.User): allowPolicy(t, fixture.User)}, DefaultLimits())
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodePolicy {
		t.Fatalf("missing target error=%v", err)
	}
	limits := DefaultLimits()
	limits.MaxTake = 5
	_, err = System(request, fixture.Registry, limits)
	if !errors.As(err, &failure) || failure.Code != CodeLimit {
		t.Fatalf("limit error=%v", err)
	}
}

func TestConditionalFieldDisclosureIsDischargedAtRootNestedAndCountPositions(t *testing.T) {
	fixture := schematest.New(t)
	allowedID, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	userDescriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userID := golem.GeneratedEqualField[planUser, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[planUser, string](fixture.UserName)
	postID := golem.GeneratedEqualField[planPost, golem.UUID](fixture.PostID)
	authorID := golem.GeneratedEqualField[planPost, golem.UUID](fixture.AuthorID)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)

	userRules := golem.NewRules[planUser]()
	userRules.CanRead(golem.All[planUser]())
	userRules.CannotReadFields(golem.All[planUser](), userName)
	userRules.CanReadFields(userID.Eq(allowedID), userName)
	userPolicy := bindPlanPolicy(t, fixture, fixture.User, userRules)

	postRules := golem.NewRules[planPost]()
	postRules.CanRead(golem.All[planPost]())
	postRules.CannotReadFields(golem.All[planPost](), postTitle)
	postRules.CanReadFields(authorID.Eq(allowedID), postTitle)
	postPolicy := bindPlanPolicy(t, fixture, fixture.Post, postRules)
	policies := policyMap{policyir.ModelID(fixture.User): userPolicy, policyir.ModelID(fixture.Post): postPolicy}

	planRequest := func(t *testing.T, options ...golem.ReadOption[planUser]) (Plan, error) {
		t.Helper()
		frozen, err := golem.FreezeFindMany(userDescriptor, options...)
		if err != nil {
			t.Fatal(err)
		}
		bound, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		return Caller(bound, fixture.Registry, policies, DefaultLimits())
	}
	assertFieldFailure := func(t *testing.T, err error, field golem.FieldID) {
		t.Helper()
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != CodeField || failure.Field != policyir.FieldID(field) {
			t.Fatalf("field failure=%v, want %x", err, field)
		}
	}

	// Root filtering narrows the complete statement reach enough to use the
	// conditional field for order and distinct. Without that narrowing both
	// positions fail before any SQL is rendered.
	if _, err := planRequest(t,
		golem.Where(userID.Eq(allowedID)), golem.OrderBy(userName.Asc()),
		golem.Distinct[planUser](userName), golem.Select[planUser](userName),
	); err != nil {
		t.Fatalf("discharged root field: %v", err)
	}
	_, err := planRequest(t, golem.OrderBy(userName.Asc()), golem.Select[planUser](userID))
	assertFieldFailure(t, err, fixture.UserName)
	_, err = planRequest(t, golem.Distinct[planUser](userName), golem.Select[planUser](userID))
	assertFieldFailure(t, err, fixture.UserName)

	// The child planner repeats the same proof for nested filter/order/distinct
	// positions; it cannot inherit a discharge from the parent model.
	if _, err := planRequest(t, golem.Select[planUser](posts.Args(
		golem.Where(authorID.Eq(allowedID)), golem.OrderBy(postTitle.Asc()),
		golem.Distinct[planPost](postTitle), golem.Select[planPost](postID, postTitle),
	))); err != nil {
		t.Fatalf("discharged nested field: %v", err)
	}
	_, err = planRequest(t, golem.Select[planUser](posts.Args(
		golem.OrderBy(postTitle.Asc()), golem.Select[planPost](postID),
	)))
	assertFieldFailure(t, err, fixture.PostTitle)

	// Relation-count predicates are independently planned target statements.
	// A sibling target predicate may discharge a conditional field used by the
	// count filter; without it the count is refused.
	if _, err := planRequest(t, golem.Select[planUser](posts.Count(
		golem.Where(authorID.Eq(allowedID).And(postTitle.Contains("visible"))),
	))); err != nil {
		t.Fatalf("discharged relation-count filter: %v", err)
	}
	_, err = planRequest(t, golem.Select[planUser](posts.Count(
		golem.Where(postTitle.Contains("visible")),
	)))
	assertFieldFailure(t, err, fixture.PostTitle)

	// A unique-selector predicate participates in the selecting reach. Cursor
	// lookup additionally requires the same field to be readable for the page's
	// deterministic order; the cursor tuple alone must not authorize every row.
	identityRules := golem.NewRules[planUser]()
	identityRules.CanRead(golem.All[planUser]())
	identityRules.CannotReadFields(golem.All[planUser](), userID)
	identityRules.CanReadFields(userID.Eq(allowedID), userID)
	identityPolicy := bindPlanPolicy(t, fixture, fixture.User, identityRules)
	identityPolicies := policyMap{policyir.ModelID(fixture.User): identityPolicy, policyir.ModelID(fixture.Post): postPolicy}
	selector := golem.GeneratedUniqueSelectorValue[planUser](fixture.User, fixture.UserKey, golem.GeneratedSelectorComponent(fixture.UserID, allowedID))
	uniqueFrozen, err := golem.FreezeFindUnique(userDescriptor, selector, golem.Select[planUser](userName))
	if err != nil {
		t.Fatal(err)
	}
	uniqueRequest, err := readbind.Request(uniqueFrozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Caller(uniqueRequest, fixture.Registry, identityPolicies, DefaultLimits()); err != nil {
		t.Fatalf("selector predicate did not discharge its conditional identity: %v", err)
	}
	cursorFrozen, err := golem.FreezeFindMany(userDescriptor,
		golem.Where(userID.Eq(allowedID)), golem.Cursor(selector), golem.OrderBy(userID.Asc()), golem.Select[planUser](userName),
	)
	if err != nil {
		t.Fatal(err)
	}
	cursorRequest, err := readbind.Request(cursorFrozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Caller(cursorRequest, fixture.Registry, identityPolicies, DefaultLimits()); err != nil {
		t.Fatalf("cursor/order identity was not discharged by statement reach: %v", err)
	}
	undischargedCursor, err := golem.FreezeFindMany(userDescriptor,
		golem.Cursor(selector), golem.OrderBy(userID.Asc()), golem.Select[planUser](userName),
	)
	if err != nil {
		t.Fatal(err)
	}
	undischargedRequest, err := readbind.Request(undischargedCursor, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Caller(undischargedRequest, fixture.Registry, identityPolicies, DefaultLimits())
	assertFieldFailure(t, err, fixture.UserID)
}

func bindPlanPolicy[M any](t *testing.T, fixture schematest.Fixture, model golem.ModelID, rules *golem.Rules[M]) policyir.Policy {
	t.Helper()
	frozen, err := rules.Freeze(model)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := policybind.Policy(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalize.Policy(bound)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func TestMaskRelationHydrationIsPrivateUnpagedAndLimitChecked(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userName := golem.GeneratedTextField[planUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	author := golem.GeneratedToOne[planPost, planUser](fixture.PostAuthor, fixture.Authorship, fixture.User)

	userRules := golem.NewRules[planUser]()
	userRules.CanRead(golem.All[planUser]())
	userRules.CannotReadFields(golem.All[planUser](), userName)
	userRules.CanReadFields(posts.Some(author.Is(userName.Eq("owner"))), userName)
	frozenUser, err := userRules.Freeze(fixture.User)
	if err != nil {
		t.Fatal(err)
	}
	userPolicy, err := policybind.Policy(frozenUser, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	userPolicy, err = normalize.Policy(userPolicy)
	if err != nil {
		t.Fatal(err)
	}
	policies := policyMap{
		policyir.ModelID(fixture.User): userPolicy,
		policyir.ModelID(fixture.Post): allowPolicy(t, fixture.Post),
	}
	frozen, err := golem.FreezeFindMany(descriptor,
		golem.Select[planUser](userName, posts.Args(
			golem.Where(postTitle.Eq("caller-page")),
			golem.Take[planPost](1),
			golem.Select[planPost](postTitle),
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := Caller(request, fixture.Registry, policies, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Relations()) != 1 || len(planned.Hydrations()) != 1 {
		t.Fatalf("public relations=%d private hydrations=%d", len(planned.Relations()), len(planned.Hydrations()))
	}
	if take, ok := planned.Relations()[0].Child().Take(); !ok || take != 1 {
		t.Fatalf("public child take=%d present=%t", take, ok)
	}
	if take, ok := planned.Hydrations()[0].Child().Take(); ok {
		t.Fatalf("private hydration inherited caller take=%d", take)
	}
	if planned.Hydrations()[0].Public() {
		t.Fatal("policy-only hydration became public")
	}
	if nested := planned.Hydrations()[0].Child().Hydrations(); len(nested) != 1 || nested[0].FieldID() != policyir.FieldID(fixture.PostAuthor) {
		t.Fatalf("recursive private hydration=%#v", nested)
	}
	deniedTargets := policyMap{
		policyir.ModelID(fixture.User): userPolicy,
		policyir.ModelID(fixture.Post): denyPolicy(t, fixture.Post),
	}
	minimalFrozen, err := golem.FreezeFindMany(descriptor, golem.Select[planUser](userName))
	if err != nil {
		t.Fatal(err)
	}
	minimalRequest, err := readbind.Request(minimalFrozen, fixture.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	deniedPlan, err := Caller(minimalRequest, fixture.Registry, deniedTargets, DefaultLimits())
	if err != nil {
		t.Fatalf("denied dependency target rejected readable root: %v", err)
	}
	deniedWhere := deniedPlan.Hydrations()[0].Child().Where()
	if truth, constant := deniedWhere.Constant(); !constant || truth {
		t.Fatalf("denied dependency target predicate truth=%t constant=%t", truth, constant)
	}

	depth := DefaultLimits()
	depth.MaxRelationDepth = 0
	_, err = Caller(request, fixture.Registry, policies, depth)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeLimit {
		t.Fatalf("dependency depth error=%v", err)
	}
	selected := DefaultLimits()
	selected.MaxSelected = 2
	_, err = Caller(request, fixture.Registry, policies, selected)
	if !errors.As(err, &failure) || failure.Code != CodeLimit || failure.Reason != golem.ReasonPolicyInvalid || failure.Limit != "" {
		t.Fatalf("dependency selection error=%#v", failure)
	}
	selected.MaxSelected = 1
	_, err = Caller(minimalRequest, fixture.Registry, policies, selected)
	if !errors.As(err, &failure) || failure.Code != CodeLimit || failure.Reason != golem.ReasonPolicyInvalid || failure.Limit != "" {
		t.Fatalf("policy-only selection overflow error=%#v", failure)
	}
	_, err = Caller(request, fixture.Registry, policies, selected)
	if !errors.As(err, &failure) || failure.Code != CodeLimit || failure.Reason != golem.ReasonLimitExceeded || failure.Limit != golem.LimitSelectedFields || failure.Maximum != 1 {
		t.Fatalf("caller selection overflow error=%#v", failure)
	}
}

func TestModelContractMaxTakeIsEnforcedWithoutInventedDefaultCap(t *testing.T) {
	limited := schematest.NewWithMaxTake(t, 5, 0)
	descriptor := golem.GeneratedModelDescriptor[planUser](limited.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[planUser, string](limited.UserName)
	frozen, err := golem.FreezeFindMany(descriptor, golem.Take[planUser](6), golem.Select[planUser](name))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, limited.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := System(request, limited.Registry, DefaultLimits()); err == nil {
		t.Fatal("model maxTake was not enforced")
	}

	unlimited := schematest.New(t)
	descriptor = golem.GeneratedModelDescriptor[planUser](unlimited.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name = golem.GeneratedTextField[planUser, string](unlimited.UserName)
	frozen, err = golem.FreezeFindMany(descriptor, golem.Take[planUser](1001), golem.Select[planUser](name))
	if err != nil {
		t.Fatal(err)
	}
	request, err = readbind.Request(frozen, unlimited.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := System(request, unlimited.Registry, DefaultLimits()); err != nil {
		t.Fatalf("unconfigured model received an invented global cap: %v", err)
	}
}

func TestConfiguredRowLimitsPlanCapPlusOneAndPreserveStricterSchemaCaps(t *testing.T) {
	fixture := schematest.NewWithMaxTake(t, 2, 1)
	users := golem.GeneratedModelDescriptor[planUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	userName := golem.GeneratedTextField[planUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[planUser, planPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	freeze := func(t *testing.T, options ...golem.ReadOption[planUser]) readir.Request {
		t.Helper()
		frozen, err := golem.FreezeFindMany(users, options...)
		if err != nil {
			t.Fatal(err)
		}
		request, err := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	limits := DefaultLimits()
	limits.MaxTake = 5
	limits.MaxRelationFanout = 5

	planned, err := System(freeze(t, golem.Select[planUser](userName, posts.Select(postTitle))), fixture.Registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	if take, ok := planned.Take(); !ok || take != 3 || planned.ResultLimit() != 2 {
		t.Fatalf("root take=%d present=%t resultLimit=%d", take, ok, planned.ResultLimit())
	}
	child := planned.Relations()[0].Child()
	if take, ok := child.Take(); !ok || take != 2 || child.ResultLimit() != 1 {
		t.Fatalf("child take=%d present=%t resultLimit=%d", take, ok, child.ResultLimit())
	}

	exact, err := System(freeze(t, golem.Take[planUser](2), golem.Select[planUser](userName)), fixture.Registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	if take, _ := exact.Take(); take != 2 || exact.ResultLimit() != 0 {
		t.Fatalf("exact take=%d resultLimit=%d", take, exact.ResultLimit())
	}
	_, err = System(freeze(t, golem.Take[planUser](3), golem.Select[planUser](userName)), fixture.Registry, limits)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeLimit {
		t.Fatalf("overflow request error=%v", err)
	}

	unlimited := schematest.New(t)
	unlimitedUsers := golem.GeneratedModelDescriptor[planUser](unlimited.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	unlimitedName := golem.GeneratedTextField[planUser, string](unlimited.UserName)
	frozen, err := golem.FreezeFindMany(unlimitedUsers, golem.Select[planUser](unlimitedName))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, unlimited.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	withoutRowCaps, err := System(request, unlimited.Registry, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := withoutRowCaps.Take(); ok || withoutRowCaps.ResultLimit() != 0 {
		t.Fatal("default limits invented a row cap")
	}
}

func allowPolicy(t *testing.T, model golem.ModelID) policyir.Policy {
	return publicPolicy(t, model, true)
}
func denyPolicy(t *testing.T, model golem.ModelID) policyir.Policy {
	return publicPolicy(t, model, false)
}

func publicPolicy(t *testing.T, model golem.ModelID, allow bool) policyir.Policy {
	t.Helper()
	type marker struct{}
	rules := golem.NewRules[marker]()
	if allow {
		rules.CanRead(golem.All[marker]())
	} else {
		rules.CanRead(golem.None[marker]())
	}
	frozen, err := rules.Freeze(model)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := policybind.Policy(frozen, schematest.New(t).Registry, policyir.PortableProviders())
	if err == nil { // The helper registry has stable fixture identities.
		normalized, normalizeErr := normalize.Policy(bound)
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		return normalized
	}
	// Binding against a fresh fixture is safe because its IDs are deterministic;
	// retain the original error if that invariant ever changes.
	t.Fatal(err)
	return policyir.Policy{}
}

type planItem struct{}
type planKeyedPost struct{}

type planFieldSummary struct {
	field       policyir.FieldID
	public      bool
	conditional bool
}

func summarizeFields(planned Plan) []planFieldSummary {
	fields := planned.Fields()
	result := make([]planFieldSummary, len(fields))
	for index, field := range fields {
		result[index] = planFieldSummary{field: field.FieldID(), public: field.Public(), conditional: field.Conditional()}
	}
	return result
}

func publicFieldIDs(planned Plan) []policyir.FieldID {
	result := make([]policyir.FieldID, 0, len(planned.Fields()))
	for _, field := range planned.Fields() {
		if field.Public() {
			result = append(result, field.FieldID())
		}
	}
	return result
}

func planRead[M any](t *testing.T, registry *schema.Registry, descriptor golem.ModelDescriptor[M], system bool, policies PolicySet, limits Limits, options ...golem.ReadOption[M]) (Plan, error) {
	t.Helper()
	frozen, err := golem.FreezeFindMany(descriptor, options...)
	if err != nil {
		t.Fatal(err)
	}
	request, err := readbind.Request(frozen, registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if system {
		return System(request, registry, limits)
	}
	return Caller(request, registry, policies, limits)
}

func scalarRequest(t *testing.T, model golem.ModelID, unordered bool, fields ...golem.FieldID) readir.Request {
	t.Helper()
	selections := make([]readir.Selection, len(fields))
	for index, field := range fields {
		selection, err := readir.NewScalarSelection(policyir.FieldID(field))
		if err != nil {
			t.Fatal(err)
		}
		selections[index] = selection
	}
	request, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(model), Projection: readir.ProjectionSelect, Selection: selections, Unordered: unordered})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestSelectedFieldLimitUnchangedByHiddenIdentity(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	policies := policyMap{policyir.ModelID(fixture.Post): allowPolicy(t, fixture.Post)}
	limits := DefaultLimits()
	limits.MaxSelected = 2
	build := func(system bool, request readir.Request) (Plan, error) {
		if system {
			return System(request, fixture.Registry, limits)
		}
		return Caller(request, fixture.Registry, policies, limits)
	}
	assertSelectedFieldLimit := func(t *testing.T, err error) {
		t.Helper()
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != CodeLimit || failure.Limit != golem.LimitSelectedFields || failure.Maximum != 2 || failure.Model != policyir.ModelID(fixture.Post) || failure.Detail != "selected field limit exceeded" {
			t.Fatalf("limit error=%#v err=%v", failure, err)
		}
	}
	for _, mode := range []struct {
		name   string
		system bool
	}{{"system", true}, {"caller", false}} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			exact, err := build(mode.system, scalarRequest(t, fixture.Post, true, fixture.AuthorID, fixture.PostTitle))
			if err != nil {
				t.Fatalf("MaxSelected non-key selections: %v", err)
			}
			if got := publicFieldIDs(exact); !reflect.DeepEqual(got, []policyir.FieldID{policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)}) {
				t.Fatalf("public fields=%x", got)
			}
			if _, err := build(mode.system, scalarRequest(t, fixture.Post, true, fixture.PostID, fixture.AuthorID)); err != nil {
				t.Fatalf("MaxSelected selections including the key: %v", err)
			}
			_, err = build(mode.system, scalarRequest(t, fixture.Post, true, fixture.AuthorID, fixture.PostTitle, fixture.PostBigInt))
			assertSelectedFieldLimit(t, err)

			ordered, err := build(mode.system, scalarRequest(t, fixture.Post, false, fixture.PostTitle))
			if err != nil {
				t.Fatalf("one selection plus the primary-key tie-breaker: %v", err)
			}
			if got := publicFieldIDs(ordered); !reflect.DeepEqual(got, []policyir.FieldID{policyir.FieldID(fixture.PostTitle)}) {
				t.Fatalf("ordered public fields=%x", got)
			}
			_, err = build(mode.system, scalarRequest(t, fixture.Post, false, fixture.AuthorID, fixture.PostTitle, fixture.PostBigInt))
			assertSelectedFieldLimit(t, err)
		})
	}
}

func TestConditionallyMaskedKeyStaysConditionalInPlan(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[planPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	postID := golem.GeneratedEqualField[planPost, golem.UUID](fixture.PostID)
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	rules := golem.NewRules[planPost]()
	rules.CanRead(golem.All[planPost]())
	rules.CannotReadFields(golem.All[planPost](), postID)
	rules.CanReadFields(postTitle.EndsWith("-open"), postID)
	policies := policyMap{policyir.ModelID(fixture.Post): bindPlanPolicy(t, fixture, fixture.Post, rules)}

	unordered, err := Caller(scalarRequest(t, fixture.Post, true, fixture.PostID), fixture.Registry, policies, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want := []planFieldSummary{
		{field: policyir.FieldID(fixture.PostID), public: true, conditional: true},
		{field: policyir.FieldID(fixture.PostTitle), public: false, conditional: false},
	}
	if got := summarizeFields(unordered); !reflect.DeepEqual(got, want) {
		t.Fatalf("masked key fields=%+v want %+v", got, want)
	}
	key := unordered.Fields()[0]
	if mask, present := key.Mask(); !present || key.DischargedByConstraint() || mask.ModelID() != policyir.ModelID(fixture.Post) {
		t.Fatalf("key mask present=%t discharged=%t", present, key.DischargedByConstraint())
	}

	_, err = planRead(t, fixture.Registry, descriptor, false, policies, DefaultLimits(), golem.Select[planPost](postTitle))
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeField || failure.Field != policyir.FieldID(fixture.PostID) {
		t.Fatalf("ordered read of a conditionally masked key without a discharging filter: %v", err)
	}

	discharged, err := planRead(t, fixture.Registry, descriptor, false, policies, DefaultLimits(),
		golem.Where(postTitle.EndsWith("-open")), golem.Select[planPost](postID))
	if err != nil {
		t.Fatal(err)
	}
	if got := discharged.Fields()[0]; got.FieldID() != policyir.FieldID(fixture.PostID) || !got.Public() || !got.Conditional() || !got.DischargedByConstraint() {
		t.Fatalf("discharged key field=%+v", got)
	}
}

func TestCompositeAndTypedKeyIdentityKeepsComponentOrderInPlan(t *testing.T) {
	assertKeyOrder := func(t *testing.T, registry *schema.Registry, model golem.ModelID, planned Plan, selected []golem.FieldID, wantKey []golem.FieldID) {
		t.Helper()
		fact, ok := registry.Model(model)
		if !ok || !reflect.DeepEqual(fact.PrimaryKey(), wantKey) {
			t.Fatalf("registry key order=%x want %x", fact.PrimaryKey(), wantKey)
		}
		wantPublic := make([]policyir.FieldID, len(selected))
		for index, field := range selected {
			wantPublic[index] = policyir.FieldID(field)
		}
		if got := publicFieldIDs(planned); !reflect.DeepEqual(got, wantPublic) {
			t.Fatalf("public fields=%x want %x", got, wantPublic)
		}
		inKey := make(map[policyir.FieldID]bool, len(wantKey))
		for _, field := range wantKey {
			inKey[policyir.FieldID(field)] = true
		}
		var present []policyir.FieldID
		for _, field := range planned.Fields() {
			if inKey[field.FieldID()] {
				present = append(present, field.FieldID())
			}
		}
		var want []policyir.FieldID
		for _, field := range wantKey {
			for _, candidate := range present {
				if candidate == policyir.FieldID(field) {
					want = append(want, candidate)
				}
			}
		}
		if !reflect.DeepEqual(present, want) {
			t.Fatalf("key components in plan order=%x want key order %x", present, want)
		}
	}

	t.Run("composite-key-in-reverse-of-field-id-order", func(t *testing.T) {
		fixture := schematest.NewCompositeRelationReversedItemKey(t, "public")
		if !(string(fixture.ItemRegion[:]) < string(fixture.ItemID[:])) {
			t.Fatal("fixture precondition: Region sorts before ID")
		}
		descriptor := golem.GeneratedModelDescriptor[planItem](fixture.Item, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
		ownerRegion := golem.GeneratedEqualField[planItem, golem.UUID](fixture.OwnerRegion)
		planned, err := planRead(t, fixture.Registry, descriptor, true, nil, DefaultLimits(), golem.Select[planItem](ownerRegion))
		if err != nil {
			t.Fatal(err)
		}
		assertKeyOrder(t, fixture.Registry, fixture.Item, planned, []golem.FieldID{fixture.OwnerRegion}, []golem.FieldID{fixture.ItemID, fixture.ItemRegion})
	})
	t.Run("bytes-key", func(t *testing.T) {
		fixture := schematest.NewBytesKeyed(t)
		descriptor := golem.GeneratedModelDescriptor[planKeyedPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
		authorName := golem.GeneratedTextField[planKeyedPost, string](fixture.PostAuthorName)
		planned, err := planRead(t, fixture.Registry, descriptor, true, nil, DefaultLimits(), golem.Select[planKeyedPost](authorName))
		if err != nil {
			t.Fatal(err)
		}
		assertKeyOrder(t, fixture.Registry, fixture.Post, planned, []golem.FieldID{fixture.PostAuthorName}, []golem.FieldID{fixture.PostID, fixture.PostTitle})
	})
	t.Run("datetime-key", func(t *testing.T) {
		fixture := schematest.NewDateTimeKeyed(t)
		descriptor := golem.GeneratedModelDescriptor[planKeyedPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
		title := golem.GeneratedTextField[planKeyedPost, string](fixture.PostTitle)
		planned, err := planRead(t, fixture.Registry, descriptor, true, nil, DefaultLimits(), golem.Select[planKeyedPost](title))
		if err != nil {
			t.Fatal(err)
		}
		assertKeyOrder(t, fixture.Registry, fixture.Post, planned, []golem.FieldID{fixture.PostTitle}, []golem.FieldID{fixture.PostID, fixture.PostAt})
	})
}

func TestSystemAndCallerPlansCarryTheSameFields(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[planPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	postTitle := golem.GeneratedTextField[planPost, string](fixture.PostTitle)
	author := golem.GeneratedToOne[planPost, planUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	userName := golem.GeneratedTextField[planUser, string](fixture.UserName)
	policies := policyMap{policyir.ModelID(fixture.User): allowPolicy(t, fixture.User), policyir.ModelID(fixture.Post): allowPolicy(t, fixture.Post)}
	for _, selection := range []struct {
		name    string
		options []golem.ReadOption[planPost]
	}{
		{"root-scalar", []golem.ReadOption[planPost]{golem.Select[planPost](postTitle)}},
		{"relation", []golem.ReadOption[planPost]{golem.Select[planPost](postTitle, author.Select(userName))}},
	} {
		selection := selection
		t.Run(selection.name, func(t *testing.T) {
			system, err := planRead(t, fixture.Registry, descriptor, true, nil, DefaultLimits(), selection.options...)
			if err != nil {
				t.Fatal(err)
			}
			caller, err := planRead(t, fixture.Registry, descriptor, false, policies, DefaultLimits(), selection.options...)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := summarizeFields(caller), summarizeFields(system); !reflect.DeepEqual(got, want) {
				t.Fatalf("caller fields=%+v system fields=%+v", got, want)
			}
			if len(caller.Relations()) != len(system.Relations()) {
				t.Fatalf("caller relations=%d system relations=%d", len(caller.Relations()), len(system.Relations()))
			}
			for index := range system.Relations() {
				if got, want := summarizeFields(caller.Relations()[index].Child()), summarizeFields(system.Relations()[index].Child()); !reflect.DeepEqual(got, want) {
					t.Fatalf("caller relation fields=%+v system relation fields=%+v", got, want)
				}
			}
		})
	}
}
