package bind

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type bindPost struct{}
type bindUser struct{}

func TestMutationBinderRejectsForgedZeroAndDuplicateValues(t *testing.T) {
	testMutationBinderRejectsForgedZeroAndDuplicateValues(t)
}

func TestM8MutationBinderRejectsUnknownAndForgedFacts(t *testing.T) {
	testMutationBinderRejectsForgedZeroAndDuplicateValues(t)
}

func testMutationBinderRejectsForgedZeroAndDuplicateValues(t *testing.T) {
	t.Helper()
	fixture := schematest.New(t)
	title := golem.GeneratedTextField[bindPost, string](fixture.PostTitle)

	t.Run("zero input", func(t *testing.T) {
		if _, err := golem.RuntimeFreezeCreateInput(golem.CreateInput[bindPost]{}); err == nil {
			t.Fatal("zero mutation input crossed the public freeze boundary")
		}
	})
	t.Run("forged field", func(t *testing.T) {
		forged := golem.GeneratedTextField[bindPost, string](golem.FieldID{15: 0xee})
		frozen, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post,
			golem.GeneratedSetFieldValue(fixture.Post, forged, "forged"),
		))
		if err != nil {
			t.Fatal(err)
		}
		_, err = UpdateInput(frozen, fixture.Registry)
		assertBindCode(t, err, CodeField, golem.FieldID{15: 0xee})
	})
	t.Run("duplicate field", func(t *testing.T) {
		_, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post,
			golem.GeneratedSetFieldValue(fixture.Post, title, "first"),
			golem.GeneratedSetFieldValue(fixture.Post, title, "second"),
		))
		if err == nil {
			t.Fatal("duplicate mutation values crossed the public freeze boundary")
		}
	})
}

func TestCreateInputBindsExactScalarsAndEnforcesRequiredness(t *testing.T) {
	fixture := schematest.New(t)
	id := golem.GeneratedEqualField[bindPost, golem.UUID](fixture.PostID)
	author := golem.GeneratedEqualField[bindPost, golem.UUID](fixture.AuthorID)
	title := golem.GeneratedTextField[bindPost, string](fixture.PostTitle)
	uuid := golem.NewUUID([16]byte{7})

	frozen := freezeCreate(t, golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, title, "title"),
		golem.GeneratedCreateFieldValue(fixture.Post, author, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, id, uuid),
	))
	bound, err := CreateInput(frozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	operations := bound.Operations()
	if bound.ModelID() != [16]byte(fixture.Post) || bound.Kind() != InputCreate || len(operations) != 3 {
		t.Fatalf("bound input = %#v operations=%#v", bound, operations)
	}
	for index := 1; index < len(operations); index++ {
		left, right := operations[index-1].FieldID(), operations[index].FieldID()
		if string(left[:]) >= string(right[:]) {
			t.Fatal("scalar operations are not in stable field order")
		}
	}
	operations[0] = mutationir.ScalarOperation{}
	if err := bound.Operations()[0].Validate(); err != nil {
		t.Fatal("operations accessor leaked mutable slice storage")
	}

	missing := freezeCreate(t, golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, id, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, author, uuid),
	))
	_, err = CreateInput(missing, fixture.Registry)
	assertBindCode(t, err, CodeRequired, fixture.PostTitle)
}

func TestCreateInputPreservesExplicitNull(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	nullable := golem.GeneratedNullableTextField[bindPost, string](fixture.PostNullableText)
	frozen := freezeCreate(t, golem.GeneratedCreateInput(fixture.Post, golem.GeneratedCreateNullFieldValue(fixture.Post, nullable)))
	public := frozen.Fields()[0]
	field, ok := fixture.Registry.Field(fixture.Post, fixture.PostNullableText)
	if !ok {
		t.Fatal("nullable fixture field is absent")
	}
	typ, err := bindType(field.LogicalType(), field.Nullable())
	if err != nil {
		t.Fatal(err)
	}
	operation, err := bindScalarOperation(InputCreate, public, field, typ, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Kind() != mutationir.ScalarNull {
		t.Fatal("explicit create null was not preserved as MutationIR null")
	}
}

func TestSingleUpdateAcceptsRelationOnlyInputButRejectsTrulyEmptyInput(t *testing.T) {
	fixture := schematest.New(t)
	user := golem.GeneratedUniqueSelectorValue[bindUser](fixture.User, fixture.UserKey,
		golem.GeneratedSelectorComponent(fixture.UserID, golem.UUID{15: 1}))
	connect := golem.GeneratedNestedConnect[bindPost, bindUser](fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, user)
	frozen := freezeUpdate(t, golem.GeneratedUpdateInput[bindPost](fixture.Post, connect))
	bound, err := UpdateInput(frozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.Operations()) != 0 || len(frozen.Relations()) != 1 {
		t.Fatalf("relation-only scalar operations=%d relations=%d", len(bound.Operations()), len(frozen.Relations()))
	}
	empty := freezeUpdate(t, golem.GeneratedUpdateInput[bindPost](fixture.Post))
	if _, err := UpdateInput(empty, fixture.Registry); err == nil {
		t.Fatal("truly empty single update was accepted")
	}
	emptyMany := freezeUpdateMany(t, golem.GeneratedUpdateManyInput[bindPost](fixture.Post))
	if _, err := UpdateManyInput(emptyMany, fixture.Registry); err == nil {
		t.Fatal("empty update-many was accepted")
	}
}

func TestCreateInputRuntimeOwnedFieldsAreExactAndFailClosed(t *testing.T) {
	fixture := schematest.New(t)
	id := golem.GeneratedEqualField[bindPost, golem.UUID](fixture.PostID)
	author := golem.GeneratedEqualField[bindPost, golem.UUID](fixture.AuthorID)
	title := golem.GeneratedTextField[bindPost, string](fixture.PostTitle)
	uuid := golem.NewUUID([16]byte{7})
	withoutAuthor := freezeCreate(t, golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, id, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, title, "title"),
	))
	if _, err := CreateInput(withoutAuthor, fixture.Registry); err == nil {
		t.Fatal("ordinary create waived a missing required correlation")
	}
	bound, runtimeOwned, err := CreateInputWithRuntimeOwnedFields(withoutAuthor, fixture.Registry, []golem.FieldID{fixture.AuthorID})
	if err != nil || len(bound.Operations()) != 2 || len(runtimeOwned) != 1 || runtimeOwned[0] != [16]byte(fixture.AuthorID) {
		t.Fatalf("runtime-owned bind=%#v owned=%#v err=%v", bound, runtimeOwned, err)
	}

	withAuthor := freezeCreate(t, golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, id, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, author, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, title, "title"),
	))
	for name, test := range map[string]struct {
		frozen golem.FrozenMutationInput
		fields []golem.FieldID
	}{
		"zero":              {withoutAuthor, []golem.FieldID{{}}},
		"foreign relation":  {withoutAuthor, []golem.FieldID{fixture.PostAuthor}},
		"duplicate":         {withoutAuthor, []golem.FieldID{fixture.AuthorID, fixture.AuthorID}},
		"explicit conflict": {withAuthor, []golem.FieldID{fixture.AuthorID}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := CreateInputWithRuntimeOwnedFields(test.frozen, fixture.Registry, test.fields); err == nil {
				t.Fatal("forged runtime ownership accepted")
			}
		})
	}
	defaulted := mutateRegistry(t, fixture.Bundle, func(model *compilerir.ModelIR) {
		model.Models[1].Fields[1].Scalar.Default = &compilerir.DefaultIR{Kind: compilerir.DefaultLiteral, Producer: compilerir.ProducerDatabase, Literal: &compilerir.TypedLiteralIR{Kind: compilerir.LiteralUUID, Canonical: "00000000-0000-0000-0000-000000000001"}}
	})
	if _, _, err := CreateInputWithRuntimeOwnedFields(withoutAuthor, defaulted, []golem.FieldID{fixture.AuthorID}); err == nil {
		t.Fatal("defaulted field accepted as runtime-owned correlation")
	}
}

func TestCreateRequirednessHonorsDefaultUpdatedGeneratedAndDatabaseOwnership(t *testing.T) {
	base := schematest.New(t)
	id := golem.GeneratedEqualField[bindPost, golem.UUID](base.PostID)
	author := golem.GeneratedEqualField[bindPost, golem.UUID](base.AuthorID)
	uuid := golem.NewUUID([16]byte{9})
	withoutTitle := freezeCreate(t, golem.GeneratedCreateInput(base.Post,
		golem.GeneratedCreateFieldValue(base.Post, id, uuid),
		golem.GeneratedCreateFieldValue(base.Post, author, uuid),
	))

	tests := []struct {
		name   string
		mutate func(*compilerir.ScalarFieldIR)
	}{
		{name: "application default", mutate: func(field *compilerir.ScalarFieldIR) {
			field.Default = &compilerir.DefaultIR{Kind: compilerir.DefaultLiteral, Producer: compilerir.ProducerApplication, Literal: &compilerir.TypedLiteralIR{Kind: compilerir.LiteralString, Canonical: "default"}}
		}},
		{name: "database default", mutate: func(field *compilerir.ScalarFieldIR) {
			field.Default = &compilerir.DefaultIR{Kind: compilerir.DefaultLiteral, Producer: compilerir.ProducerDatabase, Literal: &compilerir.TypedLiteralIR{Kind: compilerir.LiteralString, Canonical: "default"}}
		}},
		{name: "updated", mutate: func(field *compilerir.ScalarFieldIR) { field.Updated = true }},
		{name: "database read only", mutate: func(field *compilerir.ScalarFieldIR) { field.DatabaseReadOnly = true }},
		{name: "generated", mutate: func(field *compilerir.ScalarFieldIR) {
			field.Generation = &compilerir.GeneratedColumnIR{Expr: compilerir.SchemaExprIR{Kind: compilerir.SchemaExprLiteral, ResultType: field.Type, Literal: &compilerir.TypedLiteralIR{Kind: compilerir.LiteralString, Canonical: "generated"}, Provider: compilerir.ProviderScopePortable, Volatility: compilerir.SchemaVolatilityImmutable, Deterministic: true}, Storage: compilerir.GeneratedStored, Provider: compilerir.ProviderScopePortable}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := mutateRegistry(t, base.Bundle, func(model *compilerir.ModelIR) {
				test.mutate(model.Models[1].Fields[2].Scalar)
			})
			if _, err := CreateInput(withoutTitle, registry); err != nil {
				t.Fatalf("owned omission rejected: %v", err)
			}
		})
	}
}

func TestInputRejectsExposureIllegalOperationsForgedTypesAndBatchIdentityChange(t *testing.T) {
	base := schematest.New(t)
	title := golem.GeneratedTextField[bindPost, string](base.PostTitle)
	wrongTitle := golem.GeneratedOrderedField[bindPost, int64](base.PostTitle)
	id := golem.GeneratedEqualField[bindPost, golem.UUID](base.PostID)

	tests := []struct {
		name  string
		bind  func() error
		code  ErrorCode
		field golem.FieldID
	}{
		{name: "read only", code: CodeExposure, field: base.PostTitle, bind: func() error {
			fixture := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeReadOnly}})
			_, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "x"))), fixture.Registry)
			return err
		}},
		{name: "hidden", code: CodeExposure, field: base.PostTitle, bind: func() error {
			fixture := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeHidden}})
			_, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "x"))), fixture.Registry)
			return err
		}},
		{name: "immutable update", code: CodeExposure, field: base.PostTitle, bind: func() error {
			fixture := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeImmutable}})
			_, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "x"))), fixture.Registry)
			return err
		}},
		{name: "null nonnullable", code: CodeOperation, field: base.PostTitle, bind: func() error {
			_, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(base.Post, golem.GeneratedNullFieldValue(base.Post, title))), base.Registry)
			return err
		}},
		{name: "arithmetic string", code: CodeOperation, field: base.PostTitle, bind: func() error {
			_, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(base.Post, golem.GeneratedIncrementFieldValue(base.Post, wrongTitle, int64(1)))), base.Registry)
			return err
		}},
		{name: "forged logical type", code: CodeValue, field: base.PostTitle, bind: func() error {
			_, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(base.Post, golem.GeneratedSetFieldValue(base.Post, wrongTitle, int64(1)))), base.Registry)
			return err
		}},
		{name: "batch identity", code: CodeOperation, field: base.PostID, bind: func() error {
			_, err := UpdateManyInput(freezeUpdateMany(t, golem.GeneratedUpdateManyInput(base.Post, golem.GeneratedSetFieldValue(base.Post, id, golem.NewUUID([16]byte{1})))), base.Registry)
			return err
		}},
		{name: "empty update", code: CodeInput, bind: func() error {
			_, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput[bindPost](base.Post)), base.Registry)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertBindCode(t, test.bind(), test.code, test.field)
		})
	}

	writeOnly := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeWriteOnly}})
	if _, err := UpdateInput(freezeUpdate(t, golem.GeneratedUpdateInput(writeOnly.Post, golem.GeneratedSetFieldValue(writeOnly.Post, title, "allowed"))), writeOnly.Registry); err != nil {
		t.Fatalf("write-only mutation rejected: %v", err)
	}
	immutableWriteOnly := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeWriteOnly, compilerir.ModeImmutable}})
	uuid := golem.NewUUID([16]byte{8})
	create := freezeCreate(t, golem.GeneratedCreateInput(immutableWriteOnly.Post,
		golem.GeneratedCreateFieldValue(immutableWriteOnly.Post, golem.GeneratedEqualField[bindPost, golem.UUID](immutableWriteOnly.PostID), uuid),
		golem.GeneratedCreateFieldValue(immutableWriteOnly.Post, golem.GeneratedEqualField[bindPost, golem.UUID](immutableWriteOnly.AuthorID), uuid),
		golem.GeneratedCreateFieldValue(immutableWriteOnly.Post, title, "allowed"),
	))
	if _, err := CreateInput(create, immutableWriteOnly.Registry); err != nil {
		t.Fatalf("immutable write-only create rejected: %v", err)
	}

	nonAuthorable := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeReadOnly}})
	missingReadOnly := freezeCreate(t, golem.GeneratedCreateInput(nonAuthorable.Post,
		golem.GeneratedCreateFieldValue(nonAuthorable.Post, golem.GeneratedEqualField[bindPost, golem.UUID](nonAuthorable.PostID), uuid),
		golem.GeneratedCreateFieldValue(nonAuthorable.Post, golem.GeneratedEqualField[bindPost, golem.UUID](nonAuthorable.AuthorID), uuid),
	))
	_, err := CreateInput(missingReadOnly, nonAuthorable.Registry)
	assertBindCode(t, err, CodeInternal, nonAuthorable.PostTitle)
}

func TestTargetBindsSelectorValueAndGuardWithActiveOwnership(t *testing.T) {
	fixture := schematest.New(t)
	id := golem.GeneratedEqualField[bindPost, golem.UUID](fixture.PostID)
	title := golem.GeneratedTextField[bindPost, string](fixture.PostTitle)
	uuid := golem.NewUUID([16]byte{4})
	selector := golem.GeneratedUniqueSelectorValue[bindPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, uuid))
	frozen, err := golem.RuntimeFreezeMutationTarget[bindPost](selector.And(title.Eq("open")))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := Target(frozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	target := bound.Target()
	values := target.Values()
	if target.ModelID() != [16]byte(fixture.Post) || target.KeyID() != fixture.PostKey || len(values) != 1 || values[0].FieldID() != [16]byte(fixture.PostID) {
		t.Fatalf("bound target = %#v values=%#v", bound, values)
	}
	if value := values[0].Value(); value.Kind() == 0 {
		t.Fatal("selector lost exact value")
	}
	if _, present := target.Guard(); !present {
		t.Fatal("target guard was dropped")
	}
	if bound.SelectorPredicate().ModelID() != [16]byte(fixture.Post) {
		t.Fatal("already-bound selector predicate was dropped")
	}

	if _, err := Target(frozen, fixture.User, fixture.Registry); err == nil {
		t.Fatal("cross-model target was accepted")
	}
	forgedKey := golem.GeneratedUniqueSelectorValue[bindPost](fixture.Post, golem.KeyID{0xff}, golem.GeneratedSelectorComponent(fixture.PostID, uuid))
	forgedFrozen, err := golem.RuntimeFreezeMutationTarget[bindPost](forgedKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Target(forgedFrozen, fixture.Post, fixture.Registry)
	assertBindCode(t, err, CodeTarget, golem.FieldID{})

	wrongValue := golem.GeneratedUniqueSelectorValue[bindPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, "not-a-uuid"))
	wrongFrozen, err := golem.RuntimeFreezeMutationTarget[bindPost](wrongValue)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Target(wrongFrozen, fixture.Post, fixture.Registry)
	assertBindCode(t, err, CodeTarget, golem.FieldID{})

	_ = id // the generated scalar handle witnesses the selector's declared field type in real generated code.
}

func TestTargetRejectsWriteOnlyGuardWithoutLeakingTrustedCause(t *testing.T) {
	fixture := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeWriteOnly}})
	id := golem.NewUUID([16]byte{5})
	selector := golem.GeneratedUniqueSelectorValue[bindPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, id))
	guard := golem.GeneratedTextField[bindPost, string](fixture.PostTitle).Eq("secret")
	frozen, err := golem.RuntimeFreezeMutationTarget[bindPost](selector.And(guard))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Target(frozen, fixture.Post, fixture.Registry)
	assertBindCode(t, err, CodeExposure, fixture.PostTitle)
	if strings.Contains(strings.ToLower(err.Error()), "select ") || strings.Contains(strings.ToLower(err.Error()), "where ") {
		t.Fatalf("public binder error leaked SQL-like detail: %v", err)
	}
}

func TestBatchPredicateBindsExactPublicFilterAndRejectsWrongOwnership(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[bindPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	public, err := golem.GeneratedTextField[bindPost, string](fixture.PostTitle).Eq("open").Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BatchPredicate(public, fixture.Post, fixture.Registry)
	if err != nil || bound.ModelID() != [16]byte(fixture.Post) {
		t.Fatalf("batch predicate=%#v err=%v", bound, err)
	}
	if _, err := BatchPredicate(public, fixture.User, fixture.Registry); err == nil {
		t.Fatal("cross-model batch predicate accepted")
	}

	writeOnly := schematest.NewWithContractModes(t, schematest.ContractModes{PostTitle: []compilerir.FieldMode{compilerir.ModeWriteOnly}})
	_, err = BatchPredicate(public, writeOnly.Post, writeOnly.Registry)
	assertBindCode(t, err, CodeExposure, writeOnly.PostTitle)
}

func freezeCreate(t testing.TB, input golem.CreateInput[bindPost]) golem.FrozenMutationInput {
	t.Helper()
	frozen, err := golem.RuntimeFreezeCreateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func freezeUpdate(t testing.TB, input golem.UpdateInput[bindPost]) golem.FrozenMutationInput {
	t.Helper()
	frozen, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func freezeUpdateMany(t testing.TB, input golem.UpdateManyInput[bindPost]) golem.FrozenMutationInput {
	t.Helper()
	frozen, err := golem.RuntimeFreezeUpdateManyInput(input)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func assertBindCode(t testing.TB, err error, code ErrorCode, field golem.FieldID) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != code || failure.Field != field {
		t.Fatalf("error = %#v, want code=%s field=%x", err, code, field)
	}
}

func TestJSONBinderRejectsNULBeforeProviderSQL(t *testing.T) {
	for _, document := range []string{`{"value":"\u0000"}`, `{"\u0000":"value"}`} {
		decoded, err := golem.ParseJSON([]byte(document))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bindJSON(decoded); err == nil {
			t.Fatalf("portable JSON binder accepted NUL document %s", document)
		}
	}
}

func mutateRegistry(t testing.TB, bundle golem.SchemaBundle, mutate func(*compilerir.ModelIR)) *schema.Registry {
	t.Helper()
	var model compilerir.ModelIR
	if err := json.Unmarshal(bundle.Model().Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	mutate(&model)
	payload, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("fingerprint: %v", err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], decoded)
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	updated := golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), document, bundle.Contract(), bundle.Providers()...)
	registry, err := schema.New(updated)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
