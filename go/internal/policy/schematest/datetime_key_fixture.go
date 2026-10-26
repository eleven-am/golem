package schematest

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func NewDateTimeKeyed(t testing.TB) Fixture {
	t.Helper()
	fixture := New(t)
	modelDocument := fixture.Bundle.Model()
	var model compilerir.ModelIR
	if err := json.Unmarshal(modelDocument.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	post := compilerir.ModelID(hex.EncodeToString(fixture.Post[:]))
	postID := compilerir.FieldID(hex.EncodeToString(fixture.PostID[:]))
	at, stamps := compilerir.FieldID(id(35)), compilerir.FieldID(id(36))
	precision := uint16(6)
	element := compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: &precision}
	capability := compilerir.CapabilityID("scalar-list:json-array:v1")
	for index := range model.Models {
		declaration := &model.Models[index]
		if declaration.ID != post {
			continue
		}
		declaration.Fields = append(declaration.Fields,
			compilerir.FieldIR{ID: at, GoName: "At", LogicalName: "At", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "at", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: &precision}}},
			compilerir.FieldIR{ID: stamps, GoName: "Stamps", LogicalName: "Stamps", Kind: compilerir.FieldScalarList, Scalar: &compilerir.ScalarFieldIR{Column: "stamps", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeScalarList, Element: &element, Capability: &capability}, Nullable: true}},
		)
		declaration.PrimaryKey.Fields = []compilerir.FieldID{postID, at}
	}
	var contract compilerir.ContractIR
	if err := json.Unmarshal(fixture.Bundle.Contract().Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	for index := range contract.Models {
		if contract.Models[index].ModelID == post {
			contract.Models[index].Fields = append(contract.Models[index].Fields, compilerir.FieldContractIR{FieldID: at}, compilerir.FieldContractIR{FieldID: stamps})
		}
	}
	normalizeSubscribedEvents(t, model, &contract)
	contractDocument := document(t, uint32(compilerir.ContractFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalContract(contract)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ContractFingerprint(contract)
		return payload, fingerprint, err
	})
	fixture.SQLite = lowerSemantic(t, sqliteprovider.New(), model, fixture.SQLite)
	fixture.PostgreSQL = lowerSemantic(t, postgresprovider.New(), model, fixture.PostgreSQL)
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		canonicalModelDocument(t, modelDocument, model), contractDocument,
		providerDocument(t, golem.SQLite, fixture.SQLite), providerDocument(t, golem.PostgreSQL, fixture.PostgreSQL),
	)
	registry, err := schema.New(fixture.Bundle)
	if err != nil {
		t.Fatalf("bootstrap datetime-keyed schema fixture: %v", err)
	}
	fixture.Registry = registry
	fixture.PostAt = golem.FieldID(mustFixed(t, string(at)))
	fixture.PostStamps = golem.FieldID(mustFixed(t, string(stamps)))
	return fixture
}
