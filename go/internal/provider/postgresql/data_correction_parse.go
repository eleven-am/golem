package postgresql

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

// Correction SQL deliberately has a smaller language than reviewed backfills.
// Only bound stored-column references, scalar literals, CASE, and built-in
// scalar operators survive parsing. Every accepted token is rendered anew;
// functions, casts, subqueries and additional assignments have no AST node.
type correctionToken struct {
	kind   tokenKind
	text   string
	quoted bool
}
type correctionParser struct {
	tokens          []correctionToken
	position, nodes int
	table           physical.PhysicalTable
}

func renderPostgreSQLCorrection(content []byte, namespace physical.PhysicalName, table physical.PhysicalTable, target physical.PhysicalColumn) (string, error) {
	if err := migration.ValidateReviewedBackfillArtifact(content); err != nil {
		return "", err
	}
	tokens, err := lexPostgreSQLCorrection(string(content))
	if err != nil {
		return "", err
	}
	p := correctionParser{tokens: tokens, table: table}
	if !p.word("UPDATE") {
		return "", fmt.Errorf("data correction requires UPDATE")
	}
	schemaName, err := p.identifier()
	if err != nil {
		return "", err
	}
	if !p.consume(".") {
		return "", fmt.Errorf("data correction requires an exact schema-qualified table")
	}
	tableName, err := p.identifier()
	if err != nil {
		return "", err
	}
	if schemaName != string(namespace) || tableName != string(table.Name) {
		return "", fmt.Errorf("data correction targets a different table")
	}
	if !p.word("SET") {
		return "", fmt.Errorf("data correction requires SET")
	}
	field, err := p.identifier()
	if err != nil {
		return "", err
	}
	if field != string(target.Name) || !p.consume("=") {
		return "", fmt.Errorf("data correction must assign exactly its reviewed field")
	}
	value, err := p.expression(1, 0)
	if err != nil {
		return "", err
	}
	statement := `UPDATE ` + qualified(namespace, table.Name) + ` SET ` + quote(target.Name) + ` = ` + value
	if p.word("WHERE") {
		predicate, err := p.expression(1, 0)
		if err != nil {
			return "", err
		}
		statement += ` WHERE ` + predicate
	}
	p.consume(";")
	if p.peek().kind != tEOF {
		return "", fmt.Errorf("unsupported data correction syntax")
	}
	return statement, nil
}

func (p *correctionParser) peek() correctionToken { return p.tokens[p.position] }
func (p *correctionParser) consume(value string) bool {
	if p.peek().text != value || p.peek().quoted || p.peek().kind == tString {
		return false
	}
	p.position++
	return true
}
func (p *correctionParser) word(value string) bool {
	t := p.peek()
	if t.kind != tWord || t.quoted || !strings.EqualFold(t.text, value) {
		return false
	}
	p.position++
	return true
}
func (p *correctionParser) identifier() (string, error) {
	t := p.peek()
	if t.kind != tWord {
		return "", fmt.Errorf("data correction requires a physical identifier")
	}
	p.position++
	if t.quoted {
		return t.text, nil
	}
	return strings.ToLower(t.text), nil
}

func (p *correctionParser) expression(minimum, depth int) (string, error) {
	p.nodes++
	if depth > scopecontract.MaxDepth || p.nodes > scopecontract.MaxNodes {
		return "", fmt.Errorf("data correction expression exceeds bounds")
	}
	var left string
	var err error
	switch {
	case p.word("NOT"):
		left, err = p.expression(3, depth+1)
		left = "(NOT " + left + ")"
	case p.consume("+"):
		left, err = p.expression(7, depth+1)
		left = "(+" + left + ")"
	case p.consume("-"):
		left, err = p.expression(7, depth+1)
		left = "(-" + left + ")"
	case p.consume("("):
		left, err = p.expression(1, depth+1)
		if err == nil && !p.consume(")") {
			err = fmt.Errorf("data correction unclosed expression")
		}
		left = "(" + left + ")"
	case p.word("CASE"):
		left = "CASE"
		branches := 0
		for p.word("WHEN") {
			predicate, e := p.expression(1, depth+1)
			if e != nil {
				return "", e
			}
			if !p.word("THEN") {
				return "", fmt.Errorf("data correction CASE requires THEN")
			}
			value, e := p.expression(1, depth+1)
			if e != nil {
				return "", e
			}
			left += " WHEN " + predicate + " THEN " + value
			branches++
		}
		if branches == 0 {
			return "", fmt.Errorf("data correction CASE requires WHEN")
		}
		if p.word("ELSE") {
			value, e := p.expression(1, depth+1)
			if e != nil {
				return "", e
			}
			left += " ELSE " + value
		}
		if !p.word("END") {
			return "", fmt.Errorf("data correction CASE requires END")
		}
		left += " END"
	case p.word("NULL"):
		left = "NULL"
	case p.word("TRUE"):
		left = "TRUE"
	case p.word("FALSE"):
		left = "FALSE"
	default:
		t := p.peek()
		switch t.kind {
		case tString:
			p.position++
			left = correctionSQLString(t.text)
		case tNumber:
			p.position++
			left = t.text
		case tWord:
			name, e := p.identifier()
			if e != nil {
				return "", e
			}
			found := false
			for _, column := range p.table.Columns {
				if string(column.Name) == name && column.Generated == nil {
					left = quote(column.Name)
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("data correction expression references an unknown or generated column")
			}
		default:
			return "", fmt.Errorf("unsupported data correction scalar expression")
		}
	}
	if err != nil {
		return "", err
	}
	chainDepth := depth
	for {
		t := p.peek()
		operator := strings.ToUpper(t.text)
		precedence := 0
		if !t.quoted && (t.kind == tWord || t.kind == tOperator) {
			switch operator {
			case "OR":
				precedence = 1
			case "AND":
				precedence = 2
			case "=", "<>", "!=", "<", ">", "<=", ">=", "IS":
				precedence = 4
			case "+", "-":
				precedence = 5
			case "*", "/", "%":
				precedence = 6
			}
		}
		if precedence < minimum || precedence == 0 {
			break
		}
		chainDepth++
		if chainDepth > scopecontract.MaxDepth {
			return "", fmt.Errorf("data correction expression exceeds bounds")
		}
		p.position++
		if operator == "IS" {
			not := ""
			if p.word("NOT") {
				not = " NOT"
			}
			if !p.word("NULL") {
				return "", fmt.Errorf("data correction IS supports only NULL")
			}
			left = "(" + left + " IS" + not + " NULL)"
			continue
		}
		right, e := p.expression(precedence+1, depth+1)
		if e != nil {
			return "", e
		}
		if operator != "AND" && operator != "OR" {
			operator = "OPERATOR(pg_catalog." + operator + ")"
		}
		left = "(" + left + " " + operator + " " + right + ")"
	}
	return left, nil
}

func lexPostgreSQLCorrection(source string) ([]correctionToken, error) {
	var tokens []correctionToken
	for i := 0; i < len(source); {
		if len(tokens) > scopecontract.MaxNodes*4 {
			return nil, fmt.Errorf("data correction token budget exceeded")
		}
		if i+1 < len(source) {
			pair := source[i : i+2]
			if pair == "--" || pair == "/*" || pair == "*/" {
				return nil, fmt.Errorf("data correction comments are unsupported")
			}
		}
		c := source[i]
		if c == ' ' || c == '\t' || c == '\n' {
			i++
			continue
		}
		switch c {
		case '\'':
			value, rest, err := readSQLString(source[i:])
			if err != nil {
				return nil, fmt.Errorf("data correction invalid string literal")
			}
			consumed := len(source[i:]) - len(rest)
			tokens = append(tokens, correctionToken{kind: tString, text: value})
			i += consumed
		case '"':
			value, consumed, err := readQuotedIdentifier(source[i:])
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, correctionToken{kind: tWord, text: value, quoted: true})
			i += consumed
		default:
			if c >= '0' && c <= '9' {
				start := i
				for i < len(source) && source[i] >= '0' && source[i] <= '9' {
					i++
				}
				if i < len(source) && source[i] == '.' {
					i++
					for i < len(source) && source[i] >= '0' && source[i] <= '9' {
						i++
					}
				}
				if i < len(source) && (source[i] == 'e' || source[i] == 'E') {
					i++
					if i < len(source) && (source[i] == '+' || source[i] == '-') {
						i++
					}
					digits := i
					for i < len(source) && source[i] >= '0' && source[i] <= '9' {
						i++
					}
					if digits == i {
						return nil, fmt.Errorf("data correction malformed number")
					}
				}
				tokens = append(tokens, correctionToken{kind: tNumber, text: source[start:i]})
				continue
			}
			if c == '_' || c < 128 && unicode.IsLetter(rune(c)) {
				start := i
				i++
				for i < len(source) && (source[i] == '_' || source[i] < 128 && (unicode.IsLetter(rune(source[i])) || unicode.IsDigit(rune(source[i])))) {
					i++
				}
				tokens = append(tokens, correctionToken{kind: tWord, text: source[start:i]})
				continue
			}
			if strings.ContainsRune("().;=<>!+-*/%", rune(c)) {
				value := string(c)
				i++
				if i < len(source) && (value == "<" || value == ">" || value == "!") && (source[i] == '=' || value == "<" && source[i] == '>') {
					value += string(source[i])
					i++
				}
				kind := tOperator
				if value == "(" {
					kind = tLeft
				}
				if value == ")" {
					kind = tRight
				}
				tokens = append(tokens, correctionToken{kind: kind, text: value})
				continue
			}
			return nil, fmt.Errorf("unsupported data correction token")
		}
	}
	return append(tokens, correctionToken{kind: tEOF}), nil
}

func correctionSQLString(value string) string {
	return "E" + quoteLiteral(strings.ReplaceAll(value, `\`, `\\`))
}
