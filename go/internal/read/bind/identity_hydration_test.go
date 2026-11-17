package bind

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestIdentityHydrationBindsOnlyExactActivePrimaryKey(t *testing.T) {
	f := schematest.NewCompositeRelation(t)
	descriptor := golem.GeneratedModelDescriptor[post](f.Item, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	owner := golem.GeneratedEqualField[post, golem.UUID](f.OwnerID)
	base, err := golem.FreezeFindMany(descriptor, golem.Select[post](owner), golem.Take[post](1))
	if err != nil {
		t.Fatal(err)
	}
	makeIdentity := func(fields []golem.FieldID, wrongType bool) golem.RuntimeRowIdentity {
		t.Helper()
		cells := make([]golem.RuntimeReadCell, len(fields))
		for index, field := range fields {
			cells[index] = golem.RuntimePresentReadCell(field, golem.UUID{15: byte(index + 1)}, nil)
		}
		if wrongType {
			cells[0] = golem.RuntimePresentReadCell(fields[0], "not-a-UUID", nil)
		}
		identity, err := golem.RuntimeNewRowIdentity(fields, cells)
		if err != nil {
			t.Fatal(err)
		}
		return identity
	}
	for _, test := range []struct {
		name             string
		fields           []golem.FieldID
		wrongType, valid bool
	}{
		{"complete", []golem.FieldID{f.ItemRegion, f.ItemID}, false, true},
		{"partial", []golem.FieldID{f.ItemID}, false, false},
		{"reversed", []golem.FieldID{f.ItemID, f.ItemRegion}, false, false},
		{"duplicate", []golem.FieldID{f.ItemRegion, f.ItemRegion}, false, false},
		{"non-key scalar", []golem.FieldID{f.OwnerRegion, f.OwnerID}, false, false},
		{"other model", []golem.FieldID{f.TenantRegion, f.TenantID}, false, false},
		{"wrong scalar type", []golem.FieldID{f.ItemRegion, f.ItemID}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup, err := golem.RuntimeIdentityHydrationRequest(base, []golem.RuntimeRowIdentity{makeIdentity(test.fields, test.wrongType)})
			if err != nil {
				t.Fatal(err)
			}
			_, fields, present := lookup.RuntimeIdentityCorrelation()
			if !present {
				t.Fatal("missing private correlation")
			}
			fields[0] = golem.FieldID{}
			bound, err := Request(lookup, f.Registry, policyir.PortableProviders())
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v bind error=%v", test.valid, err)
			}
			if test.valid {
				hydration, present := bound.IdentityHydration()
				if !present || hydration.Count() != 1 || len(hydration.Fields()) != 2 {
					t.Fatal("bound key metadata lost")
				}
			}
		})
	}
}

func TestIdentityHydrationRefusesUnboundedOrPagingBase(t *testing.T) {
	f := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[user](f.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	id := golem.GeneratedEqualField[user, golem.UUID](f.UserID)
	name := golem.GeneratedTextField[user, string](f.UserName)
	identity, err := golem.RuntimeNewRowIdentity([]golem.FieldID{f.UserID}, []golem.RuntimeReadCell{golem.RuntimePresentReadCell(f.UserID, golem.UUID{15: 1}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	for label, options := range map[string][]golem.ReadOption[user]{
		"unbounded":     nil,
		"wrong take":    {golem.Take[user](2)},
		"backward take": {golem.Take[user](-1), golem.OrderBy(id.Asc())},
		"order":         {golem.Take[user](1), golem.OrderBy(id.Asc())},
		"skip":          {golem.Take[user](1), golem.Skip[user](1)},
		"distinct":      {golem.Take[user](1), golem.Distinct[user](id)},
	} {
		t.Run(label, func(t *testing.T) {
			options = append(options, golem.Select[user](name))
			base, err := golem.FreezeFindMany(descriptor, options...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := golem.RuntimeIdentityHydrationRequest(base, []golem.RuntimeRowIdentity{identity}); err == nil {
				t.Fatal("invalid lookup base accepted")
			}
		})
	}
	base, err := golem.FreezeFindMany(descriptor, golem.Select[user](name), golem.Take[user](1))
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := golem.RuntimeIdentityHydrationRequest(base, []golem.RuntimeRowIdentity{identity})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := golem.RuntimeIdentityHydrationRequest(lookup, []golem.RuntimeRowIdentity{identity}); err == nil {
		t.Fatal("existing private correlation replaced")
	}
	if _, err := golem.RuntimeIdentityHydrationRequest(base, make([]golem.RuntimeRowIdentity, golem.RuntimeIdentityHydrationLimit+1)); err == nil {
		t.Fatal("unbounded identity inventory accepted")
	}
}
