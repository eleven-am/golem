package schematest

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// LogicalDiffFixture is the focused exact-equality schema used by mutation
// authorization tests. SQLite stores JSON and scalar lists as text, while the
// mutation decoder compares their canonical logical trees; Score preserves
// exact Float64 bits, including signed zero.
type LogicalDiffFixture struct {
	Bundle     golem.SchemaBundle
	Registry   *schema.Registry
	SQLite     physical.PhysicalSchema
	PostgreSQL physical.PhysicalSchema

	Record             golem.ModelID
	ID, Document, Tags golem.FieldID
	Score              golem.FieldID
	Primary            golem.KeyID
}

func NewLogicalDiff(t testing.TB) LogicalDiffFixture {
	t.Helper()
	modelID := compilerir.ModelID(id(101))
	idField := compilerir.FieldID(id(102))
	documentField := compilerir.FieldID(id(103))
	tagsField := compilerir.FieldID(id(104))
	scoreField := compilerir.FieldID(id(105))
	primary := compilerir.KeyID(id(106))
	listCapability := compilerir.CapabilityID("scalar-list:json-array:v1")
	listElement := compilerir.LogicalTypeIR{Kind: compilerir.TypeString}
	model := compilerir.ModelIR{
		FormatVersion: compilerir.ModelFormatVersion,
		Schema:        compilerir.SchemaIdentityIR{ID: compilerir.SchemaID(id(107)), StableName: "logical_diff"},
		Providers:     []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL},
		Models: []compilerir.ModelDeclIR{{
			ID: modelID, LogicalName: "Record", Table: compilerir.TableBindingIR{PhysicalName: "logical_records"},
			Fields: []compilerir.FieldIR{
				scalar(idField, "ID", "id", compilerir.TypeUUID, false),
				{ID: documentField, GoName: "Document", LogicalName: "Document", DeclarationOrder: 1, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "document", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeJSON}}},
				{ID: tagsField, GoName: "Tags", LogicalName: "Tags", DeclarationOrder: 2, Kind: compilerir.FieldScalarList, Scalar: &compilerir.ScalarFieldIR{Column: "tags", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeScalarList, Element: &listElement, Capability: &listCapability}}},
				{ID: scoreField, GoName: "Score", LogicalName: "Score", DeclarationOrder: 3, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "score", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64}}},
			},
			PrimaryKey: &compilerir.KeyIR{ID: primary, Kind: compilerir.KeyPrimary, PhysicalName: "pk_logical_records", Fields: []compilerir.FieldID{idField}},
		}},
	}
	contract := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{{ModelID: modelID, Fields: []compilerir.FieldContractIR{{FieldID: idField}, {FieldID: documentField}, {FieldID: tagsField}, {FieldID: scoreField}}}}}
	modelDocument := document(t, uint32(compilerir.ModelFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalModel(model)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ModelFingerprint(model)
		return payload, fingerprint, err
	})
	contractDocument := document(t, uint32(compilerir.ContractFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalContract(contract)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ContractFingerprint(contract)
		return payload, fingerprint, err
	})
	sqliteSchema, err := sqliteprovider.New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	postgresSchema, err := postgresprovider.New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{2}, "schematest", "p4-logical-diff", modelDocument, contractDocument,
		providerDocument(t, golem.SQLite, sqliteSchema), providerDocument(t, golem.PostgreSQL, postgresSchema))
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return LogicalDiffFixture{
		Bundle: bundle, Registry: registry, SQLite: sqliteSchema, PostgreSQL: postgresSchema,
		Record: golem.ModelID(mustFixed(t, string(modelID))), ID: golem.FieldID(mustFixed(t, string(idField))),
		Document: golem.FieldID(mustFixed(t, string(documentField))), Tags: golem.FieldID(mustFixed(t, string(tagsField))),
		Score:   golem.FieldID(mustFixed(t, string(scoreField))),
		Primary: golem.KeyID(mustFixed(t, string(primary))),
	}
}
