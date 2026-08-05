package scalar

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type DefaultContext struct {
	EnumValues []string
	Span       ir.SourceSpan
}

func ResolveDefault(logicalType ir.LogicalTypeIR, token string, context DefaultContext) (*ir.DefaultIR, []ir.Diagnostic) {
	normalizedType, typeDiagnostics := NormalizeType(logicalType, context.Span)
	if hasErrors(typeDiagnostics) {
		return nil, typeDiagnostics
	}
	switch token {
	case "uuid":
		if normalizedType.Kind != ir.TypeUUID && normalizedType.Kind != ir.TypeString {
			return nil, append(typeDiagnostics, defaultError("P1_DEFAULT_UUID_TYPE", "uuid default is accepted only on UUID or String", context.Span))
		}
		return &ir.DefaultIR{Kind: ir.DefaultUUID, Producer: ir.ProducerApplication}, typeDiagnostics
	case "now":
		if normalizedType.Kind != ir.TypeDate && normalizedType.Kind != ir.TypeTime && normalizedType.Kind != ir.TypeDateTime {
			return nil, append(typeDiagnostics, defaultError("P1_DEFAULT_NOW_TYPE", "now default is accepted only on Date, Time, or DateTime", context.Span))
		}
		return &ir.DefaultIR{Kind: ir.DefaultNow, Producer: ir.ProducerApplication}, typeDiagnostics
	case "identity":
		if normalizedType.Kind != ir.TypeInt16 && normalizedType.Kind != ir.TypeInt32 && normalizedType.Kind != ir.TypeInt64 {
			return nil, append(typeDiagnostics, defaultError("P1_DEFAULT_IDENTITY_TYPE", "identity default requires a signed integer field", context.Span))
		}
		return &ir.DefaultIR{Kind: ir.DefaultIdentity, Producer: ir.ProducerDatabase}, typeDiagnostics
	}

	literal, diagnostic := parseLiteral(normalizedType, token, context)
	if diagnostic != nil {
		return nil, append(typeDiagnostics, *diagnostic)
	}
	return &ir.DefaultIR{
		Kind:     ir.DefaultLiteral,
		Producer: ir.ProducerDatabase,
		Literal:  &literal,
	}, typeDiagnostics
}

func ProviderDefault(symbol ir.ProviderSymbolRef, span ir.SourceSpan) (*ir.DefaultIR, []ir.Diagnostic) {
	if symbol.Provider != ir.SQLite && symbol.Provider != ir.PostgreSQL {
		return nil, []ir.Diagnostic{defaultError("P1_DEFAULT_PROVIDER_UNKNOWN", fmt.Sprintf("unknown provider %q", symbol.Provider), span)}
	}
	if symbol.Kind == "" || symbol.Name == "" || symbol.Version == 0 {
		return nil, []ir.Diagnostic{defaultError("P1_DEFAULT_PROVIDER_INVALID", "provider default requires registered kind, name, and version", span)}
	}
	return &ir.DefaultIR{Kind: ir.DefaultProvider, Producer: ir.ProducerProvider, Provider: &symbol}, nil
}

func ValidateUpdated(logicalType ir.LogicalTypeIR, nullable bool, _ *ir.DefaultIR, span ir.SourceSpan) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	if logicalType.Kind != ir.TypeDateTime || nullable {
		diagnostics = append(diagnostics, defaultError("P1_UPDATED_TYPE", "updated is accepted only on a non-null DateTime field", span))
	}
	return diagnostics
}

func parseLiteral(logicalType ir.LogicalTypeIR, token string, context DefaultContext) (ir.TypedLiteralIR, *ir.Diagnostic) {
	literal := ir.TypedLiteralIR{}
	fail := func(code, message string) (ir.TypedLiteralIR, *ir.Diagnostic) {
		diagnostic := defaultError(code, message, context.Span)
		return literal, &diagnostic
	}
	switch logicalType.Kind {
	case ir.TypeBool:
		if token != "true" && token != "false" {
			return fail("P1_DEFAULT_LITERAL", fmt.Sprintf("%q is not a Boolean literal", token))
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: token}, nil
	case ir.TypeInt16, ir.TypeInt32, ir.TypeInt64:
		bits := map[ir.LogicalTypeKind]int{ir.TypeInt16: 16, ir.TypeInt32: 32, ir.TypeInt64: 64}[logicalType.Kind]
		value, err := strconv.ParseInt(token, 10, bits)
		if err != nil {
			return fail("P1_DEFAULT_LITERAL", fmt.Sprintf("%q is not a valid %s literal", token, logicalType.Kind))
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: strconv.FormatInt(value, 10)}, nil
	case ir.TypeFloat32, ir.TypeFloat64:
		bits := 64
		if logicalType.Kind == ir.TypeFloat32 {
			bits = 32
		}
		value, err := strconv.ParseFloat(token, bits)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return fail("P1_DEFAULT_FLOAT_NONFINITE", fmt.Sprintf("%q is not a finite %s literal", token, logicalType.Kind))
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralFloat, Canonical: strconv.FormatFloat(value, 'g', -1, bits)}, nil
	case ir.TypeDecimal:
		canonical, err := canonicalDecimal(token, *logicalType.Precision, *logicalType.Scale)
		if err != nil {
			return fail("P1_DEFAULT_DECIMAL", err.Error())
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralDecimal, Canonical: canonical}, nil
	case ir.TypeString:
		value, err := strconv.Unquote(token)
		if err != nil || !utf8.ValidString(value) {
			return fail("P1_DEFAULT_STRING", "String default must be a valid UTF-8 Go-quoted string")
		}
		if logicalType.MaxLength != nil && uint32(utf8.RuneCountInString(value)) > *logicalType.MaxLength {
			return fail("P1_DEFAULT_LENGTH", fmt.Sprintf("String default exceeds maximum rune length %d", *logicalType.MaxLength))
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: value}, nil
	case ir.TypeBytes:
		value, err := strconv.Unquote(token)
		if err != nil {
			return fail("P1_DEFAULT_BYTES", "Bytes default must be a Go-quoted string")
		}
		bytes := []byte(value)
		if logicalType.MaxLength != nil && uint32(len(bytes)) > *logicalType.MaxLength {
			return fail("P1_DEFAULT_LENGTH", fmt.Sprintf("Bytes default exceeds maximum byte length %d", *logicalType.MaxLength))
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralBytes, Canonical: base64.RawStdEncoding.EncodeToString(bytes)}, nil
	case ir.TypeUUID:
		value, err := strconv.Unquote(token)
		if err != nil || !uuidPattern.MatchString(value) {
			return fail("P1_DEFAULT_UUID", "UUID literal must be a canonical lowercase quoted UUID")
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralUUID, Canonical: value}, nil
	case ir.TypeDate:
		value, err := strconv.Unquote(token)
		if err != nil {
			return fail("P1_DEFAULT_DATE", "Date default must be quoted")
		}
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			return fail("P1_DEFAULT_DATE", fmt.Sprintf("invalid Date literal %q", value))
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralDate, Canonical: parsed.Format("2006-01-02")}, nil
	case ir.TypeTime:
		value, err := strconv.Unquote(token)
		if err != nil {
			return fail("P1_DEFAULT_TIME", "Time default must be quoted")
		}
		canonical, err := canonicalClock(value, *logicalType.Precision)
		if err != nil {
			return fail("P1_DEFAULT_TIME", err.Error())
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralTime, Canonical: canonical}, nil
	case ir.TypeDateTime:
		value, err := strconv.Unquote(token)
		if err != nil {
			return fail("P1_DEFAULT_DATETIME", "DateTime default must be quoted")
		}
		canonical, err := canonicalDateTime(value, *logicalType.Precision)
		if err != nil {
			return fail("P1_DEFAULT_DATETIME", err.Error())
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralDateTime, Canonical: canonical}, nil
	case ir.TypeJSON:
		value, err := strconv.Unquote(token)
		if err != nil {
			return fail("P1_DEFAULT_JSON", "JSON default must be a Go-quoted JSON document")
		}
		canonical, err := CanonicalJSON([]byte(value))
		if err != nil {
			return fail("P1_DEFAULT_JSON", err.Error())
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralJSON, Canonical: string(canonical)}, nil
	case ir.TypeEnum:
		for _, value := range context.EnumValues {
			if token == value {
				return ir.TypedLiteralIR{Kind: ir.LiteralEnum, Canonical: token}, nil
			}
		}
		return fail("P1_DEFAULT_ENUM", fmt.Sprintf("%q is not a declared enum wire value", token))
	case ir.TypeScalarList:
		value, err := strconv.Unquote(token)
		if err != nil {
			return fail("P1_DEFAULT_LIST", "ScalarList default must be a Go-quoted JSON array")
		}
		parsed, err := parseJSON([]byte(value))
		if err != nil {
			return fail("P1_DEFAULT_LIST", err.Error())
		}
		if parsed.kind != jsonArray {
			return fail("P1_DEFAULT_LIST", "ScalarList default must be a JSON array")
		}
		if err := validateListElements(parsed.array, *logicalType.Element, context.EnumValues); err != nil {
			return fail("P1_DEFAULT_LIST", err.Error())
		}
		var builder bytes.Buffer
		writeJSONValue(&builder, parsed)
		return ir.TypedLiteralIR{Kind: ir.LiteralList, Canonical: builder.String()}, nil
	default:
		return fail("P1_DEFAULT_UNSUPPORTED", fmt.Sprintf("%s does not accept a common literal default", logicalType.Kind))
	}
}

func validateListElements(values []jsonValue, element ir.LogicalTypeIR, enumValues []string) error {
	for _, value := range values {
		if value.kind == jsonNull {
			return fmt.Errorf("nullable scalar-list elements are not accepted in v1")
		}
		var token string
		switch element.Kind {
		case ir.TypeBool:
			if value.kind == jsonBool {
				token = strconv.FormatBool(value.boolean)
			}
		case ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeFloat32, ir.TypeFloat64, ir.TypeDecimal:
			if value.kind == jsonNumber {
				token = value.text
			}
		case ir.TypeString, ir.TypeUUID, ir.TypeDate, ir.TypeTime, ir.TypeDateTime:
			if value.kind == jsonString {
				token = strconv.Quote(value.text)
			}
		case ir.TypeEnum:
			if value.kind == jsonString {
				token = value.text
			}
		}
		if token == "" {
			return fmt.Errorf("scalar-list element does not match %s", element.Kind)
		}
		if _, diagnostic := parseLiteral(element, token, DefaultContext{EnumValues: enumValues}); diagnostic != nil {
			return fmt.Errorf("scalar-list element does not match %s: %s", element.Kind, diagnostic.Message)
		}
	}
	return nil
}

func canonicalDecimal(input string, precision, scale uint16) (string, error) {
	negative := strings.HasPrefix(input, "-")
	if negative || strings.HasPrefix(input, "+") {
		input = input[1:]
	}
	parts := strings.Split(input, ".")
	if len(parts) > 2 || parts[0] == "" || !allDigits(parts[0]) {
		return "", fmt.Errorf("invalid Decimal literal")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || !allDigits(fraction) {
			return "", fmt.Errorf("invalid Decimal literal")
		}
	}
	if len(fraction) > int(scale) {
		return "", fmt.Errorf("Decimal literal has %d fractional digits but scale is %d", len(fraction), scale)
	}
	integer := strings.TrimLeft(parts[0], "0")
	if integer == "" {
		integer = "0"
	}
	integerDigits := len(integer)
	if integer == "0" {
		integerDigits = 0
	}
	if integerDigits+int(scale) > int(precision) {
		return "", fmt.Errorf("Decimal literal exceeds precision %d", precision)
	}
	fraction += strings.Repeat("0", int(scale)-len(fraction))
	isZero := integer == "0" && strings.Trim(fraction, "0") == ""
	prefix := ""
	if negative && !isZero {
		prefix = "-"
	}
	if scale == 0 {
		return prefix + integer, nil
	}
	return prefix + integer + "." + fraction, nil
}

func canonicalClock(value string, precision uint16) (string, error) {
	parsed, err := time.Parse("15:04:05.999999999", value)
	if err != nil {
		parsed, err = time.Parse("15:04:05", value)
	}
	if err != nil {
		return "", fmt.Errorf("invalid Time literal %q", value)
	}
	if fractionalDigits(value) > int(precision) {
		return "", fmt.Errorf("Time literal exceeds precision %d", precision)
	}
	return formatTime(parsed, precision), nil
}

func canonicalDateTime(value string, precision uint16) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("invalid DateTime literal %q", value)
	}
	if fractionalDigits(strings.SplitN(value, "Z", 2)[0]) > int(precision) {
		return "", fmt.Errorf("DateTime literal exceeds precision %d", precision)
	}
	parsed = parsed.UTC()
	base := parsed.Format("2006-01-02T15:04:05")
	if precision == 0 {
		return base + "Z", nil
	}
	fraction := fmt.Sprintf("%09d", parsed.Nanosecond())[:precision]
	return base + "." + fraction + "Z", nil
}

func formatTime(value time.Time, precision uint16) string {
	base := value.Format("15:04:05")
	if precision == 0 {
		return base
	}
	fraction := fmt.Sprintf("%09d", value.Nanosecond())[:precision]
	return base + "." + fraction
}

func fractionalDigits(value string) int {
	dot := strings.IndexByte(value, '.')
	if dot < 0 {
		return 0
	}
	end := len(value)
	for index := dot + 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			end = index
			break
		}
	}
	return end - dot - 1
}

func allDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func defaultError(code, message string, span ir.SourceSpan) ir.Diagnostic {
	return ir.NewError(code, message, span)
}

func hasErrors(diagnostics []ir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == ir.SeverityError {
			return true
		}
	}
	return false
}
