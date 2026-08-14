package batch

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// Render creates the only statement that may run before the batch bound is
// known. The capture uses the planner's complete action constraint, declared
// primary-key order, and a MaxRows+1 sentinel. It is SQL-only and performs no
// evaluation in Go.
func Render(plan mutationir.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (Program, error) {
	if registry == nil {
		return Program{}, fail(CodeInput, policyir.ModelID{}, policyir.FieldID{}, "active schema registry is required", nil)
	}
	if err := plan.Validate(); err != nil {
		return Program{}, fail(CodeInput, policyir.ModelID{}, policyir.FieldID{}, "mutation plan is invalid", err)
	}
	nodes := plan.Graph().Nodes()
	if len(nodes) != 1 {
		return Program{}, fail(CodeInput, policyir.ModelID{}, policyir.FieldID{}, "batch renderer requires exactly one root node", nil)
	}
	node := nodes[0]
	if node.Operation() != mutationir.UpdateMany && node.Operation() != mutationir.DeleteMany {
		return Program{}, fail(CodeInput, node.ModelID(), policyir.FieldID{}, "operation is not update-many or delete-many", nil)
	}
	if node.IdentityBehavior() != mutationir.IdentityBatchChangeRefused {
		return Program{}, fail(CodeIdentity, node.ModelID(), policyir.FieldID{}, "batch plan does not refuse identity changes", nil)
	}
	if plan.Bounds().MaxRows() == 0 || plan.Bounds().MaxRows() == math.MaxUint32 {
		return Program{}, fail(CodeInput, node.ModelID(), policyir.FieldID{}, "batch row bound cannot form a +1 sentinel", nil)
	}
	dialect, transaction, err := providerDialect(provider)
	if err != nil {
		return Program{}, fail(CodeProvider, node.ModelID(), policyir.FieldID{}, "provider is unsupported", err)
	}
	resolver := policysql.SchemaResolver(registry)
	if capabilities.Provider() != provider || capabilities.SchemaFingerprint() != resolver.SchemaFingerprint() {
		return Program{}, fail(CodeProvider, node.ModelID(), policyir.FieldID{}, "capability proof does not match provider and schema", nil)
	}
	model, ok := resolver.Model(provider, node.ModelID())
	if !ok {
		return Program{}, fail(CodeSchema, node.ModelID(), policyir.FieldID{}, "physical model is absent", nil)
	}
	logical, ok := registry.Model(golem.ModelID(node.ModelID()))
	if !ok || len(logical.PrimaryKey()) == 0 {
		return Program{}, fail(CodeSchema, node.ModelID(), policyir.FieldID{}, "batch mutation requires a declared primary key", nil)
	}
	primary := toPolicyFields(logical.PrimaryKey())
	identityFields := make(map[policyir.FieldID]struct{})
	for _, identity := range logical.Identities() {
		for _, field := range identity.Fields() {
			identityFields[policyir.FieldID(field)] = struct{}{}
		}
	}
	for _, operation := range node.ScalarOperations() {
		if _, identity := identityFields[operation.FieldID()]; identity {
			return Program{}, fail(CodeIdentity, node.ModelID(), operation.FieldID(), "update-many cannot author an identity component", nil)
		}
		field, found := resolver.Field(provider, node.ModelID(), operation.FieldID())
		if !found || !sameType(field.Type, operation.Type()) {
			return Program{}, fail(CodeSchema, node.ModelID(), operation.FieldID(), "scalar operation does not match its physical field", nil)
		}
	}
	context := renderContext{plan: plan, node: node, registry: registry, provider: provider, capabilities: capabilities, dialect: dialect, resolver: resolver, model: model, alias: "golem_b0", primary: primary}
	authored := make(map[policyir.FieldID]struct{}, len(node.ScalarOperations()))
	for _, operation := range node.ScalarOperations() {
		if !operation.RuntimeOwned() {
			authored[operation.FieldID()] = struct{}{}
		}
	}
	for _, authorization := range node.FieldAuthorizations() {
		if _, ok := authored[authorization.FieldID()]; !ok {
			return Program{}, fail(CodeInput, node.ModelID(), authorization.FieldID(), "field authorization does not correspond to an authored scalar", nil)
		}
		if _, err := context.compile(authorization.Condition(), 0); err != nil {
			return Program{}, err
		}
	}
	fields, columns, err := context.completeColumns()
	if err != nil {
		return Program{}, err
	}
	condition, ok := node.SelectionRequirement()
	var constraint policyir.Condition
	if ok {
		constraint = condition.Constraint()
	} else if plan.Stance() == mutationir.System {
		predicate, present := node.Predicate()
		if !present {
			return Program{}, fail(CodeInput, node.ModelID(), policyir.FieldID{}, "system batch predicate is absent", nil)
		}
		constraint = predicate
	} else {
		return Program{}, fail(CodeInput, node.ModelID(), policyir.FieldID{}, "caller batch lacks its complete action constraint", nil)
	}
	fragment, err := context.compile(constraint, 0)
	if err != nil {
		return Program{}, err
	}
	order := make([]string, len(primary))
	for index, fieldID := range primary {
		field, found := resolver.Field(provider, node.ModelID(), fieldID)
		if !found {
			return Program{}, fail(CodeSchema, node.ModelID(), fieldID, "primary-key field has no physical descriptor", nil)
		}
		order[index] = context.qualified(field.Column) + " ASC"
	}
	lock := ""
	if provider == policyir.ProviderPostgreSQL {
		lock = " FOR UPDATE"
	}
	text := "SELECT " + strings.Join(fields, ", ") + " FROM " + dialect.Table(model) + " AS " + dialect.Quote(context.alias) + " WHERE " + fragment.text + " ORDER BY " + strings.Join(order, ", ") + fmt.Sprintf(" LIMIT %d", uint64(plan.Bounds().MaxRows())+1) + lock
	capture := Statement{role: CaptureExactSet, text: text, bindings: fragment.bindings, columns: columns, cardinality: AtMostSentinelRows}
	if uint32(len(capture.bindings)) > plan.Bounds().MaxParameters() {
		return Program{}, fail(CodeLimit, node.ModelID(), policyir.FieldID{}, "capture statement exceeds parameter bound", nil)
	}
	return Program{context: context, transaction: transaction, capture: capture, maxRows: plan.Bounds().MaxRows(), primary: primary}, nil
}

type renderContext struct {
	plan         mutationir.Plan
	node         mutationir.Node
	registry     *schema.Registry
	provider     policyir.Provider
	capabilities policysql.CapabilityProof
	dialect      policysql.Dialect
	resolver     policysql.Resolver
	model        policysql.Model
	alias        physical.PhysicalName
	primary      []policyir.FieldID
}

type fragment struct {
	text     string
	bindings []Binding
}

func (context renderContext) compile(condition policyir.Condition, offset int) (fragment, error) {
	result, err := policysql.Compile(policysql.Request{Condition: condition, Provider: context.provider, Resolver: context.resolver, Dialect: context.dialect, Capabilities: context.capabilities, BoundFingerprint: context.resolver.SchemaFingerprint(), RootAlias: context.alias})
	if err != nil {
		return fragment{}, fail(CodeProvider, context.node.ModelID(), policyir.FieldID{}, "condition cannot be rendered safely", err)
	}
	args := result.Args()
	bindings := make([]Binding, len(args))
	for index := range args {
		bindings[index] = Binding{value: args[index]}
	}
	return fragment{text: policysql.RebasePlaceholders(result.SQL(), offset, context.provider), bindings: bindings}, nil
}

func (context renderContext) completeColumns() ([]string, []ResultColumn, error) {
	model, _ := context.registry.Model(golem.ModelID(context.node.ModelID()))
	var fields []string
	var columns []ResultColumn
	for _, publicID := range model.Fields() {
		fieldID := policyir.FieldID(publicID)
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), fieldID)
		if !ok { // relation field
			continue
		}
		alias := "golem_f_" + hex.EncodeToString(fieldID[:])
		fields = append(fields, context.qualified(field.Column)+" AS "+context.dialect.Quote(physical.PhysicalName(alias)))
		columns = append(columns, ResultColumn{field: fieldID, alias: alias})
	}
	if len(fields) == 0 {
		return nil, nil, fail(CodeSchema, context.node.ModelID(), policyir.FieldID{}, "model has no persisted scalar fields", nil)
	}
	return fields, columns, nil
}

func (context renderContext) qualified(column physical.PhysicalName) string {
	return context.dialect.Quote(context.alias) + "." + context.dialect.Quote(column)
}

func providerDialect(provider policyir.Provider) (policysql.Dialect, TransactionRequirement, error) {
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.NewPolicyDialect(), SQLiteImmediateTransaction, nil
	case policyir.ProviderPostgreSQL:
		return postgresprovider.NewPolicyDialect(), PostgreSQLTransaction, nil
	default:
		return nil, 0, fmt.Errorf("unknown provider %d", provider)
	}
}

func toPolicyFields(values []golem.FieldID) []policyir.FieldID {
	result := make([]policyir.FieldID, len(values))
	for index := range values {
		result[index] = policyir.FieldID(values[index])
	}
	return result
}

func sameType(left, right policyir.TypeRef) bool {
	if left.Kind() != right.Kind() || left.Nullable() != right.Nullable() || left.Precision() != right.Precision() || left.Scale() != right.Scale() || left.Capability() != right.Capability() {
		return false
	}
	le, l := left.EnumID()
	re, r := right.EnumID()
	if l != r || l && le != re {
		return false
	}
	la, l := left.Element()
	ra, r := right.Element()
	return l == r && (!l || sameType(la, ra))
}
