package physical

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestExpectedStorageOwnsProviderMappingsAndMetadata(t *testing.T) {
	precision, scale := uint16(18), uint16(4)
	temporalPrecision := uint16(3)
	maxLength := uint32(128)
	tests := []struct {
		name     string
		provider ir.Provider
		logical  ir.LogicalTypeIR
		want     StorageType
	}{
		{name: "sqlite decimal integer", provider: ir.SQLite, logical: ir.LogicalTypeIR{Kind: ir.TypeDecimal, Precision: &precision, Scale: &scale}, want: StorageType{Kind: StorageSQLiteInteger}},
		{name: "sqlite string length", provider: ir.SQLite, logical: ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &maxLength}, want: StorageType{Kind: StorageSQLiteText, Length: maxLength}},
		{name: "sqlite bytes length", provider: ir.SQLite, logical: ir.LogicalTypeIR{Kind: ir.TypeBytes, MaxLength: &maxLength}, want: StorageType{Kind: StorageSQLiteBlob, Length: maxLength}},
		{name: "postgres decimal", provider: ir.PostgreSQL, logical: ir.LogicalTypeIR{Kind: ir.TypeDecimal, Precision: &precision, Scale: &scale}, want: StorageType{Kind: StoragePostgreSQLNumeric, Precision: precision, Scale: scale}},
		{name: "postgres bounded string", provider: ir.PostgreSQL, logical: ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &maxLength}, want: StorageType{Kind: StoragePostgreSQLVarchar, Length: maxLength}},
		{name: "postgres explicit time precision", provider: ir.PostgreSQL, logical: ir.LogicalTypeIR{Kind: ir.TypeTime, Precision: &temporalPrecision}, want: StorageType{Kind: StoragePostgreSQLTime, Length: uint32(temporalPrecision)}},
		{name: "postgres default datetime precision", provider: ir.PostgreSQL, logical: ir.LogicalTypeIR{Kind: ir.TypeDateTime}, want: StorageType{Kind: StoragePostgreSQLTimestampTZ, Length: 6}},
		{name: "postgres scalar list jsonb", provider: ir.PostgreSQL, logical: ir.LogicalTypeIR{Kind: ir.TypeScalarList}, want: StorageType{Kind: StoragePostgreSQLJSONB}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ExpectedStorage(test.provider, test.logical)
			if err != nil {
				t.Fatal(err)
			}
			if !StorageEqual(got, test.want) {
				t.Fatalf("storage = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestStorageEqualIncludesAllTypeMetadata(t *testing.T) {
	base := StorageType{Kind: StoragePostgreSQLNumeric, Precision: 18, Scale: 4}
	for name, changed := range map[string]StorageType{
		"kind":      {Kind: StoragePostgreSQLText, Precision: 18, Scale: 4},
		"precision": {Kind: StoragePostgreSQLNumeric, Precision: 17, Scale: 4},
		"scale":     {Kind: StoragePostgreSQLNumeric, Precision: 18, Scale: 3},
		"length":    {Kind: StoragePostgreSQLNumeric, Precision: 18, Scale: 4, Length: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if StorageEqual(base, changed) {
				t.Fatalf("metadata drift was accepted: %#v", changed)
			}
		})
	}
}
