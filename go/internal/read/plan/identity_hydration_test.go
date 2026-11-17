package plan

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
)

func TestIdentityHydrationKeepsHiddenKeyPrivateAndRetainsRowAuthority(t *testing.T) {
	f := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[planUser](f.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	id := golem.GeneratedEqualField[planUser, golem.UUID](f.UserID)
	name := golem.GeneratedTextField[planUser, string](f.UserName)
	rules := golem.NewRules[planUser]()
	rules.CanRead(name.Ne("denied"))
	rules.CannotReadFields(golem.All[planUser](), id)
	policies := policyMap{policyir.ModelID(f.User): bindPlanPolicy(t, f, f.User, rules)}
	identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{f.UserID}, []golem.RuntimeReadCell{golem.RuntimePresentReadCell(f.UserID, golem.UUID{15: 1}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	freeze := func(options ...golem.ReadOption[planUser]) golem.FrozenReadRequest {
		t.Helper()
		options = append(options, golem.Take[planUser](1))
		value, err := golem.FreezeFindMany(descriptor, options...)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	base := freeze(golem.Select[planUser](name))
	lookup, err := golem.RuntimeIdentityHydrationRequest(base, []golem.RuntimeRowIdentity{identity})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := lookup.Where(); present {
		t.Fatal("private identity escaped into caller where")
	}
	bound, err := readbind.Request(lookup, f.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	planned, err := Caller(bound, f.Registry, policies, DefaultLimits())
	if err != nil {
		t.Fatalf("title-only hydration requires public ID: %v", err)
	}
	if len(planned.OrderBy()) != 0 {
		t.Fatal("hydration injected caller-visible identity ordering")
	}
	privateKey, publicName := false, false
	for _, field := range planned.Fields() {
		if field.FieldID() == policyir.FieldID(f.UserID) {
			privateKey = !field.Public()
		}
		if field.FieldID() == policyir.FieldID(f.UserName) {
			publicName = field.Public()
		}
	}
	if !privateKey || !publicName {
		t.Fatal("identity lookup lost private PK or public projection")
	}
	for _, test := range []struct {
		name    string
		id      byte
		allowed bool
	}{{"visible", 1, true}, {"denied", 1, false}, {"visible", 2, false}} {
		text, _ := policyir.StringValue(test.name)
		nameField, _ := evaluate.ValueField(policyir.FieldID(f.UserName), text)
		idField, _ := evaluate.ValueField(policyir.FieldID(f.UserID), policyir.UUIDValue([16]byte{15: test.id}))
		record, err := evaluate.NewRecord(policyir.ModelID(f.User), nameField, idField)
		if err != nil {
			t.Fatal(err)
		}
		allowed, err := evaluate.Condition(planned.Where(), record, policyir.PortableProviders())
		if err != nil || allowed != test.allowed {
			t.Fatalf("row/identity intersection: allowed=%v want=%v err=%v", allowed, test.allowed, err)
		}
	}
	assertRefused := func(label string, frozen golem.FrozenReadRequest) {
		t.Helper()
		request, err := readbind.Request(frozen, f.Registry, policyir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		_, err = Caller(request, f.Registry, policies, DefaultLimits())
		var failure *Error
		if !errors.As(err, &failure) || failure.Reason != golem.ReasonFieldNotReadable {
			t.Fatalf("%s bypassed field readability: %v", label, err)
		}
	}
	for label, public := range map[string]golem.FrozenReadRequest{
		"explicit projection": freeze(golem.Select[planUser](id, name)),
		"caller where":        freeze(golem.Select[planUser](name), golem.Where(id.Eq(golem.UUID{15: 1}))),
	} {
		lookup, err := golem.RuntimeIdentityHydrationRequest(public, []golem.RuntimeRowIdentity{identity})
		if err != nil {
			t.Fatal(err)
		}
		assertRefused(label, lookup)
	}
	filter, err := id.Eq(golem.UUID{15: 1}).Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := golem.RuntimeIdentityHydrationRequest(base, []golem.RuntimeRowIdentity{identity}, filter)
	if err != nil {
		t.Fatal(err)
	}
	assertRefused("extra caller filter", filtered)
	assertRefused("ordinary hidden filter", freeze(golem.Select[planUser](name), golem.Where(id.Eq(golem.UUID{15: 1}))))
	assertRefused("ordinary hidden order", freeze(golem.Select[planUser](name), golem.OrderBy(id.Asc())))
	if _, err := golem.RuntimeIdentityHydrationRequest(freeze(golem.Select[planUser](name), golem.OrderBy(id.Asc())), []golem.RuntimeRowIdentity{identity}); err == nil {
		t.Fatal("hydration silently dropped caller ordering")
	}
	// Duplicate witnesses remain bounded by their admitted count, not a
	// rewritten take or a global unordered-read exception.
	duplicateBase, err := golem.FreezeFindMany(descriptor, golem.Select[planUser](name), golem.Take[planUser](2))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := golem.RuntimeIdentityHydrationRequest(duplicateBase, []golem.RuntimeRowIdentity{identity, identity})
	if err != nil {
		t.Fatal(err)
	}
	duplicateRequest, err := readbind.Request(duplicate, f.Registry, policyir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxTake = 1
	if _, err := Caller(duplicateRequest, f.Registry, policies, limits); err == nil {
		t.Fatal("hydration bypassed runtime MaxTake")
	}
}
