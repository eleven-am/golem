package runtime

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/gentest"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type runtimeActor struct {
	ID    int64
	Name  string
	Allow bool
}
type runtimeUser struct{}
type runtimePost struct{}

func TestBuildUsesFreshActorScopedFactoriesWithRealSchema(t *testing.T) {
	registry, fixture := runtimeSchemaFixture(t)
	var calls atomic.Int64
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(actor runtimeActor) (golem.FrozenPolicy, error) {
		calls.Add(1)
		rules := golem.NewRules[runtimeUser]()
		if actor.Allow {
			rules.CanRead(golem.All[runtimeUser]())
		} else {
			rules.CanRead(golem.None[runtimeUser]())
		}
		return rules.Freeze(fixture.user)
	})
	bindings := runtimeBindings(t, fixture.generation, binding)
	if inventory := bindings.PolicyInventory(); len(inventory) != 1 || inventory[0] != fixture.user || calls.Load() != 0 {
		t.Fatalf("static inventory executed factory or drifted: inventory=%x calls=%d", inventory, calls.Load())
	}
	proof := runtimeProof(t, registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)

	const executions = 64
	results := make([]bool, executions)
	sets := make([]*Set, executions)
	var wait sync.WaitGroup
	wait.Add(executions)
	for index := range executions {
		go func(index int) {
			defer wait.Done()
			set, err := Build(BuildRequest[runtimeActor]{Bindings: bindings, Actor: runtimeActor{ID: int64(index), Allow: index%2 == 0}, Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: proof})
			if err != nil {
				t.Errorf("execution %d: %v", index, err)
				return
			}
			policy, ok := set.Policy(policyir.ModelID(fixture.user))
			if !ok || len(policy.Rules()) != 1 {
				t.Errorf("execution %d: policy absent", index)
				return
			}
			condition, ok := policy.Rules()[0].Condition()
			if !ok {
				results[index], sets[index] = true, set
				return
			}
			truth, constant := condition.Constant()
			if !constant {
				t.Errorf("execution %d: normalized condition is not constant", index)
				return
			}
			results[index], sets[index] = truth, set
		}(index)
	}
	wait.Wait()
	if calls.Load() != executions {
		t.Fatalf("factory calls=%d want=%d", calls.Load(), executions)
	}
	for index, result := range results {
		if result != (index%2 == 0) {
			t.Fatalf("actor state leaked at execution %d: truth=%t", index, result)
		}
		if sets[index] == nil || sets[index].Provider() != policyir.ProviderPostgreSQL || sets[index].GenerationDigest() != fixture.generation {
			t.Fatalf("execution %d set identity drifted", index)
		}
	}
	// A later execution cannot mutate or replace an already returned set.
	firstPolicy, _ := sets[0].Policy(policyir.ModelID(fixture.user))
	if _, conditional := firstPolicy.Rules()[0].Condition(); conditional {
		t.Fatal("later actors mutated the first execution's policy set")
	}
}

func TestBuildRejectsGenerationProviderAndFingerprintBeforeFactory(t *testing.T) {
	registry, fixture := runtimeSchemaFixture(t)
	var calls atomic.Int64
	factory := func(runtimeActor) (golem.FrozenPolicy, error) {
		calls.Add(1)
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(golem.All[runtimeUser]())
		return rules.Freeze(fixture.user)
	}
	validBindings := runtimeBindings(t, fixture.generation, golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, factory))
	validProof := runtimeProof(t, registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)

	tests := []struct {
		name     string
		bindings golem.ApplicationBindings[runtimeActor]
		provider policyir.Provider
		proof    policysql.CapabilityProof
		code     string
	}{
		{
			name: "generation", bindings: runtimeBindings(t, golem.SchemaDigest{0xff}, golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, factory)),
			provider: policyir.ProviderPostgreSQL, proof: validProof, code: "P2_RUNTIME_GENERATION",
		},
		{
			name: "provider", bindings: validBindings, provider: policyir.ProviderSQLite,
			proof: validProof, code: "P2_RUNTIME_CAPABILITY",
		},
		{
			name: "fingerprint", bindings: validBindings, provider: policyir.ProviderPostgreSQL,
			proof: runtimeRawProof(t, policyir.ProviderPostgreSQL, [32]byte{0xff}, allRuntimeCapabilities...), code: "P2_RUNTIME_CAPABILITY",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls.Store(0)
			if _, err := Build(BuildRequest[runtimeActor]{Bindings: test.bindings, Registry: registry, Provider: test.provider, Capabilities: test.proof}); err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("error=%v want code %s", err, test.code)
			}
			if calls.Load() != 0 {
				t.Fatalf("factory ran before request validation: calls=%d", calls.Load())
			}
		})
	}
}

func TestBuildValidatesPolicyCapabilitiesAndFactoryExactlyOnceOnFailure(t *testing.T) {
	registry, fixture := runtimeSchemaFixture(t)
	var calls atomic.Int64
	handle := golem.GeneratedModeTextField[runtimeUser, string](fixture.userHandle)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(actor runtimeActor) (golem.FrozenPolicy, error) {
		calls.Add(1)
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(handle.Eq(actor.Name))
		return rules.Freeze(fixture.user)
	})
	bindings := runtimeBindings(t, fixture.generation, binding)
	proof := runtimeProof(t, registry, policyir.ProviderPostgreSQL)
	_, err := Build(BuildRequest[runtimeActor]{Bindings: bindings, Actor: runtimeActor{Name: "ada"}, Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: proof})
	if err == nil || !strings.Contains(err.Error(), "P2_RUNTIME_CAPABILITY") || !strings.Contains(err.Error(), "requires capability 1") {
		t.Fatalf("missing binary-text capability error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory calls=%d want exactly one", calls.Load())
	}

	calls.Store(0)
	set, err := Build(BuildRequest[runtimeActor]{Bindings: bindings, Actor: runtimeActor{Name: "ada"}, Registry: registry, Provider: policyir.ProviderPostgreSQL, Capabilities: runtimeProof(t, registry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)})
	if err != nil {
		t.Fatalf("agreement-proved non-constant policy was refused: %v", err)
	}
	if _, ok := set.Policy(policyir.ModelID(fixture.user)); !ok || calls.Load() != 1 {
		t.Fatalf("activated policy missing or factory calls=%d", calls.Load())
	}

	withoutRegistry, withoutFixture := runtimeSchemaFixtureOmitting(t, compilerir.CapabilityID(postgresql.CapabilityPolicyBinaryText))
	calls.Store(0)
	withoutHandle := golem.GeneratedModeTextField[runtimeUser, string](withoutFixture.userHandle)
	withoutBinding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](withoutFixture.user, func(actor runtimeActor) (golem.FrozenPolicy, error) {
		calls.Add(1)
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(withoutHandle.Eq(actor.Name))
		return rules.Freeze(withoutFixture.user)
	})
	withoutBindings := runtimeBindings(t, withoutFixture.generation, withoutBinding)
	fullProof := runtimeProof(t, withoutRegistry, policyir.ProviderPostgreSQL, allRuntimeCapabilities...)
	_, err = Build(BuildRequest[runtimeActor]{Bindings: withoutBindings, Actor: runtimeActor{Name: "ada"}, Registry: withoutRegistry, Provider: policyir.ProviderPostgreSQL, Capabilities: fullProof})
	if err == nil || !strings.Contains(err.Error(), "P2_RUNTIME_CAPABILITY") {
		t.Fatalf("missing registry capability error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory calls with missing registry fact=%d want exactly one", calls.Load())
	}
}

func TestBuildFailsClosedForNilRegistryAndPolicyLookupIsDetached(t *testing.T) {
	registry, fixture := runtimeSchemaFixture(t)
	binding := golem.GeneratedPolicyBinding[runtimeActor, runtimeUser](fixture.user, func(runtimeActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[runtimeUser]()
		rules.CanRead(golem.All[runtimeUser]())
		return rules.Freeze(fixture.user)
	})
	bindings := runtimeBindings(t, fixture.generation, binding)
	proof := runtimeProof(t, registry, policyir.ProviderSQLite, allRuntimeCapabilities...)
	if _, err := Build(BuildRequest[runtimeActor]{Bindings: bindings, Registry: nil, Provider: policyir.ProviderSQLite, Capabilities: proof}); err == nil || !strings.Contains(err.Error(), "P2_RUNTIME_SCHEMA") {
		t.Fatalf("nil registry error=%v", err)
	}
	set, err := Build(BuildRequest[runtimeActor]{Bindings: bindings, Registry: registry, Provider: policyir.ProviderSQLite, Capabilities: proof})
	if err != nil {
		t.Fatal(err)
	}
	policy, ok := set.Policy(policyir.ModelID(fixture.user))
	if !ok {
		t.Fatal("policy lookup failed")
	}
	rules := policy.Rules()
	rules[0] = policyir.Rule{}
	again, _ := set.Policy(policyir.ModelID(fixture.user))
	if len(again.Rules()) != 1 || again.Rules()[0].ModelID() != policyir.ModelID(fixture.user) {
		t.Fatal("policy accessor leaked mutable rule storage")
	}
	if _, ok := set.Policy(policyir.ModelID{0xff}); ok {
		t.Fatal("unknown model returned a policy")
	}
}

var allRuntimeCapabilities = []policyir.Capability{
	policyir.CapabilityBinaryText,
	policyir.CapabilityASCIIInsensitiveText,
	policyir.CapabilityExactJSON,
	policyir.CapabilityScalarListJSON,
	policyir.CapabilityRelationCorrelation,
}

type runtimeFixture struct {
	generation golem.SchemaDigest
	user       golem.ModelID
	post       golem.ModelID
	userHandle golem.FieldID
}

func runtimeSchemaFixture(t *testing.T) (*schema.Registry, runtimeFixture) {
	return runtimeSchemaFixtureOmitting(t, "")
}

func runtimeSchemaFixtureOmitting(t *testing.T, omittedPostgreSQLCapability compilerir.CapabilityID) (*schema.Registry, runtimeFixture) {
	t.Helper()
	compilation := gentest.SocialCompilationIR()
	modelBytes, err := compilerir.CanonicalModel(compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := compilerir.ModelFingerprint(compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	modelDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), runtimeDigest(t, string(modelFingerprint)), modelBytes)
	contractDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), runtimeDigest(t, string(contractFingerprint)), contractBytes)
	sqliteSchema, err := sqlite.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	postgresSchema, err := postgresql.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{Namespace: "runtime_test"})
	if err != nil {
		t.Fatal(err)
	}
	if omittedPostgreSQLCapability != "" {
		filtered := postgresSchema.Provider.Capabilities[:0]
		for _, capability := range postgresSchema.Provider.Capabilities {
			if capability.ID != omittedPostgreSQLCapability {
				filtered = append(filtered, capability)
			}
		}
		postgresSchema.Provider.Capabilities = filtered
	}
	generation := golem.SchemaDigest{0x42}
	bundle := golem.GeneratedSchemaBundle(generation, "runtime-test-generator", "runtime-test-abi", modelDocument, contractDocument,
		runtimeProviderDocument(t, golem.SQLite, sqliteSchema), runtimeProviderDocument(t, golem.PostgreSQL, postgresSchema))
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	fixture := runtimeFixture{generation: generation}
	for _, model := range compilation.Model.Models {
		switch model.LogicalName {
		case "User":
			fixture.user = runtimeModelID(t, model.ID)
			for _, field := range model.Fields {
				if field.GoName == "Handle" {
					fixture.userHandle = runtimeFieldID(t, field.ID)
				}
			}
		case "Post":
			fixture.post = runtimeModelID(t, model.ID)
		}
	}
	if fixture.user == (golem.ModelID{}) || fixture.post == (golem.ModelID{}) || fixture.userHandle == (golem.FieldID{}) {
		t.Fatal("real social fixture identities are incomplete")
	}
	return registry, fixture
}

func runtimeProviderDocument(t *testing.T, provider golem.Provider, value physical.PhysicalSchema) golem.ProviderSchemaDocument {
	t.Helper()
	payload, err := physical.CanonicalEncode(value)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := physical.PhysicalFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	system, err := physical.SystemFingerprint(value.Provider, value.System)
	if err != nil {
		t.Fatal(err)
	}
	document := golem.GeneratedSchemaDocument(value.Version, value.CanonicalVersion, golem.SchemaDigest(fingerprint), payload)
	return golem.GeneratedProviderSchemaDocument(provider, golem.SchemaDigest(system), document)
}

func runtimeBindings(t *testing.T, generation golem.SchemaDigest, bindings ...golem.PolicyBinding[runtimeActor]) golem.ApplicationBindings[runtimeActor] {
	t.Helper()
	pkg := golem.GeneratedStampedPackageBindings(generation, bindings, nil)
	application, err := golem.GeneratedApplicationBindings(generation, pkg)
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func runtimeProof(t *testing.T, registry *schema.Registry, provider policyir.Provider, capabilities ...policyir.Capability) policysql.CapabilityProof {
	t.Helper()
	return runtimeRawProof(t, provider, [32]byte(registry.ModelFingerprint()), capabilities...)
}

func runtimeRawProof(t *testing.T, provider policyir.Provider, fingerprint [32]byte, capabilities ...policyir.Capability) policysql.CapabilityProof {
	t.Helper()
	proof, err := policysql.NewCapabilityProof(provider, fingerprint, capabilities...)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func runtimeDigest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("digest %q: %v", value, err)
	}
	var result golem.SchemaDigest
	copy(result[:], decoded)
	return result
}

func runtimeModelID(t *testing.T, value compilerir.ModelID) golem.ModelID {
	t.Helper()
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != 16 {
		t.Fatalf("model ID %q: %v", value, err)
	}
	var result golem.ModelID
	copy(result[:], decoded)
	return result
}

func runtimeFieldID(t *testing.T, value compilerir.FieldID) golem.FieldID {
	t.Helper()
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != 16 {
		t.Fatalf("field ID %q: %v", value, err)
	}
	var result golem.FieldID
	copy(result[:], decoded)
	return result
}
