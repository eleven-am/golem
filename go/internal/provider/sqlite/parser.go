package sqlite

import (
	"fmt"
	"strings"
	"unicode"
)

type ddlNode struct {
	Kind     string
	Value    string
	Children []ddlNode
}

type ddlParser struct {
	tokens []ddlNode
	index  int
}

// parseDDL is a lexer/parser for the closed CREATE TABLE/INDEX grammar emitted
// by this provider. It builds nested semantic clause groups; it never compares
// raw sqlite_schema text or uses regular expressions.
func parseDDL(input string) (ddlNode, error) {
	tokens, err := lexDDL(input)
	if err != nil {
		return ddlNode{}, err
	}
	parser := ddlParser{tokens: tokens}
	return parser.parseCreate()
}

func lexDDL(input string) ([]ddlNode, error) {
	var result []ddlNode
	for index := 0; index < len(input); {
		r := rune(input[index])
		if unicode.IsSpace(r) {
			index++
			continue
		}
		switch input[index] {
		case '"':
			index++
			var value strings.Builder
			terminated := false
			for index < len(input) {
				if input[index] == '"' {
					if index+1 < len(input) && input[index+1] == '"' {
						value.WriteByte('"')
						index += 2
						continue
					}
					index++
					terminated = true
					break
				}
				value.WriteByte(input[index])
				index++
			}
			if !terminated {
				return nil, fmt.Errorf("sqlite DDL lexer: unterminated quoted identifier")
			}
			result = append(result, ddlNode{Kind: "identifier", Value: value.String()})
		case '\'':
			index++
			var value strings.Builder
			terminated := false
			for index < len(input) {
				if input[index] == '\'' {
					if index+1 < len(input) && input[index+1] == '\'' {
						value.WriteByte('\'')
						index += 2
						continue
					}
					index++
					terminated = true
					break
				}
				value.WriteByte(input[index])
				index++
			}
			if !terminated {
				return nil, fmt.Errorf("sqlite DDL lexer: unterminated string literal")
			}
			result = append(result, ddlNode{Kind: "literal", Value: value.String()})
		default:
			if strings.ContainsRune("(),;", r) {
				result = append(result, ddlNode{Kind: "punctuation", Value: string(r)})
				index++
				continue
			}
			start := index
			for index < len(input) && !unicode.IsSpace(rune(input[index])) && !strings.ContainsRune("(),;\"'", rune(input[index])) {
				index++
			}
			value := input[start:index]
			kind := "token"
			if isWord(value) {
				value, kind = strings.ToLower(value), "word"
			}
			result = append(result, ddlNode{Kind: kind, Value: value})
		}
	}
	return result, nil
}

func isWord(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && r != '_' {
			return false
		}
	}
	return true
}

func (parser *ddlParser) parseCreate() (ddlNode, error) {
	if !parser.consumeWord("create") {
		return ddlNode{}, fmt.Errorf("sqlite DDL parser: expected CREATE")
	}
	root := ddlNode{Kind: "create"}
	if parser.consumeWord("unique") {
		root.Value = "unique"
	}
	if parser.consumeWord("table") {
		root.Kind = "table"
	} else if parser.consumeWord("index") {
		root.Kind = "index"
	} else {
		return ddlNode{}, fmt.Errorf("sqlite DDL parser: expected TABLE or INDEX")
	}
	name, err := parser.identifier()
	if err != nil {
		return ddlNode{}, err
	}
	root.Children = append(root.Children, ddlNode{Kind: "name", Value: name})
	if root.Kind == "index" {
		if !parser.consumeWord("on") {
			return ddlNode{}, fmt.Errorf("sqlite DDL parser: expected ON")
		}
		table, idErr := parser.identifier()
		if idErr != nil {
			return ddlNode{}, idErr
		}
		root.Children = append(root.Children, ddlNode{Kind: "on", Value: table})
	}
	group, groupErr := parser.group()
	if groupErr != nil {
		return ddlNode{}, groupErr
	}
	if root.Kind == "table" {
		group.Kind = "table-items"
		if err := validateTableItems(group); err != nil {
			return ddlNode{}, err
		}
	} else {
		group.Kind = "index-keys"
	}
	root.Children = append(root.Children, group)
	if root.Kind == "table" && parser.consumeWord("strict") {
		root.Value = strings.TrimSpace(root.Value + " strict")
	}
	if parser.consumeWord("where") {
		where := ddlNode{Kind: "where"}
		for parser.index < len(parser.tokens) {
			where.Children = append(where.Children, parser.nextTree())
		}
		root.Children = append(root.Children, where)
	}
	if parser.index < len(parser.tokens) && parser.tokens[parser.index].Value == ";" {
		parser.index++
	}
	if parser.index != len(parser.tokens) {
		return ddlNode{}, fmt.Errorf("sqlite DDL parser: trailing tokens")
	}
	return root, nil
}

func (parser *ddlParser) group() (ddlNode, error) {
	if parser.index >= len(parser.tokens) || parser.tokens[parser.index].Value != "(" {
		return ddlNode{}, fmt.Errorf("sqlite DDL parser: expected group")
	}
	parser.index++
	node := ddlNode{Kind: "group"}
	for parser.index < len(parser.tokens) && parser.tokens[parser.index].Value != ")" {
		node.Children = append(node.Children, parser.nextTree())
	}
	if parser.index >= len(parser.tokens) {
		return ddlNode{}, fmt.Errorf("sqlite DDL parser: unterminated group")
	}
	parser.index++
	return node, nil
}

func (parser *ddlParser) nextTree() ddlNode {
	if parser.tokens[parser.index].Value == "(" {
		group, _ := parser.group()
		return group
	}
	value := parser.tokens[parser.index]
	parser.index++
	return value
}

func validateTableItems(group ddlNode) error {
	start := true
	for _, token := range group.Children {
		if start {
			valid := token.Kind == "identifier" || token.Kind == "word" && (token.Value == "constraint" || token.Value == "primary" || token.Value == "unique" || token.Value == "foreign" || token.Value == "check")
			if !valid {
				return fmt.Errorf("sqlite DDL parser: invalid table item start %q", token.Value)
			}
			start = false
		}
		if token.Value == "," {
			start = true
		}
	}
	if start {
		return fmt.Errorf("sqlite DDL parser: empty table item")
	}
	return nil
}

func (parser *ddlParser) consumeWord(value string) bool {
	if parser.index < len(parser.tokens) && parser.tokens[parser.index].Kind == "word" && parser.tokens[parser.index].Value == value {
		parser.index++
		return true
	}
	return false
}

func (parser *ddlParser) identifier() (string, error) {
	if parser.index >= len(parser.tokens) {
		return "", fmt.Errorf("sqlite DDL parser: expected identifier")
	}
	token := parser.tokens[parser.index]
	if token.Kind != "identifier" && token.Kind != "word" {
		return "", fmt.Errorf("sqlite DDL parser: expected identifier")
	}
	parser.index++
	return token.Value, nil
}
