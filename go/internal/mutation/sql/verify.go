package sql

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// PersistedVerification is a provider-bound, final-graph authorization query.
// It is used by nested create execution after every graph write has completed.
type PersistedVerification struct {
	text string
	args []any
}

func (verification PersistedVerification) SQL() string { return verification.text }
func (verification PersistedVerification) Args() []any {
	return append([]any(nil), verification.args...)
}

func RenderPersistedVerification(node mutationir.Node, row mutationdecode.Row, conditions []policyir.Condition, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (PersistedVerification, error) {
	if registry == nil || row.ModelID() != node.ModelID() || len(conditions) == 0 {
		return PersistedVerification{}, fmt.Errorf("P4_MUTATION_SQL_VERIFY: node, persisted row, and conditions are required")
	}
	dialect, _, err := providerDialect(provider)
	if err != nil {
		return PersistedVerification{}, err
	}
	resolver := policysql.SchemaResolver(registry)
	if capabilities.Provider() != provider || capabilities.SchemaFingerprint() != resolver.SchemaFingerprint() {
		return PersistedVerification{}, fmt.Errorf("P4_MUTATION_SQL_VERIFY: capability proof does not match provider/schema")
	}
	model, ok := resolver.Model(provider, node.ModelID())
	if !ok {
		return PersistedVerification{}, fmt.Errorf("P4_MUTATION_SQL_VERIFY: physical model is absent")
	}
	context := renderContext{node: node, registry: registry, provider: provider, capabilities: capabilities, dialect: dialect, resolver: resolver, model: model, alias: "golem_mv0"}
	metadata, ok := registry.Model(golem.ModelID(node.ModelID()))
	if !ok || len(metadata.PrimaryKey()) == 0 {
		return PersistedVerification{}, fmt.Errorf("P4_MUTATION_SQL_VERIFY: primary identity is absent")
	}
	parts := make([]string, 0, len(metadata.PrimaryKey())+len(conditions))
	args := make([]any, 0, len(metadata.PrimaryKey()))
	for _, fieldID := range metadata.PrimaryKey() {
		logical := policyir.FieldID(fieldID)
		field, present := resolver.Field(provider, node.ModelID(), logical)
		cell, cellPresent := row.Cell(logical)
		if !present || !cellPresent || cell.IsNull() {
			return PersistedVerification{}, fmt.Errorf("P4_MUTATION_SQL_VERIFY: persisted primary identity is incomplete")
		}
		value, _ := cell.PolicyValue()
		encoded, encodeErr := context.encode(value, field.Type)
		if encodeErr != nil {
			return PersistedVerification{}, encodeErr
		}
		args = append(args, encoded)
		parts = append(parts, context.qualified(field.Column)+" = "+dialect.Placeholder(len(args)))
	}
	for _, condition := range conditions {
		fragment, compileErr := context.compileCondition(condition, len(args))
		if compileErr != nil {
			return PersistedVerification{}, compileErr
		}
		parts = append(parts, fragment.text)
		for _, binding := range fragment.bindings {
			if binding.kind != StaticBinding {
				return PersistedVerification{}, fmt.Errorf("P4_MUTATION_SQL_VERIFY: condition contains non-static binding")
			}
			args = append(args, binding.value)
		}
	}
	return PersistedVerification{text: "SELECT 1 FROM " + dialect.Table(model) + " AS " + dialect.Quote(context.alias) + " WHERE " + strings.Join(parts, " AND "), args: args}, nil
}
