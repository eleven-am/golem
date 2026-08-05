package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func (provider *Provider) lower(_ context.Context, model ir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	namespace := options.Namespace
	if namespace == "" {
		namespace = "main"
	}
	if namespace != "main" {
		return physical.PhysicalSchema{}, fmt.Errorf("sqlite lower: namespace %q is unsupported; portable SQLite uses main", namespace)
	}
	if !containsProvider(model.Providers, ir.SQLite) {
		return physical.PhysicalSchema{}, fmt.Errorf("sqlite lower: logical schema %s does not target SQLite", model.Schema.ID)
	}
	if len(model.Extensions) != 0 {
		return physical.PhysicalSchema{}, fmt.Errorf("sqlite lower: provider extensions require a registered SQLite lowering implementation; extension=%s owner=%s", model.Extensions[0].ID, model.Extensions[0].Owner)
	}

	lowering := lowerState{
		model:     model,
		manifest:  provider.Manifest(),
		models:    make(map[ir.ModelID]ir.ModelDeclIR, len(model.Models)),
		fields:    make(map[ir.ModelID]map[ir.FieldID]physical.PhysicalColumn, len(model.Models)),
		enums:     make(map[ir.EnumID]ir.EnumIR, len(model.Enums)),
		relations: make(map[ir.RelationID]ir.RelationIR, len(model.Relations)),
	}
	for _, enum := range model.Enums {
		lowering.enums[enum.ID] = enum
	}
	for _, relation := range model.Relations {
		lowering.relations[relation.ID] = relation
	}
	for _, declaration := range model.Models {
		lowering.models[declaration.ID] = declaration
	}

	schema := physical.PhysicalSchema{
		Version:          physical.SchemaFormatVersion,
		CanonicalVersion: physical.CanonicalFormatVersion,
		Provider:         lowering.manifest,
		Namespace:        physical.Namespace{Name: "main"},
		System: physical.SystemSchema{
			Version:   1,
			Namespace: physical.Namespace{Name: "main"},
			Objects: []physical.SystemObject{
				{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
				{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
			},
		},
	}
	models := append([]ir.ModelDeclIR(nil), model.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	for _, declaration := range models {
		table, err := lowering.lowerTable(declaration)
		if err != nil {
			return physical.PhysicalSchema{}, err
		}
		schema.Tables = append(schema.Tables, table)
	}
	for index := range schema.Tables {
		if err := lowering.lowerForeignKeys(&schema.Tables[index]); err != nil {
			return physical.PhysicalSchema{}, err
		}
	}
	return physical.Normalize(schema)
}

type lowerState struct {
	model     ir.ModelIR
	manifest  physical.ProviderManifest
	models    map[ir.ModelID]ir.ModelDeclIR
	fields    map[ir.ModelID]map[ir.FieldID]physical.PhysicalColumn
	enums     map[ir.EnumID]ir.EnumIR
	relations map[ir.RelationID]ir.RelationIR
}

func (state *lowerState) lowerTable(model ir.ModelDeclIR) (physical.PhysicalTable, error) {
	table := physical.PhysicalTable{ID: model.ID, Name: physical.PhysicalName(model.Table.PhysicalName)}
	fieldMap := make(map[ir.FieldID]physical.PhysicalColumn)
	fields := append([]ir.FieldIR(nil), model.Fields...)
	sort.SliceStable(fields, func(i, j int) bool {
		if fields[i].DeclarationOrder != fields[j].DeclarationOrder {
			return fields[i].DeclarationOrder < fields[j].DeclarationOrder
		}
		return fields[i].ID < fields[j].ID
	})
	ordinal := uint32(0)
	for _, field := range fields {
		if field.Scalar == nil {
			continue
		}
		column, checks, err := state.lowerColumn(model, field, ordinal)
		if err != nil {
			return physical.PhysicalTable{}, err
		}
		ordinal++
		table.Columns = append(table.Columns, column)
		table.Checks = append(table.Checks, checks...)
		fieldMap[field.ID] = column
	}
	state.fields[model.ID] = fieldMap
	if model.PrimaryKey != nil {
		key := lowerKey(*model.PrimaryKey)
		table.PrimaryKey = &key
	}
	for _, key := range model.Uniques {
		table.Uniques = append(table.Uniques, lowerKey(key))
	}
	for _, check := range model.Checks {
		if check.Provider == ir.ProviderScopePostgreSQL {
			return physical.PhysicalTable{}, fmt.Errorf("sqlite lower: check %s model=%s is PostgreSQL-scoped", check.ID, model.ID)
		}
		expression, err := state.lowerPredicate(model.ID, check.Predicate)
		if err != nil {
			return physical.PhysicalTable{}, fmt.Errorf("sqlite lower: check=%s model=%s: %w", check.ID, model.ID, err)
		}
		table.Checks = append(table.Checks, physical.PhysicalCheck{ID: check.ID, Name: physical.PhysicalName(check.PhysicalName), Expression: expression})
	}
	for _, index := range model.Indexes {
		lowered, err := state.lowerIndex(model.ID, index)
		if err != nil {
			return physical.PhysicalTable{}, err
		}
		table.Indexes = append(table.Indexes, lowered)
	}
	return table, nil
}

func (state *lowerState) lowerColumn(model ir.ModelDeclIR, field ir.FieldIR, ordinal uint32) (physical.PhysicalColumn, []physical.PhysicalCheck, error) {
	storage, err := state.storage(field.Scalar.Type)
	if err != nil {
		return physical.PhysicalColumn{}, nil, fmt.Errorf("sqlite lower: model=%s field=%s: %w", model.ID, field.ID, err)
	}
	column := physical.PhysicalColumn{
		ID:       field.ID,
		Name:     physical.PhysicalName(field.Scalar.Column),
		Ordinal:  ordinal,
		Storage:  storage,
		Nullable: field.Scalar.Nullable,
		Default:  physical.PhysicalDefault{Kind: physical.DefaultNone},
	}
	if field.Scalar.Default != nil {
		column.Default, err = state.lowerDefault(field.Scalar.Type, storage, *field.Scalar.Default)
		if err != nil {
			return physical.PhysicalColumn{}, nil, fmt.Errorf("sqlite lower: model=%s field=%s default: %w", model.ID, field.ID, err)
		}
	}
	if field.Scalar.Generation != nil {
		if field.Scalar.Generation.Provider == ir.ProviderScopePostgreSQL {
			return physical.PhysicalColumn{}, nil, fmt.Errorf("sqlite lower: generated field model=%s field=%s is PostgreSQL-scoped", model.ID, field.ID)
		}
		expression, expressionErr := state.lowerExpression(model.ID, field.Scalar.Generation.Expr)
		if expressionErr != nil {
			return physical.PhysicalColumn{}, nil, fmt.Errorf("sqlite lower: generated field model=%s field=%s: %w", model.ID, field.ID, expressionErr)
		}
		kind := physical.GeneratedStored
		if field.Scalar.Generation.Storage == ir.GeneratedVirtual {
			kind = physical.GeneratedVirtual
		}
		column.Generated = &physical.GeneratedExpression{Kind: kind, Expression: expression}
		owner := physical.ObjectRef{Kind: ir.ObjectField, ModelID: model.ID, FieldID: field.ID}
		column.RequiredCapabilities = append(column.RequiredCapabilities, physical.CapabilityRequirement{Capability: CapabilityGeneratedColumns, Owner: owner})
	}

	checks := state.syntheticChecks(model, field, storage)
	if requiresJSON1(field.Scalar.Type) {
		owner := physical.ObjectRef{Kind: ir.ObjectField, ModelID: model.ID, FieldID: field.ID}
		column.RequiredCapabilities = append(column.RequiredCapabilities, physical.CapabilityRequirement{Capability: CapabilityJSON1, Owner: owner})
	}
	return column, checks, nil
}

func (state *lowerState) storage(logical ir.LogicalTypeIR) (physical.StorageType, error) {
	if logical.JSONSchemaID != nil && logical.Kind != ir.TypeJSON {
		return physical.StorageType{}, fmt.Errorf("JSON schema identity is invalid for %s", logical.Kind)
	}
	if logical.Capability != nil {
		if logical.Kind != ir.TypeScalarList || *logical.Capability != ir.CapabilityID("scalar-list:json-array:v1") {
			return physical.StorageType{}, fmt.Errorf("unsupported logical capability %q on %s", *logical.Capability, logical.Kind)
		}
	}
	if logical.Kind == ir.TypeScalarList {
		if logical.Element == nil {
			return physical.StorageType{}, fmt.Errorf("scalar list has no element type")
		}
		if _, err := scalarListDescriptor(*logical.Element, state.enums); err != nil {
			return physical.StorageType{}, err
		}
	}
	return physical.ExpectedStorage(ir.SQLite, logical)
}

func (state *lowerState) lowerDefault(logical ir.LogicalTypeIR, storage physical.StorageType, value ir.DefaultIR) (physical.PhysicalDefault, error) {
	switch value.Producer {
	case ir.ProducerApplication:
		return physical.PhysicalDefault{Kind: physical.DefaultNone}, nil
	case ir.ProducerDatabase:
		if value.Kind == ir.DefaultIdentity {
			expression := physical.Expression{Kind: physical.ExpressionFunction, Type: storage, Symbol: semanticSymbol("sqlite.identity", ir.SchemaSymbolFunction, ir.ProviderScopeSQLite)}
			return physical.PhysicalDefault{Kind: physical.DefaultProvider, Expression: &expression}, nil
		}
		if value.Kind != ir.DefaultLiteral || value.Literal == nil {
			return physical.PhysicalDefault{}, fmt.Errorf("database default kind %q is not accepted", value.Kind)
		}
		literal, err := sqliteLiteral(logical, *value.Literal)
		if err != nil {
			return physical.PhysicalDefault{}, err
		}
		return physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &literal}, nil
	case ir.ProducerProvider:
		return physical.PhysicalDefault{}, fmt.Errorf("provider default %v has no registered SQLite renderer", value.Provider)
	default:
		return physical.PhysicalDefault{}, fmt.Errorf("unknown default producer %q", value.Producer)
	}
}

func sqliteLiteral(logical ir.LogicalTypeIR, literal ir.TypedLiteralIR) (ir.TypedLiteralIR, error) {
	if logical.Kind == ir.TypeDateTime {
		value, err := time.Parse(time.RFC3339Nano, literal.Canonical)
		if err != nil {
			return ir.TypedLiteralIR{}, fmt.Errorf("invalid DateTime literal %q", literal.Canonical)
		}
		return ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: fmt.Sprint(value.UTC().UnixMicro())}, nil
	}
	if logical.Kind != ir.TypeDecimal {
		return literal, nil
	}
	if logical.Scale == nil {
		return ir.TypedLiteralIR{}, fmt.Errorf("decimal literal requires scale")
	}
	rational, ok := new(big.Rat).SetString(literal.Canonical)
	if !ok {
		return ir.TypedLiteralIR{}, fmt.Errorf("invalid decimal literal %q", literal.Canonical)
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(*logical.Scale)), nil)
	rational.Mul(rational, new(big.Rat).SetInt(factor))
	if !rational.IsInt() || !rational.Num().IsInt64() {
		return ir.TypedLiteralIR{}, fmt.Errorf("decimal literal %q is not an exact SQLite scaled integer", literal.Canonical)
	}
	return ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: rational.Num().String()}, nil
}

func (state *lowerState) syntheticChecks(model ir.ModelDeclIR, field ir.FieldIR, storage physical.StorageType) []physical.PhysicalCheck {
	logical := field.Scalar.Type
	column := columnExpression(field.ID, storage, field.Scalar.Nullable)
	var checks []syntheticCheck
	switch logical.Kind {
	case ir.TypeBool:
		checks = append(checks, syntheticCheck{rule: "bool", symbol: "sqlite.check.bool-domain", operands: []physical.Expression{column}})
	case ir.TypeInt16:
		checks = append(checks, rangeCheck("int16", column, math.MinInt16, math.MaxInt16))
	case ir.TypeInt32:
		checks = append(checks, rangeCheck("int32", column, math.MinInt32, math.MaxInt32))
	case ir.TypeDecimal:
		if logical.Precision != nil {
			limit := new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(*logical.Precision)), nil), big.NewInt(1))
			checks = append(checks, syntheticCheck{rule: "decimal", symbol: "sqlite.check.integer-range", operands: []physical.Expression{column, integerExpression(new(big.Int).Neg(limit).String()), integerExpression(limit.String())}})
		}
	case ir.TypeUUID:
		checks = append(checks, syntheticCheck{rule: "uuid", symbol: "sqlite.check.uuid-canonical", operands: []physical.Expression{column}})
	case ir.TypeDate:
		checks = append(checks, syntheticCheck{rule: "date", symbol: "sqlite.check.date-canonical", operands: []physical.Expression{column}})
	case ir.TypeTime:
		precision := uint16(6)
		if logical.Precision != nil {
			precision = *logical.Precision
		}
		checks = append(checks, syntheticCheck{rule: "time", symbol: "sqlite.check.time-canonical", operands: []physical.Expression{column, integerExpression(fmt.Sprint(precision))}})
	case ir.TypeDateTime:
		precision := uint16(6)
		if logical.Precision != nil {
			precision = *logical.Precision
		}
		step := int64(1)
		for count := precision; count < 6; count++ {
			step *= 10
		}
		checks = append(checks, syntheticCheck{rule: "datetime", symbol: "sqlite.check.datetime-precision", operands: []physical.Expression{column, integerExpression(fmt.Sprint(step))}})
	case ir.TypeString:
		if logical.MaxLength != nil {
			checks = append(checks, syntheticCheck{rule: "string_length", symbol: "sqlite.check.max-length", operands: []physical.Expression{column, integerExpression(fmt.Sprint(*logical.MaxLength))}})
		}
	case ir.TypeBytes:
		if logical.MaxLength != nil {
			checks = append(checks, syntheticCheck{rule: "bytes_length", symbol: "sqlite.check.max-length", operands: []physical.Expression{column, integerExpression(fmt.Sprint(*logical.MaxLength))}})
		}
	case ir.TypeEnum:
		if logical.EnumID != nil {
			if enum, exists := state.enums[*logical.EnumID]; exists {
				operands := []physical.Expression{column}
				for _, value := range enum.Values {
					operands = append(operands, stringExpression(value.WireValue))
				}
				checks = append(checks, syntheticCheck{rule: "enum", symbol: "sqlite.check.enum-membership", operands: operands})
			}
		}
	case ir.TypeJSON:
		checks = append(checks, syntheticCheck{rule: "json", symbol: "sqlite.check.json-valid", operands: []physical.Expression{column}, capability: CapabilityJSON1})
	case ir.TypeScalarList:
		checks = append(checks, syntheticCheck{rule: "json_array", symbol: "sqlite.check.json-array", operands: []physical.Expression{column}, capability: CapabilityJSON1})
	}
	result := make([]physical.PhysicalCheck, 0, len(checks))
	for _, check := range checks {
		checkID := ir.CheckID(derivedID("sqlite-check", string(model.ID), string(field.ID), check.rule))
		owner := physical.ObjectRef{Kind: ir.ObjectCheck, ModelID: model.ID, ObjectID: ir.ObjectID(checkID)}
		requirements := []physical.CapabilityRequirement(nil)
		if check.capability != "" {
			requirements = append(requirements, physical.CapabilityRequirement{Capability: check.capability, Owner: owner})
		}
		result = append(result, physical.PhysicalCheck{
			ID: checkID, Name: generatedName("ck", string(model.Table.PhysicalName), string(field.Scalar.Column), check.rule, string(checkID)),
			Expression:           physical.Expression{Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Symbol: semanticSymbol(check.symbol, ir.SchemaSymbolOperator, ir.ProviderScopeSQLite), Operands: check.operands},
			RequiredCapabilities: requirements,
		})
	}
	return result
}

type syntheticCheck struct {
	rule       string
	symbol     string
	operands   []physical.Expression
	capability ir.CapabilityID
}

func rangeCheck(rule string, column physical.Expression, minimum, maximum int64) syntheticCheck {
	return syntheticCheck{rule: rule, symbol: "sqlite.check.integer-range", operands: []physical.Expression{column, integerExpression(fmt.Sprint(minimum)), integerExpression(fmt.Sprint(maximum))}}
}

func (state *lowerState) lowerForeignKeys(table *physical.PhysicalTable) error {
	for _, relation := range state.model.Relations {
		if relation.SourceModel != table.ID || relation.ForeignKey == nil {
			continue
		}
		if relation.ForeignKey.Match != ir.MatchSimple {
			return fmt.Errorf("sqlite lower: foreign key %s model=%s match=%q is unsupported; P1 requires MATCH SIMPLE", relation.ForeignKey.ID, table.ID, relation.ForeignKey.Match)
		}
		foreignKey := physical.PhysicalForeignKey{
			ID: relation.ForeignKey.ID, Name: physical.PhysicalName(relation.ForeignKey.PhysicalName),
			Columns: append([]ir.FieldID(nil), relation.LocalFields...), ReferencedTable: relation.TargetModel, ReferencedColumns: append([]ir.FieldID(nil), relation.RemoteFields...),
			OnUpdate: relation.ForeignKey.OnUpdate, OnDelete: relation.ForeignKey.OnDelete, Deferrable: relation.ForeignKey.Deferrable,
		}
		owner := physical.ObjectRef{Kind: ir.ObjectForeignKey, ModelID: table.ID, ObjectID: ir.ObjectID(foreignKey.ID)}
		foreignKey.RequiredCapabilities = []physical.CapabilityRequirement{{Capability: physical.CapabilitySQLiteForeignKeys, Owner: owner}}
		table.ForeignKeys = append(table.ForeignKeys, foreignKey)
	}
	return nil
}

func (state *lowerState) lowerIndex(modelID ir.ModelID, index ir.IndexIR) (physical.PhysicalIndex, error) {
	if index.Provider == ir.ProviderScopePostgreSQL {
		return physical.PhysicalIndex{}, fmt.Errorf("sqlite lower: index=%s model=%s is PostgreSQL-scoped", index.ID, modelID)
	}
	if index.Method != ir.IndexBTree {
		return physical.PhysicalIndex{}, fmt.Errorf("sqlite lower: index=%s model=%s method=%s is unsupported", index.ID, modelID, index.Method)
	}
	if len(index.Include) != 0 {
		return physical.PhysicalIndex{}, fmt.Errorf("sqlite lower: index=%s model=%s included columns are unsupported", index.ID, modelID)
	}
	result := physical.PhysicalIndex{ID: index.ID, Name: physical.PhysicalName(index.PhysicalName), Unique: index.Unique, Method: physical.IndexBTree, CreationMode: physical.IndexTransactional}
	owner := physical.ObjectRef{Kind: ir.ObjectIndex, ModelID: modelID, ObjectID: ir.ObjectID(index.ID)}
	for _, key := range index.Keys {
		if key.OpClass != nil || key.Collation != nil {
			return physical.PhysicalIndex{}, fmt.Errorf("sqlite lower: index=%s model=%s opclass/collation requires a registered SQLite extension", index.ID, modelID)
		}
		if key.Nulls != ir.NullsDefault {
			return physical.PhysicalIndex{}, fmt.Errorf("sqlite lower: index=%s model=%s explicit NULL ordering is unsupported", index.ID, modelID)
		}
		lowered := physical.IndexKey{Direction: key.Direction, Nulls: key.Nulls}
		if key.Column != nil {
			fieldID := *key.Column
			lowered.Column = &fieldID
		}
		if key.Expr != nil {
			expression, err := state.lowerExpression(modelID, *key.Expr)
			if err != nil {
				return physical.PhysicalIndex{}, fmt.Errorf("sqlite lower: index=%s expression: %w", index.ID, err)
			}
			lowered.Expression = &expression
			result.RequiredCapabilities = appendCapability(result.RequiredCapabilities, CapabilityExpressionIndexes, owner)
		}
		result.Keys = append(result.Keys, lowered)
	}
	if index.Predicate != nil {
		predicate, err := state.lowerPredicate(modelID, *index.Predicate)
		if err != nil {
			return physical.PhysicalIndex{}, fmt.Errorf("sqlite lower: index=%s predicate: %w", index.ID, err)
		}
		result.Predicate = &predicate
		result.RequiredCapabilities = appendCapability(result.RequiredCapabilities, CapabilityPartialIndexes, owner)
	}
	return result, nil
}

func (state *lowerState) lowerExpression(modelID ir.ModelID, expression ir.SchemaExprIR) (physical.Expression, error) {
	storage, err := state.storage(expression.ResultType)
	if err != nil {
		return physical.Expression{}, err
	}
	result := physical.Expression{Type: storage, Nullable: expression.Nullable}
	switch expression.Kind {
	case ir.SchemaExprField:
		if expression.Field == nil {
			return physical.Expression{}, fmt.Errorf("field expression has no FieldID")
		}
		result.Kind = physical.ExpressionColumn
		fieldID := *expression.Field
		result.Column = &fieldID
	case ir.SchemaExprLiteral:
		if expression.Literal == nil {
			return physical.Expression{}, fmt.Errorf("literal expression has no typed literal")
		}
		result.Kind = physical.ExpressionLiteral
		literal, literalErr := sqliteLiteral(expression.ResultType, *expression.Literal)
		if literalErr != nil {
			return physical.Expression{}, literalErr
		}
		result.Literal = &literal
	case ir.SchemaExprOperator, ir.SchemaExprFunction, ir.SchemaExprCast:
		if expression.Symbol == nil {
			return physical.Expression{}, fmt.Errorf("registered expression has no symbol")
		}
		if expression.Symbol.Provider == ir.ProviderScopePostgreSQL {
			return physical.Expression{}, fmt.Errorf("symbol %s is PostgreSQL-scoped", expression.Symbol.Identity)
		}
		result.Kind = mapExpressionKind(expression.Kind)
		result.Symbol = &physical.SemanticSymbol{Identity: expression.Symbol.Identity, Kind: expression.Symbol.Kind, Version: expression.Symbol.Version, Provider: expression.Symbol.Provider}
		for _, operand := range expression.Operands {
			lowered, operandErr := state.lowerExpression(modelID, operand)
			if operandErr != nil {
				return physical.Expression{}, operandErr
			}
			result.Operands = append(result.Operands, lowered)
		}
	default:
		return physical.Expression{}, fmt.Errorf("unsupported schema expression kind %q", expression.Kind)
	}
	return result, nil
}

func (state *lowerState) lowerPredicate(modelID ir.ModelID, predicate ir.SchemaPredicateIR) (physical.Expression, error) {
	result := physical.Expression{Type: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Nullable: predicate.Nullable}
	switch predicate.Kind {
	case ir.SchemaPredicateConstant:
		if predicate.Constant == nil {
			return physical.Expression{}, fmt.Errorf("constant predicate has no value")
		}
		result.Kind = physical.ExpressionLiteral
		literal := ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: fmt.Sprint(*predicate.Constant)}
		result.Literal = &literal
	case ir.SchemaPredicateOperator:
		if predicate.Symbol == nil || predicate.Symbol.Provider == ir.ProviderScopePostgreSQL {
			return physical.Expression{}, fmt.Errorf("predicate has unavailable registered symbol")
		}
		result.Kind = physical.ExpressionOperator
		result.Symbol = &physical.SemanticSymbol{Identity: predicate.Symbol.Identity, Kind: predicate.Symbol.Kind, Version: predicate.Symbol.Version, Provider: predicate.Symbol.Provider}
		for _, operand := range predicate.ExpressionOperands {
			lowered, err := state.lowerExpression(modelID, operand)
			if err != nil {
				return physical.Expression{}, err
			}
			result.Operands = append(result.Operands, lowered)
		}
	case ir.SchemaPredicateAnd, ir.SchemaPredicateOr, ir.SchemaPredicateNot:
		result.Kind = physical.ExpressionOperator
		identity := map[ir.SchemaPredicateKind]string{ir.SchemaPredicateAnd: "sqlite.predicate.and", ir.SchemaPredicateOr: "sqlite.predicate.or", ir.SchemaPredicateNot: "sqlite.predicate.not"}[predicate.Kind]
		result.Symbol = semanticSymbol(identity, ir.SchemaSymbolOperator, ir.ProviderScopeSQLite)
		for _, child := range predicate.Children {
			lowered, err := state.lowerPredicate(modelID, child)
			if err != nil {
				return physical.Expression{}, err
			}
			result.Operands = append(result.Operands, lowered)
		}
	default:
		return physical.Expression{}, fmt.Errorf("unsupported schema predicate kind %q", predicate.Kind)
	}
	return result, nil
}

func lowerKey(key ir.KeyIR) physical.PhysicalKey {
	return physical.PhysicalKey{ID: key.ID, Name: physical.PhysicalName(key.PhysicalName), Columns: append([]ir.FieldID(nil), key.Fields...)}
}

func mapExpressionKind(kind ir.SchemaExprKind) physical.ExpressionKind {
	switch kind {
	case ir.SchemaExprOperator:
		return physical.ExpressionOperator
	case ir.SchemaExprCast:
		return physical.ExpressionCast
	default:
		return physical.ExpressionFunction
	}
}

func semanticSymbol(identity string, kind ir.SchemaSymbolKind, provider ir.ProviderScope) *physical.SemanticSymbol {
	return &physical.SemanticSymbol{Identity: identity, Kind: kind, Version: 1, Provider: provider}
}

func columnExpression(fieldID ir.FieldID, storage physical.StorageType, nullable bool) physical.Expression {
	return physical.Expression{Kind: physical.ExpressionColumn, Type: storage, Nullable: nullable, Column: &fieldID}
}

func integerExpression(value string) physical.Expression {
	literal := ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: value}
	return physical.Expression{Kind: physical.ExpressionLiteral, Type: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Literal: &literal}
}

func stringExpression(value string) physical.Expression {
	literal := ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: value}
	return physical.Expression{Kind: physical.ExpressionLiteral, Type: physical.StorageType{Kind: physical.StorageSQLiteText}, Literal: &literal}
}

func appendCapability(values []physical.CapabilityRequirement, capability ir.CapabilityID, owner physical.ObjectRef) []physical.CapabilityRequirement {
	for _, existing := range values {
		if existing.Capability == capability {
			return values
		}
	}
	return append(values, physical.CapabilityRequirement{Capability: capability, Owner: owner})
}

func generatedName(parts ...string) physical.PhysicalName {
	digest := parts[len(parts)-1]
	prefix := strings.Join(parts[:len(parts)-1], "_")
	const suffixLength = 10
	if len(digest) > suffixLength {
		digest = digest[:suffixLength]
	}
	maximumPrefix := 63 - 1 - len(digest)
	if len(prefix) > maximumPrefix {
		prefix = strings.TrimRight(prefix[:maximumPrefix], "_")
	}
	return physical.PhysicalName(prefix + "_" + digest)
}

func derivedID(domain string, parts ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:" + domain + ":v1\x00"))
	for _, part := range parts {
		_, _ = hash.Write([]byte(fmt.Sprintf("%d:", len(part))))
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func requiresJSON1(logical ir.LogicalTypeIR) bool {
	return logical.Kind == ir.TypeJSON || logical.Kind == ir.TypeScalarList
}

func containsProvider(providers []ir.Provider, wanted ir.Provider) bool {
	for _, provider := range providers {
		if provider == wanted {
			return true
		}
	}
	return false
}

func scalarListDescriptor(element ir.LogicalTypeIR, enums map[ir.EnumID]ir.EnumIR) (string, error) {
	switch element.Kind {
	case ir.TypeString, ir.TypeBool, ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeFloat32, ir.TypeFloat64, ir.TypeUUID, ir.TypeDate, ir.TypeTime, ir.TypeDateTime:
		precision := ""
		if element.Precision != nil {
			precision = fmt.Sprintf(":p%d", *element.Precision)
		}
		return string(element.Kind) + precision, nil
	case ir.TypeDecimal:
		if element.Precision == nil || element.Scale == nil {
			return "", fmt.Errorf("scalar-list Decimal requires precision and scale")
		}
		return fmt.Sprintf("decimal:p%d:s%d", *element.Precision, *element.Scale), nil
	case ir.TypeEnum:
		if element.EnumID == nil {
			return "", fmt.Errorf("scalar-list enum requires EnumID")
		}
		enum, exists := enums[*element.EnumID]
		if !exists {
			return "", fmt.Errorf("scalar-list enum %s not found", *element.EnumID)
		}
		values := make([]string, len(enum.Values))
		for index, value := range enum.Values {
			values[index] = strconv.Quote(value.WireValue)
		}
		return "enum:" + string(*element.EnumID) + ":[" + strings.Join(values, ",") + "]", nil
	default:
		return "", fmt.Errorf("%s is not an accepted non-null scalar-list element", element.Kind)
	}
}
