package physical

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// ExpectedStorage returns the one built-in physical storage representation for
// a logical type on provider. Provider lowerers and trust-boundary consumers
// must use this function instead of maintaining independent type maps.
func ExpectedStorage(provider ir.Provider, logical ir.LogicalTypeIR) (StorageType, error) {
	switch provider {
	case ir.SQLite:
		return expectedSQLiteStorage(logical)
	case ir.PostgreSQL:
		return expectedPostgreSQLStorage(logical)
	default:
		return StorageType{}, fmt.Errorf("logical storage: unsupported provider %q", provider)
	}
}

// StorageEqual compares every semantic storage component. In particular,
// precision, scale, and length are part of the type and cannot be ignored at a
// generated-artifact trust boundary.
func StorageEqual(left, right StorageType) bool {
	if left.Kind != right.Kind || left.Precision != right.Precision || left.Scale != right.Scale || left.Length != right.Length {
		return false
	}
	if left.Symbol == nil || right.Symbol == nil {
		return left.Symbol == nil && right.Symbol == nil
	}
	return *left.Symbol == *right.Symbol
}

func expectedSQLiteStorage(logical ir.LogicalTypeIR) (StorageType, error) {
	switch logical.Kind {
	case ir.TypeBool, ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeDecimal, ir.TypeDateTime:
		return StorageType{Kind: StorageSQLiteInteger}, nil
	case ir.TypeFloat32, ir.TypeFloat64:
		return StorageType{Kind: StorageSQLiteReal}, nil
	case ir.TypeString, ir.TypeUUID, ir.TypeDate, ir.TypeTime, ir.TypeJSON, ir.TypeEnum, ir.TypeScalarList:
		return StorageType{Kind: StorageSQLiteText, Length: optionalUint32(logical.MaxLength)}, nil
	case ir.TypeBytes:
		return StorageType{Kind: StorageSQLiteBlob, Length: optionalUint32(logical.MaxLength)}, nil
	default:
		return StorageType{}, fmt.Errorf("logical type %q has no portable SQLite storage", logical.Kind)
	}
}

func expectedPostgreSQLStorage(logical ir.LogicalTypeIR) (StorageType, error) {
	switch logical.Kind {
	case ir.TypeBool:
		return StorageType{Kind: StoragePostgreSQLBoolean}, nil
	case ir.TypeInt16:
		return StorageType{Kind: StoragePostgreSQLSmallInt}, nil
	case ir.TypeInt32:
		return StorageType{Kind: StoragePostgreSQLInteger}, nil
	case ir.TypeInt64:
		return StorageType{Kind: StoragePostgreSQLBigInt}, nil
	case ir.TypeFloat32:
		return StorageType{Kind: StoragePostgreSQLReal}, nil
	case ir.TypeFloat64:
		return StorageType{Kind: StoragePostgreSQLDouble}, nil
	case ir.TypeDecimal:
		if logical.Precision == nil || logical.Scale == nil {
			return StorageType{}, fmt.Errorf("decimal requires precision and scale")
		}
		if *logical.Precision > 18 {
			return StorageType{}, fmt.Errorf("portable decimal precision %d exceeds 18", *logical.Precision)
		}
		return StorageType{Kind: StoragePostgreSQLNumeric, Precision: *logical.Precision, Scale: *logical.Scale}, nil
	case ir.TypeString, ir.TypeEnum:
		return StorageType{Kind: StoragePostgreSQLText}, nil
	case ir.TypeBytes:
		return StorageType{Kind: StoragePostgreSQLBytea}, nil
	case ir.TypeUUID:
		return StorageType{Kind: StoragePostgreSQLUUID}, nil
	case ir.TypeDate:
		return StorageType{Kind: StoragePostgreSQLDate}, nil
	case ir.TypeTime:
		precision := optionalPrecision(logical.Precision)
		if precision > 6 {
			return StorageType{}, fmt.Errorf("time precision %d exceeds microseconds", precision)
		}
		return StorageType{Kind: StoragePostgreSQLTime, Length: uint32(precision)}, nil
	case ir.TypeDateTime:
		precision := optionalPrecision(logical.Precision)
		if precision > 6 {
			return StorageType{}, fmt.Errorf("datetime precision %d exceeds microseconds", precision)
		}
		return StorageType{Kind: StoragePostgreSQLTimestampTZ, Length: uint32(precision)}, nil
	case ir.TypeJSON, ir.TypeScalarList:
		return StorageType{Kind: StoragePostgreSQLJSONB}, nil
	default:
		return StorageType{}, fmt.Errorf("unsupported logical type %q", logical.Kind)
	}
}

func optionalUint32(value *uint32) uint32 {
	if value == nil {
		return 0
	}
	return *value
}

func optionalPrecision(value *uint16) uint16 {
	if value == nil {
		return 6
	}
	return *value
}
