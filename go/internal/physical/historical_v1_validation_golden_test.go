package physical

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestHistoricalV1FutureFieldsMustBeZeroAtEveryFrozenStructBoundary(t *testing.T) {
	tests := []struct {
		name    string
		zero    any
		nonzero any
		fields  []string
	}{
		{name: "root concurrency projection", zero: struct {
			Version     uint32
			Concurrency string
		}{Version: 1}, nonzero: struct {
			Version     uint32
			Concurrency string
		}{Version: 1, Concurrency: "revision"}, fields: []string{"Version"}},
		{name: "table projection", zero: struct {
			ID         ir.ModelID
			Projection []string
		}{ID: "81000000000000000000000000000001"}, nonzero: struct {
			ID         ir.ModelID
			Projection []string
		}{ID: "81000000000000000000000000000001", Projection: []string{"future"}}, fields: []string{"ID"}},
		{name: "column concurrency", zero: struct {
			ID          ir.FieldID
			Concurrency bool
		}{ID: "82000000000000000000000000000001"}, nonzero: struct {
			ID          ir.FieldID
			Concurrency bool
		}{ID: "82000000000000000000000000000001", Concurrency: true}, fields: []string{"ID"}},
		{name: "storage option", zero: struct {
			Kind         StorageKind
			FutureLength uint32
		}{Kind: StorageSQLiteText}, nonzero: struct {
			Kind         StorageKind
			FutureLength uint32
		}{Kind: StorageSQLiteText, FutureLength: 8}, fields: []string{"Kind"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateHistoricalV1ZeroOutsideFrozenFields(reflect.ValueOf(test.zero), test.fields); err != nil {
				t.Fatalf("zero-valued future field rejected: %v", err)
			}
			if err := validateHistoricalV1ZeroOutsideFrozenFields(reflect.ValueOf(test.nonzero), test.fields); err == nil {
				t.Fatal("nonzero current-only field was silently ignored")
			}
		})
	}
}

func TestHistoricalV1ValidatorClosedMutationGoldenInventory(t *testing.T) {
	base := PhysicalSchema{
		Version: 1, CanonicalVersion: 1,
		Provider:  ProviderManifest{Provider: ir.SQLite, Driver: DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}, MinimumVersion: Version{Major: 3, Minor: 38}},
		Namespace: Namespace{Name: "main"},
		Tables: []PhysicalTable{{
			ID: "81000000000000000000000000000001", Name: "items",
			Columns: []PhysicalColumn{{ID: "82000000000000000000000000000001", Name: "id", Ordinal: 0, Storage: StorageType{Kind: StorageSQLiteText}, Default: PhysicalDefault{Kind: DefaultNone}}},
		}},
	}
	if _, err := NormalizeHistoricalV1(base); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		want string
		edit func(*PhysicalSchema)
	}{
		{name: "current-driver", want: "9b557840eb0adfc01faf439a6fe63878509e474a5fad7c9ed52cd397caecb435", edit: func(schema *PhysicalSchema) { schema.Provider.Driver.Module = "github.com/ncruces/go-sqlite3/driver" }},
		{name: "current-storage-varchar", want: "11efb9bb39227db0d9dd1776ee7ffd3d729cd6f2e20cf64e325d23488eb4d50f", edit: func(schema *PhysicalSchema) {
			schema.Tables[0].Columns[0].Storage = StorageType{Kind: StoragePostgreSQLVarchar, Length: 32}
		}},
		{name: "unknown-storage", want: "6edc8ccbbcb1dd59d68152002ba1a7ec1cc220d606fe5098507bda245b9effd6", edit: func(schema *PhysicalSchema) { schema.Tables[0].Columns[0].Storage.Kind = StorageKind("future_storage") }},
		{name: "unknown-generated-kind", want: "7b9e4a9927dd793f9bca8780e1a171ab78862097d76054b4efa26c41ce6ddc5e", edit: func(schema *PhysicalSchema) {
			field := schema.Tables[0].Columns[0].ID
			schema.Tables[0].Columns[0].Generated = &GeneratedExpression{Kind: GeneratedKind("future"), Expression: Expression{Kind: ExpressionColumn, Type: schema.Tables[0].Columns[0].Storage, Column: &field, Operands: []Expression{}}}
		}},
		{name: "unknown-index-mode", want: "68ac08ba63408488bb81ab86fa5cbd74f49740ca5f8f27bb90d50ed3896cf7c8", edit: func(schema *PhysicalSchema) {
			field := schema.Tables[0].Columns[0].ID
			schema.Tables[0].Indexes = []PhysicalIndex{{ID: "83000000000000000000000000000001", Name: "idx_items", Method: IndexBTree, Keys: []IndexKey{{Column: &field, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: IndexCreationMode("future")}}
		}},
		{name: "unknown-system-object", want: "c1e999eaaccae748aba94d2cc1574fac1281aa61d6f5ee6911964736636ae8fb", edit: func(schema *PhysicalSchema) {
			schema.System = SystemSchema{Version: 1, Namespace: Namespace{Name: "_golem"}, Objects: []SystemObject{{ID: "84000000000000000000000000000001", Kind: SystemObjectKind("future"), Version: 1, Name: "_golem_future"}}}
		}},
		{name: "unknown-extension-value", want: "11bd7053fbf42ae0dd83bd373eb7e3e5fce8e889e026367444f6e2aff5c3297c", edit: func(schema *PhysicalSchema) {
			schema.Extensions = []Extension{{ID: "85000000000000000000000000000001", Provider: ir.SQLite, Kind: "historical.test", Version: 1, Owner: ObjectRef{Kind: ir.ObjectModel, ModelID: schema.Tables[0].ID}, Attributes: []Attribute{{Name: "value", Value: SemanticValue{Kind: SemanticValueKind("future")}}}}}
		}},
		{name: "unknown-capability-verification", want: "d56459324da9578c0886648d76cca352bb0f0e143733d70a63cc259fc95d4ec7", edit: func(schema *PhysicalSchema) {
			schema.Provider.Capabilities = []CapabilityFact{{ID: "future.capability.v1", Version: 1, Verification: CapabilityVerification("future")}}
		}},
		{name: "malformed-stable-id", want: "13056e6c338c700686e8bcb4c626ea0e68ae4980a0ace7f529149b9f1b009f83", edit: func(schema *PhysicalSchema) { schema.Tables[0].Columns[0].ID = "short" }},
		{name: "duplicate-field", want: "227042e55006773f255bb4036e988d4402fde1b68c2670060709d9cea45212f9", edit: func(schema *PhysicalSchema) {
			schema.Tables[0].Columns = append(schema.Tables[0].Columns, schema.Tables[0].Columns[0])
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			mutated := base
			mutated.Provider.Capabilities = append([]CapabilityFact(nil), base.Provider.Capabilities...)
			mutated.Tables = []PhysicalTable{historicalV1CloneTable(base.Tables[0])}
			test.edit(&mutated)
			_, err := NormalizeHistoricalV1(mutated)
			if err == nil {
				t.Fatal("historical v1 mutation was accepted")
			}
			got := fmt.Sprintf("%x", sha256.Sum256([]byte(err.Error())))
			if test.want == "" {
				t.Fatalf("freeze historical v1 validation golden: %s (%v)", got, err)
			}
			if got != test.want {
				t.Fatalf("historical v1 validation changed: got %s want %s", got, test.want)
			}
		})
	}
}
