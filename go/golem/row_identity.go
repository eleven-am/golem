package golem

import (
	"fmt"

	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
)

type rowIdentity struct {
	fields []FieldID
	cells  []readCell
	token  RuntimeSemanticIdentityToken
}

type RuntimeRowIdentity struct{ state *rowIdentity }

func RuntimeNewRowIdentity(fields []FieldID, cells []RuntimeReadCell) (RuntimeRowIdentity, error) {
	if len(fields) == 0 || len(fields) != len(cells) {
		return RuntimeRowIdentity{}, fmt.Errorf("row identity: %d fields and %d cells do not describe a key", len(fields), len(cells))
	}
	state := &rowIdentity{fields: append([]FieldID(nil), fields...), cells: make([]readCell, len(cells))}
	values := make([]any, len(cells))
	for index, cell := range cells {
		if fields[index] == (FieldID{}) || cell.field != fields[index] {
			return RuntimeRowIdentity{}, fmt.Errorf("row identity: cell %d does not belong to key field %x", index, fields[index])
		}
		if cell.cell.state != ReadPresent {
			return RuntimeRowIdentity{}, fmt.Errorf("row identity: key field %x has no value", fields[index])
		}
		state.cells[index] = cloneReadCell(cell.cell)
		values[index] = cell.cell.value
	}
	key, err := semantickey.Encode(values)
	if err != nil {
		return RuntimeRowIdentity{}, fmt.Errorf("row identity: %w", err)
	}
	state.token = runtimeSemanticIdentityToken(key)
	return RuntimeRowIdentity{state: state}, nil
}

func (identity RuntimeRowIdentity) Present() bool { return identity.state != nil }

func (identity RuntimeRowIdentity) Fields() []FieldID {
	if identity.state == nil {
		return nil
	}
	return append([]FieldID(nil), identity.state.fields...)
}

func (identity RuntimeRowIdentity) Values() []any {
	if identity.state == nil {
		return nil
	}
	values := make([]any, len(identity.state.cells))
	for index, cell := range identity.state.cells {
		values[index] = cloneReadCell(cell).value
	}
	return values
}

func (identity RuntimeRowIdentity) Token() RuntimeSemanticIdentityToken {
	if identity.state == nil {
		return RuntimeSemanticIdentityToken{}
	}
	return identity.state.token
}

func RuntimeModelRowWithIdentity(row RuntimeModelRow, identity RuntimeRowIdentity) RuntimeModelRow {
	result := cloneRuntimeModelRow(row)
	result.identity = identity.state
	return result
}

func RuntimeModelRowIdentity(row RuntimeModelRow) RuntimeRowIdentity {
	return RuntimeRowIdentity{state: row.identity}
}

func RuntimeTypedRowIdentity[M any](row Row[M]) RuntimeRowIdentity {
	return RuntimeRowIdentity{state: row.identity}
}
