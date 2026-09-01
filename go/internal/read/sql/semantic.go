package sql

import (
	"strings"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

// SemanticCandidates is the authorized candidate subquery of one semantic rank
// statement. Its projection is the owner's primary-key columns in physical key
// order, each aliased to its own physical name, so managed shadow storage can
// join ranked vectors to exactly the rows an ordinary read would return.
type SemanticCandidates struct {
	text    string
	args    []any
	columns []physical.PhysicalName
	fields  []policyir.FieldID
}

func (statement SemanticCandidates) SQL() string { return statement.text }
func (statement SemanticCandidates) Args() []any { return cloneArgs(statement.args) }
func (statement SemanticCandidates) Columns() []physical.PhysicalName {
	return append([]physical.PhysicalName(nil), statement.columns...)
}
func (statement SemanticCandidates) Fields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), statement.fields...)
}

// RenderSemanticCandidates compiles the plan's authorized predicate, conjoined
// with the field mask of every conditional primary-key field, into a subquery
// over the owner table. The masks are mandatory: a masked primary identity was
// previously dropped in Go while extracting record keys, and candidacy is now
// decided entirely in SQL.
func RenderSemanticCandidates(plan readplan.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, enclosingParameters int) (SemanticCandidates, error) {
	if registry == nil || plan.ModelID() == (policyir.ModelID{}) {
		return SemanticCandidates{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "plan and registry are required", nil)
	}
	if enclosingParameters < 0 {
		return SemanticCandidates{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "enclosing parameter count cannot be negative", nil)
	}
	dialect, err := providerDialect(provider)
	if err != nil {
		return SemanticCandidates{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "provider is unsupported", err)
	}
	resolver := policysql.SchemaResolver(registry)
	model, ok := resolver.Model(provider, plan.ModelID())
	if !ok {
		return SemanticCandidates{}, fail(CodeSchema, plan.ModelID(), policyir.FieldID{}, "physical model is absent", nil)
	}
	modelFact, ok := registry.Model(golem.ModelID(plan.ModelID()))
	if !ok || len(modelFact.PrimaryKey()) == 0 {
		return SemanticCandidates{}, fail(CodeSchema, plan.ModelID(), policyir.FieldID{}, "semantic candidate model has no primary identity", nil)
	}
	// Candidacy is the whole authorized set, never a page of it. Take is
	// deliberately ignored here, but a plan that also carried pagination would
	// silently rank rows the same request would not return.
	_, skip := plan.Skip()
	_, cursor := plan.Cursor()
	if skip || cursor || len(plan.Distinct()) != 0 {
		return SemanticCandidates{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "semantic candidate plan carries pagination", nil)
	}
	planned := make(map[policyir.FieldID]readplan.Field, len(plan.Fields()))
	for _, field := range plan.Fields() {
		planned[field.FieldID()] = field
	}
	conditions := []policyir.Condition{plan.Where()}
	primary := modelFact.PrimaryKey()
	columns := make([]physical.PhysicalName, len(primary))
	fields := make([]policyir.FieldID, len(primary))
	projection := make([]string, len(primary))
	for index, identity := range primary {
		field := policyir.FieldID(identity)
		fields[index] = field
		resolved, found := resolver.Field(provider, plan.ModelID(), field)
		if !found {
			return SemanticCandidates{}, fail(CodeSchema, plan.ModelID(), field, "semantic candidate identity column is absent", nil)
		}
		columns[index] = resolved.Column
		projection[index] = dialect.Quote(semanticCandidateAlias) + "." + dialect.Quote(resolved.Column) + " AS " + dialect.Quote(resolved.Column)
		selected, present := planned[field]
		if !present {
			return SemanticCandidates{}, fail(CodeSchema, plan.ModelID(), field, "semantic candidate identity field is absent from the read plan", nil)
		}
		if !selected.Conditional() || selected.DischargedByConstraint() {
			continue
		}
		mask, has := selected.Mask()
		if !has {
			return SemanticCandidates{}, fail(CodeRender, plan.ModelID(), field, "conditional semantic identity field has no mask", nil)
		}
		conditions = append(conditions, mask)
	}
	condition := conditions[0]
	if len(conditions) > 1 {
		condition, err = policyir.NewLogical(plan.ModelID(), policyir.LogicalAnd, conditions)
		if err != nil {
			return SemanticCandidates{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "semantic identity masks could not be merged", err)
		}
		condition, err = normalize.Condition(condition)
		if err != nil {
			return SemanticCandidates{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "semantic candidate predicate could not be normalized", err)
		}
	}
	fragment, err := policysql.CompileWithPolicyAliasAllocator(policysql.Request{Condition: condition, Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: semanticCandidateAlias}, policysql.NewPolicyAliasAllocator())
	if err != nil {
		return SemanticCandidates{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "authorized semantic candidate predicate did not render", err)
	}
	args := fragment.Args()
	parameters := make([]any, len(args)+enclosingParameters)
	if err := enforceStatementParameterLimitWith(plan.ModelID(), parameters, plan.Limits().MaxStatementParameters); err != nil {
		return SemanticCandidates{}, err
	}
	text := "SELECT " + strings.Join(projection, ", ") + " FROM " + dialect.Table(model) + " AS " + dialect.Quote(semanticCandidateAlias) + " WHERE " + fragment.SQL()
	if err := enforceStatementComplexityWith(plan.ModelID(), text, plan.Limits().MaxStatementBytes, plan.Limits().MaxStatementAliases); err != nil {
		return SemanticCandidates{}, err
	}
	return SemanticCandidates{text: text, args: cloneArgs(args), columns: columns, fields: fields}, nil
}

const semanticCandidateAlias = physical.PhysicalName("golem_r0")
