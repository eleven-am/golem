package runtime

import (
	"encoding/hex"
	"encoding/json"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"strings"
	"testing"
)

func p5GraphQLBundle(t testing.TB, bundle golem.SchemaBundle) (golem.SchemaBundle, compilerir.CompilationIR) {
	t.Helper()
	var compilation compilerir.CompilationIR
	if err := json.Unmarshal(bundle.Model().Bytes(), &compilation.Model); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bundle.Contract().Bytes(), &compilation.Contract); err != nil {
		t.Fatal(err)
	}
	models := make(map[compilerir.ModelID]compilerir.ModelDeclIR, len(compilation.Model.Models))
	for _, model := range compilation.Model.Models {
		models[model.ID] = model
	}
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		model := models[contract.ModelID]
		contract.GraphQLName, contract.Exposed = model.LogicalName, true
		fieldByID := make(map[compilerir.FieldID]compilerir.FieldIR, len(model.Fields))
		for _, field := range model.Fields {
			fieldByID[field.ID] = field
		}
		for fieldIndex := range contract.Fields {
			field := fieldByID[contract.Fields[fieldIndex].FieldID]
			contract.Fields[fieldIndex].GraphQLName = graphqlcontract.LowerCamel(field.GoName)
		}
		keys := make([]compilerir.KeyIR, 0, 1+len(model.Uniques))
		if model.PrimaryKey != nil {
			keys = append(keys, *model.PrimaryKey)
		}
		keys = append(keys, model.Uniques...)
		for _, key := range keys {
			if len(key.Fields) == 0 {
				continue
			}
			parts := make([]string, len(key.Fields))
			for i, fieldID := range key.Fields {
				parts[i] = graphqlcontract.LowerCamel(fieldByID[fieldID].GoName)
			}
			contract.Selectors = append(contract.Selectors, compilerir.SelectorContractIR{KeyID: key.ID, Kind: key.Kind, Name: strings.Join(parts, "_"), Fields: append([]compilerir.FieldID(nil), key.Fields...)})
		}
	}
	for _, diagnostic := range graphqlcontract.Normalize(&compilation, nil) {
		if diagnostic.Severity == compilerir.SeverityError {
			t.Fatalf("normalize GraphQL fixture: %s: %s", diagnostic.Code, diagnostic.Message)
		}
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(string(fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], decoded)
	contractDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	return golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), contractDocument, bundle.Providers()...), compilation
}

func postgresRuntimeBundle(t *testing.T, fixture schematest.Fixture, postgresSchema physical.PhysicalSchema) golem.SchemaBundle {
	t.Helper()
	payload, err := physical.CanonicalEncode(postgresSchema)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := physical.PhysicalFingerprint(postgresSchema)
	if err != nil {
		t.Fatal(err)
	}
	system, err := physical.SystemFingerprint(postgresSchema.Provider, postgresSchema.System)
	if err != nil {
		t.Fatal(err)
	}
	postgresDocument := golem.GeneratedProviderSchemaDocument(golem.PostgreSQL, golem.SchemaDigest(system), golem.GeneratedSchemaDocument(postgresSchema.Version, postgresSchema.CanonicalVersion, golem.SchemaDigest(fingerprint), payload))
	providers := fixture.Bundle.Providers()
	for index := range providers {
		if providers[index].Provider() == golem.PostgreSQL {
			providers[index] = postgresDocument
		}
	}
	return golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), fixture.Bundle.Contract(), providers...)
}
