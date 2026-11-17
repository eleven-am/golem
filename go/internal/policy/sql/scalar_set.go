package sql

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// MaxScalarSetBytes bounds one compact set parameter, independently of the
// enclosing statement's parameter and SQL-text limits. The ceiling follows the
// existing 1 MiB SQL-text budget so compaction cannot create an unbounded bind.
const MaxScalarSetBytes = 1 << 20

type ScalarSetLimitError struct{ Maximum int }

func (err *ScalarSetLimitError) Error() string {
	return fmt.Sprintf("scalar set exceeds MaxScalarSetBytes (%d)", err.Maximum)
}

// JSONScalarSet uses the same physical codecs as individual comparison binds.
// Bytes retain individual binds: their JSON base64 representation is not the
// providers' native binary comparison value.
func (binder *Binder) JSONScalarSet(values []ir.Value, typ ir.TypeRef) (string, bool, error) {
	switch typ.Kind() {
	case ir.ValueBool, ir.ValueInt16, ir.ValueInt32, ir.ValueInt64,
		ir.ValueFloat32, ir.ValueFloat64, ir.ValueDecimal, ir.ValueString,
		ir.ValueUUID, ir.ValueDate, ir.ValueTime, ir.ValueDateTime, ir.ValueEnum:
	default:
		return "", false, nil
	}
	var output strings.Builder
	output.WriteByte('[')
	for index, value := range values {
		encoded, err := EncodeOperand(binder.dialect, binder.resolver, value, typ)
		if err != nil {
			return "", true, err
		}
		if text, ok := encoded.(string); ok && len(text)+2 > MaxScalarSetBytes-output.Len()-1 {
			return "", true, &ScalarSetLimitError{Maximum: MaxScalarSetBytes}
		}
		token, err := json.Marshal(encoded)
		if err != nil {
			return "", true, err
		}
		separator := 0
		if index != 0 {
			separator = 1
		}
		if len(token)+separator+1 > MaxScalarSetBytes-output.Len() {
			return "", true, &ScalarSetLimitError{Maximum: MaxScalarSetBytes}
		}
		if separator != 0 {
			output.WriteByte(',')
		}
		output.Write(token)
	}
	output.WriteByte(']')
	return binder.Text(output.String()), true, nil
}
