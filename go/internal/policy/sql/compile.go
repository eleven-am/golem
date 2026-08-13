package sql

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func Compile(request Request) (Fragment, error) {
	return compile(request, NewPolicyAliasAllocator())
}

// CompileWithPolicyAliasAllocator compiles one fragment within an outer
// renderer-owned statement allocation scope. The allocator must be fresh for
// that statement and shared by every fragment that contributes SQL to it.
func CompileWithPolicyAliasAllocator(request Request, aliases *PolicyAliasAllocator) (Fragment, error) {
	if aliases == nil {
		return Fragment{}, &Error{Code: CodeInput, ModelID: request.Condition.ModelID(), Detail: "policy alias allocator is required"}
	}
	return compile(request, aliases)
}

func compile(request Request, aliases *PolicyAliasAllocator) (Fragment, error) {
	if request.Resolver == nil || request.Dialect == nil || request.RootAlias == "" {
		return Fragment{}, &Error{Code: CodeInput, ModelID: request.Condition.ModelID(), Detail: "resolver, dialect, and root alias are required"}
	}
	if request.Provider != request.Dialect.Provider() {
		return Fragment{}, &Error{Code: CodeProvider, ModelID: request.Condition.ModelID(), Detail: "request and dialect providers differ"}
	}
	if strings.HasPrefix(string(request.RootAlias), "golem_p") {
		return Fragment{}, &Error{Code: CodeInput, ModelID: request.Condition.ModelID(), Detail: "root alias uses the reserved golem_p prefix"}
	}
	fingerprint := request.Resolver.SchemaFingerprint()
	if fingerprint == ([32]byte{}) || request.BoundFingerprint != fingerprint {
		return Fragment{}, &Error{Code: CodeSchema, ModelID: request.Condition.ModelID(), Detail: "condition and runtime schema fingerprints differ"}
	}
	if request.Capabilities.Provider() != request.Provider || request.Capabilities.SchemaFingerprint() != fingerprint {
		return Fragment{}, &Error{Code: CodeCapability, ModelID: request.Condition.ModelID(), Detail: "runtime capability proof does not match provider and schema"}
	}
	providers := request.Resolver.Providers()
	if !providers.Valid() || !providers.Contains(request.Provider) {
		return Fragment{}, &Error{Code: CodeProvider, ModelID: request.Condition.ModelID(), Detail: "provider is not declared by the schema"}
	}
	if err := operator.ValidateCondition(request.Condition, providers); err != nil {
		return Fragment{}, &Error{Code: CodeInput, ModelID: request.Condition.ModelID(), Detail: "condition validation failed", Cause: err}
	}
	if model, ok := request.Resolver.Model(request.Provider, request.Condition.ModelID()); !ok || model.ID != request.Condition.ModelID() {
		return Fragment{}, &Error{Code: CodeSchema, ModelID: request.Condition.ModelID(), Detail: "root model has no physical descriptor"}
	}
	if err := validateCapabilities(request.Condition, request.Provider, request.Resolver, request.Capabilities); err != nil {
		return Fragment{}, err
	}
	compiler := compiler{request: request, binder: Binder{dialect: request.Dialect, resolver: request.Resolver}, aliases: aliases}
	text, err := compiler.condition(request.Condition, request.RootAlias)
	if err != nil {
		return Fragment{}, err
	}
	return Fragment{
		text:                  text,
		args:                  cloneArgs(compiler.binder.args),
		policyRelationAliases: append([]PolicyRelationAliasFact(nil), compiler.policyRelationAliases...),
	}, nil
}

func validateCapabilities(condition ir.Condition, provider ir.Provider, resolver Resolver, proof CapabilityProof) error {
	check := func(node ir.Condition, requirements []ir.Requirement) error {
		for _, requirement := range requirements {
			if requirement.Providers().Contains(provider) && (!resolver.Capability(provider, requirement.Capability()) || !proof.Has(requirement.Capability())) {
				field, _ := node.Field()
				operatorID, _ := node.Operator()
				return &Error{Code: CodeCapability, ModelID: node.ModelID(), FieldID: field, OperatorID: operatorID, Detail: fmt.Sprintf("required capability %d is unavailable or unproved at runtime", requirement.Capability())}
			}
		}
		return nil
	}
	switch condition.Kind() {
	case ir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := validateCapabilities(child, provider, resolver, proof); err != nil {
				return err
			}
		}
		return nil
	case ir.ConditionRelation:
		own, _ := condition.RelationOwnRequirements()
		if err := check(condition, own); err != nil {
			return err
		}
		_, _, _, _, child, _ := condition.Relation()
		if child != nil {
			return validateCapabilities(*child, provider, resolver, proof)
		}
		return nil
	default:
		if err := check(condition, condition.Requirements()); err != nil {
			return err
		}
		return nil
	}
}

type compiler struct {
	request               Request
	binder                Binder
	aliases               *PolicyAliasAllocator
	policyRelationAliases []PolicyRelationAliasFact
}

func (compiler *compiler) condition(condition ir.Condition, alias physical.PhysicalName) (string, error) {
	switch condition.Kind() {
	case ir.ConditionConstant:
		truth, _ := condition.Constant()
		if truth {
			return "TRUE", nil
		}
		return "FALSE", nil
	case ir.ConditionLogical:
		logical, children, _ := condition.Logical()
		rendered := make([]string, len(children))
		for index, child := range children {
			value, err := compiler.condition(child, alias)
			if err != nil {
				return "", err
			}
			rendered[index] = "(" + value + ")"
		}
		switch logical {
		case ir.LogicalAnd:
			return strings.Join(rendered, " AND "), nil
		case ir.LogicalOr:
			return strings.Join(rendered, " OR "), nil
		case ir.LogicalNot:
			return "NOT (" + rendered[0] + ")", nil
		default:
			return "", compiler.failure(condition, CodeInput, "unknown logical operator", nil)
		}
	case ir.ConditionScalar, ir.ConditionList, ir.ConditionJSON:
		return compiler.leaf(condition, alias)
	case ir.ConditionRelation:
		return compiler.relation(condition, alias)
	default:
		return "", compiler.failure(condition, CodeInput, "unknown condition kind", nil)
	}
}

func (compiler *compiler) leaf(condition ir.Condition, alias physical.PhysicalName) (string, error) {
	fieldID, _ := condition.Field()
	field, ok := compiler.request.Resolver.Field(compiler.request.Provider, condition.ModelID(), fieldID)
	conditionType, _ := condition.FieldType()
	if !ok || field.Model != condition.ModelID() || field.ID != fieldID || !sameType(field.Type, conditionType) || field.Nullable != conditionType.Nullable() {
		return "", compiler.failure(condition, CodeSchema, "field has no physical descriptor for its owning model", nil)
	}
	operatorID, _ := condition.Operator()
	if !compiler.request.Dialect.Supports(operatorID) {
		return "", compiler.failure(condition, CodeRender, "dialect does not declare support for the operator", nil)
	}
	operand, _ := condition.Operand()
	column := Column{Alias: alias, Name: field.Column}
	var text string
	var err error
	switch condition.Kind() {
	case ir.ConditionScalar:
		mode, _ := condition.Mode()
		text, err = compiler.request.Dialect.RenderScalar(ScalarLeaf{Column: column, Field: field, Operator: operatorID, Mode: mode, Operand: operand}, &compiler.binder)
	case ir.ConditionList:
		text, err = compiler.request.Dialect.RenderList(ListLeaf{Column: column, Field: field, Operator: operatorID, Operand: operand}, &compiler.binder)
	case ir.ConditionJSON:
		mode, _ := condition.Mode()
		path, _ := condition.Path()
		text, err = compiler.request.Dialect.RenderJSON(JSONLeaf{Column: column, Field: field, Operator: operatorID, Mode: mode, Path: path, Operand: operand}, &compiler.binder)
	}
	if err != nil {
		return "", compiler.failure(condition, CodeRender, "dialect rejected leaf", err)
	}
	if strings.TrimSpace(text) == "" {
		return "", compiler.failure(condition, CodeRender, "dialect returned an empty leaf", nil)
	}
	return "(" + text + ")", nil
}

func (compiler *compiler) relation(condition ir.Condition, parentAlias physical.PhysicalName) (string, error) {
	fieldID, relationID, target, cardinality, child, _ := condition.Relation()
	descriptor, ok := compiler.request.Resolver.Relation(condition.ModelID(), fieldID, relationID)
	if !ok || descriptor.Model != condition.ModelID() || descriptor.Field != fieldID || descriptor.ID != relationID || descriptor.Target != target || descriptor.Cardinality != cardinality || len(descriptor.Pairs) == 0 {
		return "", compiler.failure(condition, CodeSchema, "relation descriptor does not match the condition endpoint", nil)
	}
	model, ok := compiler.request.Resolver.Model(compiler.request.Provider, target)
	if !ok || model.ID != target {
		return "", compiler.failure(condition, CodeSchema, "relation target has no physical descriptor", nil)
	}
	alias := compiler.allocatePolicyRelationAlias(target, relationID)
	correlations := make([]string, len(descriptor.Pairs))
	for index, pair := range descriptor.Pairs {
		parent, parentOK := compiler.request.Resolver.Field(compiler.request.Provider, condition.ModelID(), pair.Parent)
		remote, childOK := compiler.request.Resolver.Field(compiler.request.Provider, target, pair.Child)
		if !parentOK || !childOK || parent.Model != condition.ModelID() || parent.ID != pair.Parent || remote.Model != target || remote.ID != pair.Child {
			return "", compiler.failure(condition, CodeSchema, "relation correlation references an unknown physical field", nil)
		}
		correlations[index] = compiler.column(Column{Alias: parentAlias, Name: parent.Column}) + " = " + compiler.column(Column{Alias: alias, Name: remote.Column})
	}
	predicate := strings.Join(correlations, " AND ")
	operatorID, _ := condition.Operator()
	if !compiler.request.Dialect.Supports(operatorID) {
		return "", compiler.failure(condition, CodeRender, "dialect does not declare support for the operator", nil)
	}
	if child != nil {
		childSQL, err := compiler.condition(*child, alias)
		if err != nil {
			return "", err
		}
		if operatorID == ir.OperatorRelationEvery {
			predicate += " AND ((" + childSQL + ") IS NOT TRUE)"
		} else {
			predicate += " AND (" + childSQL + ")"
		}
	}
	exists := "EXISTS (SELECT 1 FROM " + compiler.request.Dialect.Table(model) + " AS " + compiler.request.Dialect.Quote(alias) + " WHERE " + predicate + ")"
	switch operatorID {
	case ir.OperatorRelationIs, ir.OperatorRelationIsNotNull, ir.OperatorRelationSome:
		return exists, nil
	case ir.OperatorRelationIsNot, ir.OperatorRelationIsNull, ir.OperatorRelationEvery, ir.OperatorRelationNone:
		return "NOT (" + exists + ")", nil
	default:
		return "", compiler.failure(condition, CodeRender, "unsupported relation operator", nil)
	}
}

// allocatePolicyRelationAlias is the single ownership point for both the SQL
// alias and its sanitizer fact. Callers cannot allocate one without recording
// the other.
func (compiler *compiler) allocatePolicyRelationAlias(model ir.ModelID, relation ir.RelationID) physical.PhysicalName {
	alias := compiler.aliases.allocate()
	compiler.policyRelationAliases = append(compiler.policyRelationAliases, newPolicyRelationAliasFact(alias, model, relation))
	return physical.PhysicalName(alias)
}

func sameType(left, right ir.TypeRef) bool {
	if left.Kind() != right.Kind() || left.Nullable() != right.Nullable() || left.Precision() != right.Precision() || left.Scale() != right.Scale() || left.Capability() != right.Capability() {
		return false
	}
	leftEnum, leftHasEnum := left.EnumID()
	rightEnum, rightHasEnum := right.EnumID()
	if leftHasEnum != rightHasEnum || leftHasEnum && leftEnum != rightEnum {
		return false
	}
	leftElement, leftHasElement := left.Element()
	rightElement, rightHasElement := right.Element()
	return leftHasElement == rightHasElement && (!leftHasElement || sameType(leftElement, rightElement))
}

func (compiler *compiler) column(column Column) string {
	return compiler.request.Dialect.Quote(column.Alias) + "." + compiler.request.Dialect.Quote(column.Name)
}

func (compiler *compiler) failure(condition ir.Condition, code ErrorCode, detail string, cause error) error {
	field, _ := condition.Field()
	operatorID, _ := condition.Operator()
	return &Error{Code: code, ModelID: condition.ModelID(), FieldID: field, OperatorID: operatorID, Detail: detail, Cause: cause}
}
