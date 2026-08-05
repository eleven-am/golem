// Package tags parses the deliberately small db and golem struct-tag languages.
// It performs lexical validation only; cross-field and logical-type validation
// belong to later compiler stages.
package tags

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	stableIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
)

// Error is a source-relative lexical error. Start and End are byte offsets in
// the decoded tag value, with End exclusive.
type Error struct {
	Code  string
	Start int
	End   int
	Text  string
}

func (e *Error) Error() string { return e.Text }

// Attribute is one decoded golem attribute in authored order.
type Attribute struct {
	Name       string
	Value      string
	HasValue   bool
	Start, End int
}

// DB is one decoded db tag.
type DB struct {
	Name    string
	Ignored bool
}

// Directive is the common table-level constraint/index grammar.
type Directive struct {
	Kind       string
	Name       string
	Components []string
}

// Allowed is a closed attribute registry for a declaration context.
type Allowed map[string]bool

// ParseDB accepts exactly an unquoted SQL identifier or "-". sqlx-style comma
// options are intentionally not part of the schema language.
func ParseDB(value string) (DB, *Error) {
	if value == "-" {
		return DB{Ignored: true}, nil
	}
	if value == "" {
		return DB{}, tagError("P1_DB_TAG_EMPTY", 0, 0, "db tag must name a column or use -")
	}
	if !IsIdentifier(value) {
		return DB{}, tagError("P1_DB_TAG_INVALID", 0, len(value), "db tag %q is not an unquoted identifier", value)
	}
	return DB{Name: value}, nil
}

// ParseGolem splits and validates a golem tag against a context-specific closed
// registry. It preserves authored order and never interprets SQL.
func ParseGolem(value string, allowed Allowed) ([]Attribute, []*Error) {
	if strings.TrimSpace(value) == "" {
		return nil, []*Error{tagError("P1_GOLEM_TAG_EMPTY", 0, len(value), "golem tag is empty")}
	}

	parts, splitErr := splitAttributes(value)
	if splitErr != nil {
		return nil, []*Error{splitErr}
	}

	attrs := make([]Attribute, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	var errs []*Error
	for _, part := range parts {
		start, end := trimBounds(value, part.start, part.end)
		if start == end {
			errs = append(errs, tagError("P1_GOLEM_TAG_EMPTY_TOKEN", part.start, part.end, "golem tag contains an empty attribute"))
			continue
		}

		eq := indexUnquoted(value[start:end], '=')
		nameEnd := end
		if eq >= 0 {
			nameEnd = start + eq
		}
		nameStart, nameEnd := trimBounds(value, start, nameEnd)
		name := value[nameStart:nameEnd]
		if !IsIdentifier(name) {
			errs = append(errs, tagError("P1_GOLEM_TAG_BAD_NAME", nameStart, nameEnd, "golem attribute name %q is invalid", name))
			continue
		}
		if _, ok := allowed[name]; !ok {
			errs = append(errs, tagError("P1_GOLEM_TAG_UNKNOWN", nameStart, nameEnd, "unknown golem attribute %q", name))
			continue
		}
		if _, ok := seen[name]; ok {
			errs = append(errs, tagError("P1_GOLEM_TAG_DUPLICATE", nameStart, nameEnd, "duplicate golem attribute %q", name))
			continue
		}
		seen[name] = struct{}{}

		attr := Attribute{Name: name, Start: start, End: end}
		if eq >= 0 {
			valueStart, valueEnd := trimBounds(value, start+eq+1, end)
			if valueStart == valueEnd {
				errs = append(errs, tagError("P1_GOLEM_TAG_MISSING_VALUE", start+eq, end, "golem attribute %q requires a value", name))
				continue
			}
			attr.HasValue = true
			attr.Value = value[valueStart:valueEnd]
		}
		attrs = append(attrs, attr)
	}
	return attrs, errs
}

// ParseDirective parses primary=name(a,b), unique=name(a,b), or
// index=name(a,b). Attribute context validation must happen first.
func ParseDirective(attr Attribute) (Directive, *Error) {
	if attr.Name != "primary" && attr.Name != "unique" && attr.Name != "index" {
		return Directive{}, tagError("P1_DIRECTIVE_KIND", attr.Start, attr.End, "%q is not a common table directive", attr.Name)
	}
	if !attr.HasValue {
		return Directive{}, tagError("P1_DIRECTIVE_VALUE", attr.Start, attr.End, "%s directive requires name(columns)", attr.Name)
	}

	open := strings.IndexByte(attr.Value, '(')
	close := strings.LastIndexByte(attr.Value, ')')
	if open <= 0 || close != len(attr.Value)-1 || strings.Contains(attr.Value[open+1:close], "(") || strings.Contains(attr.Value[open+1:close], ")") {
		return Directive{}, tagError("P1_DIRECTIVE_SYNTAX", attr.Start, attr.End, "%s directive must use name(column,...) syntax", attr.Name)
	}
	name := strings.TrimSpace(attr.Value[:open])
	if !IsIdentifier(name) {
		return Directive{}, tagError("P1_DIRECTIVE_NAME", attr.Start, attr.End, "directive name %q is not an identifier", name)
	}

	rawComponents := strings.Split(attr.Value[open+1:close], ",")
	components := make([]string, 0, len(rawComponents))
	seen := make(map[string]struct{}, len(rawComponents))
	for _, raw := range rawComponents {
		component := strings.TrimSpace(raw)
		if !IsIdentifier(component) {
			return Directive{}, tagError("P1_DIRECTIVE_COMPONENT", attr.Start, attr.End, "directive component %q is not an identifier", component)
		}
		if _, exists := seen[component]; exists {
			return Directive{}, tagError("P1_DIRECTIVE_DUPLICATE_COMPONENT", attr.Start, attr.End, "directive component %q is duplicated", component)
		}
		seen[component] = struct{}{}
		components = append(components, component)
	}
	if len(components) == 0 {
		return Directive{}, tagError("P1_DIRECTIVE_COMPONENT", attr.Start, attr.End, "directive requires at least one component")
	}
	return Directive{Kind: attr.Name, Name: name, Components: components}, nil
}

func IsIdentifier(value string) bool { return identifierPattern.MatchString(value) }

func IsStableID(value string) bool { return stableIDPattern.MatchString(value) }

type segment struct{ start, end int }

func splitAttributes(value string) ([]segment, *Error) {
	var result []segment
	start := 0
	var quote byte
	escaped := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ';' {
			result = append(result, segment{start: start, end: i})
			start = i + 1
		}
	}
	if quote != 0 || escaped {
		return nil, tagError("P1_GOLEM_TAG_UNTERMINATED_QUOTE", start, len(value), "unterminated quoted value in golem tag")
	}
	result = append(result, segment{start: start, end: len(value)})
	return result, nil
}

func indexUnquoted(value string, target byte) int {
	var quote byte
	escaped := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == target {
			return i
		}
	}
	return -1
}

func trimBounds(value string, start, end int) (int, int) {
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\r' || value[start] == '\n') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\r' || value[end-1] == '\n') {
		end--
	}
	return start, end
}

func tagError(code string, start, end int, format string, args ...any) *Error {
	return &Error{Code: code, Start: start, End: end, Text: fmt.Sprintf(format, args...)}
}
