package migration

import (
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

// SafeWidening reports whether a physical column type transition preserves
// every value the before type can represent, for every row that could ever
// exist in the column.
//
// The decision is a closed property of the normalized typed pair and its
// declared precision, scale, and length. It never consults stored data, a
// rendered SQL string, or a Go type name, so a transition can never be
// admitted because a particular dataset happened to cast successfully. Safe
// means value-preserving only: it does not mean lock-free, rewrite-free, or
// instant, and every accepted transition remains a reviewed migration
// operation carrying its own approval.
//
// Version 1 answers true only for the closed PostgreSQL allowlist:
//
//	smallint            -> integer
//	smallint            -> bigint
//	integer             -> bigint
//	real                -> double precision
//	numeric(p,s)        -> numeric(p2,s)          where p2 >= p
//	time(p)             -> time(p2)               where p2 >= p
//	timestamptz(p)      -> timestamptz(p2)        where p2 >= p
//
// Every other transition, including every narrowing, every scale change,
// every cross-family conversion, and every provider other than PostgreSQL, is
// refused.
func SafeWidening(provider ir.Provider, before, after physical.StorageType) bool {
	if provider != ir.PostgreSQL || before.Symbol != nil || after.Symbol != nil {
		return false
	}
	if physical.StorageEqual(before, after) {
		return false
	}
	switch before.Kind {
	case physical.StoragePostgreSQLSmallInt:
		return unparameterizedPair(before, after) && (after.Kind == physical.StoragePostgreSQLInteger || after.Kind == physical.StoragePostgreSQLBigInt)
	case physical.StoragePostgreSQLInteger:
		return unparameterizedPair(before, after) && after.Kind == physical.StoragePostgreSQLBigInt
	case physical.StoragePostgreSQLReal:
		return unparameterizedPair(before, after) && after.Kind == physical.StoragePostgreSQLDouble
	case physical.StoragePostgreSQLNumeric:
		if after.Kind != physical.StoragePostgreSQLNumeric || before.Length != 0 || after.Length != 0 {
			return false
		}
		if before.Precision == 0 || after.Precision == 0 || before.Scale > before.Precision {
			return false
		}
		return after.Scale == before.Scale && after.Precision >= before.Precision
	case physical.StoragePostgreSQLTime, physical.StoragePostgreSQLTimestampTZ:
		if after.Kind != before.Kind {
			return false
		}
		if before.Precision != 0 || after.Precision != 0 || before.Scale != 0 || after.Scale != 0 {
			return false
		}
		return after.Length >= before.Length && after.Length <= maximumTemporalPrecision
	default:
		return false
	}
}

const maximumTemporalPrecision = 6

func unparameterizedPair(values ...physical.StorageType) bool {
	for _, value := range values {
		if value.Precision != 0 || value.Scale != 0 || value.Length != 0 {
			return false
		}
	}
	return true
}
