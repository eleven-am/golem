package postgresql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

const (
	CapabilityPolicyBinaryText          = "policy.binary-text.v1"
	CapabilityPolicyASCIIInsensitive    = "policy.ascii-insensitive-text.v1"
	CapabilityPolicyExactJSON           = "policy.exact-json.v1"
	CapabilityPolicyScalarListJSON      = "scalar-list.json-array.v1"
	CapabilityPolicyRelationCorrelation = "policy.relation-correlation.v1"
)

// PolicyDialect renders the closed P2 predicate vocabulary for PostgreSQL 15+.
// It is stateless: all runtime capability evidence is carried by the
// fingerprint-bound CapabilityProof passed to the shared compiler.
type PolicyDialect struct{}

var _ policysql.Dialect = PolicyDialect{}

func NewPolicyDialect() PolicyDialect { return PolicyDialect{} }

func (PolicyDialect) Provider() policyir.Provider { return policyir.ProviderPostgreSQL }

func (PolicyDialect) Quote(name physical.PhysicalName) string { return quote(name) }

func (PolicyDialect) Table(model policysql.Model) string {
	return qualified(model.Namespace, model.Table)
}

func (PolicyDialect) Placeholder(position int) string { return "$" + strconv.Itoa(position) }

func (PolicyDialect) Supports(operator policyir.OperatorID) bool {
	switch operator {
	case policyir.OperatorEqual, policyir.OperatorNotEqual, policyir.OperatorIn, policyir.OperatorNotIn,
		policyir.OperatorLessThan, policyir.OperatorLessThanOrEqual, policyir.OperatorGreaterThan, policyir.OperatorGreaterThanOrEqual,
		policyir.OperatorContains, policyir.OperatorStartsWith, policyir.OperatorEndsWith, policyir.OperatorIsNull, policyir.OperatorIsNotNull,
		policyir.OperatorListEqual, policyir.OperatorListHas, policyir.OperatorListHasEvery, policyir.OperatorListHasSome,
		policyir.OperatorListIsEmpty, policyir.OperatorListIsNull, policyir.OperatorListIsNotNull,
		policyir.OperatorJSONIsNull, policyir.OperatorJSONIsNotNull, policyir.OperatorJSONEqual, policyir.OperatorJSONNotEqual,
		policyir.OperatorJSONLessThan, policyir.OperatorJSONLessThanOrEqual, policyir.OperatorJSONGreaterThan, policyir.OperatorJSONGreaterThanOrEqual,
		policyir.OperatorJSONStringContains, policyir.OperatorJSONStringStartsWith, policyir.OperatorJSONStringEndsWith,
		policyir.OperatorJSONArrayContains, policyir.OperatorJSONArrayStartsWith, policyir.OperatorJSONArrayEndsWith,
		policyir.OperatorRelationIs, policyir.OperatorRelationIsNot, policyir.OperatorRelationIsNull, policyir.OperatorRelationIsNotNull,
		policyir.OperatorRelationSome, policyir.OperatorRelationEvery, policyir.OperatorRelationNone:
		return true
	default:
		return false
	}
}

func (PolicyDialect) Encode(bound policysql.BoundValue) (any, error) {
	return encodePolicyValue(bound)
}

func (dialect PolicyDialect) RenderScalar(leaf policysql.ScalarLeaf, binder *policysql.Binder) (string, error) {
	column := dialect.column(leaf.Column)
	switch leaf.Operator {
	case policyir.OperatorIsNull:
		return column + " IS NULL", nil
	case policyir.OperatorIsNotNull:
		return column + " IS NOT NULL", nil
	}

	comparison := func(value policyir.Value, operator string) (string, error) {
		placeholder, err := binder.Value(value, leaf.Field.Type)
		if err != nil {
			return "", err
		}
		left, right := scalarComparisonOperands(column, policyCast(placeholder, leaf.Field.Type), leaf.Field.Type.Kind(), leaf.Mode)
		return "COALESCE(" + left + " " + operator + " " + right + ", FALSE)", nil
	}

	switch leaf.Operator {
	case policyir.OperatorEqual, policyir.OperatorNotEqual:
		value, _ := leaf.Operand.One()
		predicate, err := comparison(value, "=")
		if err != nil {
			return "", err
		}
		if leaf.Operator == policyir.OperatorNotEqual {
			return "NOT (" + predicate + ")", nil
		}
		return predicate, nil
	case policyir.OperatorIn, policyir.OperatorNotIn:
		values, _ := leaf.Operand.Many()
		if len(values) == 0 {
			if leaf.Operator == policyir.OperatorNotIn {
				return "TRUE", nil
			}
			return "FALSE", nil
		}
		parts := make([]string, len(values))
		for index, value := range values {
			predicate, err := comparison(value, "=")
			if err != nil {
				return "", err
			}
			parts[index] = "(" + predicate + ")"
		}
		predicate := strings.Join(parts, " OR ")
		if leaf.Operator == policyir.OperatorNotIn {
			return "NOT (" + predicate + ")", nil
		}
		return predicate, nil
	case policyir.OperatorLessThan, policyir.OperatorLessThanOrEqual, policyir.OperatorGreaterThan, policyir.OperatorGreaterThanOrEqual:
		value, _ := leaf.Operand.One()
		operator := map[policyir.OperatorID]string{
			policyir.OperatorLessThan: "<", policyir.OperatorLessThanOrEqual: "<=",
			policyir.OperatorGreaterThan: ">", policyir.OperatorGreaterThanOrEqual: ">=",
		}[leaf.Operator]
		return comparison(value, operator)
	case policyir.OperatorContains, policyir.OperatorStartsWith, policyir.OperatorEndsWith:
		value, _ := leaf.Operand.One()
		text, _ := value.Text()
		placeholder := binder.Text(text)
		left, right := textOperands(column, placeholder+"::text", leaf.Mode)
		var predicate string
		switch leaf.Operator {
		case policyir.OperatorContains:
			predicate = "pg_catalog.strpos(" + left + ", " + right + ") > 0"
		case policyir.OperatorStartsWith:
			predicate = "pg_catalog.left(" + left + ", pg_catalog.char_length(" + right + ")) = " + right
		default:
			predicate = "pg_catalog.right(" + left + ", pg_catalog.char_length(" + right + ")) = " + right
		}
		return "COALESCE(" + predicate + ", FALSE)", nil
	default:
		return "", fmt.Errorf("postgresql policy scalar: unsupported operator %d", leaf.Operator)
	}
}

func (dialect PolicyDialect) RenderList(leaf policysql.ListLeaf, binder *policysql.Binder) (string, error) {
	column := dialect.column(leaf.Column)
	switch leaf.Operator {
	case policyir.OperatorListIsNull:
		return column + " IS NULL", nil
	case policyir.OperatorListIsNotNull:
		return column + " IS NOT NULL", nil
	case policyir.OperatorListEqual:
		value, _ := leaf.Operand.One()
		placeholder, err := binder.Value(value, leaf.Field.Type)
		if err != nil {
			return "", err
		}
		return "COALESCE(pg_catalog.jsonb_typeof(" + column + ") = 'array' AND " + column + " = " + placeholder + "::jsonb, FALSE)", nil
	case policyir.OperatorListIsEmpty:
		wantEmpty, _ := leaf.Operand.Flag()
		operator := "= 0"
		if !wantEmpty {
			operator = "> 0"
		}
		return "COALESCE(pg_catalog.jsonb_typeof(" + column + ") = 'array' AND pg_catalog.jsonb_array_length(" + column + ") " + operator + ", FALSE)", nil
	case policyir.OperatorListHas:
		value, _ := leaf.Operand.One()
		return dialect.listElementExists(column, value, leaf.Field.Type, binder)
	case policyir.OperatorListHasEvery, policyir.OperatorListHasSome:
		values, _ := leaf.Operand.Many()
		if len(values) == 0 {
			if leaf.Operator == policyir.OperatorListHasEvery {
				return "COALESCE(pg_catalog.jsonb_typeof(" + column + ") = 'array', FALSE)", nil
			}
			return "FALSE", nil
		}
		parts := make([]string, len(values))
		for index, value := range values {
			predicate, err := dialect.listElementExists(column, value, leaf.Field.Type, binder)
			if err != nil {
				return "", err
			}
			parts[index] = "(" + predicate + ")"
		}
		join := " OR "
		if leaf.Operator == policyir.OperatorListHasEvery {
			join = " AND "
		}
		return strings.Join(parts, join), nil
	default:
		return "", fmt.Errorf("postgresql policy list: unsupported operator %d", leaf.Operator)
	}
}

func (dialect PolicyDialect) listElementExists(column string, value policyir.Value, listType policyir.TypeRef, binder *policysql.Binder) (string, error) {
	elementType, ok := listType.Element()
	if !ok {
		return "", fmt.Errorf("postgresql policy list: missing element type")
	}
	list, err := policyir.NewListValue([]policyir.Value{value})
	if err != nil {
		return "", err
	}
	listTypeForBinding, err := policyir.NewTypeRef(policyir.ValueScalarList, false, 0, 0, policyir.EnumID{}, &elementType, listType.Capability())
	if err != nil {
		return "", err
	}
	placeholder, err := binder.Value(list, listTypeForBinding)
	if err != nil {
		return "", err
	}
	typeName := jsonTypeName(elementType.Kind())
	predicate := "EXISTS (SELECT 1 FROM pg_catalog.jsonb_array_elements(" + column + ") AS golem_e(value) WHERE pg_catalog.jsonb_typeof(golem_e.value) = '" + typeName + "' AND golem_e.value = (" + placeholder + "::jsonb -> 0))"
	return "COALESCE(pg_catalog.jsonb_typeof(" + column + ") = 'array' AND " + predicate + ", FALSE)", nil
}

func (dialect PolicyDialect) RenderJSON(leaf policysql.JSONLeaf, binder *policysql.Binder) (string, error) {
	column := dialect.column(leaf.Column)
	if leaf.Operator == policyir.OperatorJSONIsNull {
		return column + " IS NULL", nil
	}
	if leaf.Operator == policyir.OperatorJSONIsNotNull {
		return column + " IS NOT NULL", nil
	}
	slot := jsonSlot(column, leaf.Path, binder)
	if sentinel, ok := leaf.Operand.JSONNull(); ok {
		var equal string
		switch sentinel {
		case policyir.JSONDbNull:
			equal = slot + " IS NULL"
		case policyir.JSONDocumentNull:
			equal = slot + " IS NOT NULL AND " + slot + " = 'null'::jsonb"
		case policyir.JSONAnyNull:
			equal = slot + " IS NULL OR " + slot + " = 'null'::jsonb"
		default:
			return "", fmt.Errorf("postgresql policy JSON: unknown null sentinel %d", sentinel)
		}
		if leaf.Operator == policyir.OperatorJSONNotEqual {
			switch sentinel {
			case policyir.JSONDbNull:
				return slot + " IS NOT NULL", nil
			default:
				return slot + " IS NOT NULL AND " + slot + " <> 'null'::jsonb", nil
			}
		}
		return "(" + equal + ")", nil
	}

	wrapper, _ := leaf.Operand.One()
	wanted, _ := wrapper.JSON()
	typeName := jsonTypeNameForJSON(wanted.Kind())
	typeGuard := "pg_catalog.jsonb_typeof(" + slot + ") = '" + typeName + "'"
	placeholder, err := binder.Value(wrapper, leaf.Field.Type)
	if err != nil {
		return "", err
	}
	boundJSON := placeholder + "::jsonb"

	switch leaf.Operator {
	case policyir.OperatorJSONEqual, policyir.OperatorJSONNotEqual:
		comparison := slot + " = " + boundJSON
		if leaf.Operator == policyir.OperatorJSONNotEqual {
			comparison = slot + " <> " + boundJSON
		}
		return "COALESCE(" + typeGuard + " AND " + comparison + ", FALSE)", nil
	case policyir.OperatorJSONLessThan, policyir.OperatorJSONLessThanOrEqual, policyir.OperatorJSONGreaterThan, policyir.OperatorJSONGreaterThanOrEqual:
		operator := map[policyir.OperatorID]string{
			policyir.OperatorJSONLessThan: "<", policyir.OperatorJSONLessThanOrEqual: "<=",
			policyir.OperatorJSONGreaterThan: ">", policyir.OperatorJSONGreaterThanOrEqual: ">=",
		}[leaf.Operator]
		var left, right string
		if wanted.Kind() == policyir.JSONNumber {
			left = "(" + slot + " #>> '{}')::numeric"
			right = "(" + boundJSON + " #>> '{}')::numeric"
		} else {
			left, right = textOperands("("+slot+" #>> '{}')", "("+boundJSON+" #>> '{}')", leaf.Mode)
		}
		return "COALESCE(" + typeGuard + " AND " + left + " " + operator + " " + right + ", FALSE)", nil
	case policyir.OperatorJSONStringContains, policyir.OperatorJSONStringStartsWith, policyir.OperatorJSONStringEndsWith:
		left, right := textOperands("("+slot+" #>> '{}')", "("+boundJSON+" #>> '{}')", leaf.Mode)
		var predicate string
		switch leaf.Operator {
		case policyir.OperatorJSONStringContains:
			predicate = "pg_catalog.strpos(" + left + ", " + right + ") > 0"
		case policyir.OperatorJSONStringStartsWith:
			predicate = "pg_catalog.left(" + left + ", pg_catalog.char_length(" + right + ")) = " + right
		default:
			predicate = "pg_catalog.right(" + left + ", pg_catalog.char_length(" + right + ")) = " + right
		}
		return "COALESCE(" + typeGuard + " AND " + predicate + ", FALSE)", nil
	case policyir.OperatorJSONArrayContains:
		return "COALESCE(pg_catalog.jsonb_typeof(" + slot + ") = 'array' AND " + slot + " @> " + boundJSON + ", FALSE)", nil
	case policyir.OperatorJSONArrayStartsWith, policyir.OperatorJSONArrayEndsWith:
		index := "0"
		if leaf.Operator == policyir.OperatorJSONArrayEndsWith {
			index = "pg_catalog.jsonb_array_length(" + slot + ") - 1"
		}
		return "COALESCE(pg_catalog.jsonb_typeof(" + slot + ") = 'array' AND pg_catalog.jsonb_array_length(" + slot + ") > 0 AND (" + slot + " -> " + index + ") = " + boundJSON + ", FALSE)", nil
	default:
		return "", fmt.Errorf("postgresql policy JSON: unsupported operator %d", leaf.Operator)
	}
}

func (dialect PolicyDialect) column(column policysql.Column) string {
	return dialect.Quote(column.Alias) + "." + dialect.Quote(column.Name)
}

func policyCast(placeholder string, typ policyir.TypeRef) string {
	switch typ.Kind() {
	case policyir.ValueBool:
		return placeholder + "::boolean"
	case policyir.ValueInt16:
		return placeholder + "::smallint"
	case policyir.ValueInt32:
		return placeholder + "::integer"
	case policyir.ValueInt64:
		return placeholder + "::bigint"
	case policyir.ValueFloat32:
		return placeholder + "::real"
	case policyir.ValueFloat64:
		return placeholder + "::double precision"
	case policyir.ValueDecimal:
		return fmt.Sprintf("%s::numeric(%d,%d)", placeholder, typ.Precision(), typ.Scale())
	case policyir.ValueString, policyir.ValueEnum:
		return placeholder + "::text"
	case policyir.ValueBytes:
		return placeholder + "::bytea"
	case policyir.ValueUUID:
		return placeholder + "::uuid"
	case policyir.ValueDate:
		return placeholder + "::date"
	case policyir.ValueTime:
		return fmt.Sprintf("%s::time(%d) without time zone", placeholder, typ.Precision())
	case policyir.ValueDateTime:
		return fmt.Sprintf("%s::timestamp(%d) with time zone", placeholder, typ.Precision())
	default:
		return placeholder
	}
}

const asciiUpper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
const asciiLower = "abcdefghijklmnopqrstuvwxyz"

func scalarComparisonOperands(column, parameter string, kind policyir.ValueKind, mode policyir.ComparisonMode) (string, string) {
	if kind == policyir.ValueString || kind == policyir.ValueEnum {
		return textOperands(column, parameter, mode)
	}
	return column, parameter
}

func textOperands(left, right string, mode policyir.ComparisonMode) (string, string) {
	left = "(" + left + " COLLATE \"C\")"
	right = "(" + right + " COLLATE \"C\")"
	if mode == policyir.ComparisonASCIIInsensitive {
		left = "(pg_catalog.translate(" + left + ", '" + asciiUpper + "', '" + asciiLower + "') COLLATE \"C\")"
		right = "(pg_catalog.translate(" + right + ", '" + asciiUpper + "', '" + asciiLower + "') COLLATE \"C\")"
	}
	return left, right
}

func jsonSlot(column string, path policyir.JSONPath, binder *policysql.Binder) string {
	segments := path.Segments()
	if len(segments) == 0 {
		return column
	}
	values := make([]string, len(segments))
	for index, segment := range segments {
		if key, ok := segment.Key(); ok {
			values[index] = binder.Text(key) + "::text"
		} else {
			arrayIndex, _ := segment.Index()
			values[index] = binder.Text(strconv.FormatUint(arrayIndex, 10)) + "::text"
		}
	}
	return "(" + column + " #> ARRAY[" + strings.Join(values, ", ") + "]::text[])"
}

func jsonTypeName(kind policyir.ValueKind) string {
	switch kind {
	case policyir.ValueBool:
		return "boolean"
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64, policyir.ValueFloat32, policyir.ValueFloat64, policyir.ValueDecimal:
		return "number"
	default:
		return "string"
	}
}

func jsonTypeNameForJSON(kind policyir.JSONKind) string {
	return map[policyir.JSONKind]string{
		policyir.JSONNull: "null", policyir.JSONBool: "boolean", policyir.JSONNumber: "number",
		policyir.JSONString: "string", policyir.JSONArray: "array", policyir.JSONObject: "object",
	}[kind]
}
