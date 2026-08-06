package resolve_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/resolve"
	"github.com/eleven-am/golem/go/internal/compiler/schema"
)

func TestBaseResolvesSocialScalarsEnumsDefaultsAndContract(t *testing.T) {
	rawResult := schema.Extract(context.Background(), schema.Config{Dir: "testdata/social", Pattern: "."})
	if len(rawResult.Diagnostics) != 0 {
		t.Fatalf("schema diagnostics: %#v", rawResult.Diagnostics)
	}
	result := resolve.Base(rawResult.Raw)
	if len(result.Diagnostics) != 0 {
		t.Fatalf("resolve diagnostics: %#v", result.Diagnostics)
	}
	if result.IDs == nil {
		t.Fatal("base resolver did not return the compilation-unit ID registry")
	}
	modelIR := result.Compilation.Model
	if !reflect.DeepEqual(modelIR.Providers, []ir.Provider{ir.SQLite, ir.PostgreSQL}) {
		t.Fatalf("default providers = %#v", modelIR.Providers)
	}
	if modelIR.Schema.StableName != "social" || modelIR.Schema.Actor.Name != "Actor" || modelIR.Schema.ID == "" {
		t.Fatalf("schema = %#v", modelIR.Schema)
	}
	if len(modelIR.Enums) != 1 || len(modelIR.Enums[0].Values) != 2 || modelIR.Enums[0].Values[0].WireValue != "PUBLIC" {
		t.Fatalf("enums = %#v", modelIR.Enums)
	}
	if len(modelIR.Models) != 2 || modelIR.Models[1].Go.Name != "User" {
		t.Fatalf("models = %#v", modelIR.Models)
	}
	user := modelIR.Models[1]
	fields := fieldsByName(user.Fields)
	assertType(t, fields["ID"], ir.TypeUUID, false, ir.FieldScalar)
	if fields["ID"].Scalar.Default == nil || fields["ID"].Scalar.Default.Kind != ir.DefaultUUID || fields["ID"].Scalar.Default.Producer != ir.ProducerApplication {
		t.Fatalf("ID default = %#v", fields["ID"].Scalar.Default)
	}
	assertType(t, fields["Nickname"], ir.TypeString, true, ir.FieldScalar)
	assertType(t, fields["Biography"], ir.TypeString, true, ir.FieldScalar)
	assertType(t, fields["Metadata"], ir.TypeJSON, false, ir.FieldScalar)
	if fields["Metadata"].Scalar.Type.JSONSchemaID == nil || *fields["Metadata"].Scalar.Type.JSONSchemaID != "github.com/eleven-am/golem/go/internal/compiler/resolve/testdata/social.Profile" {
		t.Fatalf("JSON schema identity = %#v", fields["Metadata"].Scalar.Type.JSONSchemaID)
	}
	assertType(t, fields["Scores"], ir.TypeScalarList, false, ir.FieldScalarList)
	if fields["Scores"].Scalar.Type.Element == nil || fields["Scores"].Scalar.Type.Element.Kind != ir.TypeInt32 {
		t.Fatalf("list type = %#v", fields["Scores"].Scalar.Type)
	}
	assertType(t, fields["Visibility"], ir.TypeEnum, false, ir.FieldEnum)
	if fields["Visibility"].Scalar.Default == nil || fields["Visibility"].Scalar.Default.Literal.Canonical != "PUBLIC" {
		t.Fatalf("enum default = %#v", fields["Visibility"].Scalar.Default)
	}
	assertType(t, fields["UpdatedAt"], ir.TypeDateTime, false, ir.FieldScalar)
	if !fields["UpdatedAt"].Scalar.Updated || fields["UpdatedAt"].Scalar.Default.Kind != ir.DefaultNow {
		t.Fatalf("updated field = %#v", fields["UpdatedAt"].Scalar)
	}
	if _, exists := fields["Manager"]; exists {
		t.Fatal("relation field entered the base scalar registry")
	}
	contract := result.Compilation.Contract.Models[1]
	if contract.GraphQLName != "Person" || !contract.Exposed {
		t.Fatalf("model contract = %#v", contract)
	}
	modeByID := map[ir.FieldID][]ir.FieldMode{}
	graphqlByID := map[ir.FieldID]string{}
	for _, mode := range contract.Fields {
		modeByID[mode.FieldID] = mode.Modes
		graphqlByID[mode.FieldID] = mode.GraphQLName
	}
	if !reflect.DeepEqual(modeByID[fields["Secret"].ID], []ir.FieldMode{ir.ModeWriteOnly, ir.ModeImmutable}) {
		t.Fatalf("secret modes = %#v", modeByID[fields["Secret"].ID])
	}
	if !reflect.DeepEqual(modeByID[fields["UpdatedAt"].ID], []ir.FieldMode{ir.ModeReadOnly}) {
		t.Fatalf("updated modes = %#v", modeByID[fields["UpdatedAt"].ID])
	}
	if graphqlByID[fields["Secret"].ID] != "secretValue" || graphqlByID[fields["ID"].ID] != "id" {
		t.Fatalf("field GraphQL names = %#v", graphqlByID)
	}
}

func TestBaseRejectsExposureConflictsAndBadDefaults(t *testing.T) {
	rawResult := schema.Extract(context.Background(), schema.Config{Dir: "testdata/social", Pattern: "."})
	if len(rawResult.Diagnostics) != 0 {
		t.Fatalf("schema diagnostics: %#v", rawResult.Diagnostics)
	}
	raw := rawResult.Raw
	for modelIndex := range raw.Models {
		if raw.Models[modelIndex].GoName != "User" {
			continue
		}
		for fieldIndex := range raw.Models[modelIndex].Fields {
			field := &raw.Models[modelIndex].Fields[fieldIndex]
			if field.GoName == "Nickname" {
				field.GolemAttrs = append(field.GolemAttrs, flag("hidden", field.Span), flag("readonly", field.Span))
			}
			if field.GoName == "Scores" {
				field.GolemAttrs = append(field.GolemAttrs, value("default", "uuid", field.Span))
			}
		}
	}
	result := resolve.Base(raw)
	assertDiagnostic(t, result.Diagnostics, "P1_EXPOSURE_MODE_CONFLICT")
	assertDiagnostic(t, result.Diagnostics, "P1_DEFAULT_UUID_TYPE")
}

func TestBaseIsDeterministicUnderUnorderedDeclarationTraversal(t *testing.T) {
	rawResult := schema.Extract(context.Background(), schema.Config{Dir: "testdata/social", Pattern: "."})
	if len(rawResult.Diagnostics) != 0 {
		t.Fatalf("schema diagnostics: %#v", rawResult.Diagnostics)
	}
	left := resolve.Base(rawResult.Raw)
	rightRaw := rawResult.Raw
	reverseModels(rightRaw.Models)
	reverseEnums(rightRaw.Enums)
	rightRaw.Root.Providers = []ir.RawProviderRef{{Provider: ir.PostgreSQL}, {Provider: ir.SQLite}}
	right := resolve.Base(rightRaw)
	leftModel, err := ir.CanonicalModel(left.Compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	rightModel, err := ir.CanonicalModel(right.Compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	leftContract, err := ir.CanonicalContract(left.Compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	rightContract, err := ir.CanonicalContract(right.Compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	if string(leftModel) != string(rightModel) || string(leftContract) != string(rightContract) {
		t.Fatalf("base resolution is traversal-dependent\nleft model: %s\nright model: %s", leftModel, rightModel)
	}
}

func TestFieldGraphQLRenameChangesOnlyContractFingerprint(t *testing.T) {
	rawResult := schema.Extract(context.Background(), schema.Config{Dir: "testdata/social", Pattern: "."})
	if len(rawResult.Diagnostics) != 0 {
		t.Fatalf("schema diagnostics: %#v", rawResult.Diagnostics)
	}
	encoded, err := json.Marshal(rawResult.Raw)
	if err != nil {
		t.Fatal(err)
	}
	var renamed ir.RawDeclIR
	if err := json.Unmarshal(encoded, &renamed); err != nil {
		t.Fatal(err)
	}
	for modelIndex := range renamed.Models {
		for fieldIndex := range renamed.Models[modelIndex].Fields {
			field := &renamed.Models[modelIndex].Fields[fieldIndex]
			if field.GoName != "Secret" {
				continue
			}
			for attributeIndex := range field.GolemAttrs {
				attribute := &field.GolemAttrs[attributeIndex]
				if attribute.Name == "graphql" {
					value := "credential"
					attribute.RawValue = &value
				}
			}
		}
	}
	left := resolve.Base(rawResult.Raw)
	right := resolve.Base(renamed)
	leftModel, _ := ir.ModelFingerprint(left.Compilation.Model)
	rightModel, _ := ir.ModelFingerprint(right.Compilation.Model)
	if leftModel != rightModel {
		t.Fatal("field GraphQL rename changed ModelFingerprint")
	}
	leftContract, _ := ir.ContractFingerprint(left.Compilation.Contract)
	rightContract, _ := ir.ContractFingerprint(right.Compilation.Contract)
	if leftContract == rightContract {
		t.Fatal("field GraphQL rename did not change ContractFingerprint")
	}
}

func assertType(t *testing.T, field ir.FieldIR, kind ir.LogicalTypeKind, nullable bool, fieldKind ir.FieldKind) {
	t.Helper()
	if field.Scalar == nil || field.Scalar.Type.Kind != kind || field.Scalar.Nullable != nullable || field.Kind != fieldKind {
		t.Fatalf("field %s = %#v, want kind=%s nullable=%v fieldKind=%s", field.GoName, field, kind, nullable, fieldKind)
	}
}

func fieldsByName(fields []ir.FieldIR) map[string]ir.FieldIR {
	result := make(map[string]ir.FieldIR, len(fields))
	for _, field := range fields {
		result[field.GoName] = field
	}
	return result
}

func assertDiagnostic(t *testing.T, diagnostics []ir.Diagnostic, code string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %s in %#v", code, diagnostics)
}

func flag(name string, span ir.SourceSpan) ir.RawAttribute {
	return ir.RawAttribute{Name: name, Span: span}
}

func value(name, raw string, span ir.SourceSpan) ir.RawAttribute {
	return ir.RawAttribute{Name: name, RawValue: &raw, Span: span}
}

func reverseModels(values []ir.RawModelDecl) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseEnums(values []ir.RawEnumDecl) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
