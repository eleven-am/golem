package batch

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// RenderPolicyCapture reads private physical policy evidence. It applies no
// actor or readable-row policy and confers no public selection authority. The
// caller owns transaction/snapshot consistency and must reject a result with
// more than maxRows rows; the additional row is an overflow sentinel.
// Empty equalities request the bounded physical domain of the capture model.
func RenderPolicyCapture(registry *schema.Registry, provider policyir.Provider, model policyir.ModelID, equalities []mutationdecode.Cell, maxRows uint32, maxParameters uint32) (Statement, error) {
	if maxRows == 0 || maxParameters == 0 {
		return Statement{}, fail(CodeInput, model, policyir.FieldID{}, "policy capture requires positive row and parameter bounds", nil)
	}
	context, err := standaloneContext(registry, provider, model, "golem_e0")
	if err != nil {
		return Statement{}, err
	}
	projection := registry.PolicyCaptureFields(golem.ModelID(model))
	if len(projection) == 0 {
		return Statement{}, fail(CodeSchema, model, policyir.FieldID{}, "model has no static policy capture projection", nil)
	}
	// The persisted-row owner rejects duplicate, foreign, relational, malformed,
	// and wrongly typed cells and normalizes declared temporal precision.
	match, err := mutationdecode.NewRow(registry, model, equalities)
	if err != nil {
		return Statement{}, fail(CodeInput, model, policyir.FieldID{}, "policy capture equalities are not exact scalar cells", err)
	}
	var predicates []string
	var bindings []Binding
	for _, cell := range match.Cells() {
		field, ok := context.resolver.Field(provider, model, cell.FieldID())
		if !ok {
			return Statement{}, fail(CodeSchema, model, cell.FieldID(), "policy capture equality field has no physical descriptor", nil)
		}
		column := context.qualified(field.Column)
		if cell.IsNull() {
			predicates = append(predicates, column+" IS NULL")
			continue
		}
		if uint64(len(bindings)) >= uint64(maxParameters) {
			return Statement{}, failLimit(golem.LimitStatementParameters, int(maxParameters), model, "policy capture exceeds parameter bound")
		}
		value, _ := cell.PolicyValue()
		encoded, encodeErr := context.encode(value, field.Type)
		if encodeErr != nil {
			return Statement{}, fail(CodeInput, model, cell.FieldID(), "policy capture equality cannot be encoded", encodeErr)
		}
		bindings = append(bindings, Binding{value: encoded})
		predicates = append(predicates, column+" = "+context.dialect.Placeholder(len(bindings)))
	}
	where := "TRUE"
	if len(predicates) != 0 {
		where = strings.Join(predicates, " AND ")
	}
	fields, columns, err := context.projectColumns(projection, true)
	if err != nil {
		return Statement{}, err
	}
	text := "SELECT " + strings.Join(fields, ", ") + " FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + where + " ORDER BY " + context.orderBy() + fmt.Sprintf(" LIMIT %d", uint64(maxRows)+1)
	return Statement{role: RehydrateAfterImage, text: text, bindings: bindings, columns: columns, cardinality: AtMostSentinelRows}, nil
}
