package model

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"go/token"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

// emitPolicyScopes lowers only normalized compiler-owned expressions. Author
// declaration functions and mutable field namespaces never execute here.
func emitPolicyScopes(body *bytes.Buffer, models []ir.ModelDeclIR, modelByID map[ir.ModelID]ir.ModelDeclIR, enumByID map[ir.EnumID]ir.EnumIR, relations map[ir.RelationID]ir.RelationIR, contracts map[ir.ModelID]ir.ModelContractIR, imports *importSet) error {
	hasTemplates := false
	for _, model := range models {
		hasTemplates = hasTemplates || len(contracts[model.ID].ScopeTemplates) != 0 || len(contracts[model.ID].FactTemplates) != 0
	}
	if !hasTemplates {
		return nil
	}
	reserved := map[string]string{"GolemGeneratedDescriptors": "descriptor registry"}
	for _, model := range models {
		for name, description := range map[string]string{
			model.Go.Name: "model type", plural(model.LogicalName): "model namespace",
			"GolemGenerated" + model.Go.Name + "Descriptor": "model descriptor",
			"golemGenerated" + model.Go.Name + "Fields":     "model field type",
		} {
			reserved[name] = description
		}
		for _, template := range contracts[model.ID].ScopeTemplates {
			if template.Arguments.PackagePath == imports.current {
				reserved[template.Arguments.Name] = "scope arguments type"
			}
		}
		for _, template := range contracts[model.ID].FactTemplates {
			if template.Arguments.PackagePath == imports.current {
				reserved[template.Arguments.Name] = "fact arguments type"
			}
		}
	}
	for _, model := range models {
		families := []policyTemplateFamily{{suffix: "Scopes", templates: append([]ir.ScopeTemplateIR(nil), contracts[model.ID].ScopeTemplates...)}, {suffix: "Facts", data: true}}
		for _, fact := range contracts[model.ID].FactTemplates {
			families[1].templates = append(families[1].templates, ir.ScopeTemplateIR{ID: ir.ScopeTemplateID(fact.ID), Name: fact.Name, Arguments: fact.Arguments, Parameters: fact.Parameters, Expression: fact.Expression, Fingerprint: fact.Fingerprint})
		}
		for _, family := range families {
			templates := family.templates
			if len(templates) == 0 {
				continue
			}
			if len(templates) > scopecontract.MaxTemplatesPerModel {
				return fmt.Errorf("model codegen: scope template count exceeds its bound")
			}
			namespace, receiver := model.Go.Name+family.suffix, "golemGenerated"+model.Go.Name+family.suffix
			for _, name := range []string{namespace, receiver} {
				if !token.IsIdentifier(name) || reserved[name] != "" {
					return fmt.Errorf("model codegen: scope namespace %q collides with %s or is invalid", name, reserved[name])
				}
				reserved[name] = "scope namespace"
			}
			sort.Slice(templates, func(a, b int) bool { return templates[a].ID < templates[b].ID })
			fmt.Fprintf(body, "type %s struct{}\n\nvar %s = %s{}\n\n", receiver, namespace, receiver)
			methods := map[string]bool{}
			for _, template := range templates {
				name := exportedName(template.Name)
				if !token.IsIdentifier(name) || !token.IsExported(name) || methods[name] {
					return fmt.Errorf("model codegen: scope template %q has an invalid or colliding method in %s", template.Name, namespace)
				}
				methods[name] = true
				id, err := hex.DecodeString(string(template.ID))
				if err != nil || len(id) != 16 || hex.EncodeToString(id) != string(template.ID) {
					return fmt.Errorf("model codegen: scope template %q has no canonical template identity", template.Name)
				}
				fingerprint, err := schemaDigestLiteral(string(template.Fingerprint))
				if err != nil {
					return err
				}
				if template.Expression.ModelID != model.ID || template.Arguments.PackagePath == "" || !token.IsIdentifier(template.Arguments.Name) || !token.IsExported(template.Arguments.Name) {
					return fmt.Errorf("model codegen: scope template %q has invalid model/argument ownership", template.Name)
				}
				lowerer := scopeLowerer{models: modelByID, enums: enumByID, relations: relations, imports: imports, parameters: map[string]ir.ScopeParameterIR{}, data: family.data}
				if len(template.Parameters) > scopecontract.MaxParameters {
					return fmt.Errorf("model codegen: scope parameter count exceeds its bound")
				}
				for _, parameter := range template.Parameters {
					if !token.IsIdentifier(parameter.Name) || !token.IsExported(parameter.Name) {
						return fmt.Errorf("model codegen: invalid scope parameter %q", parameter.Name)
					}
					if _, duplicate := lowerer.parameters[parameter.Name]; duplicate {
						return fmt.Errorf("model codegen: duplicate scope parameter %q", parameter.Name)
					}
					lowerer.parameters[parameter.Name] = parameter
				}
				predicate, err := lowerer.expression(template.Expression, model.ID, 0)
				if err != nil {
					return fmt.Errorf("model codegen: scope %s.%s: %w", model.Go.Name, template.Name, err)
				}
				modelLiteral, err := idLiteral("ModelID", string(model.ID))
				if err != nil {
					return err
				}
				argumentType := imports.qualify(template.Arguments.PackagePath, template.Arguments.Name)
				boundType := "BoundFactScope"
				if family.data {
					boundType = "BoundFact"
				}
				fmt.Fprintf(body, "func (%s) %s(args %s) golem.%s[%s] {\n", receiver, name, argumentType, boundType, model.Go.Name)
				fmt.Fprintf(body, "\treturn golem.Generated%s[%s](%s, %q, %s, ", boundType, model.Go.Name, modelLiteral, template.ID, fingerprint)
				if family.data {
					fmt.Fprintf(body, "%q, ", template.Name)
				}
				body.WriteString(predicate)
				for _, parameter := range template.Parameters {
					valueType, err := logicalGoType(parameter.Type, enumByID, imports)
					if err != nil {
						return err
					}
					constructor := "GeneratedTemplateArgument"
					if parameter.Many {
						constructor = "GeneratedTemplateSetArgument"
					}
					fmt.Fprintf(body, ", golem.%s[%s](%q, args.%s)", constructor, valueType, parameter.Name, parameter.Name)
				}
				body.WriteString(")\n}\n\n")
			}
		}
	}
	return nil
}

type policyTemplateFamily struct {
	suffix    string
	data      bool
	templates []ir.ScopeTemplateIR
}

type scopeLowerer struct {
	models     map[ir.ModelID]ir.ModelDeclIR
	enums      map[ir.EnumID]ir.EnumIR
	relations  map[ir.RelationID]ir.RelationIR
	imports    *importSet
	parameters map[string]ir.ScopeParameterIR
	nodes      int
	data       bool
}

func (lowerer *scopeLowerer) expression(expression ir.ScopeExpressionIR, owner ir.ModelID, depth int) (string, error) {
	lowerer.nodes++
	if lowerer.nodes > scopecontract.MaxNodes || depth > scopecontract.MaxDepth || expression.ModelID != owner {
		return "", fmt.Errorf("scope expression exceeds limits or crosses an uncorrelated model")
	}
	model, present := lowerer.models[owner]
	if !present {
		return "", fmt.Errorf("scope expression model is absent")
	}
	modelType := lowerer.imports.qualify(model.Go.PackagePath, model.Go.Name)
	switch expression.Operator {
	case "true":
		return "golem.All[" + modelType + "]()", nil
	case "false":
		return "golem.None[" + modelType + "]()", nil
	case "and", "or", "not":
		if len(expression.Children) > scopecontract.MaxNodes-lowerer.nodes {
			return "", fmt.Errorf("scope logical children exceed the remaining node budget")
		}
		children := make([]string, len(expression.Children))
		for index, child := range expression.Children {
			value, err := lowerer.expression(child, owner, depth+1)
			if err != nil {
				return "", err
			}
			children[index] = value
		}
		if expression.Operator == "not" {
			if len(children) != 1 {
				return "", fmt.Errorf("scope Not must have one child")
			}
			return "(" + children[0] + ").Not()", nil
		}
		operation := "And"
		if expression.Operator == "or" {
			operation = "Or"
		}
		return "golem." + operation + "[" + modelType + "](" + strings.Join(children, ", ") + ")", nil
	case "eq", "in", "ne", "notin", "lt", "lte", "gt", "gte", "is", "some", "isnot", "none", "every", "isnull", "isnotnull":
		if !lowerer.data && expression.Operator != "eq" && expression.Operator != "in" && expression.Operator != "is" && expression.Operator != "some" {
			return "", fmt.Errorf("unsupported authority expression operator")
		}
		var field ir.FieldIR
		for _, candidate := range model.Fields {
			if candidate.ID == expression.FieldID {
				field = candidate
				break
			}
		}
		fieldLiteral, err := idLiteral("FieldID", string(expression.FieldID))
		if err != nil {
			return "", err
		}
		presence := expression.Operator == "isnull" || expression.Operator == "isnotnull"
		if field.Relation != nil {
			arity := 1
			if presence {
				arity = 0
			}
			if field.Relation.RelationID != expression.RelationID || len(expression.Children) != arity {
				return "", fmt.Errorf("scope relation metadata is incomplete")
			}
			target, err := relationTarget(field, owner, lowerer.models, lowerer.relations)
			if err != nil || target.ID != expression.TargetModelID {
				return "", fmt.Errorf("scope relation target disagrees with its expression")
			}
			relationLiteral, err := idLiteral("RelationID", string(expression.RelationID))
			if err != nil {
				return "", err
			}
			targetLiteral, err := idLiteral("ModelID", string(target.ID))
			if err != nil {
				return "", err
			}
			child := ""
			if !presence {
				child, err = lowerer.expression(expression.Children[0], target.ID, depth+1)
				if err != nil {
					return "", err
				}
			}
			constructor := "GeneratedToOne"
			if field.Relation.Kind == ir.RelationHasMany {
				constructor = "GeneratedToMany"
			}
			operation := map[string]string{"is": "Is", "some": "Some", "isnot": "IsNot", "none": "None", "every": "Every", "isnull": "IsNull", "isnotnull": "IsNotNull"}[expression.Operator]
			if operation == "" {
				return "", fmt.Errorf("scalar operator cannot apply to a relation")
			}
			targetType := lowerer.imports.qualify(target.Go.PackagePath, target.Go.Name)
			return fmt.Sprintf("golem.%s[%s, %s](%s, %s, %s).%s(%s)", constructor, modelType, targetType, fieldLiteral, relationLiteral, targetLiteral, operation, child), nil
		}
		if field.Scalar == nil || field.Relation != nil {
			return "", fmt.Errorf("scope scalar field is absent")
		}
		if field.Scalar.Type.Kind == ir.TypeJSON || field.Scalar.Type.Kind == ir.TypeScalarList {
			return "", fmt.Errorf("scope scalar type has no supported positive-template constructor")
		}
		valueType, err := logicalGoType(field.Scalar.Type, lowerer.enums, lowerer.imports)
		if err != nil {
			return "", err
		}
		constructor := "GeneratedEqualField"
		if expression.Operator == "lt" || expression.Operator == "lte" || expression.Operator == "gt" || expression.Operator == "gte" {
			constructor = "GeneratedOrderedField"
		}
		if presence {
			constructor = "GeneratedNullableEqualField"
		}
		handle := fmt.Sprintf("golem.%s[%s, %s](%s)", constructor, modelType, valueType, fieldLiteral)
		if field.Scalar.Type.Kind == ir.TypeBytes {
			constructor = "GeneratedBytesField"
			if presence {
				constructor = "GeneratedNullableBytesField"
			}
			handle = fmt.Sprintf("golem.%s[%s](%s)", constructor, modelType, fieldLiteral)
		}
		operation := map[string]string{"eq": "Eq", "in": "In", "ne": "Ne", "notin": "NotIn", "lt": "LT", "lte": "LTE", "gt": "GT", "gte": "GTE", "isnull": "IsNull", "isnotnull": "IsNotNull"}[expression.Operator]
		if operation == "" {
			return "", fmt.Errorf("relation operator cannot apply to scalar")
		}
		if presence {
			return handle + "." + operation + "()", nil
		}
		many := expression.Operator == "in" || expression.Operator == "notin"
		var operands []string
		if expression.Parameter != "" {
			parameter, exists := lowerer.parameters[expression.Parameter]
			if !exists || parameter.Many != many || len(expression.Literals) != 0 {
				return "", fmt.Errorf("scope parameter has invalid ownership/cardinality")
			}
			operand := "args." + expression.Parameter
			if parameter.Many {
				operand += "..."
			}
			operands = []string{operand}
		} else {
			if len(expression.Literals) > scopecontract.MaxLiteralValues || !many && len(expression.Literals) != 1 {
				return "", fmt.Errorf("scope scalar literal arity is invalid")
			}
			for _, literal := range expression.Literals {
				value, err := scopeLiteral(literal, field.Scalar.Type, valueType)
				if err != nil {
					return "", err
				}
				operands = append(operands, value)
			}
		}
		return handle + "." + operation + "(" + strings.Join(operands, ", ") + ")", nil
	default:
		return "", fmt.Errorf("unsupported scope template operator %q", expression.Operator)
	}
}

func scopeLiteral(literal ir.TypedLiteralIR, typ ir.LogicalTypeIR, valueType string) (string, error) {
	switch literal.Kind {
	case ir.LiteralBool:
		if typ.Kind == ir.TypeBool && (literal.Canonical == "true" || literal.Canonical == "false") {
			return literal.Canonical, nil
		}
	case ir.LiteralInteger:
		bits := map[ir.LogicalTypeKind]int{ir.TypeInt16: 16, ir.TypeInt32: 32, ir.TypeInt64: 64}[typ.Kind]
		if bits != 0 {
			if value, err := strconv.ParseInt(literal.Canonical, 10, bits); err == nil && strconv.FormatInt(value, 10) == literal.Canonical {
				return valueType + "(" + literal.Canonical + ")", nil
			}
		}
	case ir.LiteralFloat:
		bits := 0
		if typ.Kind == ir.TypeFloat32 {
			bits = 32
		} else if typ.Kind == ir.TypeFloat64 {
			bits = 64
		}
		if bits != 0 {
			if value, err := strconv.ParseFloat(literal.Canonical, bits); err == nil && !math.IsInf(value, 0) && !math.IsNaN(value) && !(value == 0 && math.Signbit(value)) && strconv.FormatFloat(value, 'g', -1, bits) == literal.Canonical {
				return valueType + "(" + literal.Canonical + ")", nil
			}
		}
	case ir.LiteralString:
		if typ.Kind == ir.TypeString {
			return strconv.Quote(literal.Canonical), nil
		}
	case ir.LiteralEnum:
		if typ.Kind == ir.TypeEnum {
			return valueType + "(" + strconv.Quote(literal.Canonical) + ")", nil
		}
	}
	return "", fmt.Errorf("unsupported or invalid scope literal kind %q for %s", literal.Kind, typ.Kind)
}
