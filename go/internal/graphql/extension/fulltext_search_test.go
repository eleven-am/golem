package extension

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
)

func TestAddFullTextSearchOperationsCreatesOneProviderNeutralCallerQuery(t *testing.T) {
	compilation := extensionFixture()
	compilation.Model.Schema.PackagePath = "example/social"
	compilation.Contract.Models[0].GraphQLPlural = "accounts"
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "mail_content", Folding: fulltextcontract.FoldingDiacritics, Prefix: []uint8{}, Fields: []fulltextcontract.Field{{ID: "user-name", Weight: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	for index, provider := range []ir.Provider{ir.SQLite, ir.PostgreSQL} {
		compilation.Model.Extensions = append(compilation.Model.Extensions, ir.ProviderExtensionIR{ID: ir.ExtensionID(strings.Repeat(string(rune('5'+index)), 32)), Provider: provider, Version: 1, Owner: "user", Kind: fulltextcontract.IndexKind, Payload: payload})
	}
	if diagnostics := AddFullTextSearchOperations(&compilation); hasErrors(diagnostics) {
		t.Fatalf("full-text diagnostics=%#v", diagnostics)
	}
	if len(compilation.Contract.CustomOperations) != 1 {
		t.Fatalf("full-text operations=%#v", compilation.Contract.CustomOperations)
	}
	operation := compilation.Contract.CustomOperations[0]
	if operation.Name != "textSearchAccountsByMailContent" || operation.Capability != ir.CustomOperationCallerOnly || !IsFullTextSearchOperation(compilation, operation) {
		t.Fatalf("full-text operation=%#v", operation)
	}
	tampered := operation
	tampered.Name = "textSearchAccountsByOther"
	if IsFullTextSearchOperation(compilation, tampered) {
		t.Fatal("tampered full-text operation matched")
	}
}
