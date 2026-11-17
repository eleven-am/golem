package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type invalidationRelationSource struct{}
type invalidationRelationTarget struct{}
type invalidationRelationActor struct{}

// This tests the actual runtime admission planner with bound public policies,
// fingerprinted real relation schemas and a verified SQLite execution binding.
// No stream is opened: unsafe ordinary routes must fail before hub registration.
type invalidationAdmissionCase struct {
	name, origin                string
	inverse, privateKeys, local bool
}

func TestInvalidationRelatedAdmissionRequiresSourceEvidence(t *testing.T) {
	runInvalidationAdmissionCases(t, []invalidationAdmissionCase{
		{name: "some private correlation", origin: "filter", privateKeys: true},
		{name: "narrowed parent unrelated readable child", origin: "filter"},
		{name: "inverse one membership", origin: "filter", inverse: true},
		{name: "ordinary row authority", origin: "row", privateKeys: true},
		{name: "ordinary watched field mask", origin: "mask", privateKeys: true},
	})
}

func TestInvalidationDeniedAdmissionIsSubscriptionForbidden(t *testing.T) {
	runInvalidationAdmissionCases(t, []invalidationAdmissionCase{
		{name: "read action denied", origin: "denied", local: true},
	})
}

func TestInvalidationCDCAdmissionRequiresCompleteBeforeIngress(t *testing.T) {
	runInvalidationAdmissionCases(t, []invalidationAdmissionCase{
		{name: "CDC update may omit before", origin: "cdc", local: true},
	})
}

func TestInvalidationLocalAdmissionRequiresBeforeEvidence(t *testing.T) {
	runInvalidationAdmissionCases(t, []invalidationAdmissionCase{
		{name: "uncaptured filter", origin: "filter", local: true},
		{name: "uncaptured row authority", origin: "row", local: true},
		{name: "uncaptured field mask", origin: "mask", local: true},
		{name: "uncaptured watched scalar", origin: "watch", local: true},
	})
}

func runInvalidationAdmissionCases(t *testing.T, cases []invalidationAdmissionCase) {
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var registry *schema.Registry
			var bundle golem.SchemaBundle
			var source, target golem.ModelID
			var sourceID, sourceName, targetID, targetFK, relationField golem.FieldID
			var relation golem.RelationID
			if test.inverse {
				f := schematest.NewSubscribedIndexedInverseRequiredHasOne(t)
				registry, bundle, source, target = f.Registry, f.Bundle, f.User, f.Post
				sourceID, sourceName, targetID, targetFK, relationField, relation = f.UserID, f.UserName, f.PostID, f.AuthorID, f.UserPosts, f.Authorship
			} else {
				f := schematest.NewSubscribedGraph(t)
				if test.local {
					f = schematest.NewSubscribedGraphRuntimeDefaults(t)
				}
				registry, bundle, source, target = f.Registry, f.Bundle, f.User, f.Post
				sourceID, sourceName, targetID, targetFK, relationField, relation = f.UserID, f.UserName, f.PostID, f.AuthorID, f.UserPosts, f.Authorship
			}
			parentID := golem.GeneratedEqualField[invalidationRelationSource, golem.UUID](sourceID)
			parentName := golem.GeneratedTextField[invalidationRelationSource, string](sourceName)
			watchID := sourceName
			var watchField golem.Field[invalidationRelationSource] = parentName
			if test.local && test.origin != "watch" {
				watchID = sourceID
				watchField = parentID
			}
			childID := golem.GeneratedEqualField[invalidationRelationTarget, golem.UUID](targetID)
			childFK := golem.GeneratedEqualField[invalidationRelationTarget, golem.UUID](targetFK)
			related := golem.GeneratedToMany[invalidationRelationSource, invalidationRelationTarget](relationField, relation, target).Some(golem.All[invalidationRelationTarget]())
			if test.inverse {
				related = golem.GeneratedToOne[invalidationRelationSource, invalidationRelationTarget](relationField, relation, target).IsNotNull()
			}
			if test.local {
				related = parentName.Eq("watch")
				metadata, _ := registry.Model(source)
				_, captured, _ := metadata.EventSchema()
				for _, field := range captured {
					if field == sourceName {
						t.Fatal("local missing-evidence fixture unexpectedly captures Name")
					}
				}
			}
			bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[invalidationRelationActor]{
				golem.GeneratedPolicyBinding[invalidationRelationActor, invalidationRelationSource](source, func(invalidationRelationActor) (golem.FrozenPolicy, error) {
					rules := golem.NewRules[invalidationRelationSource]()
					if test.origin == "denied" {
						rules.CanRead(golem.None[invalidationRelationSource]())
					} else if test.origin == "row" {
						rules.CanRead(related)
					} else {
						rules.CanRead(golem.All[invalidationRelationSource]())
					}
					if test.origin == "mask" {
						rules.CannotReadFields(golem.All[invalidationRelationSource](), watchField)
						rules.CanReadFields(related, watchField)
					}
					return rules.Freeze(source)
				}),
				golem.GeneratedPolicyBinding[invalidationRelationActor, invalidationRelationTarget](target, func(invalidationRelationActor) (golem.FrozenPolicy, error) {
					rules := golem.NewRules[invalidationRelationTarget]()
					rules.CanRead(golem.All[invalidationRelationTarget]())
					if test.privateKeys {
						rules.CannotReadFields(golem.All[invalidationRelationTarget](), childID, childFK)
					}
					return rules.Freeze(target)
				}),
			}, nil))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			database, _, err := sqlite.New().Open(ctx, "file:"+t.TempDir()+"/admission.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			proof, err := proveCapabilities(ctx, database, policyir.ProviderSQLite, [32]byte(registry.ModelFingerprint()))
			if err != nil {
				t.Fatal(err)
			}
			policies, err := policyruntime.Build(policyruntime.BuildRequest[invalidationRelationActor]{Bindings: bindings, Registry: registry, Provider: policyir.ProviderSQLite, Capabilities: proof})
			if err != nil {
				t.Fatal(err)
			}
			model, _ := registry.Model(target)
			digest, _, _ := model.EventSchema()
			parsed, err := mutationfact.ParseEventSchemaFingerprint(digest)
			if err != nil {
				t.Fatal(err)
			}
			metadata := golem.GeneratedEventModelMetadata(target, golem.EventSchemaDigest(parsed), []golem.FieldID{targetID}, "TargetEvent", "golem.UUID")
			eventRegistry, err := golem.GeneratedEventRegistry(bundle.GenerationDigest(), golem.GeneratedPackageEventRegistry(bundle.GenerationDigest(), metadata))
			if err != nil {
				t.Fatal(err)
			}
			providers, err := providerSet(registry)
			if err != nil {
				t.Fatal(err)
			}
			limits, err := normalizeReadLimits(ReadLimits{})
			if err != nil {
				t.Fatal(err)
			}
			app := &App[string, invalidationRelationActor]{database: database, registry: registry, provider: policyir.ProviderSQLite, providers: providers, capabilities: proof, bindings: bindings, eventRegistry: eventRegistry, readLimits: limits}
			if test.origin == "cdc" {
				// initializeEventRuntime retains these exact public adapters on App.
				// Exercise the readable admission owner directly, before any hub or
				// adapter is started, just as the other admission cases above do.
				app.eventAdapters = []events.CDCAdapter{invalidationIncompleteCDCAdapter{}}
				if _, err := events.ValidateCDCAdapters(golem.SQLite, app.eventAdapters); err != nil {
					t.Fatal(err)
				}
			}
			caller := &Caller[string, invalidationRelationActor]{app: app, policies: policies, execution: 1, executor: databaseExecution(database)}
			descriptor := golem.GeneratedModelDescriptor[invalidationRelationSource](source, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
			predicate := parentID.Eq(golem.UUID{15: 1})
			if test.local && test.origin == "mask" {
				predicate = golem.All[invalidationRelationSource]()
			}
			if test.origin == "filter" {
				predicate = predicate.And(related)
			}
			value, err := golem.RuntimeInvalidationRowsValue(descriptor, predicate)
			if err != nil {
				t.Fatal(err)
			}
			declaration := compilerir.CustomInvalidationDependencyIR{Kind: compilerir.CustomInvalidationReadableRows, ModelID: compilerir.ModelID(fmt.Sprintf("%x", source)), Fields: []compilerir.FieldID{compilerir.FieldID(fmt.Sprintf("%x", watchID))}}
			_, _, _, members, err := app.invalidationReadable(ctx, caller, declaration, value)
			if test.origin == "denied" {
				if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionForbidden {
					t.Fatalf("expected classified subscription forbidden for denied read action, got %v", err)
				}
				if len(members) != 0 {
					t.Fatalf("denied admission retained %d members", len(members))
				}
				return
			}
			if err == nil {
				t.Fatalf("invalidation admitted without complete source observation evidence: %d members; origin=%s privateKeys=%v inverse=%v", len(members), test.origin, test.privateKeys, test.inverse)
			}
			if !strings.Contains(err.Error(), "P7_INVALIDATION_COVERAGE") {
				t.Fatalf("route failed for unrelated setup/permission reason: %v", err)
			}
			// A locally scoped declaration remains valid; this gate belongs only to
			// invalidation route coverage, not to ordinary GraphQL query semantics.
			// The inverse-one fixture intentionally has no User event contract;
			// it proves route rejection above, not local source admission.
			if test.origin == "filter" && !test.local && !test.inverse {
				local, err := golem.RuntimeInvalidationRowsValue(descriptor, parentID.Eq(golem.UUID{15: 1}))
				if err != nil {
					t.Fatal(err)
				}
				localDeclaration := declaration
				localDeclaration.Fields = []compilerir.FieldID{compilerir.FieldID(fmt.Sprintf("%x", sourceID))}
				if _, _, _, members, err := app.invalidationReadable(ctx, caller, localDeclaration, local); err != nil || len(members) != 1 {
					t.Fatalf("local source rejected by relation gate: %d %v", len(members), err)
				}
			}
		})
	}
}

func TestInvalidationCoverageAdmitsExplicitLocalSnapshot(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprintf("captured=%v", captured), func(t *testing.T) {
			fixture := schematest.NewSubscribedIndexed(t)
			if captured {
				fixture = schematest.NewSubscribedIndexedWithFullDeleteSnapshot(t)
			}
			descriptor := golem.GeneratedModelDescriptor[invalidationRelationTarget](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
			title := golem.GeneratedTextField[invalidationRelationTarget, string](fixture.PostTitle)
			frozen, err := title.Eq("watch").Freeze(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			providers, err := providerSet(fixture.Registry)
			if err != nil {
				t.Fatal(err)
			}
			condition, err := policybind.Predicate(frozen, fixture.Registry, providers)
			if err != nil {
				t.Fatal(err)
			}
			err = validateInvalidationReadableCoverage(fixture.Registry, fixture.Post, []golem.FieldID{fixture.PostTitle}, condition)
			if captured && err != nil {
				t.Fatalf("explicit captured local source refused: %v", err)
			}
			if !captured && (err == nil || !strings.Contains(err.Error(), "P7_INVALIDATION_COVERAGE")) {
				t.Fatalf("unrecorded local source admitted: %v", err)
			}
		})
	}
}
