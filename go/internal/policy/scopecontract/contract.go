// Package scopecontract owns static, parameterized positive authority domains.
// Capture projections are derived from the same expression that binding will
// instantiate; they are not a separately authored approximation of a policy.
package scopecontract

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/scalar"
)

const (
	MaxNodes             = 4096
	MaxDepth             = 64
	MaxParameters        = 64
	MaxLiteralValues     = 1024
	MaxTemplatesPerModel = 64
	MaxProjections       = 256
	MaxTemplateBytes     = 1 << 20
)

type Declaration struct {
	ModelID  ir.ModelID
	Template ir.ScopeTemplateIR
	Span     ir.SourceSpan
}

// ProjectionHop preserves the ordered, direction-specific correlation of one
// declared relationship. ParentFields and ChildFields are paired by position.
type ProjectionHop struct {
	Occurrence    string        `json:"occurrence"`
	ModelID       ir.ModelID    `json:"modelId"`
	FieldID       ir.FieldID    `json:"fieldId"`
	RelationID    ir.RelationID `json:"relationId"`
	TargetModelID ir.ModelID    `json:"targetModelId"`
	ParentFields  []ir.FieldID  `json:"parentFields"`
	ChildFields   []ir.FieldID  `json:"childFields"`
}

// CaptureProjection is one factorized path occurrence, not a flattened join of
// independent existential witnesses. Fields include its complete primary key,
// scalar leaves and every incident correlation field used by the template.
type CaptureProjection struct {
	ModelID ir.ModelID      `json:"modelId"`
	Path    []ProjectionHop `json:"path"`
	Fields  []ir.FieldID    `json:"fields"`
}

// Normalize publishes no partial changes if any declaration is invalid. It
// derives identities and fingerprints, but never enables historical capture.
func Normalize(compilation *ir.CompilationIR, declarations []Declaration) []ir.Diagnostic {
	if compilation == nil {
		return []ir.Diagnostic{ir.NewError("P05_SCOPE_CONTRACT", "scope templates require a compilation", ir.SourceSpan{})}
	}
	if len(declarations) == 0 {
		if err := Validate(compilation.Model, compilation.Contract); err != nil {
			return []ir.Diagnostic{ir.NewError("P05_SCOPE_CONTRACT", err.Error(), ir.SourceSpan{})}
		}
		return nil
	}
	catalog, err := newCatalog(compilation.Model)
	if err != nil {
		return []ir.Diagnostic{ir.NewError("P05_SCOPE_CONTRACT", err.Error(), ir.SourceSpan{})}
	}
	contracts, err := contractInventory(compilation.Contract)
	if err != nil {
		return []ir.Diagnostic{ir.NewError("P05_SCOPE_CONTRACT", err.Error(), ir.SourceSpan{})}
	}
	pending := make(map[ir.ModelID][]ir.ScopeTemplateIR)
	seen := make(map[ir.ScopeTemplateID]bool)
	var diagnostics []ir.Diagnostic
	for _, declaration := range declarations {
		normalized, projections, failure := catalog.normalize(declaration.ModelID, declaration.Template)
		if failure == nil {
			failure = requireSubscriptions(contracts, projections)
		}
		if failure == nil && seen[normalized.ID] {
			failure = fmt.Errorf("duplicate scope template %q on model %s", normalized.Name, declaration.ModelID)
		}
		if failure == nil && len(pending[declaration.ModelID]) >= MaxTemplatesPerModel {
			failure = fmt.Errorf("model %s exceeds %d scope templates", declaration.ModelID, MaxTemplatesPerModel)
		}
		if failure != nil {
			diagnostics = append(diagnostics, ir.NewError("P05_SCOPE_TEMPLATE", failure.Error(), declaration.Span))
			continue
		}
		seen[normalized.ID] = true
		pending[declaration.ModelID] = append(pending[declaration.ModelID], normalized)
	}
	if len(diagnostics) != 0 {
		ir.SortDiagnostics(diagnostics)
		return diagnostics
	}
	for index := range compilation.Contract.Models {
		model := &compilation.Contract.Models[index]
		values := pending[model.ModelID]
		sort.Slice(values, func(a, b int) bool { return values[a].ID < values[b].ID })
		model.ScopeTemplates = values
	}
	return nil
}

// Validate recomputes the complete normalized shape and all derived metadata.
// A matching stored digest alone never establishes template validity.
func Validate(model ir.ModelIR, contract ir.ContractIR) error {
	hasTemplates := false
	for _, owner := range contract.Models {
		hasTemplates = hasTemplates || len(owner.ScopeTemplates) != 0 || len(owner.FactTemplates) != 0
	}
	if !hasTemplates {
		return nil
	}
	catalog, err := newCatalog(model)
	if err != nil {
		return err
	}
	contracts, err := contractInventory(contract)
	if err != nil {
		return err
	}
	if err := validateScopes(catalog, contract, contracts); err != nil {
		return err
	}
	return validateFacts(catalog, contract, contracts)
}

func validateScopes(catalog *catalog, contract ir.ContractIR, contracts map[ir.ModelID]ir.ModelContractIR) error {
	seen := map[ir.ScopeTemplateID]bool{}
	for _, owner := range contract.Models {
		if len(owner.ScopeTemplates) > MaxTemplatesPerModel {
			return fmt.Errorf("model %s exceeds %d scope templates", owner.ModelID, MaxTemplatesPerModel)
		}
		var previous ir.ScopeTemplateID
		for index, template := range owner.ScopeTemplates {
			normalized, projections, err := catalog.normalize(owner.ModelID, template)
			if err != nil {
				return err
			}
			if seen[normalized.ID] || index != 0 && previous >= normalized.ID {
				return fmt.Errorf("scope templates must have unique, sorted identities")
			}
			if !reflect.DeepEqual(template, normalized) {
				return fmt.Errorf("scope template %q is not its exact normalized contract", template.Name)
			}
			if err := requireSubscriptions(contracts, projections); err != nil {
				return err
			}
			seen[normalized.ID], previous = true, normalized.ID
		}
	}
	return nil
}

// Projection derives exact actor-independent fields and path correlations.
// Template identity/fingerprint are recomputed by Validate, not trusted here.
func Projection(model ir.ModelIR, template ir.ScopeTemplateIR) ([]CaptureProjection, error) {
	catalog, err := newCatalog(model)
	if err != nil {
		return nil, err
	}
	_, projections, err := catalog.normalize(template.Expression.ModelID, template)
	return projections, err
}

type catalog struct {
	models    map[ir.ModelID]ir.ModelDeclIR
	fields    map[ir.ModelID]map[ir.FieldID]ir.FieldIR
	relations map[ir.RelationID]ir.RelationIR
	enums     map[ir.EnumID]ir.EnumIR
}

func newCatalog(model ir.ModelIR) (*catalog, error) {
	c := &catalog{models: map[ir.ModelID]ir.ModelDeclIR{}, fields: map[ir.ModelID]map[ir.FieldID]ir.FieldIR{}, relations: map[ir.RelationID]ir.RelationIR{}, enums: map[ir.EnumID]ir.EnumIR{}}
	globalFields := map[ir.FieldID]bool{}
	for _, item := range model.Models {
		if item.ID == "" {
			return nil, fmt.Errorf("scope template model identity is absent")
		}
		if _, duplicate := c.models[item.ID]; duplicate {
			return nil, fmt.Errorf("duplicate model %s", item.ID)
		}
		c.models[item.ID], c.fields[item.ID] = item, map[ir.FieldID]ir.FieldIR{}
		for _, field := range item.Fields {
			if field.ID == "" || globalFields[field.ID] {
				return nil, fmt.Errorf("scope template field identities must be unique and present")
			}
			globalFields[field.ID] = true
			c.fields[item.ID][field.ID] = field
		}
	}
	for _, item := range model.Relations {
		if _, duplicate := c.relations[item.ID]; item.ID == "" || duplicate {
			return nil, fmt.Errorf("scope template relation identities must be unique and present")
		}
		c.relations[item.ID] = item
	}
	for _, item := range model.Enums {
		if _, duplicate := c.enums[item.ID]; item.ID == "" || duplicate {
			return nil, fmt.Errorf("scope template enum identities must be unique and present")
		}
		c.enums[item.ID] = item
	}
	return c, nil
}

func contractInventory(contract ir.ContractIR) (map[ir.ModelID]ir.ModelContractIR, error) {
	result := map[ir.ModelID]ir.ModelContractIR{}
	for _, item := range contract.Models {
		if _, duplicate := result[item.ModelID]; item.ModelID == "" || duplicate {
			return nil, fmt.Errorf("scope template model contracts must be unique and present")
		}
		result[item.ModelID] = item
	}
	return result, nil
}

func requireSubscriptions(contracts map[ir.ModelID]ir.ModelContractIR, projections []CaptureProjection) error {
	for _, projection := range projections {
		model, present := contracts[projection.ModelID]
		if !present || !model.Subscriptions {
			return fmt.Errorf("scope template participating model %s requires subscriptions", projection.ModelID)
		}
	}
	return nil
}

type normalization struct {
	catalog *catalog
	slots   map[string]ir.ScopeParameterIR
	used    map[string]bool
	nodes   int
	data    bool
}

func (c *catalog) normalize(root ir.ModelID, template ir.ScopeTemplateIR) (ir.ScopeTemplateIR, []CaptureProjection, error) {
	return c.normalizeMode(root, template, false)
}

func (c *catalog) normalizeMode(root ir.ModelID, template ir.ScopeTemplateIR, data bool) (ir.ScopeTemplateIR, []CaptureProjection, error) {
	fail := func(err error) (ir.ScopeTemplateIR, []CaptureProjection, error) {
		return ir.ScopeTemplateIR{}, nil, err
	}
	if _, exists := c.models[root]; !exists || template.Expression.ModelID != root {
		return fail(fmt.Errorf("scope template expression must belong to its declared model root"))
	}
	if template.Name == "" || len(template.Name) > 256 || !utf8.ValidString(template.Name) || strings.TrimSpace(template.Name) != template.Name || strings.ContainsRune(template.Name, 0) {
		return fail(fmt.Errorf("scope template requires an exact bounded UTF-8 name"))
	}
	if template.Arguments.PackagePath == "" || len(template.Arguments.PackagePath) > 4096 || strings.TrimSpace(template.Arguments.PackagePath) != template.Arguments.PackagePath || strings.ContainsRune(template.Arguments.PackagePath, 0) || !utf8.ValidString(template.Arguments.PackagePath) || len(template.Arguments.Name) > 256 || !token.IsIdentifier(template.Arguments.Name) || !token.IsExported(template.Arguments.Name) {
		return fail(fmt.Errorf("scope template arguments require an exported named Go type"))
	}
	if len(template.Parameters) > MaxParameters {
		return fail(fmt.Errorf("scope template exceeds %d parameters", MaxParameters))
	}
	n := normalization{catalog: c, slots: map[string]ir.ScopeParameterIR{}, used: map[string]bool{}, data: data}
	parameters := make([]ir.ScopeParameterIR, len(template.Parameters))
	for index, parameter := range template.Parameters {
		if len(parameter.Name) > 256 || !token.IsIdentifier(parameter.Name) || !token.IsExported(parameter.Name) {
			return fail(fmt.Errorf("scope parameter %q must name an exported argument field", parameter.Name))
		}
		if _, duplicate := n.slots[parameter.Name]; duplicate {
			return fail(fmt.Errorf("duplicate scope parameter %q", parameter.Name))
		}
		typ, err := c.scalarType(parameter.Type)
		if err != nil {
			return fail(err)
		}
		parameter.Type = typ
		parameters[index], n.slots[parameter.Name] = parameter, parameter
	}
	expression, err := n.expression(template.Expression, root, false, 0)
	if err != nil {
		return fail(err)
	}
	if len(n.used) != len(n.slots) {
		return fail(fmt.Errorf("scope template parameter inventory contains unused arguments"))
	}
	sort.Slice(parameters, func(a, b int) bool { return parameters[a].Name < parameters[b].Name })
	identityDomain, fingerprintDomain := "golem:scope-template-id:v1\x00", "golem:scope-template:v1\x00"
	if data {
		identityDomain, fingerprintDomain = "golem:fact-template-id:v1\x00", "golem:fact-template-body:v1\x00"
	}
	identity := sha256.Sum256([]byte(identityDomain + string(root) + "\x00" + template.Name))
	result := ir.ScopeTemplateIR{ID: ir.ScopeTemplateID(hex.EncodeToString(identity[:16])), Name: template.Name, Arguments: template.Arguments, Parameters: parameters, Expression: expression}
	projections, err := c.project(expression)
	if err != nil {
		return fail(err)
	}
	// Include the derived capture schema in the digest without persisting a
	// second footprint. Correlation/key/type changes must change this contract.
	type fieldShape struct {
		ID       ir.FieldID
		Type     ir.LogicalTypeIR
		Nullable bool
	}
	type modelShape struct {
		ID     ir.ModelID
		Key    ir.KeyID
		Fields []fieldShape
	}
	shapes := make([]modelShape, len(projections))
	for index, projection := range projections {
		shape := modelShape{ID: projection.ModelID, Key: c.models[projection.ModelID].PrimaryKey.ID}
		for _, id := range projection.Fields {
			field := c.fields[projection.ModelID][id]
			shape.Fields = append(shape.Fields, fieldShape{ID: id, Type: field.Scalar.Type, Nullable: field.Scalar.Nullable})
		}
		shapes[index] = shape
	}
	encoded, err := json.Marshal(struct {
		Template    ir.ScopeTemplateIR
		Projections []CaptureProjection
		Shapes      []modelShape
	}{result, projections, shapes})
	if err != nil {
		return fail(err)
	}
	if len(encoded) > MaxTemplateBytes {
		return fail(fmt.Errorf("scope template exceeds %d canonical bytes", MaxTemplateBytes))
	}
	hash := sha256.Sum256(append([]byte(fingerprintDomain), encoded...))
	result.Fingerprint = ir.Fingerprint(hex.EncodeToString(hash[:]))
	return result, projections, nil
}

func (c *catalog) scalarType(input ir.LogicalTypeIR) (ir.LogicalTypeIR, error) {
	if input.Element != nil || input.Kind == ir.TypeJSON || input.Kind == ir.TypeScalarList {
		return ir.LogicalTypeIR{}, fmt.Errorf("scope templates require portable scalar values")
	}
	typ, diagnostics := scalar.NormalizeType(input, ir.SourceSpan{})
	if len(diagnostics) != 0 {
		return ir.LogicalTypeIR{}, fmt.Errorf("invalid scope scalar type: %s", diagnostics[0].Message)
	}
	if typ.Kind == ir.TypeEnum {
		if typ.EnumID == nil {
			return ir.LogicalTypeIR{}, fmt.Errorf("scope enum identity is absent")
		}
		if _, exists := c.enums[*typ.EnumID]; !exists {
			return ir.LogicalTypeIR{}, fmt.Errorf("scope enum identity is unknown")
		}
	}
	// Copy pointer-owned type metadata; normalized output never aliases input.
	encoded, err := json.Marshal(typ)
	if err != nil {
		return ir.LogicalTypeIR{}, err
	}
	var owned ir.LogicalTypeIR
	if err := json.Unmarshal(encoded, &owned); err != nil {
		return ir.LogicalTypeIR{}, err
	}
	return owned, nil
}

func (n *normalization) expression(input ir.ScopeExpressionIR, owner ir.ModelID, negative bool, depth int) (ir.ScopeExpressionIR, error) {
	n.nodes++
	if n.nodes > MaxNodes || depth > MaxDepth {
		return ir.ScopeExpressionIR{}, fmt.Errorf("scope template expression exceeds node/depth limits")
	}
	if input.ModelID != owner {
		return ir.ScopeExpressionIR{}, fmt.Errorf("scope expression crosses model roots without a relation")
	}
	result := ir.ScopeExpressionIR{ModelID: owner, Operator: input.Operator}
	noOperands := input.FieldID == "" && input.RelationID == "" && input.TargetModelID == "" && input.Parameter == "" && len(input.Literals) == 0
	switch input.Operator {
	case "true", "false":
		if !noOperands || len(input.Children) != 0 {
			return result, fmt.Errorf("scope constant has unexpected payload")
		}
	case "and", "or", "not":
		if !noOperands || input.Operator == "not" && len(input.Children) != 1 || input.Operator != "not" && len(input.Children) == 0 {
			return result, fmt.Errorf("invalid scope logical shape")
		}
		childNegative := negative
		if input.Operator == "not" {
			childNegative = !negative
		}
		result.Children = make([]ir.ScopeExpressionIR, len(input.Children))
		for index, child := range input.Children {
			value, err := n.expression(child, owner, childNegative, depth+1)
			if err != nil {
				return result, err
			}
			result.Children[index] = value
		}
		if input.Operator != "not" && len(result.Children) == 1 {
			return result.Children[0], nil
		}
		if input.Operator != "not" {
			sort.Slice(result.Children, func(a, b int) bool {
				left, _ := json.Marshal(result.Children[a])
				right, _ := json.Marshal(result.Children[b])
				return string(left) < string(right)
			})
		}
	case "eq", "in", "ne", "notin", "lt", "lte", "gt", "gte", "isnull", "isnotnull":
		if (input.Operator == "isnull" || input.Operator == "isnotnull") && input.RelationID != "" {
			return n.relationExpression(input, negative, depth)
		}
		if !n.data && input.Operator != "eq" && input.Operator != "in" {
			return result, fmt.Errorf("unsupported authority scalar operator %q", input.Operator)
		}
		if input.FieldID == "" || input.RelationID != "" || input.TargetModelID != "" || len(input.Children) != 0 || input.Parameter != "" && len(input.Literals) != 0 {
			return result, fmt.Errorf("invalid scope scalar shape")
		}
		field, exists := n.catalog.fields[owner][input.FieldID]
		if !exists || field.Scalar == nil || field.Relation != nil || field.Kind != ir.FieldScalar && field.Kind != ir.FieldEnum {
			return result, fmt.Errorf("scope scalar field %s does not belong to model %s", input.FieldID, owner)
		}
		typ, err := n.catalog.scalarType(field.Scalar.Type)
		if err != nil {
			return result, err
		}
		result.FieldID = input.FieldID
		if input.Operator == "isnull" || input.Operator == "isnotnull" {
			if input.Parameter != "" || len(input.Literals) != 0 {
				return result, fmt.Errorf("scalar presence predicates have no value operands")
			}
			return result, nil
		}
		if orderedOperator(input.Operator) && !orderedType(typ.Kind) {
			return result, fmt.Errorf("ordered data predicate requires an ordered scalar")
		}
		many := input.Operator == "in" || input.Operator == "notin"
		if input.Parameter != "" {
			parameter, exists := n.slots[input.Parameter]
			if !exists || parameter.Many != many || !reflect.DeepEqual(parameter.Type, typ) {
				return result, fmt.Errorf("scope parameter %q has incompatible scalar type/cardinality", input.Parameter)
			}
			n.used[input.Parameter], result.Parameter = true, input.Parameter
		} else {
			if len(input.Literals) > MaxLiteralValues || !many && len(input.Literals) != 1 {
				return result, fmt.Errorf("invalid scope literal arity")
			}
			result.Literals = append([]ir.TypedLiteralIR(nil), input.Literals...)
			for _, literal := range result.Literals {
				if err := n.catalog.literal(typ, literal); err != nil {
					return result, err
				}
			}
			if many {
				sort.Slice(result.Literals, func(a, b int) bool { return result.Literals[a].Canonical < result.Literals[b].Canonical })
			}
		}
	case "is", "some", "isnot", "none", "every":
		return n.relationExpression(input, negative, depth)
	default:
		return result, fmt.Errorf("unsupported scope operator %q", input.Operator)
	}
	return result, nil
}

func (n *normalization) relationExpression(input ir.ScopeExpressionIR, negative bool, depth int) (ir.ScopeExpressionIR, error) {
	result := ir.ScopeExpressionIR{ModelID: input.ModelID, Operator: input.Operator}
	presence := input.Operator == "isnull" || input.Operator == "isnotnull"
	children := 1
	if presence {
		children = 0
	}
	if !n.data && (negative || input.Operator != "is" && input.Operator != "some") || input.Parameter != "" || len(input.Literals) != 0 || len(input.Children) != children {
		return result, fmt.Errorf("authority requires positive Is/Some; data relationships require exact operand shape")
	}
	hop, err := n.catalog.hop(input)
	if err != nil {
		return result, err
	}
	result.FieldID, result.RelationID, result.TargetModelID = hop.FieldID, hop.RelationID, hop.TargetModelID
	if !presence {
		child, err := n.expression(input.Children[0], hop.TargetModelID, false, depth+1)
		if err != nil {
			return result, err
		}
		result.Children = []ir.ScopeExpressionIR{child}
	}
	return result, nil
}

func orderedOperator(operation string) bool {
	return operation == "lt" || operation == "lte" || operation == "gt" || operation == "gte"
}
func orderedType(kind ir.LogicalTypeKind) bool {
	switch kind {
	case ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeFloat32, ir.TypeFloat64, ir.TypeDecimal, ir.TypeString, ir.TypeDate, ir.TypeTime, ir.TypeDateTime:
		return true
	}
	return false
}

func (c *catalog) literal(typ ir.LogicalTypeIR, literal ir.TypedLiteralIR) error {
	if len(literal.Canonical) > MaxTemplateBytes {
		return fmt.Errorf("scope literal exceeds size limit")
	}
	if typ.Kind == ir.TypeEnum {
		if literal.Kind == ir.LiteralEnum && typ.EnumID != nil {
			for _, value := range c.enums[*typ.EnumID].Values {
				if value.WireValue == literal.Canonical {
					return nil
				}
			}
		}
		return fmt.Errorf("scope enum literal is not a declared wire value")
	}
	tokenValue := literal.Canonical
	switch literal.Kind {
	case ir.LiteralString, ir.LiteralUUID, ir.LiteralDate, ir.LiteralTime, ir.LiteralDateTime:
		if !utf8.ValidString(tokenValue) {
			return fmt.Errorf("scope literal is not UTF-8")
		}
		tokenValue = strconv.Quote(tokenValue)
	case ir.LiteralBytes:
		decoded, err := base64.RawStdEncoding.DecodeString(tokenValue)
		if err != nil {
			return fmt.Errorf("invalid scope bytes literal")
		}
		tokenValue = strconv.Quote(string(decoded))
	}
	context := scalar.DefaultContext{}
	if typ.EnumID != nil {
		for _, value := range c.enums[*typ.EnumID].Values {
			context.EnumValues = append(context.EnumValues, value.WireValue)
		}
	}
	value, diagnostics := scalar.ResolveDefault(typ, tokenValue, context)
	if len(diagnostics) != 0 || value == nil || value.Kind != ir.DefaultLiteral || value.Literal == nil || *value.Literal != literal {
		return fmt.Errorf("scope literal is not a canonical %s value", typ.Kind)
	}
	return nil
}

func (c *catalog) hop(expression ir.ScopeExpressionIR) (ProjectionHop, error) {
	field, exists := c.fields[expression.ModelID][expression.FieldID]
	relation, related := c.relations[expression.RelationID]
	if !exists || !related || field.Kind != ir.FieldRelation || field.Relation == nil || field.Scalar != nil || field.Relation.RelationID != relation.ID {
		return ProjectionHop{}, fmt.Errorf("scope relation endpoint does not belong to its model")
	}
	if relation.Through != nil {
		return ProjectionHop{}, fmt.Errorf("scope through relations require explicit supported correlation metadata")
	}
	source := c.fields[relation.SourceModel][relation.SourceField]
	if source.Relation == nil || source.Scalar != nil || source.Kind != ir.FieldRelation || source.Relation.RelationID != relation.ID || source.Relation.Role != ir.RelationSource || source.Relation.Kind != ir.RelationBelongsTo {
		return ProjectionHop{}, fmt.Errorf("scope relation source endpoint is invalid")
	}
	if relation.InverseField != nil {
		inverse := c.fields[relation.TargetModel][*relation.InverseField]
		expected := ir.RelationHasOne
		if relation.Cardinality == ir.RelationMany {
			expected = ir.RelationHasMany
		}
		if inverse.Relation == nil || inverse.Scalar != nil || inverse.Kind != ir.FieldRelation || inverse.Relation.RelationID != relation.ID || inverse.Relation.Role != ir.RelationInverse || inverse.Relation.Kind != expected {
			return ProjectionHop{}, fmt.Errorf("scope relation inverse endpoint is invalid")
		}
	}
	hop := ProjectionHop{ModelID: expression.ModelID, FieldID: expression.FieldID, RelationID: relation.ID}
	var many bool
	switch field.Relation.Role {
	case ir.RelationSource:
		if expression.ModelID != relation.SourceModel || expression.FieldID != relation.SourceField || field.Relation.Kind != ir.RelationBelongsTo {
			return hop, fmt.Errorf("scope source endpoint ownership disagrees with relation")
		}
		hop.TargetModelID, hop.ParentFields, hop.ChildFields = relation.TargetModel, relation.LocalFields, relation.RemoteFields
	case ir.RelationInverse:
		if expression.ModelID != relation.TargetModel || relation.InverseField == nil || expression.FieldID != *relation.InverseField {
			return hop, fmt.Errorf("scope inverse endpoint ownership disagrees with relation")
		}
		many = relation.Cardinality == ir.RelationMany
		if many && field.Relation.Kind != ir.RelationHasMany || !many && field.Relation.Kind != ir.RelationHasOne {
			return hop, fmt.Errorf("scope inverse relation cardinality disagrees with field")
		}
		hop.TargetModelID, hop.ParentFields, hop.ChildFields = relation.SourceModel, relation.RemoteFields, relation.LocalFields
	default:
		return hop, fmt.Errorf("scope relation endpoint role is invalid")
	}
	collection := expression.Operator == "some" || expression.Operator == "none" || expression.Operator == "every"
	if relation.Cardinality != ir.RelationOne && relation.Cardinality != ir.RelationMany || expression.TargetModelID != hop.TargetModelID || collection != many {
		return hop, fmt.Errorf("scope relation operator/target/cardinality mismatch")
	}
	if len(hop.ParentFields) == 0 || len(hop.ParentFields) != len(hop.ChildFields) {
		return hop, fmt.Errorf("scope correlation requires equal nonempty key arity")
	}
	parents, children := map[ir.FieldID]bool{}, map[ir.FieldID]bool{}
	for index, parentID := range hop.ParentFields {
		childID := hop.ChildFields[index]
		parent, parentOK := c.fields[hop.ModelID][parentID]
		child, childOK := c.fields[hop.TargetModelID][childID]
		if parents[parentID] || children[childID] || !parentOK || !childOK || parent.Scalar == nil || child.Scalar == nil || parent.Relation != nil || child.Relation != nil {
			return hop, fmt.Errorf("scope correlation must name unique owned scalar fields")
		}
		parents[parentID], children[childID] = true, true
		parentType, err := c.scalarType(parent.Scalar.Type)
		if err != nil {
			return hop, err
		}
		childType, err := c.scalarType(child.Scalar.Type)
		if err != nil || !reflect.DeepEqual(parentType, childType) {
			return hop, fmt.Errorf("scope correlation scalar types disagree")
		}
	}
	if !c.unique(relation.TargetModel, relation.RemoteFields, true) || relation.Cardinality == ir.RelationOne && !c.unique(relation.SourceModel, relation.LocalFields, false) {
		return hop, fmt.Errorf("scope relation correlation is not backed by exact key ownership")
	}
	hop.ParentFields, hop.ChildFields = append([]ir.FieldID(nil), hop.ParentFields...), append([]ir.FieldID(nil), hop.ChildFields...)
	return hop, nil
}

func (c *catalog) unique(model ir.ModelID, fields []ir.FieldID, nonnull bool) bool {
	owner, exists := c.models[model]
	if !exists {
		return false
	}
	keys := append([]ir.KeyIR(nil), owner.Uniques...)
	if owner.PrimaryKey != nil {
		keys = append(keys, *owner.PrimaryKey)
	}
	for _, key := range keys {
		if key.ID == "" || key.Kind != ir.KeyPrimary && key.Kind != ir.KeyUnique {
			continue
		}
		if !reflect.DeepEqual(key.Fields, fields) {
			continue
		}
		for _, id := range fields {
			field := c.fields[model][id]
			if field.Scalar == nil || nonnull && field.Scalar.Nullable {
				return false
			}
		}
		return true
	}
	return false
}

func (c *catalog) project(expression ir.ScopeExpressionIR) ([]CaptureProjection, error) {
	var result []CaptureProjection
	indices := map[string]int{}
	ensure := func(model ir.ModelID, path []ProjectionHop) (int, error) {
		encoded, _ := json.Marshal(path)
		key := string(model) + "\x00" + string(encoded)
		if index, exists := indices[key]; exists {
			return index, nil
		}
		owner, exists := c.models[model]
		if !exists || owner.PrimaryKey == nil || owner.PrimaryKey.Kind != ir.KeyPrimary || len(owner.PrimaryKey.Fields) == 0 || owner.PrimaryKey.ID == "" {
			return 0, fmt.Errorf("scope capture model %s requires an exact primary key", model)
		}
		if len(result) >= MaxProjections {
			return 0, fmt.Errorf("scope capture exceeds %d path projections", MaxProjections)
		}
		seen := map[ir.FieldID]bool{}
		for _, id := range owner.PrimaryKey.Fields {
			field := c.fields[model][id]
			if seen[id] || field.Scalar == nil || field.Scalar.Nullable || field.Relation != nil {
				return 0, fmt.Errorf("scope capture primary key has invalid owned scalar fields")
			}
			if _, err := c.scalarType(field.Scalar.Type); err != nil {
				return 0, err
			}
			seen[id] = true
		}
		index := len(result)
		indices[key] = index
		result = append(result, CaptureProjection{ModelID: model, Path: append([]ProjectionHop{}, path...), Fields: append([]ir.FieldID(nil), owner.PrimaryKey.Fields...)})
		return index, nil
	}
	addFields := func(index int, fields ...ir.FieldID) { result[index].Fields = append(result[index].Fields, fields...) }
	var walk func(ir.ScopeExpressionIR, []ProjectionHop, string) error
	walk = func(node ir.ScopeExpressionIR, path []ProjectionHop, occurrence string) error {
		index, err := ensure(node.ModelID, path)
		if err != nil {
			return err
		}
		switch node.Operator {
		case "eq", "in", "ne", "notin", "lt", "lte", "gt", "gte":
			addFields(index, node.FieldID)
		case "isnull", "isnotnull":
			if node.RelationID == "" {
				addFields(index, node.FieldID)
				return nil
			}
			fallthrough
		case "is", "some", "isnot", "none", "every":
			hop, err := c.hop(node)
			if err != nil {
				return err
			}
			hop.Occurrence = occurrence
			addFields(index, hop.ParentFields...)
			next := append(append([]ProjectionHop{}, path...), hop)
			child, err := ensure(hop.TargetModelID, next)
			if err != nil {
				return err
			}
			addFields(child, hop.ChildFields...)
			if len(node.Children) != 0 {
				return walk(node.Children[0], next, occurrence+".0")
			}
			return nil
		default:
			for childIndex, child := range node.Children {
				if err := walk(child, path, occurrence+"."+strconv.Itoa(childIndex)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(expression, nil, "0"); err != nil {
		return nil, err
	}
	for index := range result {
		fields := result[index].Fields
		sort.Slice(fields, func(a, b int) bool { return fields[a] < fields[b] })
		unique := fields[:0]
		for _, field := range fields {
			if len(unique) == 0 || unique[len(unique)-1] != field {
				unique = append(unique, field)
			}
		}
		result[index].Fields = unique
	}
	sort.Slice(result, func(a, b int) bool {
		left, _ := json.Marshal(result[a].Path)
		right, _ := json.Marshal(result[b].Path)
		return string(left) < string(right)
	})
	return result, nil
}
