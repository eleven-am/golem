package golem

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestBoundFactScopeFreezeRetainsTypedSortedInputsAndOwnsMutableValues(t *testing.T) {
	ids := []int64{7, 8}
	blob := []byte("original")
	blobs := [][]byte{[]byte("copy")}
	account := UUID{15: 9}
	arguments := []TemplateArgument{
		GeneratedTemplateSetArgument("IDs", ids),
		GeneratedTemplateArgument("Number", int64(31)),
		GeneratedTemplateSetArgument("Blobs", blobs),
		GeneratedTemplateArgument("Active", true),
		GeneratedTemplateArgument("Blob", blob),
		GeneratedTemplateArgument("AccountID", account),
	}
	integerField := GeneratedEqualField[freezeModel, int64](freezeFieldID)
	bytesField := GeneratedBytesField[freezeModel](freezeSecondFieldID)
	predicate := integerField.In(ids...).And(bytesField.Eq(blob), bytesField.In(blobs...))
	scope := GeneratedBoundFactScope(freezeModelID, scopeBindingTestID(), scopeBindingTestDigest(), predicate, arguments...)
	first := freezeBoundScopeTestPolicy(t, scope)
	before := first.CanonicalBytes()
	beforeScope, _ := first.View().FactScope()
	beforeBody := beforeScope.CanonicalBytes()
	view, present := first.View().FactScopeBinding()
	if !present || first.Version() != 4 || view.TemplateID() != scopeBindingTestID() || view.Fingerprint() != scopeBindingTestDigest() || len(view.CanonicalBytes()) == 0 {
		t.Fatal("generated scope lost its version or exact static identity")
	}
	assertScopeBindingTestArguments(t, view)

	// Both the predicate body and every metadata operand must own the values
	// before freezing, not merely borrow the generated binder's argument DTO.
	ids[0], blob[0], blobs[0][0] = 99, 'X', 'Y'
	blobs[0] = []byte("replacement")
	arguments[0] = GeneratedTemplateArgument("Corrupt", false)
	second := freezeBoundScopeTestPolicy(t, scope)
	secondScope, _ := second.View().FactScope()
	if !bytes.Equal(before, second.CanonicalBytes()) || !bytes.Equal(beforeBody, secondScope.CanonicalBytes()) {
		t.Fatal("bound scope retained a mutable constructor input")
	}
	secondBinding, _ := second.View().FactScopeBinding()
	assertScopeBindingTestArguments(t, secondBinding)

	// Exercise every collection-valued accessor, including byte payloads
	// nested within set operands and values returned from a scope body.
	encoded := view.CanonicalBytes()
	encoded[0] ^= 0xff
	policyBytes := first.CanonicalBytes()
	policyBytes[0] ^= 0xff
	returned := view.Arguments()
	for _, argument := range returned {
		operand := argument.Operand()
		if value, one := operand.One(); one {
			if binary, ok := value.Bytes(); ok && len(binary) != 0 {
				original := bytes.Clone(binary)
				binary[0] ^= 0xff
				again, _ := value.Bytes()
				if !bytes.Equal(original, again) {
					t.Fatal("scalar byte accessor mutated its own immutable value view")
				}
			}
		}
		values := operand.Many()
		for _, value := range values {
			if binary, ok := value.Bytes(); ok && len(binary) != 0 {
				original := bytes.Clone(binary)
				binary[0] ^= 0xff
				again, _ := value.Bytes()
				if !bytes.Equal(original, again) {
					t.Fatal("set byte accessor mutated its own immutable value view")
				}
			}
		}
		if len(values) != 0 {
			values[0] = nil
		}
	}
	returned[0] = nil
	mutateScopeBodyTestBytes(beforeScope.Root())
	freshBinding, _ := first.View().FactScopeBinding()
	freshScope, _ := first.View().FactScope()
	if !bytes.Equal(before, first.CanonicalBytes()) || !bytes.Equal(beforeBody, freshScope.CanonicalBytes()) || bytes.Equal(encoded, freshBinding.CanonicalBytes()) {
		t.Fatal("a scope accessor exposed mutable frozen canonical storage")
	}
	assertScopeBindingTestArguments(t, freshBinding)
}

func TestBoundFactScopeCanonicalIdentityIncludesTemplateDigestAndEmptyArguments(t *testing.T) {
	base := freezeBoundScopeTestPolicy(t, GeneratedBoundFactScope(freezeModelID, scopeBindingTestID(), scopeBindingTestDigest(), None[freezeModel](), GeneratedTemplateSetArgument[int64]("IDs", nil)))
	for _, name := range []string{"template", "digest", "nonempty-set", "no-argument"} {
		t.Run(name, func(t *testing.T) {
			template, digest := scopeBindingTestID(), scopeBindingTestDigest()
			arguments := []TemplateArgument{GeneratedTemplateSetArgument[int64]("IDs", nil)}
			switch name {
			case "template":
				template = strings.Repeat("cd", 16)
			case "digest":
				digest = SchemaDigest{0x92}
			case "nonempty-set":
				arguments[0] = GeneratedTemplateSetArgument("IDs", []int64{7})
			case "no-argument":
				arguments = nil
			}
			changed := freezeBoundScopeTestPolicy(t, GeneratedBoundFactScope(freezeModelID, template, digest, None[freezeModel](), arguments...))
			if bytes.Equal(base.CanonicalBytes(), changed.CanonicalBytes()) {
				t.Fatal("constant-false body erased a change in binding identity")
			}
		})
	}
}

func TestBoundFactScopeFalseBodiesKeepEmptySetsAndActorIdentity(t *testing.T) {
	var previousPolicy, previousBinding, previousBody []byte
	for _, actor := range []int64{7, 8} {
		scope := GeneratedBoundFactScope(freezeModelID, scopeBindingTestID(), scopeBindingTestDigest(), None[freezeModel](),
			GeneratedTemplateSetArgument[int64]("IDs", nil), GeneratedTemplateArgument("AccountID", actor))
		policy := freezeBoundScopeTestPolicy(t, scope)
		body, _ := policy.View().FactScope()
		if body.Root().Kind() != FrozenConditionConstant {
			t.Fatal("fixture lost its constant-false body")
		}
		truth, constant := body.Root().Operand().Flag()
		if !constant || truth {
			t.Fatal("scope was not constant false")
		}
		binding, present := policy.View().FactScopeBinding()
		if !present || policy.Version() != 4 {
			t.Fatal("constant normalization erased the generated binding")
		}
		arguments := binding.Arguments()
		if len(arguments) != 2 || arguments[1].Name() != "IDs" || arguments[1].Operand().Kind() != FrozenOperandMany || len(arguments[1].Operand().Many()) != 0 {
			t.Fatal("empty set argument vanished or became a scalar")
		}
		if previousPolicy != nil {
			if bytes.Equal(previousPolicy, policy.CanonicalBytes()) || bytes.Equal(previousBinding, binding.CanonicalBytes()) || !bytes.Equal(previousBody, body.CanonicalBytes()) {
				t.Fatal("equal false bodies erased their distinct actor argument identities")
			}
		}
		previousPolicy, previousBinding, previousBody = policy.CanonicalBytes(), binding.CanonicalBytes(), body.CanonicalBytes()
	}
}

func TestBoundFactScopeRejectsInvalidConstructionAndDuplicateDeclarations(t *testing.T) {
	for _, name := range []string{"zero", "model", "zero-model", "id", "zero-id", "uppercase-id", "digest", "duplicate-argument", "argument-name", "unsupported-value", "nonfinite-value", "invalid-string", "invalid-set", "invalid-date", "too-many-arguments", "oversized-arguments", "zero-body", "duplicate-bound", "bound-then-inline", "inline-then-bound", "no-facts"} {
		t.Run(name, func(t *testing.T) {
			model, template, digest := freezeModelID, scopeBindingTestID(), scopeBindingTestDigest()
			body := All[freezeModel]()
			arguments := []TemplateArgument{GeneratedTemplateArgument("AccountID", int64(7))}
			switch name {
			case "model":
				model = freezeRelatedID
			case "zero-model":
				model = ModelID{}
			case "id":
				template = "short"
			case "zero-id":
				template = strings.Repeat("0", 32)
			case "uppercase-id":
				template = strings.ToUpper(template)
			case "digest":
				digest = SchemaDigest{}
			case "duplicate-argument":
				arguments = append(arguments, arguments[0])
			case "argument-name":
				arguments[0] = GeneratedTemplateArgument("private", int64(7))
			case "unsupported-value":
				arguments[0] = GeneratedTemplateArgument("AccountID", struct{}{})
			case "nonfinite-value":
				arguments[0] = GeneratedTemplateArgument("AccountID", math.NaN())
			case "invalid-string":
				arguments[0] = GeneratedTemplateArgument("AccountID", "bad\x00value")
			case "invalid-set":
				arguments[0] = GeneratedTemplateSetArgument("AccountID", []float64{math.Inf(1)})
			case "invalid-date":
				arguments[0] = GeneratedTemplateArgument("AccountID", Date{})
			case "too-many-arguments":
				arguments = make([]TemplateArgument, 65)
				for index := range arguments {
					arguments[index] = GeneratedTemplateArgument(fmt.Sprintf("Arg%d", index), int64(index))
				}
			case "oversized-arguments":
				arguments[0] = GeneratedTemplateArgument("AccountID", strings.Repeat("x", maxTemplateBindingBytes+1))
			case "zero-body":
				body = Predicate[freezeModel]{}
			}
			scope := GeneratedBoundFactScope(model, template, digest, body, arguments...)
			if name == "zero" {
				scope = BoundFactScope[freezeModel]{}
			}
			rules := NewRules[freezeModel]()
			if name == "inline-then-bound" {
				rules.FactScope(All[freezeModel]())
			}
			rules.BindFactScope(scope)
			if name == "duplicate-bound" {
				rules.BindFactScope(scope)
			}
			if name == "bound-then-inline" {
				rules.FactScope(All[freezeModel]())
			}
			if name != "no-facts" {
				rules.Fact("ready", All[freezeModel]())
			}
			rules.CanRead(All[freezeModel]())
			if _, err := rules.Freeze(freezeModelID); err == nil {
				t.Fatal("invalid generated scope crossed the freeze boundary")
			}
		})
	}
}

func TestBoundFactScopeSurvivesGeneratedOperationAndAggregatePolicyCopies(t *testing.T) {
	factory := func(actor bindingActor) (FrozenPolicy, error) {
		rules := NewRules[bindingModel]()
		body := GeneratedEqualField[bindingModel, int64](operationFieldID).Eq(actor.ID)
		rules.BindFactScope(GeneratedBoundFactScope(operationModelID, scopeBindingTestID(), scopeBindingTestDigest(), body, GeneratedTemplateArgument("AccountID", actor.ID)))
		ready := rules.Fact("ready", All[bindingModel]())
		rules.CanRead(ready)
		Within(rules, operationCreateResolver).CanCreate(ready)
		Within(rules, operationOtherResolver).CanCount(ready)
		return rules.Freeze(operationModelID)
	}
	application := operationBindings(t, factory,
		GeneratedCustomMutationOperation("scope-create", operationCreateResolver),
		GeneratedTxOperation("scope-aggregate", operationOtherResolver))
	actor := bindingActor{ID: 7}
	set, err := BuildGeneratedPolicySet(application, actor)
	if err != nil {
		t.Fatal(err)
	}
	base := set.Policies()[0]
	want, present := base.View().FactScopeBinding()
	if !present {
		t.Fatal("generated base policy lost its scope binding")
	}
	policy, operations, err := BuildGeneratedModelPolicy(application, operationModelID, actor)
	if err != nil {
		t.Fatal(err)
	}
	// Aggregate construction consumes the original factory result. Public
	// Policies copies intentionally expose only the already-built base rules.
	aggregateSource, err := factory(actor)
	if err != nil {
		t.Fatal(err)
	}
	aggregates, err := GeneratedAggregatePolicies(application, aggregateSource)
	if err != nil || len(aggregates) != 1 {
		t.Fatalf("aggregate copies=%d err=%v", len(aggregates), err)
	}
	copies := []FrozenPolicy{base, set.Policies()[0], policy}
	for _, operation := range set.Operations() {
		copies = append(copies, set.OperationPolicies(operation)...)
	}
	for _, scoped := range operations {
		copies = append(copies, scoped)
	}
	for _, aggregate := range aggregates {
		copies = append(copies, aggregate)
	}
	if len(copies) < 6 {
		t.Fatal("fixture did not exercise generated, operation and aggregate copies")
	}
	for _, copied := range copies {
		binding, present := copied.View().FactScopeBinding()
		if !present || copied.Version() != 4 || binding.TemplateID() != scopeBindingTestID() || binding.Fingerprint() != scopeBindingTestDigest() || !bytes.Equal(binding.CanonicalBytes(), want.CanonicalBytes()) {
			t.Fatal("a generated policy copy lost v4 scope metadata")
		}
		value, _ := binding.Arguments()[0].Operand().One()
		if actual, width, ok := value.Signed(); !ok || width != 64 || actual != actor.ID {
			t.Fatal("a generated policy copy retained another actor's argument")
		}
	}
	legacy := NewRules[freezeModel]()
	legacy.CanRead(All[freezeModel]())
	plain, err := legacy.Freeze(freezeModelID)
	if err != nil || plain.Version() != 2 {
		t.Fatalf("ordinary policy version=%d err=%v", plain.Version(), err)
	}
	legacy.FactScope(All[freezeModel]())
	legacy.Fact("ready", All[freezeModel]())
	inline, err := legacy.Freeze(freezeModelID)
	if err != nil || inline.Version() != 3 {
		t.Fatalf("inline fact version=%d err=%v", inline.Version(), err)
	}
	if _, present := inline.View().FactScopeBinding(); present {
		t.Fatal("inline scope acquired generated binding provenance")
	}
}

func freezeBoundScopeTestPolicy(t *testing.T, scope BoundFactScope[freezeModel]) FrozenPolicy {
	t.Helper()
	rules := NewRules[freezeModel]()
	rules.BindFactScope(scope)
	rules.Fact("ready", All[freezeModel]())
	rules.CanRead(All[freezeModel]())
	policy, err := rules.Freeze(freezeModelID)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func assertScopeBindingTestArguments(t *testing.T, binding FrozenTemplateBindingView) {
	t.Helper()
	arguments := binding.Arguments()
	names := []string{"AccountID", "Active", "Blob", "Blobs", "IDs", "Number"}
	if len(arguments) != len(names) {
		t.Fatal("binding argument inventory changed")
	}
	for index, name := range names {
		argument := arguments[index]
		if argument.Name() != name {
			t.Fatalf("argument %d=%q want=%q", index, argument.Name(), name)
		}
		operand := argument.Operand()
		if name == "IDs" || name == "Blobs" {
			if operand.Kind() != FrozenOperandMany {
				t.Fatal("set argument lost its arity")
			}
			values := operand.Many()
			if name == "IDs" {
				if len(values) != 2 {
					t.Fatal("integer set changed size")
				}
				for index, value := range values {
					if actual, width, ok := value.Signed(); !ok || width != 64 || actual != int64(index+7) {
						t.Fatal("integer set changed typed values")
					}
				}
			} else if len(values) != 1 {
				t.Fatal("byte set changed size")
			} else if value, ok := values[0].Bytes(); !ok || !bytes.Equal(value, []byte("copy")) {
				t.Fatal("byte set changed payload")
			}
			continue
		}
		value, one := operand.One()
		if !one || operand.Kind() != FrozenOperandOne {
			t.Fatal("scalar argument lost its arity")
		}
		switch name {
		case "AccountID":
			if actual, ok := value.UUID(); !ok || actual != (UUID{15: 9}) {
				t.Fatal("UUID argument changed")
			}
		case "Active":
			if actual, ok := value.Bool(); !ok || !actual {
				t.Fatal("Boolean argument changed")
			}
		case "Blob":
			if actual, ok := value.Bytes(); !ok || !bytes.Equal(actual, []byte("original")) {
				t.Fatal("byte argument changed")
			}
		case "Number":
			if actual, width, ok := value.Signed(); !ok || width != 64 || actual != 31 {
				t.Fatal("numeric argument changed type or value")
			}
		}
	}
}

func mutateScopeBodyTestBytes(condition FrozenConditionView) {
	if value, one := condition.Operand().One(); one {
		if binary, ok := value.Bytes(); ok && len(binary) != 0 {
			binary[0] ^= 0xff
		}
	}
	for _, value := range condition.Operand().Many() {
		if binary, ok := value.Bytes(); ok && len(binary) != 0 {
			binary[0] ^= 0xff
		}
	}
	for _, child := range condition.Children() {
		mutateScopeBodyTestBytes(child)
	}
}

func scopeBindingTestID() string           { return strings.Repeat("ab", 16) }
func scopeBindingTestDigest() SchemaDigest { return SchemaDigest{0x91} }
