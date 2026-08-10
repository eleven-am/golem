package decode

import (
	"bytes"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// EqualValue is exact logical equality. It deliberately compares floating
// point bits, byte content, normalized Decimal values, and canonical JSON/list
// structure without any interface or float64 JSON conversion.
func EqualValue(left, right policyir.Value) bool {
	if left.Kind() != right.Kind() {
		return false
	}
	switch left.Kind() {
	case policyir.ValueBool:
		l, _ := left.Bool()
		r, _ := right.Bool()
		return l == r
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		l, _ := left.Signed()
		r, _ := right.Signed()
		return l == r
	case policyir.ValueFloat32:
		l, _ := left.Float32Bits()
		r, _ := right.Float32Bits()
		return l == r
	case policyir.ValueFloat64:
		l, _ := left.Float64Bits()
		r, _ := right.Float64Bits()
		return l == r
	case policyir.ValueDecimal:
		lc, ls, _ := left.Decimal()
		rc, rs, _ := right.Decimal()
		return lc == rc && ls == rs
	case policyir.ValueString:
		l, _ := left.Text()
		r, _ := right.Text()
		return l == r
	case policyir.ValueBytes:
		l, _ := left.Bytes()
		r, _ := right.Bytes()
		return bytes.Equal(l, r)
	case policyir.ValueUUID:
		l, _ := left.UUID()
		r, _ := right.UUID()
		return l == r
	case policyir.ValueDate:
		ly, lm, ld, _ := left.Date()
		ry, rm, rd, _ := right.Date()
		return ly == ry && lm == rm && ld == rd
	case policyir.ValueTime:
		l, _ := left.Time()
		r, _ := right.Time()
		return l == r
	case policyir.ValueDateTime:
		ls, ln, _ := left.DateTime()
		rs, rn, _ := right.DateTime()
		return ls == rs && ln == rn
	case policyir.ValueEnum:
		le, lv, _ := left.Enum()
		re, rv, _ := right.Enum()
		return le == re && lv == rv
	case policyir.ValueJSON:
		l, _ := left.JSON()
		r, _ := right.JSON()
		return equalJSON(l, r)
	case policyir.ValueScalarList:
		l, _ := left.List()
		r, _ := right.List()
		if len(l) != len(r) {
			return false
		}
		for index := range l {
			if !EqualValue(l[index], r[index]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func EqualCell(left, right Cell) bool {
	if left.field != right.field || left.null != right.null {
		return false
	}
	return left.null || EqualValue(left.value, right.value)
}

func EqualRow(left, right Row) bool {
	if left.model != right.model || len(left.cells) != len(right.cells) {
		return false
	}
	for index := range left.cells {
		if !EqualCell(left.cells[index], right.cells[index]) {
			return false
		}
	}
	return true
}

func equalJSON(left, right policyir.JSONValue) bool {
	if left.Kind() != right.Kind() {
		return false
	}
	switch left.Kind() {
	case policyir.JSONNull:
		return true
	case policyir.JSONBool:
		l, _ := left.Bool()
		r, _ := right.Bool()
		return l == r
	case policyir.JSONNumber:
		l, _ := left.Number()
		r, _ := right.Number()
		return l.Negative() == r.Negative() && l.Exponent() == r.Exponent() && bytes.Equal(l.Coefficient(), r.Coefficient())
	case policyir.JSONString:
		l, _ := left.Text()
		r, _ := right.Text()
		return l == r
	case policyir.JSONArray:
		l, _ := left.Array()
		r, _ := right.Array()
		if len(l) != len(r) {
			return false
		}
		for index := range l {
			if !equalJSON(l[index], r[index]) {
				return false
			}
		}
		return true
	case policyir.JSONObject:
		l, _ := left.Object()
		r, _ := right.Object()
		if len(l) != len(r) {
			return false
		}
		for index := range l {
			if l[index].Key() != r[index].Key() || !equalJSON(l[index].Value(), r[index].Value()) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
