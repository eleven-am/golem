package postgresql

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	"github.com/eleven-am/golem/go/internal/physical"
)

type tokenKind uint8

const (
	tEOF tokenKind = iota
	tWord
	tString
	tNumber
	tOperator
	tLeft
	tRight
	tComma
	tLeftBracket
	tRightBracket
)

type token struct {
	kind tokenKind
	text string
}

type expressionParser struct {
	tokens   []token
	position int
	table    physical.PhysicalTable
}

func parseCatalogExpression(source string, table physical.PhysicalTable) (physical.Expression, error) {
	tokens, err := lexExpression(source)
	if err != nil {
		return physical.Expression{}, err
	}
	parser := expressionParser{tokens: tokens, table: table}
	result, err := parser.parseOr()
	if err != nil {
		return physical.Expression{}, err
	}
	if parser.peek().kind != tEOF {
		return physical.Expression{}, fmt.Errorf("unexpected token %q", parser.peek().text)
	}
	return result, nil
}

func (p *expressionParser) peek() token {
	if p.position >= len(p.tokens) {
		return token{kind: tEOF}
	}
	return p.tokens[p.position]
}
func (p *expressionParser) take() token {
	value := p.peek()
	if value.kind != tEOF {
		p.position++
	}
	return value
}
func (p *expressionParser) word(value string) bool {
	return p.peek().kind == tWord && strings.EqualFold(p.peek().text, value)
}

func (p *expressionParser) parseOr() (physical.Expression, error) {
	return p.parseLogical("OR", "golem.schema.predicate.or.v1", p.parseAnd)
}
func (p *expressionParser) parseAnd() (physical.Expression, error) {
	return p.parseLogical("AND", "golem.schema.predicate.and.v1", p.parseNot)
}
func (p *expressionParser) parseLogical(word, identity string, next func() (physical.Expression, error)) (physical.Expression, error) {
	left, err := next()
	if err != nil {
		return physical.Expression{}, err
	}
	values := []physical.Expression{left}
	for p.word(word) {
		p.take()
		value, parseErr := next()
		if parseErr != nil {
			return physical.Expression{}, parseErr
		}
		values = append(values, value)
	}
	if len(values) == 1 {
		return left, nil
	}
	return parsedPredicate(identity, values...), nil
}
func (p *expressionParser) parseNot() (physical.Expression, error) {
	if p.word("NOT") {
		p.take()
		value, err := p.parseNot()
		if err != nil {
			return physical.Expression{}, err
		}
		return parsedPredicate("golem.schema.predicate.not.v1", value), nil
	}
	return p.parseComparison()
}
func (p *expressionParser) parseComparison() (physical.Expression, error) {
	left, err := p.parseAdd()
	if err != nil {
		return physical.Expression{}, err
	}
	if p.word("IS") {
		p.take()
		identity := schemaexpr.IsNull
		if p.word("NOT") {
			p.take()
			identity = schemaexpr.IsNotNull
		}
		if !p.word("NULL") {
			return physical.Expression{}, fmt.Errorf("IS requires NULL")
		}
		p.take()
		return parsedPredicate(identity, left), nil
	}
	if p.word("IN") {
		p.take()
		if p.take().kind != tLeft {
			return physical.Expression{}, fmt.Errorf("IN requires parenthesized values")
		}
		values := []physical.Expression{left}
		for {
			value, parseErr := p.parseOr()
			if parseErr != nil {
				return physical.Expression{}, parseErr
			}
			values = append(values, value)
			if p.peek().kind == tRight {
				p.take()
				break
			}
			if p.take().kind != tComma {
				return physical.Expression{}, fmt.Errorf("invalid IN list")
			}
		}
		for index := 1; index < len(values); index++ {
			values[index] = coerceLiteral(values[index], left.Type)
		}
		return parsedPredicate(schemaexpr.In, values...), nil
	}
	if p.peek().kind != tOperator {
		return left, nil
	}
	identity, ok := map[string]string{"=": schemaexpr.Equal, "<>": schemaexpr.NotEqual, "!=": schemaexpr.NotEqual, "<": schemaexpr.Less, "<=": schemaexpr.LessEqual, ">": schemaexpr.Greater, ">=": schemaexpr.GreaterEq}[p.peek().text]
	if !ok {
		return left, nil
	}
	p.take()
	right, err := p.parseAdd()
	if err != nil {
		return physical.Expression{}, err
	}
	if identity == schemaexpr.Equal && right.Symbol != nil && right.Symbol.Identity == "golem.postgresql.parser.any-array.v1" {
		for index := range right.Operands {
			right.Operands[index] = coerceLiteral(right.Operands[index], left.Type)
		}
		return parsedPredicate(schemaexpr.In, append([]physical.Expression{left}, right.Operands...)...), nil
	}
	right = coerceLiteral(right, left.Type)
	return parsedPredicate(identity, left, right), nil
}
func (p *expressionParser) parseAdd() (physical.Expression, error) {
	return p.parseBinary(p.parseMultiply, map[string]string{"+": schemaexpr.Add, "-": schemaexpr.Subtract, "||": schemaexpr.Concat})
}
func (p *expressionParser) parseMultiply() (physical.Expression, error) {
	return p.parseBinary(p.parsePostfix, map[string]string{"*": schemaexpr.Multiply, "/": schemaexpr.Divide, "%": schemaexpr.Remainder})
}
func (p *expressionParser) parseBinary(next func() (physical.Expression, error), identities map[string]string) (physical.Expression, error) {
	left, err := next()
	if err != nil {
		return physical.Expression{}, err
	}
	for {
		identity, ok := identities[p.peek().text]
		if p.peek().kind != tOperator || !ok {
			return left, nil
		}
		p.take()
		right, parseErr := next()
		if parseErr != nil {
			return physical.Expression{}, parseErr
		}
		left = parsedValueOperator(identity, left, right)
	}
}
func (p *expressionParser) parsePostfix() (physical.Expression, error) {
	value, err := p.parsePrimary()
	if err != nil {
		return physical.Expression{}, err
	}
	for p.peek().kind == tOperator && p.peek().text == "::" {
		p.take()
		typeToken := p.take()
		if typeToken.kind != tWord {
			return physical.Expression{}, fmt.Errorf("cast lacks target type")
		}
		target, parseErr := parseCastStorage(typeToken.text)
		if parseErr != nil {
			return physical.Expression{}, parseErr
		}
		if value.Kind == physical.ExpressionLiteral && value.Literal != nil {
			literal := *value.Literal
			literal.Kind = literalKind(target)
			value.Literal, value.Type = &literal, target
			continue
		}
		if value.Type == target {
			continue
		}
		identity := castIdentity(value.Type, target)
		if identity == "" {
			return physical.Expression{}, fmt.Errorf("unsupported catalog cast %s", typeToken.text)
		}
		value = physical.Expression{Kind: physical.ExpressionCast, Type: target, Nullable: value.Nullable, Symbol: semanticSymbol(identity, ir.SchemaSymbolCast), Operands: []physical.Expression{value}}
	}
	return value, nil
}
func (p *expressionParser) parsePrimary() (physical.Expression, error) {
	current := p.take()
	switch current.kind {
	case tLeft:
		value, err := p.parseOr()
		if err != nil {
			return physical.Expression{}, err
		}
		if p.take().kind != tRight {
			return physical.Expression{}, fmt.Errorf("missing closing parenthesis")
		}
		return value, nil
	case tString:
		literal := ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: current.text}
		return physical.Expression{Kind: physical.ExpressionLiteral, Type: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Literal: &literal}, nil
	case tNumber:
		kind, storage := ir.LiteralInteger, physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
		if strings.ContainsAny(current.text, ".eE") {
			kind, storage = ir.LiteralDecimal, physical.StorageType{Kind: physical.StoragePostgreSQLNumeric, Precision: 18, Scale: uint16(decimalScale(current.text))}
		}
		literal := ir.TypedLiteralIR{Kind: kind, Canonical: current.text}
		return physical.Expression{Kind: physical.ExpressionLiteral, Type: storage, Literal: &literal}, nil
	case tWord:
		if strings.EqualFold(current.text, "TRUE") || strings.EqualFold(current.text, "FALSE") {
			literal := ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: strings.ToLower(current.text)}
			return physical.Expression{Kind: physical.ExpressionLiteral, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}, Literal: &literal}, nil
		}
		if strings.EqualFold(current.text, "ARRAY") && p.peek().kind == tLeftBracket {
			return p.parseArray()
		}
		if p.peek().kind == tLeft {
			return p.parseFunction(current.text)
		}
		for _, column := range p.table.Columns {
			if string(column.Name) == current.text {
				id := column.ID
				return physical.Expression{Kind: physical.ExpressionColumn, Type: column.Storage, Nullable: column.Nullable, Column: &id}, nil
			}
		}
		return physical.Expression{}, fmt.Errorf("unknown catalog identifier %q", current.text)
	default:
		return physical.Expression{}, fmt.Errorf("unexpected token %q", current.text)
	}
}
func (p *expressionParser) parseFunction(name string) (physical.Expression, error) {
	p.take()
	var values []physical.Expression
	if p.peek().kind != tRight {
		for {
			value, err := p.parseOr()
			if err != nil {
				return physical.Expression{}, err
			}
			values = append(values, value)
			if p.peek().kind == tRight {
				break
			}
			if p.take().kind != tComma {
				return physical.Expression{}, fmt.Errorf("invalid function arguments")
			}
		}
	}
	p.take()
	lower := strings.ToLower(name)
	if lower == "any" {
		if len(values) != 1 || values[0].Symbol == nil || values[0].Symbol.Identity != "golem.postgresql.parser.array.v1" {
			return physical.Expression{}, fmt.Errorf("ANY requires one ARRAY operand")
		}
		return physical.Expression{Kind: physical.ExpressionFunction, Type: values[0].Type, Symbol: postgresSymbol("golem.postgresql.parser.any-array.v1", ir.SchemaSymbolFunction), Operands: values[0].Operands}, nil
	}
	identity := map[string]string{"lower": schemaexpr.Lower, "upper": schemaexpr.Upper, "char_length": schemaexpr.Length, "length": schemaexpr.Length, "coalesce": schemaexpr.Coalesce, "octet_length": "golem.postgresql.function.octet-length.v1", "jsonb_typeof": "golem.postgresql.function.jsonb-typeof.v1"}[lower]
	if identity == "" {
		return physical.Expression{}, fmt.Errorf("unsupported catalog function %q", name)
	}
	if len(values) == 0 {
		return physical.Expression{}, fmt.Errorf("function %s lacks operands", name)
	}
	storage := values[0].Type
	if lower == "char_length" || lower == "length" || lower == "octet_length" {
		storage = physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	}
	if lower == "jsonb_typeof" {
		storage = physical.StorageType{Kind: physical.StoragePostgreSQLText}
	}
	return physical.Expression{Kind: physical.ExpressionFunction, Type: storage, Nullable: anyNullable(values), Symbol: semanticSymbol(identity, ir.SchemaSymbolFunction), Operands: values}, nil
}

func (p *expressionParser) parseArray() (physical.Expression, error) {
	p.take()
	var values []physical.Expression
	if p.peek().kind != tRightBracket {
		for {
			value, err := p.parseOr()
			if err != nil {
				return physical.Expression{}, err
			}
			values = append(values, value)
			if p.peek().kind == tRightBracket {
				break
			}
			if p.take().kind != tComma {
				return physical.Expression{}, fmt.Errorf("invalid ARRAY values")
			}
		}
	}
	p.take()
	if len(values) == 0 {
		return physical.Expression{}, fmt.Errorf("empty ARRAY is not accepted in managed expressions")
	}
	return physical.Expression{Kind: physical.ExpressionFunction, Type: values[0].Type, Symbol: postgresSymbol("golem.postgresql.parser.array.v1", ir.SchemaSymbolFunction), Operands: values}, nil
}

func parsedPredicate(identity string, values ...physical.Expression) physical.Expression {
	return physical.Expression{Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}, Nullable: anyNullable(values), Symbol: semanticSymbol(identity, ir.SchemaSymbolOperator), Operands: values}
}
func parsedValueOperator(identity string, left, right physical.Expression) physical.Expression {
	right = coerceLiteral(right, left.Type)
	if left.Symbol != nil && left.Symbol.Identity == identity {
		left.Operands = append(left.Operands, right)
		left.Nullable = left.Nullable || right.Nullable
		return left
	}
	return physical.Expression{Kind: physical.ExpressionOperator, Type: left.Type, Nullable: left.Nullable || right.Nullable, Symbol: semanticSymbol(identity, ir.SchemaSymbolOperator), Operands: []physical.Expression{left, right}}
}
func coerceLiteral(value physical.Expression, target physical.StorageType) physical.Expression {
	if value.Kind == physical.ExpressionLiteral && value.Literal != nil {
		literal := *value.Literal
		literal.Kind = literalKind(target)
		value.Literal = &literal
		value.Type = target
	}
	return value
}
func parseCastStorage(value string) (physical.StorageType, error) {
	switch strings.ToLower(value) {
	case "text":
		return physical.StorageType{Kind: physical.StoragePostgreSQLText}, nil
	case "uuid":
		return physical.StorageType{Kind: physical.StoragePostgreSQLUUID}, nil
	case "date":
		return physical.StorageType{Kind: physical.StoragePostgreSQLDate}, nil
	case "time":
		return physical.StorageType{Kind: physical.StoragePostgreSQLTime, Length: 6}, nil
	case "timestamptz":
		return physical.StorageType{Kind: physical.StoragePostgreSQLTimestampTZ, Length: 6}, nil
	case "jsonb":
		return physical.StorageType{Kind: physical.StoragePostgreSQLJSONB}, nil
	case "smallint":
		return physical.StorageType{Kind: physical.StoragePostgreSQLSmallInt}, nil
	case "integer":
		return physical.StorageType{Kind: physical.StoragePostgreSQLInteger}, nil
	case "bigint":
		return physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}, nil
	}
	return physical.StorageType{}, fmt.Errorf("unsupported cast type %q", value)
}
func castIdentity(from, to physical.StorageType) string {
	if from.Kind == physical.StoragePostgreSQLSmallInt && to.Kind == physical.StoragePostgreSQLInteger {
		return schemaexpr.CastInt16ToInt32
	}
	if from.Kind == physical.StoragePostgreSQLSmallInt && to.Kind == physical.StoragePostgreSQLBigInt {
		return schemaexpr.CastInt16ToInt64
	}
	if from.Kind == physical.StoragePostgreSQLInteger && to.Kind == physical.StoragePostgreSQLBigInt {
		return schemaexpr.CastInt32ToInt64
	}
	if from.Kind == physical.StoragePostgreSQLBigInt && to.Kind == physical.StoragePostgreSQLText {
		return schemaexpr.CastInt64ToString
	}
	return ""
}
func decimalScale(value string) int {
	value = strings.ToLower(value)
	dot := strings.IndexByte(value, '.')
	if dot < 0 {
		return 0
	}
	end := len(value)
	if exponent := strings.IndexByte(value, 'e'); exponent >= 0 {
		end = exponent
	}
	return end - dot - 1
}

func lexExpression(source string) ([]token, error) {
	var result []token
	for index := 0; index < len(source); {
		if unicode.IsSpace(rune(source[index])) {
			index++
			continue
		}
		switch source[index] {
		case '(':
			result = append(result, token{kind: tLeft, text: "("})
			index++
		case ')':
			result = append(result, token{kind: tRight, text: ")"})
			index++
		case ',':
			result = append(result, token{kind: tComma, text: ","})
			index++
		case '[':
			result = append(result, token{kind: tLeftBracket, text: "["})
			index++
		case ']':
			result = append(result, token{kind: tRightBracket, text: "]"})
			index++
		case '\'':
			value, rest, err := readSQLString(source[index:])
			if err != nil {
				return nil, err
			}
			consumed := len(source[index:]) - len(rest)
			result = append(result, token{kind: tString, text: value})
			index += consumed
		case '"':
			value, consumed, err := readQuotedIdentifier(source[index:])
			if err != nil {
				return nil, err
			}
			result = append(result, token{kind: tWord, text: value})
			index += consumed
		default:
			if strings.ContainsRune("=<>!:+-*/%|", rune(source[index])) && !(source[index] == '-' && index+1 < len(source) && source[index+1] >= '0' && source[index+1] <= '9') {
				start := index
				index++
				if index < len(source) && (source[start:index+1] == "<=" || source[start:index+1] == ">=" || source[start:index+1] == "<>" || source[start:index+1] == "!=" || source[start:index+1] == "::" || source[start:index+1] == "||") {
					index++
				}
				result = append(result, token{kind: tOperator, text: source[start:index]})
				continue
			}
			if source[index] >= '0' && source[index] <= '9' || source[index] == '-' && index+1 < len(source) && source[index+1] >= '0' && source[index+1] <= '9' {
				start := index
				index++
				for index < len(source) && (source[index] >= '0' && source[index] <= '9' || strings.ContainsRune(".eE+-", rune(source[index]))) {
					index++
				}
				result = append(result, token{kind: tNumber, text: source[start:index]})
				continue
			}
			if unicode.IsLetter(rune(source[index])) || source[index] == '_' {
				start := index
				index++
				for index < len(source) && (unicode.IsLetter(rune(source[index])) || unicode.IsDigit(rune(source[index])) || source[index] == '_' || source[index] == '.') {
					index++
				}
				result = append(result, token{kind: tWord, text: source[start:index]})
				continue
			}
			return nil, fmt.Errorf("unsupported expression character %q", source[index])
		}
	}
	return append(result, token{kind: tEOF}), nil
}
func readQuotedIdentifier(value string) (string, int, error) {
	var result strings.Builder
	for index := 1; index < len(value); index++ {
		if value[index] == '"' {
			if index+1 < len(value) && value[index+1] == '"' {
				result.WriteByte('"')
				index++
				continue
			}
			return result.String(), index + 1, nil
		}
		result.WriteByte(value[index])
	}
	return "", 0, fmt.Errorf("unterminated quoted identifier")
}

func stripOuter(value string) string {
	value = strings.TrimSpace(value)
	for len(value) >= 2 && value[0] == '(' && value[len(value)-1] == ')' && outerParentheses(value) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func outerParentheses(value string) bool {
	depth, quoted := 0, false
	for index := 0; index < len(value); index++ {
		if value[index] == '\'' {
			if quoted && index+1 < len(value) && value[index+1] == '\'' {
				index++
				continue
			}
			quoted = !quoted
		}
		if quoted {
			continue
		}
		if value[index] == '(' {
			depth++
		}
		if value[index] == ')' {
			depth--
			if depth == 0 && index != len(value)-1 {
				return false
			}
		}
	}
	return depth == 0
}
