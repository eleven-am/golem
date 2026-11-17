package model

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestEmitPolicyScopesLowersTypedBodiesAndPreservesEmptySetArguments(t *testing.T) {
	compilation, declaration := scopeEmissionFixture()
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{declaration}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	request := Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}}
	result, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	source := string(result.Files[0].Source)
	template := compilation.Contract.Models[0].ScopeTemplates[0]
	for _, fragment := range []string{
		"type golemGeneratedUserScopes struct{}",
		"var UserScopes = golemGeneratedUserScopes{}",
		"func (golemGeneratedUserScopes) Access(args ScopeArgs) golem.BoundFactScope[User]",
		"golem.GeneratedBoundFactScope[User](golem.ModelID{",
		`"` + string(template.ID) + `", golem.SchemaDigest{`,
		"golem.GeneratedEqualField[User, golem.UUID]",
		".Eq(args.AccountID)",
		"golem.GeneratedToMany[User, Post]",
		"golem.GeneratedEqualField[Post, string]",
		".In(args.Titles...)",
		".In()",
		"golem.GeneratedBytesField[Post]",
		".Eq(args.Blob)",
		".In(args.Blobs...)",
		`golem.GeneratedTemplateArgument[golem.UUID]("AccountID", args.AccountID)`,
		`golem.GeneratedTemplateArgument[[]byte]("Blob", args.Blob)`,
		`golem.GeneratedTemplateSetArgument[[]byte]("Blobs", args.Blobs)`,
		`golem.GeneratedTemplateSetArgument[string]("Titles", args.Titles)`,
		"golem.All[User]()",
		"golem.None[User]()",
		").Not()",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("scope emitter omitted %q:\n%s", fragment, source)
		}
	}
	if strings.Contains(source, "if len(args.") || strings.Contains(source, ".GolemModel(") || strings.Contains(source, "ScopeTemplate(") {
		t.Fatal("scope emission skipped empty arguments or executed an author declaration")
	}
	fingerprint, err := schemaDigestLiteral(string(template.Fingerprint))
	if err != nil || !strings.Contains(source, fingerprint) {
		t.Fatalf("generated binder did not pin the exact template fingerprint: %v", err)
	}
	// Surface/bootstrap and final generation lower the same scope body. Stamps
	// affect descriptors, not which actor slots or expression are bound.
	request.FinalStamp = &FinalStamp{GenerationDigest: strings.Repeat("a", 64), GeneratorVersion: "test", TemplateABIVersion: "test"}
	final, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(source, "type golemGeneratedUserScopes")
	end := strings.Index(source[start:], "func GolemGeneratedDescriptors") + start
	if !strings.Contains(string(final.Files[0].Source), source[start:end]) {
		t.Fatal("final generation changed the scope binding emitted by the bootstrap")
	}
}

func TestEmitPolicyScopesQualifiesImportedModelsArgumentsAndEnums(t *testing.T) {
	compilation := crossPackageCompilation()
	post, user := &compilation.Model.Models[0], &compilation.Model.Models[1]
	post.PrimaryKey = &ir.KeyIR{ID: ir.KeyID(id(81)), Kind: ir.KeyPrimary, Fields: []ir.FieldID{ir.FieldID(id(74))}}
	user.PrimaryKey = &ir.KeyIR{ID: ir.KeyID(id(82)), Kind: ir.KeyPrimary, Fields: []ir.FieldID{ir.FieldID(id(76))}}
	post.Fields = append(post.Fields, scalarField(id(77), "AuthorID", 2, ir.TypeUUID))
	enumID := ir.EnumID(id(80))
	state := scalarField(id(78), "State", 1, ir.TypeEnum)
	state.Kind, state.Scalar.Type.EnumID = ir.FieldEnum, &enumID
	user.Fields = append(user.Fields, state)
	compilation.Model.Enums = []ir.EnumIR{{ID: enumID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/shared", Name: "Visibility"}, Values: []ir.EnumValueIR{{ID: ir.EnumValueID(id(83)), WireValue: "open"}}}}
	compilation.Model.Relations[0].Cardinality = ir.RelationMany
	compilation.Model.Relations[0].LocalFields = []ir.FieldID{ir.FieldID(id(77))}
	compilation.Model.Relations[0].RemoteFields = []ir.FieldID{ir.FieldID(id(76))}
	compilation.Contract.Models = []ir.ModelContractIR{{ModelID: post.ID, Subscriptions: true}, {ModelID: user.ID, Subscriptions: true}}
	template := ir.ScopeTemplateIR{Name: "access", Arguments: ir.GoNamedTypeIR{PackagePath: "example.test/app/grants", Name: "GrantArgs"}, Parameters: []ir.ScopeParameterIR{{Name: "AccountID", Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}}, Expression: ir.ScopeExpressionIR{
		ModelID: post.ID, Operator: "is", FieldID: ir.FieldID(id(75)), RelationID: ir.RelationID(id(73)), TargetModelID: user.ID,
		Children: []ir.ScopeExpressionIR{{ModelID: user.ID, Operator: "and", Children: []ir.ScopeExpressionIR{
			{ModelID: user.ID, Operator: "eq", FieldID: ir.FieldID(id(76)), Parameter: "AccountID"},
			{ModelID: user.ID, Operator: "in", FieldID: ir.FieldID(id(78)), Literals: []ir.TypedLiteralIR{{Kind: ir.LiteralEnum, Canonical: "open"}}},
		}}},
	}}
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{{ModelID: post.ID, Template: template}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/content", PackageName: "content"}, {ImportPath: "example.test/app/accounts", PackageName: "accounts"}}})
	if err != nil {
		t.Fatal(err)
	}
	var source string
	for _, file := range result.Files {
		if file.ImportPath == "example.test/app/content" {
			source = string(file.Source)
		}
	}
	for _, fragment := range []string{
		`"example.test/app/grants"`, `"example.test/app/shared"`, `"example.test/app/accounts"`,
		"Access(args grants.GrantArgs) golem.BoundFactScope[Post]",
		"golem.GeneratedToOne[Post, accounts.User]",
		"golem.GeneratedEqualField[accounts.User, golem.UUID]",
		"golem.GeneratedEqualField[accounts.User, shared.Visibility]",
		`shared.Visibility("open")`,
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("imported scope omitted %q:\n%s", fragment, source)
		}
	}
}

func TestEmitPolicyScopesRejectsNamespaceAndMethodCollisions(t *testing.T) {
	for _, name := range []string{"namespace", "argument-type", "method", "invalid-method"} {
		t.Run(name, func(t *testing.T) {
			compilation, declaration := scopeEmissionFixture()
			declarations := []scopecontract.Declaration{declaration}
			switch name {
			case "namespace":
				compilation.Model.Models = append(compilation.Model.Models, ir.ModelDeclIR{ID: ir.ModelID(id(90)), LogicalName: "UserScope", Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/social", Name: "Other"}})
			case "argument-type":
				declarations[0].Template.Arguments.Name = "UserScopes"
			case "method":
				second := declaration
				second.Template.Name = "Access"
				declarations = append(declarations, second)
			case "invalid-method":
				declarations[0].Template.Name = "not-valid"
			}
			if diagnostics := scopecontract.Normalize(&compilation, declarations); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			_, err := Emit(Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}})
			if err == nil || !strings.Contains(err.Error(), "scope") {
				t.Fatalf("scope namespace collision accepted: %v", err)
			}
		})
	}
}

func TestEmitWithoutPolicyScopesRemainsByteIdentical(t *testing.T) {
	compilation := socialCompilation()
	request := Request{Compilation: compilation, Packages: []PackageSpec{{ImportPath: "example.test/app/social", PackageName: "social"}}}
	before, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	for index := range compilation.Contract.Models {
		compilation.Contract.Models[index].ScopeTemplates = []ir.ScopeTemplateIR{}
	}
	request.Compilation = compilation
	after, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Files) != len(after.Files) || !bytes.Equal(before.Files[0].Source, after.Files[0].Source) || strings.Contains(string(after.Files[0].Source), "BoundFactScope") {
		t.Fatal("empty scope inventory changed existing emission")
	}
}

func TestScopeLiteralEmissionUsesTypedCanonicalConstants(t *testing.T) {
	for _, test := range []struct {
		literal ir.TypedLiteralIR
		typ     ir.LogicalTypeKind
		goType  string
		want    string
	}{
		{ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: "true"}, ir.TypeBool, "bool", "true"},
		{ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: "-32768"}, ir.TypeInt16, "int16", "int16(-32768)"},
		{ir.TypedLiteralIR{Kind: ir.LiteralFloat, Canonical: "1.25"}, ir.TypeFloat32, "float32", "float32(1.25)"},
		{ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: "a\"b\n"}, ir.TypeString, "string", `"a\"b\n"`},
		{ir.TypedLiteralIR{Kind: ir.LiteralEnum, Canonical: "open"}, ir.TypeEnum, "shared.Visibility", `shared.Visibility("open")`},
	} {
		actual, err := scopeLiteral(test.literal, ir.LogicalTypeIR{Kind: test.typ}, test.goType)
		if err != nil || actual != test.want {
			t.Fatalf("literal %v emitted %q want %q: %v", test.literal, actual, test.want, err)
		}
	}
	for _, literal := range []ir.TypedLiteralIR{{Kind: ir.LiteralInteger, Canonical: "32768"}, {Kind: ir.LiteralInteger, Canonical: "01"}, {Kind: ir.LiteralUUID, Canonical: "00000000-0000-0000-0000-000000000000"}} {
		if _, err := scopeLiteral(literal, ir.LogicalTypeIR{Kind: ir.TypeInt16}, "int16"); err == nil {
			t.Fatalf("unsupported or noncanonical literal was emitted: %v", literal)
		}
	}
}

func scopeEmissionFixture() (ir.CompilationIR, scopecontract.Declaration) {
	pkg := "example.test/app/social"
	userID, postID := ir.ModelID(id(1)), ir.ModelID(id(2))
	relationID, userPosts := ir.RelationID(id(40)), ir.FieldID(id(12))
	user := ir.ModelDeclIR{ID: userID, LogicalName: "User", Go: ir.GoNamedTypeIR{PackagePath: pkg, Name: "User"}, PrimaryKey: &ir.KeyIR{ID: ir.KeyID(id(61)), Kind: ir.KeyPrimary, Fields: []ir.FieldID{ir.FieldID(id(11))}}, Fields: []ir.FieldIR{scalarField(id(11), "ID", 0, ir.TypeUUID), relationField(id(12), "Posts", 1, relationID, ir.RelationInverse, ir.RelationHasMany)}}
	post := ir.ModelDeclIR{ID: postID, LogicalName: "Post", Go: ir.GoNamedTypeIR{PackagePath: pkg, Name: "Post"}, PrimaryKey: &ir.KeyIR{ID: ir.KeyID(id(62)), Kind: ir.KeyPrimary, Fields: []ir.FieldID{ir.FieldID(id(21))}}, Fields: []ir.FieldIR{scalarField(id(21), "ID", 0, ir.TypeString), scalarField(id(22), "Title", 1, ir.TypeString), scalarField(id(24), "AuthorID", 2, ir.TypeUUID), scalarField(id(25), "Blob", 3, ir.TypeBytes), relationField(id(23), "Author", 4, relationID, ir.RelationSource, ir.RelationBelongsTo)}}
	compilation := ir.CompilationIR{Model: ir.ModelIR{Models: []ir.ModelDeclIR{user, post}, Relations: []ir.RelationIR{{ID: relationID, SourceModel: postID, TargetModel: userID, SourceField: ir.FieldID(id(23)), InverseField: &userPosts, Cardinality: ir.RelationMany, LocalFields: []ir.FieldID{ir.FieldID(id(24))}, RemoteFields: []ir.FieldID{ir.FieldID(id(11))}}}}, Contract: ir.ContractIR{Models: []ir.ModelContractIR{{ModelID: userID, Subscriptions: true}, {ModelID: postID, Subscriptions: true}}}}
	template := ir.ScopeTemplateIR{Name: "access", Arguments: ir.GoNamedTypeIR{PackagePath: pkg, Name: "ScopeArgs"}, Parameters: []ir.ScopeParameterIR{{Name: "AccountID", Type: ir.LogicalTypeIR{Kind: ir.TypeUUID}}, {Name: "Titles", Type: ir.LogicalTypeIR{Kind: ir.TypeString}, Many: true}, {Name: "Blob", Type: ir.LogicalTypeIR{Kind: ir.TypeBytes}}, {Name: "Blobs", Type: ir.LogicalTypeIR{Kind: ir.TypeBytes}, Many: true}}, Expression: ir.ScopeExpressionIR{ModelID: userID, Operator: "and", Children: []ir.ScopeExpressionIR{
		{ModelID: userID, Operator: "eq", FieldID: ir.FieldID(id(11)), Parameter: "AccountID"},
		{ModelID: userID, Operator: "true"},
		{ModelID: userID, Operator: "not", Children: []ir.ScopeExpressionIR{{ModelID: userID, Operator: "false"}}},
		{ModelID: userID, Operator: "some", FieldID: userPosts, RelationID: relationID, TargetModelID: postID, Children: []ir.ScopeExpressionIR{{ModelID: postID, Operator: "and", Children: []ir.ScopeExpressionIR{
			{ModelID: postID, Operator: "in", FieldID: ir.FieldID(id(22)), Parameter: "Titles"},
			{ModelID: postID, Operator: "eq", FieldID: ir.FieldID(id(25)), Parameter: "Blob"},
			{ModelID: postID, Operator: "in", FieldID: ir.FieldID(id(25)), Parameter: "Blobs"},
			{ModelID: postID, Operator: "in", FieldID: ir.FieldID(id(22))},
		}}}},
	}}}
	return compilation, scopecontract.Declaration{ModelID: userID, Template: template}
}
