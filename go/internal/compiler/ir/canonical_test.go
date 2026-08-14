package ir

import (
	"bytes"
	"testing"
)

func TestCanonicalModelIgnoresUnorderedDeclarationOrder(t *testing.T) {
	left := validModelFixture()
	right := validModelFixture()
	right.Providers[0], right.Providers[1] = right.Providers[1], right.Providers[0]
	right.Models[0], right.Models[1] = right.Models[1], right.Models[0]

	leftBytes, err := CanonicalModel(left)
	if err != nil {
		t.Fatal(err)
	}
	rightBytes, err := CanonicalModel(right)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leftBytes, rightBytes) {
		t.Fatalf("canonical model differs:\n%s\n%s", leftBytes, rightBytes)
	}
	leftHash, _ := ModelFingerprint(left)
	rightHash, _ := ModelFingerprint(right)
	if leftHash != rightHash {
		t.Fatalf("fingerprints differ: %s != %s", leftHash, rightHash)
	}
}

func TestCanonicalModelPreservesOrderedKeyComponents(t *testing.T) {
	left := validModelFixture()
	right := validModelFixture()
	right.Models[0].PrimaryKey.Fields[0], right.Models[0].PrimaryKey.Fields[1] =
		right.Models[0].PrimaryKey.Fields[1], right.Models[0].PrimaryKey.Fields[0]
	leftHash, _ := ModelFingerprint(left)
	rightHash, _ := ModelFingerprint(right)
	if leftHash == rightHash {
		t.Fatal("ordered primary-key components must affect the model fingerprint")
	}
}

func TestCanonicalRawUsesAcceptedFunctionRootAndIgnoresRegistrationOrder(t *testing.T) {
	raw := RawDeclIR{
		FormatVersion: RawDeclFormatVersion,
		Root: RawSchemaDecl{
			PackagePath:   "example/social",
			FunctionName:  "DefineSchema",
			ParameterName: "schema",
			SchemaName:    "social",
			Actor:         &RawNamedTypeRef{PackagePath: "example/social", GoName: "Actor"},
			Providers:     []RawProviderRef{{Provider: PostgreSQL, Ordinal: 1}, {Provider: SQLite, Ordinal: 0}},
			Models: []RawModelRef{
				{PackagePath: "example/social", GoName: "Post", Ordinal: 1},
				{PackagePath: "example/social", GoName: "User", Ordinal: 0},
			},
		},
	}
	encoded, err := CanonicalRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"functionName":"DefineSchema"`)) || bytes.Contains(encoded, []byte(`"marker"`)) {
		t.Fatalf("unexpected root ABI: %s", encoded)
	}
}

func TestCanonicalEncodingRejectsUnknownVersions(t *testing.T) {
	if _, err := CanonicalModel(ModelIR{FormatVersion: 99}); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestCanonicalEncodingTreatsNilAndEmptyCollectionsEqually(t *testing.T) {
	left := validModelFixture()
	right := validModelFixture()
	left.Enums = []EnumIR{{ID: "enum", Values: nil}}
	right.Enums = []EnumIR{{ID: "enum", Values: []EnumValueIR{}}}
	left.Relations = []RelationIR{{ID: "relation", LocalFields: nil, RemoteFields: nil}}
	right.Relations = []RelationIR{{ID: "relation", LocalFields: []FieldID{}, RemoteFields: []FieldID{}}}

	leftBytes, err := CanonicalModel(left)
	if err != nil {
		t.Fatal(err)
	}
	rightBytes, err := CanonicalModel(right)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leftBytes, rightBytes) {
		t.Fatalf("nil and empty collections differ:\n%s\n%s", leftBytes, rightBytes)
	}
}

func TestCanonicalRawGoTypeNormalizesRecursiveArgs(t *testing.T) {
	left := RawGoTypeRef{
		Kind:        RawGoTypeInstantiation,
		PackagePath: "github.com/eleven-am/golem/go/golem",
		GoName:      "Null",
		Args: []RawGoTypeRef{{
			Kind: RawGoTypePointer,
			Args: []RawGoTypeRef{{Kind: RawGoTypeBuiltin, GoName: "string", Args: nil}},
		}},
	}
	right := left
	right.Args = []RawGoTypeRef{{
		Kind: RawGoTypePointer,
		Args: []RawGoTypeRef{{Kind: RawGoTypeBuiltin, GoName: "string", Args: []RawGoTypeRef{}}},
	}}
	leftBytes, err := CanonicalRawGoType(left)
	if err != nil {
		t.Fatal(err)
	}
	rightBytes, err := CanonicalRawGoType(right)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leftBytes, rightBytes) {
		t.Fatalf("nil and empty type arguments differ:\n%s\n%s", leftBytes, rightBytes)
	}
}

func TestCanonicalRawGoTypeIsImportAliasIndependent(t *testing.T) {
	// The source spellings `golem.UUID` and `domain.UUID` differ only in
	// TypeSyntax on RawFieldDecl. Their canonical evidence is identical.
	left := RawFieldDecl{
		TypeSyntax: "golem.UUID",
		GoType: RawGoTypeRef{
			Kind:        RawGoTypeNamed,
			PackagePath: "github.com/eleven-am/golem/go/golem",
			GoName:      "UUID",
		},
	}
	right := left
	right.TypeSyntax = "domain.UUID"
	leftBytes, err := CanonicalRawGoType(left.GoType)
	if err != nil {
		t.Fatal(err)
	}
	rightBytes, err := CanonicalRawGoType(right.GoType)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leftBytes, rightBytes) {
		t.Fatalf("canonical Go type depends on import alias:\n%s\n%s", leftBytes, rightBytes)
	}
}

func TestCanonicalSchemaExpressionsNormalizeSetsButPreserveOperandOrder(t *testing.T) {
	first, second := FieldID("field-a"), FieldID("field-b")
	left := validModelFixture()
	left.Models[0].Checks = []CheckIR{{
		ID: "check",
		Predicate: SchemaPredicateIR{
			Kind: SchemaPredicateOperator, CanonicalIdentity: "a-less-than-b",
			ResultType: LogicalTypeIR{Kind: TypeBool}, Provider: ProviderScopePortable,
			Volatility: SchemaVolatilityImmutable, Deterministic: true,
			Symbol: &SchemaSymbolRef{Identity: "operator:lt:v1", Kind: SchemaSymbolOperator, Name: "lt", Version: 1, Provider: ProviderScopePortable, Volatility: SchemaVolatilityImmutable, Deterministic: true},
			ExpressionOperands: []SchemaExprIR{
				{Kind: SchemaExprField, CanonicalIdentity: "field-a", ResultType: LogicalTypeIR{Kind: TypeInt64}, Field: &first},
				{Kind: SchemaExprField, CanonicalIdentity: "field-b", ResultType: LogicalTypeIR{Kind: TypeInt64}, Field: &second},
			},
			Children:         nil,
			ReferencedFields: []FieldID{second, first},
		},
	}}
	right, err := cloneJSON(left)
	if err != nil {
		t.Fatal(err)
	}
	right.Models[0].Checks[0].Predicate.Children = []SchemaPredicateIR{}
	right.Models[0].Checks[0].Predicate.ReferencedFields = []FieldID{first, second}
	for index := range right.Models[0].Checks[0].Predicate.ExpressionOperands {
		right.Models[0].Checks[0].Predicate.ExpressionOperands[index].Operands = []SchemaExprIR{}
		right.Models[0].Checks[0].Predicate.ExpressionOperands[index].ReferencedFields = []FieldID{}
	}
	leftHash, _ := ModelFingerprint(left)
	rightHash, _ := ModelFingerprint(right)
	if leftHash != rightHash {
		t.Fatalf("equivalent recursive expression metadata differs: %s != %s", leftHash, rightHash)
	}
	right.Models[0].Checks[0].Predicate.ExpressionOperands[0], right.Models[0].Checks[0].Predicate.ExpressionOperands[1] =
		right.Models[0].Checks[0].Predicate.ExpressionOperands[1], right.Models[0].Checks[0].Predicate.ExpressionOperands[0]
	reorderedHash, _ := ModelFingerprint(right)
	if leftHash == reorderedHash {
		t.Fatal("ordered expression operands must affect ModelFingerprint")
	}
}

func TestGraphQLNamesAndSelectorsAffectOnlyContractFingerprint(t *testing.T) {
	model := validModelFixture()
	modelBefore, _ := ModelFingerprint(model)
	left := ContractIR{FormatVersion: ContractFormatVersion, Models: []ModelContractIR{{
		ModelID:   "model",
		Fields:    []FieldContractIR{{FieldID: "field", GraphQLName: "title", Modes: []FieldMode{ModeVisible}}},
		Selectors: []SelectorContractIR{{KeyID: "key", Kind: KeyUnique, Name: "Email", Fields: []FieldID{"email"}}},
	}}}
	right, err := cloneJSON(left)
	if err != nil {
		t.Fatal(err)
	}
	right.Models[0].Fields[0].GraphQLName = "headline"
	right.Models[0].Selectors[0].Name = "EmailAddress"
	leftContract, _ := ContractFingerprint(left)
	rightContract, _ := ContractFingerprint(right)
	if leftContract == rightContract {
		t.Fatal("GraphQL and selector API renames must affect ContractFingerprint")
	}
	modelAfter, _ := ModelFingerprint(model)
	if modelBefore != modelAfter {
		t.Fatal("contract-only names changed ModelFingerprint")
	}
}

func TestGraphQLHookOwnedFieldsAreCanonicalContractOnlyMetadata(t *testing.T) {
	model := validModelFixture()
	modelBefore, _ := ModelFingerprint(model)
	left := ContractIR{FormatVersion: ContractFormatVersion, Models: []ModelContractIR{{
		ModelID: "model", HookOwnedCreateFields: []FieldID{"slug", "tenant", "slug"},
	}}}
	right := ContractIR{FormatVersion: ContractFormatVersion, Models: []ModelContractIR{{
		ModelID: "model", HookOwnedCreateFields: []FieldID{"tenant", "slug"},
	}}}
	leftContract, err := ContractFingerprint(left)
	if err != nil {
		t.Fatal(err)
	}
	rightContract, err := ContractFingerprint(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftContract != rightContract {
		t.Fatalf("hook-owned field inventory was not sorted and deduplicated: %s != %s", leftContract, rightContract)
	}
	empty := ContractIR{FormatVersion: ContractFormatVersion, Models: []ModelContractIR{{ModelID: "model"}}}
	emptyContract, _ := ContractFingerprint(empty)
	if leftContract == emptyContract {
		t.Fatal("hook-owned fields did not affect ContractFingerprint")
	}
	modelAfter, _ := ModelFingerprint(model)
	if modelBefore != modelAfter {
		t.Fatal("hook-owned contract metadata changed ModelFingerprint")
	}
}

func TestEventSchemaFingerprintCanonicalLogicalShape(t *testing.T) {
	id := FieldID("00000000000000000000000000000001")
	title := FieldID("00000000000000000000000000000002")
	enumField := FieldID("00000000000000000000000000000003")
	enumID := EnumID("00000000000000000000000000000004")
	firstMember := EnumValueID("00000000000000000000000000000005")
	secondMember := EnumValueID("00000000000000000000000000000006")
	model := ModelDeclIR{
		ID: "00000000000000000000000000000010",
		Fields: []FieldIR{
			{ID: id, Kind: FieldScalar, Scalar: &ScalarFieldIR{Type: LogicalTypeIR{Kind: TypeUUID}}},
			{ID: title, Kind: FieldScalar, Scalar: &ScalarFieldIR{Type: LogicalTypeIR{Kind: TypeString}, Nullable: true}},
			{ID: enumField, Kind: FieldEnum, Scalar: &ScalarFieldIR{Type: LogicalTypeIR{Kind: TypeEnum, EnumID: &enumID}}},
		},
		PrimaryKey: &KeyIR{ID: "00000000000000000000000000000011", Kind: KeyPrimary, Fields: []FieldID{id}},
	}
	enums := []EnumIR{{ID: enumID, Values: []EnumValueIR{{ID: secondMember}, {ID: firstMember}}}}
	shape, err := BuildEventSchemaShape(model, enums, []FieldID{id, title, enumField})
	if err != nil {
		t.Fatal(err)
	}
	if len(shape.IdentityFields) != 1 || shape.IdentityFields[0].FieldID != id || len(shape.SnapshotFields) != 3 || shape.SnapshotFields[1].FieldID != title || len(shape.Enums) != 1 || shape.Enums[0].Members[0] != firstMember {
		t.Fatalf("event schema shape = %#v", shape)
	}
	base, err := EventSchemaFingerprint(shape)
	if err != nil {
		t.Fatal(err)
	}
	reorderedEnums, _ := cloneJSON(shape)
	reorderedEnums.Enums[0].Members[0], reorderedEnums.Enums[0].Members[1] = reorderedEnums.Enums[0].Members[1], reorderedEnums.Enums[0].Members[0]
	canonical, _ := EventSchemaFingerprint(reorderedEnums)
	if canonical != base {
		t.Fatal("enum inventory order changed the canonical event schema fingerprint")
	}
	reorderedSnapshot, _ := cloneJSON(shape)
	reorderedSnapshot.SnapshotFields[0], reorderedSnapshot.SnapshotFields[1] = reorderedSnapshot.SnapshotFields[1], reorderedSnapshot.SnapshotFields[0]
	changed, _ := EventSchemaFingerprint(reorderedSnapshot)
	if changed == base {
		t.Fatal("private snapshot semantic order did not change event schema fingerprint")
	}
	changedType, _ := cloneJSON(shape)
	changedType.SnapshotFields[1].Type.Kind = TypeBytes
	changed, _ = EventSchemaFingerprint(changedType)
	if changed == base {
		t.Fatal("logical value shape did not change event schema fingerprint")
	}
}

func TestEventSchemaBuilderRejectsNonScalarDuplicateAndUnknownEnumShapes(t *testing.T) {
	id := FieldID("00000000000000000000000000000001")
	relation := FieldID("00000000000000000000000000000002")
	enumField := FieldID("00000000000000000000000000000003")
	missingEnum := EnumID("00000000000000000000000000000004")
	model := ModelDeclIR{ID: "00000000000000000000000000000010", Fields: []FieldIR{
		{ID: id, Kind: FieldScalar, Scalar: &ScalarFieldIR{Type: LogicalTypeIR{Kind: TypeUUID}}},
		{ID: relation, Kind: FieldRelation, Relation: &RelationFieldIR{}},
		{ID: enumField, Kind: FieldEnum, Scalar: &ScalarFieldIR{Type: LogicalTypeIR{Kind: TypeEnum, EnumID: &missingEnum}}},
	}, PrimaryKey: &KeyIR{ID: "00000000000000000000000000000011", Kind: KeyPrimary, Fields: []FieldID{id}}}
	for name, snapshot := range map[string][]FieldID{"relation": {relation}, "duplicate": {id, id}, "unknown-enum": {enumField}} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildEventSchemaShape(model, nil, snapshot); err == nil {
				t.Fatalf("snapshot %#v was accepted", snapshot)
			}
		})
	}
}

func TestSelectorComponentsRemainOrderedWhileInventoriesCanonicalize(t *testing.T) {
	left := ContractIR{FormatVersion: ContractFormatVersion, Models: []ModelContractIR{{
		ModelID: "model",
		Selectors: []SelectorContractIR{
			{KeyID: "b", Name: "B", Fields: []FieldID{"one"}},
			{KeyID: "a", Name: "A", Fields: []FieldID{"first", "second"}},
		},
	}}}
	right, _ := cloneJSON(left)
	right.Models[0].Selectors[0], right.Models[0].Selectors[1] = right.Models[0].Selectors[1], right.Models[0].Selectors[0]
	leftHash, _ := ContractFingerprint(left)
	rightHash, _ := ContractFingerprint(right)
	if leftHash != rightHash {
		t.Fatal("selector inventory order must be canonical")
	}
	right.Models[0].Selectors[0].Fields[0], right.Models[0].Selectors[0].Fields[1] =
		right.Models[0].Selectors[0].Fields[1], right.Models[0].Selectors[0].Fields[0]
	orderedHash, _ := ContractFingerprint(right)
	if leftHash == orderedHash {
		t.Fatal("selector component order must affect ContractFingerprint")
	}
}

func TestEqualityIndexInventoryIsCanonicalModelMetadata(t *testing.T) {
	left := validModelFixture()
	keyID, indexID := KeyID("key"), IndexID("index")
	left.Models[0].EqualityIndexes = []EqualityIndexIR{
		{FieldID: "second", Kind: EqualityViaIndex, IndexID: &indexID},
		{FieldID: "first", Kind: EqualityViaKey, KeyID: &keyID},
	}
	right, _ := cloneJSON(left)
	right.Models[0].EqualityIndexes[0], right.Models[0].EqualityIndexes[1] =
		right.Models[0].EqualityIndexes[1], right.Models[0].EqualityIndexes[0]
	leftHash, _ := ModelFingerprint(left)
	rightHash, _ := ModelFingerprint(right)
	if leftHash != rightHash {
		t.Fatal("EqualityIndexed inventory order must be canonical")
	}
	right.Models[0].EqualityIndexes = right.Models[0].EqualityIndexes[:1]
	changedHash, _ := ModelFingerprint(right)
	if leftHash == changedHash {
		t.Fatal("EqualityIndexed planning metadata must affect ModelFingerprint")
	}
}

func validModelFixture() ModelIR {
	first := FieldID("00000000000000000000000000000001")
	second := FieldID("00000000000000000000000000000002")
	return ModelIR{
		FormatVersion: ModelFormatVersion,
		Schema: SchemaIdentityIR{
			ID: "00000000000000000000000000000010", StableName: "social",
			PackagePath: "example/social", RootFunction: "DefineSchema",
			Actor: GoNamedTypeIR{PackagePath: "example/social", Name: "Actor"},
		},
		Providers: []Provider{SQLite, PostgreSQL},
		Models: []ModelDeclIR{
			{
				ID: "00000000000000000000000000000020", LogicalName: "Post",
				Fields:     []FieldIR{{ID: first}, {ID: second}},
				PrimaryKey: &KeyIR{ID: "00000000000000000000000000000030", Kind: KeyPrimary, Fields: []FieldID{first, second}},
			},
			{ID: "00000000000000000000000000000021", LogicalName: "User"},
		},
	}
}
