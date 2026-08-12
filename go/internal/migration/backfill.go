package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

const backfillPostconditionKind = "golem.migration.postcondition.no-null.v1"

const reviewedBackfillMaximumBytes = 1 << 20

// ValidateReviewedBackfillArtifact applies the provider-independent, closed
// artifact rules at authoring and again at apply time. PostgreSQL's extended
// protocol remains the authority for the exactly-one-statement proof.
func ValidateReviewedBackfillArtifact(content []byte) error {
	if len(content) == 0 || len(content) > reviewedBackfillMaximumBytes {
		return fmt.Errorf("reviewed SQL must be between 1 byte and 1 MiB")
	}
	if !utf8.Valid(content) {
		return fmt.Errorf("reviewed SQL must be UTF-8")
	}
	if bytes.ContainsRune(content, '\r') || bytes.ContainsRune(content, 0) {
		return fmt.Errorf("reviewed SQL must use LF endings and contain no NUL")
	}
	if content[len(content)-1] != '\n' || bytes.HasSuffix(content, []byte("\n\n")) {
		return fmt.Errorf("reviewed SQL must end with exactly one final newline")
	}
	for _, marker := range []string{"{{", "}}", "${", "%s", "%d", "%v", "%q"} {
		if bytes.Contains(content, []byte(marker)) {
			return fmt.Errorf("reviewed SQL contains a template or interpolation marker")
		}
	}
	for index := 0; index+1 < len(content); index++ {
		if content[index] == '$' && content[index+1] >= '0' && content[index+1] <= '9' {
			return fmt.Errorf("reviewed SQL must be zero-parameter")
		}
	}
	return nil
}

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
