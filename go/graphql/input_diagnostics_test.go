package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

func TestEnumInputDiagnosticsThroughServerValidation(t *testing.T) {
	const sdl = `
enum AddressRole { AddressRoleFrom AddressRoleTo }
input RoleFilter { equals: AddressRole in: [AddressRole!] }
input AddressWhereInput { role: RoleFilter }
input AddressRelationFilter { some: AddressWhereInput }
input MessageWhereInput { addresses: AddressRelationFilter AND: [MessageWhereInput!] }
type Message { id: ID! }
type Query { messages(where: MessageWhereInput, take: Int = 10): [Message!]! }
`
	const marker = "private_caller_value"
	tests := []struct {
		name, query, argument, expectedType string
		variables                           map[string]any
	}{
		{"literal", `{ inbox: messages(where: {addresses: {some: {role: {equals: From}}}}) { id } }`, "where.addresses.some.role.equals", "AddressRole", nil},
		{"literal-marker", `{ inbox: messages(where: {addresses: {some: {role: {equals: ` + marker + `}}}}) { id } }`, "where.addresses.some.role.equals", "AddressRole", nil},
		{"quoted-enum", `{ inbox: messages(where: {addresses: {some: {role: {equals: "AddressRoleFrom"}}}}) { id } }`, "where.addresses.some.role.equals", "AddressRole", nil},
		{"list-literal", `{ inbox: messages(where: {addresses: {some: {role: {in: [AddressRoleFrom, ` + marker + `]}}}}) { id } }`, "where.addresses.some.role.in[1]", "AddressRole!", nil},
		{"leaf-variable", `query($role: AddressRole!) { inbox: messages(where: {addresses: {some: {role: {equals: $role}}}}) { id } }`, "where.addresses.some.role.equals", "AddressRole", map[string]any{"role": marker}},
		{"object-variable", `query($where: MessageWhereInput) { inbox: messages(where: $where) { id } }`, "where.AND[0].addresses.some.role.equals", "AddressRole", map[string]any{"where": map[string]any{"AND": []any{map[string]any{"addresses": map[string]any{"some": map[string]any{"role": map[string]any{"equals": marker}}}}}}}},
		{"fragment", `query { ...Inbox } fragment Inbox on Query { inbox: messages(where: {addresses: {some: {role: {equals: ` + marker + `}}}}) { id } }`, "where.addresses.some.role.equals", "AddressRole", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &testExecutor{}
			server, err := NewServer(sdl, Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(_ context.Context, err error) { t.Error(err) }}, executor)
			if err != nil {
				t.Fatal(err)
			}
			response := server.Execute(context.Background(), 1, Request{Query: test.query, Variables: test.variables})
			if executor.called || response.Data != nil || len(response.Errors) != 1 {
				t.Fatalf("invalid input reached executor or produced an unexpected response: %#v", response)
			}
			failure := response.Errors[0]
			want := map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": test.argument, "expectedType": test.expectedType, "allowedValues": []string{"AddressRoleFrom", "AddressRoleTo"}}
			if !reflect.DeepEqual(failure.Extensions, want) || !reflect.DeepEqual(failure.Path, []any{"inbox"}) {
				t.Fatalf("diagnostic=%#v; want extensions=%#v", failure, want)
			}
			for _, text := range []string{test.argument, test.expectedType, "AddressRoleFrom", "AddressRoleTo"} {
				if !strings.Contains(failure.Message, text) {
					t.Fatalf("message %q omits %q", failure.Message, text)
				}
			}
			encoded, err := json.Marshal(response)
			if err != nil || strings.Contains(string(encoded), marker) {
				t.Fatalf("response echoes caller value: %s / %v", encoded, err)
			}
		})
	}
}

func TestEnumInputDiagnosticDoesNotRejectValidInputs(t *testing.T) {
	executor := &testExecutor{}
	server, err := NewServer(`enum AddressRole { AddressRoleFrom AddressRoleTo } type Query { viewer(role: AddressRole): Int! }`, Config[int]{PrincipalFromContext: func(context.Context) (int, bool) { return 1, true }, ReportInternalError: func(_ context.Context, err error) { t.Error(err) }}, executor)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []Request{
		{Query: `{ viewer(role: AddressRoleFrom) }`},
		{Query: `query($role: AddressRole) { viewer(role: $role) }`, Variables: map[string]any{"role": "AddressRoleTo"}},
	} {
		executor.called = false
		response := server.Execute(context.Background(), 1, request)
		if !executor.called || len(response.Errors) != 0 {
			t.Fatalf("valid enum refused: %#v", response)
		}
	}
}

func TestEnumDiagnosticTraversalChargesEmptyFieldsAndStopsAtBudget(t *testing.T) {
	var sdl strings.Builder
	sdl.WriteString("input Wide { ")
	for index := 0; index < 200; index++ {
		fmt.Fprintf(&sdl, "f%d: String ", index)
	}
	sdl.WriteString("} type Query { viewer(values: [Wide!]): Int! }")
	executor := &testExecutor{}
	server, err := NewServer(sdl.String(), Config[int]{
		PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
		ReportInternalError:  func(_ context.Context, err error) { t.Error(err) },
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, 512)
	for index := range values {
		values[index] = map[string]any{}
	}
	typ := &ast.Type{Elem: &ast.Type{NamedType: "Wide", NonNull: true}}
	for _, raw := range []any{map[string]any{}, values} {
		walker := enumDiagnosticWalker{schema: server.schema, remaining: 8}
		expected := typ
		if _, object := raw.(map[string]any); object {
			expected = typ.Elem
		}
		if failure := walker.variable(raw, expected, "values", 0); failure != nil || walker.remaining != 0 {
			t.Fatalf("empty fields/list did not consume and stop at budget: failure=%v remaining=%d", failure, walker.remaining)
		}
		walker.variable(raw, expected, "values", 0)
		if walker.remaining != 0 {
			t.Fatalf("exhausted walker continued charging: %d", walker.remaining)
		}
	}
	response := server.Execute(context.Background(), 1, Request{
		Query:     `query($values: [Wide!]) { viewer(values: $values) unavailable }`,
		Variables: map[string]any{"values": values},
	})
	if executor.called || len(response.Errors) == 0 {
		t.Fatalf("invalid document reached execution: %#v", response)
	}
	for _, failure := range response.Errors {
		if failure.Extensions["code"] != "GRAPHQL_VALIDATION_FAILED" {
			t.Fatalf("fallback changed: %#v", failure)
		}
	}
}

func TestEnumDiagnosticsDoNotTraverseImpossibleFragments(t *testing.T) {
	const sdl = `enum SecretEnum { PRIVATE_ENUM_MEMBER } type Private { run(mode: SecretEnum): Int! } type Query { viewer: Int! }`
	for _, query := range []string{
		`{ ... on Private { run(mode: BAD) } }`,
		`query { ...Impossible } fragment Impossible on Private { run(mode: BAD) }`,
	} {
		executor := &testExecutor{}
		server, err := NewServer(sdl, Config[int]{
			PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
			Introspection:        false,
			ReportInternalError:  func(_ context.Context, err error) { t.Error(err) },
		}, executor)
		if err != nil {
			t.Fatal(err)
		}
		response := server.Execute(context.Background(), 1, Request{Query: query})
		if executor.called || len(response.Errors) == 0 {
			t.Fatalf("impossible fragment reached execution: %#v", response)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		for _, hidden := range []string{"PRIVATE_ENUM_MEMBER", "SecretEnum", "allowedValues"} {
			if strings.Contains(string(encoded), hidden) {
				t.Fatalf("impossible fragment disclosed %s: %s", hidden, encoded)
			}
		}
		for _, failure := range response.Errors {
			if failure.Extensions["code"] != "GRAPHQL_VALIDATION_FAILED" {
				t.Fatalf("fallback changed: %#v", failure)
			}
		}
	}
}
