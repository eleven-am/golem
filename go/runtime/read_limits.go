package runtime

import (
	"fmt"

	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
)

// ReadLimits bounds the work performed by one read without imposing a default
// row cap. MaxTake and MaxRelationFanout deliberately treat zero as unlimited;
// every other zero value selects the documented safe default.
//
// Configured hard-complexity limits may lower, but never raise, Golem's
// provider-neutral ceilings. This keeps acceptance portable between SQLite and
// PostgreSQL.
type ReadLimits struct {
	MaxTake                int
	MaxRelationFanout      int
	MaxRelationDepth       int
	MaxSelectedFields      int
	MaxStatementParameters int
	MaxStatementBytes      int
	MaxStatementAliases    int
	MaxLoaderKeys          int
}

type normalizedReadLimits struct {
	plan       readplan.Limits
	loaderKeys int
}

func normalizeReadLimits(value ReadLimits) (normalizedReadLimits, error) {
	if value.MaxTake < 0 || value.MaxRelationFanout < 0 || value.MaxRelationDepth < 0 || value.MaxSelectedFields < 0 || value.MaxStatementParameters < 0 || value.MaxStatementBytes < 0 || value.MaxStatementAliases < 0 || value.MaxLoaderKeys < 0 {
		return normalizedReadLimits{}, fmt.Errorf("read limits cannot be negative")
	}
	maxInt := int(^uint(0) >> 1)
	if value.MaxTake == maxInt || value.MaxRelationFanout == maxInt {
		return normalizedReadLimits{}, fmt.Errorf("row limits leave no room for overflow detection")
	}
	result := normalizedReadLimits{plan: readplan.DefaultLimits(), loaderKeys: MaxBatchLoaderKeys}
	result.plan.MaxTake = value.MaxTake
	result.plan.MaxRelationFanout = value.MaxRelationFanout
	if value.MaxRelationDepth != 0 {
		result.plan.MaxRelationDepth = value.MaxRelationDepth
	}
	if value.MaxSelectedFields != 0 {
		result.plan.MaxSelected = value.MaxSelectedFields
	}
	if value.MaxStatementParameters != 0 {
		result.plan.MaxStatementParameters = value.MaxStatementParameters
	}
	if value.MaxStatementBytes != 0 {
		result.plan.MaxStatementBytes = value.MaxStatementBytes
	}
	if value.MaxStatementAliases != 0 {
		result.plan.MaxStatementAliases = value.MaxStatementAliases
	}
	if value.MaxLoaderKeys != 0 {
		result.loaderKeys = value.MaxLoaderKeys
	}
	if result.plan.MaxStatementParameters > readsql.MaxStatementParameters {
		return normalizedReadLimits{}, fmt.Errorf("MaxStatementParameters exceeds provider-neutral ceiling %d", readsql.MaxStatementParameters)
	}
	if result.plan.MaxStatementBytes > readsql.MaxStatementBytes {
		return normalizedReadLimits{}, fmt.Errorf("MaxStatementBytes exceeds hard ceiling %d", readsql.MaxStatementBytes)
	}
	if result.plan.MaxStatementAliases > readsql.MaxStatementAliases {
		return normalizedReadLimits{}, fmt.Errorf("MaxStatementAliases exceeds hard ceiling %d", readsql.MaxStatementAliases)
	}
	if result.loaderKeys > MaxBatchLoaderKeys {
		return normalizedReadLimits{}, fmt.Errorf("MaxLoaderKeys exceeds hard ceiling %d", MaxBatchLoaderKeys)
	}
	return result, nil
}
