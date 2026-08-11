package migration

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

const backfillPostconditionKind = "golem.migration.postcondition.no-null.v1"

// BackfillPostcondition returns the generated postcondition digest for a
// reviewed required-column backfill.
//
// The condition is closed and is never author SQL: "the target table contains
// zero rows whose target column is NULL". It is bound to the stable model and
// field identity so a sealed companion cannot be retargeted at another column
// without breaking the entry chain hash.
func BackfillPostcondition(model ir.ModelID, field ir.FieldID) Digest {
	sum := sha256.Sum256([]byte(backfillPostconditionKind + "\x00" + string(model) + "\x00" + string(field)))
	return Digest(hex.EncodeToString(sum[:]))
}

// RequiresManualCompanion reports whether an operation may appear in reviewed
// history only together with exactly one checksummed ManualCompanion. Reviewed
// backfills carry the operator's checked-in SQL through that seam rather than
// through an application callback ABI.
func RequiresManualCompanion(operation Operation) bool {
	return operation.Kind == ManualStep || operation.Kind == BackfillColumn
}

// BackfillOwner resolves the stable model that owns a backfilled field in a
// normalized snapshot.
func BackfillOwner(schema physical.PhysicalSchema, field ir.FieldID) (ir.ModelID, bool) {
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			if column.ID == field {
				return table.ID, true
			}
		}
	}
	return "", false
}

func (b *diffBuilder) requiredColumnBackfill(column physical.PhysicalColumn) error {
	id := string(column.ID)
	if err := b.add(AddColumn, 30, id, nil, column, RiskManual); err != nil {
		return err
	}
	if err := b.add(BackfillColumn, 31, id, nil, column, RiskManual); err != nil {
		return err
	}
	if err := b.add(ValidateConstraint, 32, id, nil, column, RiskSafe); err != nil {
		return err
	}
	return b.add(AlterColumnNullability, 33, id, true, false, RiskDataLoss)
}
