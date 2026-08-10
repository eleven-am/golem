package sqlite

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// PolicyDialect is SQLite's closed P2 leaf renderer. Traversal, aliases,
// relation correlation, and argument order remain owned by internal/policy/sql.
type PolicyDialect struct{}

func NewPolicyDialect() PolicyDialect { return PolicyDialect{} }

func (PolicyDialect) Provider() ir.Provider                   { return ir.ProviderSQLite }
func (PolicyDialect) Quote(name physical.PhysicalName) string { return quote(name) }
func (PolicyDialect) Table(model policysql.Model) string      { return quote(model.Table) }
func (PolicyDialect) Placeholder(position int) string         { return "?" + strconv.Itoa(position) }

func (PolicyDialect) Supports(operator ir.OperatorID) bool {
	return operator >= ir.OperatorEqual && operator <= ir.OperatorIsNotNull ||
		operator >= ir.OperatorListEqual && operator <= ir.OperatorListIsNotNull ||
		operator >= ir.OperatorJSONIsNull && operator <= ir.OperatorJSONArrayEndsWith ||
		operator >= ir.OperatorRelationIs && operator <= ir.OperatorRelationNone
}

func (PolicyDialect) Encode(bound policysql.BoundValue) (any, error) {
	value, typ := bound.Value, bound.Type
	if value.Kind() != typ.Kind() {
		return nil, fmt.Errorf("sqlite policy codec: value kind %d does not match type %d", value.Kind(), typ.Kind())
	}
	switch typ.Kind() {
	case ir.ValueBool:
		value, _ := value.Bool()
		if value {
			return int64(1), nil
		}
		return int64(0), nil
	case ir.ValueInt16, ir.ValueInt32, ir.ValueInt64:
		value, _ := value.Signed()
		return value, nil
	case ir.ValueFloat32:
		bits, _ := value.Float32Bits()
		return float64(math.Float32frombits(bits)), nil
	case ir.ValueFloat64:
		bits, _ := value.Float64Bits()
		return math.Float64frombits(bits), nil
	case ir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		return scaleDecimal(coefficient, scale, typ.Scale())
	case ir.ValueString:
		value, _ := value.Text()
		return value, nil
	case ir.ValueBytes:
		value, _ := value.Bytes()
		return value, nil
	case ir.ValueUUID:
		value, _ := value.UUID()
		encoded := hex.EncodeToString(value[:])
		return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
	case ir.ValueDate:
		year, month, day, _ := value.Date()
		return fmt.Sprintf("%04d-%02d-%02d", year, month, day), nil
	case ir.ValueTime:
		microseconds, _ := value.Time()
		return formatTime(microseconds, typ.Precision()), nil
	case ir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		microseconds := int64(nanos) / 1_000
		if seconds > math.MaxInt64/1_000_000 || seconds < math.MinInt64/1_000_000 {
			return nil, fmt.Errorf("sqlite policy codec: datetime is outside microsecond storage range")
		}
		whole := seconds * 1_000_000
		if whole > math.MaxInt64-microseconds {
			return nil, fmt.Errorf("sqlite policy codec: datetime is outside microsecond storage range")
		}
		return whole + microseconds, nil
	case ir.ValueEnum:
		if len(bound.EnumWires) != 1 {
			return nil, fmt.Errorf("sqlite policy codec: enum wire label is missing")
		}
		return bound.EnumWires[0], nil
	case ir.ValueJSON:
		value, _ := value.JSON()
		return encodePolicyJSON(value)
	case ir.ValueScalarList:
		values, _ := value.List()
		element, ok := typ.Element()
		if !ok {
			return nil, fmt.Errorf("sqlite policy codec: scalar-list element type is missing")
		}
		return encodePolicyList(values, element, bound.EnumWires)
	default:
		return nil, fmt.Errorf("sqlite policy codec: unsupported value kind %d", typ.Kind())
	}
}

func (PolicyDialect) RenderScalar(leaf policysql.ScalarLeaf, binder *policysql.Binder) (string, error) {
	column := policyColumn(leaf.Column)
	if leaf.Operator == ir.OperatorIsNull {
		return booleanSQL(column + " IS NULL"), nil
	}
	if leaf.Operator == ir.OperatorIsNotNull {
		return booleanSQL(column + " IS NOT NULL"), nil
	}

	comparison := func(value ir.Value, operator string) (string, error) {
		argument, err := binder.Value(value, leaf.Field.Type)
		if err != nil {
			return "", err
		}
		left, right := column, argument
		if leaf.Field.Type.Kind() == ir.ValueString || leaf.Field.Type.Kind() == ir.ValueEnum {
			if leaf.Mode == ir.ComparisonASCIIInsensitive {
				left, right = "golem_policy_ascii_fold("+left+")", "golem_policy_ascii_fold("+right+")"
			} else {
				left, right = "("+left+" COLLATE BINARY)", "("+right+" COLLATE BINARY)"
			}
		}
		return left + " " + operator + " " + right, nil
	}

	switch leaf.Operator {
	case ir.OperatorEqual, ir.OperatorNotEqual:
		value, _ := leaf.Operand.One()
		predicate, err := comparison(value, "=")
		if err != nil {
			return "", err
		}
		if leaf.Operator == ir.OperatorNotEqual {
			predicate = column + " IS NULL OR NOT (" + predicate + ")"
		}
		return booleanSQL(predicate), nil
	case ir.OperatorIn, ir.OperatorNotIn:
		values, _ := leaf.Operand.Many()
		if len(values) == 0 {
			return booleanSQL(strconv.FormatBool(leaf.Operator == ir.OperatorNotIn)), nil
		}
		predicates := make([]string, len(values))
		for index, value := range values {
			predicate, err := comparison(value, "=")
			if err != nil {
				return "", err
			}
			predicates[index] = predicate
		}
		if leaf.Operator == ir.OperatorIn {
			return booleanSQL(column + " IS NOT NULL AND (" + strings.Join(predicates, " OR ") + ")"), nil
		}
		return booleanSQL(column + " IS NULL OR NOT (" + strings.Join(predicates, " OR ") + ")"), nil
	case ir.OperatorLessThan, ir.OperatorLessThanOrEqual, ir.OperatorGreaterThan, ir.OperatorGreaterThanOrEqual:
		value, _ := leaf.Operand.One()
		token := map[ir.OperatorID]string{ir.OperatorLessThan: "<", ir.OperatorLessThanOrEqual: "<=", ir.OperatorGreaterThan: ">", ir.OperatorGreaterThanOrEqual: ">="}[leaf.Operator]
		predicate, err := comparison(value, token)
		if err != nil {
			return "", err
		}
		return booleanSQL(column + " IS NOT NULL AND (" + predicate + ")"), nil
	case ir.OperatorContains, ir.OperatorStartsWith, ir.OperatorEndsWith:
		value, _ := leaf.Operand.One()
		argument, err := binder.Value(value, leaf.Field.Type)
		if err != nil {
			return "", err
		}
		left, right := column, argument
		if leaf.Mode == ir.ComparisonASCIIInsensitive {
			left, right = "golem_policy_ascii_fold("+left+")", "golem_policy_ascii_fold("+right+")"
		}
		predicate := "instr(" + left + ", " + right + ") > 0"
		if leaf.Operator == ir.OperatorStartsWith {
			predicate = "substr(" + left + ", 1, length(" + right + ")) = " + right
		}
		if leaf.Operator == ir.OperatorEndsWith {
			predicate = "length(" + right + ") = 0 OR substr(" + left + ", -length(" + right + ")) = " + right
		}
		return booleanSQL(column + " IS NOT NULL AND (" + predicate + ")"), nil
	default:
		return "", fmt.Errorf("sqlite policy scalar: unsupported operator %d", leaf.Operator)
	}
}

func (PolicyDialect) RenderList(leaf policysql.ListLeaf, binder *policysql.Binder) (string, error) {
	column := policyColumn(leaf.Column)
	if leaf.Operator == ir.OperatorListIsNull {
		return booleanSQL(column + " IS NULL"), nil
	}
	if leaf.Operator == ir.OperatorListIsNotNull {
		return booleanSQL(column + " IS NOT NULL"), nil
	}
	element, ok := leaf.Field.Type.Element()
	if !ok {
		return "", fmt.Errorf("sqlite policy list: element type is missing")
	}
	var wanted string
	switch leaf.Operand.Kind() {
	case ir.OperandOne:
		value, _ := leaf.Operand.One()
		if value.Kind() == ir.ValueScalarList {
			var err error
			wanted, err = binder.Value(value, leaf.Field.Type)
			if err != nil {
				return "", err
			}
		} else if element.Kind() == ir.ValueEnum {
			var err error
			wanted, err = binder.Value(value, element)
			if err != nil {
				return "", err
			}
		} else {
			encoded, err := encodePolicyListElement(value, element, nil)
			if err != nil {
				return "", err
			}
			wanted = binder.Text(encoded)
		}
	case ir.OperandMany:
		values, _ := leaf.Operand.Many()
		encoded, err := encodePolicyList(values, element, nil)
		if element.Kind() == ir.ValueEnum {
			// Resolve enum wire labels through the sole binder path.
			list, listErr := ir.NewListValue(values)
			if listErr != nil {
				return "", listErr
			}
			wanted, err = binder.Value(list, leaf.Field.Type)
		} else if err == nil {
			wanted = binder.Text(encoded)
		}
		if err != nil {
			return "", err
		}
	case ir.OperandFlag:
		flag, _ := leaf.Operand.Flag()
		if flag {
			wanted = binder.Text("true")
		} else {
			wanted = binder.Text("false")
		}
	default:
		return "", fmt.Errorf("sqlite policy list: invalid operand kind %d", leaf.Operand.Kind())
	}
	typeArgument := binder.Text(policyListTypeDescriptor(element))
	return "golem_policy_list(" + column + ", " + wanted + ", " + typeArgument + ", " + strconv.Itoa(int(leaf.Operator)) + ")", nil
}

func (PolicyDialect) RenderJSON(leaf policysql.JSONLeaf, binder *policysql.Binder) (string, error) {
	column := policyColumn(leaf.Column)
	if leaf.Operator == ir.OperatorJSONIsNull {
		return booleanSQL(column + " IS NULL"), nil
	}
	if leaf.Operator == ir.OperatorJSONIsNotNull {
		return booleanSQL(column + " IS NOT NULL"), nil
	}
	path, err := encodePolicyPath(leaf.Path)
	if err != nil {
		return "", err
	}
	pathArgument := binder.Text(path)
	operandKind := int(leaf.Operand.Kind())
	operand := ""
	if nullKind, ok := leaf.Operand.JSONNull(); ok {
		operand = strconv.Itoa(int(nullKind))
	} else {
		wrapped, ok := leaf.Operand.One()
		if !ok {
			return "", fmt.Errorf("sqlite policy JSON: invalid operand")
		}
		value, ok := wrapped.JSON()
		if !ok {
			return "", fmt.Errorf("sqlite policy JSON: operand is not JSON")
		}
		operand, err = encodePolicyJSON(value)
		if err != nil {
			return "", err
		}
	}
	operandArgument := binder.Text(operand)
	return "golem_policy_json(" + column + ", " + pathArgument + ", " + operandArgument + ", " + strconv.Itoa(int(leaf.Operator)) + ", " + strconv.Itoa(int(leaf.Mode)) + ", " + strconv.Itoa(operandKind) + ")", nil
}

func policyColumn(column policysql.Column) string {
	return quote(column.Alias) + "." + quote(column.Name)
}
func booleanSQL(predicate string) string { return "CASE WHEN (" + predicate + ") THEN 1 ELSE 0 END" }

func scaleDecimal(coefficient int64, from uint8, to uint16) (int64, error) {
	if uint16(from) > to {
		return 0, fmt.Errorf("sqlite policy codec: decimal scale %d exceeds field scale %d", from, to)
	}
	for index := uint16(from); index < to; index++ {
		if coefficient > math.MaxInt64/10 || coefficient < math.MinInt64/10 {
			return 0, fmt.Errorf("sqlite policy codec: decimal scaled coefficient overflows int64")
		}
		coefficient *= 10
	}
	return coefficient, nil
}

func formatTime(microseconds int64, precision uint16) string {
	hours := microseconds / 3_600_000_000
	microseconds %= 3_600_000_000
	minutes := microseconds / 60_000_000
	microseconds %= 60_000_000
	seconds := microseconds / 1_000_000
	fraction := microseconds % 1_000_000
	base := fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	if precision == 0 {
		return base
	}
	return base + "." + fmt.Sprintf("%06d", fraction)[:precision]
}

func policyListTypeDescriptor(typ ir.TypeRef) string {
	return fmt.Sprintf("%d:%d:%d", typ.Kind(), typ.Precision(), typ.Scale())
}

func encodePolicyPath(path ir.JSONPath) (string, error) {
	segments := make([][2]any, 0, len(path.Segments()))
	for _, segment := range path.Segments() {
		if key, ok := segment.Key(); ok {
			segments = append(segments, [2]any{"k", key})
			continue
		}
		index, ok := segment.Index()
		if !ok {
			return "", fmt.Errorf("sqlite policy JSON: invalid path segment")
		}
		segments = append(segments, [2]any{"i", index})
	}
	encoded, err := json.Marshal(segments)
	return string(encoded), err
}

var _ policysql.Dialect = PolicyDialect{}
