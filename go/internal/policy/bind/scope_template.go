package bind

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

func (b *binder) scopeTemplate(model ir.ModelID, actual ir.Condition, view golem.FrozenTemplateBindingView) (ir.FactScope, error) {
	metadata, present := b.registry.Model(golem.ModelID(model))
	if !present {
		return ir.FactScope{}, b.failure(CodeModel, "policy.factScope.template", "scope model is absent")
	}
	template, present := metadata.ScopeTemplate(compilerir.ScopeTemplateID(view.TemplateID()))
	if !present {
		return ir.FactScope{}, b.failure(CodeCondition, "policy.factScope.template", "scope template identity is absent")
	}
	body, err := b.bindTemplateBody(actual, view, template.Fingerprint, template.Parameters, template.Expression)
	if err != nil {
		return ir.FactScope{}, err
	}
	id, err := fixedID(view.TemplateID())
	if err != nil {
		return ir.FactScope{}, err
	}
	digest := sha256.Sum256(view.CanonicalBytes())
	return ir.NewBoundFactScope(body, ir.ScopeBinding{TemplateID: id, Template: ir.Fingerprint(view.Fingerprint()), Arguments: ir.Fingerprint(digest)})
}

func (b *binder) factTemplate(model ir.ModelID, name string, actual ir.Condition, view golem.FrozenTemplateBindingView) (ir.FactDefinition, error) {
	metadata, present := b.registry.Model(golem.ModelID(model))
	if !present {
		return ir.FactDefinition{}, b.failure(CodeModel, "policy.facts.template", "fact model is absent")
	}
	template, present := metadata.FactTemplate(compilerir.FactTemplateID(view.TemplateID()))
	if !present || name != template.Name {
		return ir.FactDefinition{}, b.failure(CodeCondition, "policy.facts.template", "fact template identity or name does not match")
	}
	body, err := b.bindTemplateBody(actual, view, template.Fingerprint, template.Parameters, template.Expression)
	if err != nil {
		return ir.FactDefinition{}, err
	}
	id, err := fixedID(view.TemplateID())
	if err != nil {
		return ir.FactDefinition{}, err
	}
	scope, err := fixedID(string(template.ScopeTemplateID))
	if err != nil {
		return ir.FactDefinition{}, err
	}
	digest := sha256.Sum256(view.CanonicalBytes())
	return ir.NewBoundFactDefinition(model, name, body, ir.FactBinding{TemplateID: id, ScopeTemplateID: scope, Template: ir.Fingerprint(view.Fingerprint()), Arguments: ir.Fingerprint(digest)})
}

func (b *binder) bindTemplateBody(actual ir.Condition, view golem.FrozenTemplateBindingView, fingerprint compilerir.Fingerprint, parameters []compilerir.ScopeParameterIR, expression compilerir.ScopeExpressionIR) (ir.Condition, error) {
	fail := func(detail string) (ir.Condition, error) {
		return ir.Condition{}, b.failure(CodeCondition, "policy.template", "%s", detail)
	}
	digest := view.Fingerprint()
	if string(fingerprint) != hex.EncodeToString(digest[:]) || len(view.CanonicalBytes()) == 0 {
		return fail("template fingerprint or frozen provenance does not match")
	}
	arguments := view.Arguments()
	if len(arguments) != len(parameters) {
		return fail("argument inventory does not match the template")
	}
	values := make(map[string]golem.FrozenOperandView, len(arguments))
	for index, argument := range arguments {
		parameter := parameters[index]
		if argument == nil || argument.Name() != parameter.Name || argument.Operand() == nil {
			return fail("parameters must match the template's exact sorted inventory")
		}
		operand := argument.Operand()
		if parameter.Many && operand.Kind() != golem.FrozenOperandMany || !parameter.Many && operand.Kind() != golem.FrozenOperandOne {
			return fail("parameter scalar/set cardinality does not match")
		}
		values[parameter.Name] = operand
	}
	expected, err := b.templateExpression(expression, values)
	if err != nil {
		return ir.Condition{}, err
	}
	expected, err = normalize.Condition(expected)
	if err != nil {
		return ir.Condition{}, err
	}
	normalizedActual, err := normalize.Condition(actual)
	if err != nil {
		return ir.Condition{}, err
	}
	left, err := ir.ConditionFingerprint(expected)
	if err != nil {
		return ir.Condition{}, err
	}
	right, err := ir.ConditionFingerprint(normalizedActual)
	if err != nil || left != right {
		return fail("generated body does not match its template and arguments")
	}
	// Matching uses semantic normal form; admission still needs every authored
	// participant, including Boolean-dead branches outside the static template.
	// Policy normalization owns executable simplification after that check.
	return actual, nil
}

// Instantiate through the same scalar binding owner used for public policy
// predicates, including off-grid temporal equality and physical capabilities.
func (b *binder) templateExpression(expression compilerir.ScopeExpressionIR, arguments map[string]golem.FrozenOperandView) (ir.Condition, error) {
	model := ir.ModelID(mustTemplateID(expression.ModelID))
	switch expression.Operator {
	case "true", "false":
		return ir.NewConstant(model, expression.Operator == "true")
	case "and", "or", "not":
		children := make([]ir.Condition, len(expression.Children))
		for index, child := range expression.Children {
			value, err := b.templateExpression(child, arguments)
			if err != nil {
				return ir.Condition{}, err
			}
			children[index] = value
		}
		operation := ir.LogicalAnd
		if expression.Operator == "or" {
			operation = ir.LogicalOr
		} else if expression.Operator == "not" {
			operation = ir.LogicalNot
		}
		if len(children) == 1 && operation != ir.LogicalNot {
			return children[0], nil
		}
		return ir.NewLogical(model, operation, children)
	case "eq", "in", "ne", "notin", "lt", "lte", "gt", "gte":
		field := ir.FieldID(mustTemplateID(expression.FieldID))
		fact, present := b.registry.Field(golem.ModelID(model), golem.FieldID(field))
		if !present {
			return ir.Condition{}, b.failure(CodeField, "policy.template", "template scalar field is absent")
		}
		typ, err := bindType(fact.LogicalType(), fact.Nullable())
		if err != nil {
			return ir.Condition{}, err
		}
		operation := map[string]ir.OperatorID{"eq": ir.OperatorEqual, "ne": ir.OperatorNotEqual, "in": ir.OperatorIn, "notin": ir.OperatorNotIn, "lt": ir.OperatorLessThan, "lte": ir.OperatorLessThanOrEqual, "gt": ir.OperatorGreaterThan, "gte": ir.OperatorGreaterThanOrEqual}[expression.Operator]
		if expression.Parameter != "" {
			view := arguments[expression.Parameter]
			if typ.Kind() == ir.ValueDateTime && !b.exact {
				condition, rewritten, err := b.offGridDateTime(view, model, field, fact, typ, operation, ir.ComparisonSensitive, "policy.template")
				if rewritten || err != nil {
					return condition, err
				}
			}
			operand, err := b.operand(view, typ, operation, "policy.template.arguments."+expression.Parameter)
			if err != nil {
				return ir.Condition{}, err
			}
			return b.scalarLeaf(model, field, fact, typ, operation, ir.ComparisonSensitive, operand, "policy.template")
		}
		values := make([]ir.Value, len(expression.Literals))
		for index, literal := range expression.Literals {
			values[index], err = b.templateLiteral(literal, typ)
			if err != nil {
				return ir.Condition{}, err
			}
		}
		var operand ir.Operand
		if operation == ir.OperatorIn || operation == ir.OperatorNotIn {
			operand, err = ir.ManyOperand(values)
		} else if len(values) == 1 {
			operand, err = ir.OneOperand(values[0])
		} else {
			return ir.Condition{}, b.failure(CodeValue, "policy.template", "template comparison requires one literal")
		}
		if err != nil {
			return ir.Condition{}, err
		}
		return b.scalarLeaf(model, field, fact, typ, operation, ir.ComparisonSensitive, operand, "policy.template")
	case "is", "isnot", "isnull", "isnotnull", "some", "none", "every":
		field := ir.FieldID(mustTemplateID(expression.FieldID))
		if expression.RelationID == "" {
			fact, present := b.registry.Field(golem.ModelID(model), golem.FieldID(field))
			if !present {
				return ir.Condition{}, b.failure(CodeField, "policy.template", "null-test field is absent")
			}
			typ, err := bindType(fact.LogicalType(), fact.Nullable())
			if err != nil {
				return ir.Condition{}, err
			}
			operation := ir.OperatorIsNull
			if expression.Operator == "isnotnull" {
				operation = ir.OperatorIsNotNull
			}
			return b.scalarLeaf(model, field, fact, typ, operation, ir.ComparisonSensitive, ir.NoOperand(), "policy.template")
		}
		relation, target := ir.RelationID(mustTemplateID(expression.RelationID)), ir.ModelID(mustTemplateID(expression.TargetModelID))
		var child *ir.Condition
		if len(expression.Children) != 0 {
			value, err := b.templateExpression(expression.Children[0], arguments)
			if err != nil {
				return ir.Condition{}, err
			}
			child = &value
		}
		cardinality := ir.RelationToOne
		if expression.Operator == "some" || expression.Operator == "none" || expression.Operator == "every" {
			cardinality = ir.RelationToMany
		}
		operation := map[string]ir.OperatorID{"is": ir.OperatorRelationIs, "isnot": ir.OperatorRelationIsNot, "isnull": ir.OperatorRelationIsNull, "isnotnull": ir.OperatorRelationIsNotNull, "some": ir.OperatorRelationSome, "none": ir.OperatorRelationNone, "every": ir.OperatorRelationEvery}[expression.Operator]
		requirements, err := operator.ValidateShape(operation, operator.Shape{Node: ir.ConditionRelation, Cardinality: cardinality, HasChild: child != nil, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Providers: b.providers})
		if err != nil {
			return ir.Condition{}, err
		}
		return ir.NewRelation(model, field, relation, target, cardinality, operation, child, requirements)
	default:
		return ir.Condition{}, b.failure(CodeCondition, "policy.template", "unsupported template operator %q", expression.Operator)
	}
}

func mustTemplateID[T ~string](value T) [16]byte {
	// All template identities have already crossed schema bootstrap validation.
	id, _ := fixedID(string(value))
	return id
}

func (b *binder) templateLiteral(literal compilerir.TypedLiteralIR, typ ir.TypeRef) (ir.Value, error) {
	switch literal.Kind {
	case compilerir.LiteralBool:
		value, err := strconv.ParseBool(literal.Canonical)
		return ir.BoolValue(value), err
	case compilerir.LiteralInteger:
		value, err := strconv.ParseInt(literal.Canonical, 10, 64)
		if err != nil {
			return ir.Value{}, err
		}
		return ir.SignedValue(typ.Kind(), value)
	case compilerir.LiteralFloat:
		value, err := strconv.ParseFloat(literal.Canonical, 64)
		if err != nil {
			return ir.Value{}, err
		}
		if typ.Kind() == ir.ValueFloat32 {
			return ir.Float32Value(float32(value))
		}
		return ir.Float64Value(value)
	case compilerir.LiteralString:
		return ir.StringValue(literal.Canonical)
	case compilerir.LiteralEnum:
		enum, _ := typ.EnumID()
		member, present := b.registry.EnumValue(compilerir.EnumID(hex.EncodeToString(enum[:])), literal.Canonical)
		if !present {
			return ir.Value{}, fmt.Errorf("template enum literal is not registered")
		}
		return ir.NewEnumValue(enum, ir.EnumValueID(mustTemplateID(member)))
	default:
		return ir.Value{}, b.failure(CodeValue, "policy.template", "unsupported scope literal kind %s", literal.Kind)
	}
}
