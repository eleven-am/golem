package postgresql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/scalar"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	"github.com/eleven-am/golem/go/internal/physical"
)

const capabilityAdvancedIndexes ir.CapabilityID = "postgresql.advanced_indexes"

func (provider *Provider) lower(ctx context.Context, model ir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	if err := ctx.Err(); err != nil {
		return physical.PhysicalSchema{}, err
	}
	targeted := false
	for _, target := range model.Providers {
		if target == ir.PostgreSQL {
			targeted = true
		}
	}
	if !targeted {
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql lower: ModelIR does not target PostgreSQL")
	}
	namespace := options.Namespace
	if namespace == "" {
		namespace = "public"
	}
	schema := physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: provider.Manifest(), Namespace: physical.Namespace{Name: namespace},
		System: systemSchema(),
	}
	enums := make(map[ir.EnumID]ir.EnumIR, len(model.Enums))
	for _, enum := range model.Enums {
		enums[enum.ID] = enum
	}
	for _, declaration := range model.Models {
		table, err := lowerTable(declaration, enums)
		if err != nil {
			return physical.PhysicalSchema{}, err
		}
		schema.Tables = append(schema.Tables, table)
	}
	tables := make(map[ir.ModelID]*physical.PhysicalTable, len(schema.Tables))
	for index := range schema.Tables {
		tables[schema.Tables[index].ID] = &schema.Tables[index]
	}
	for _, relation := range model.Relations {
		if relation.ForeignKey == nil {
			continue
		}
		table := tables[relation.SourceModel]
		if table == nil {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql lower: relation %s has unknown source model %s", relation.ID, relation.SourceModel)
		}
		foreign := relation.ForeignKey
		if foreign.Match != ir.MatchSimple {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql lower: foreign key %s uses unsupported match kind %q", foreign.ID, foreign.Match)
		}
		table.ForeignKeys = append(table.ForeignKeys, physical.PhysicalForeignKey{
			ID: foreign.ID, Name: physical.PhysicalName(foreign.PhysicalName),
			Columns: append([]ir.FieldID(nil), relation.LocalFields...), ReferencedTable: relation.TargetModel,
			ReferencedColumns: append([]ir.FieldID(nil), relation.RemoteFields...), OnUpdate: foreign.OnUpdate,
			OnDelete: foreign.OnDelete, Deferrable: foreign.Deferrable,
		})
	}
	for _, extension := range model.Extensions {
		if extension.Provider != ir.PostgreSQL {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql lower: extension %q is scoped to %s", extension.Kind, extension.Provider)
		}
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql lower: unsupported registered extension %q owned by %s", extension.Kind, extension.Owner)
	}
	return physical.Normalize(schema)
}

func lowerTable(model ir.ModelDeclIR, enums map[ir.EnumID]ir.EnumIR) (physical.PhysicalTable, error) {
	table := physical.PhysicalTable{ID: model.ID, Name: physical.PhysicalName(model.Table.PhysicalName)}
	var persistedOrdinal uint32
	fields := append([]ir.FieldIR(nil), model.Fields...)
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].DeclarationOrder != fields[j].DeclarationOrder {
			return fields[i].DeclarationOrder < fields[j].DeclarationOrder
		}
		return fields[i].ID < fields[j].ID
	})
	for _, field := range fields {
		if field.Kind == ir.FieldRelation || field.Scalar == nil {
			continue
		}
		column, checks, err := lowerColumn(model.ID, field, enums)
		if err != nil {
			return physical.PhysicalTable{}, fmt.Errorf("postgresql lower %s.%s: %w", model.LogicalName, field.LogicalName, err)
		}
		column.Ordinal = persistedOrdinal
		persistedOrdinal++
		table.Columns = append(table.Columns, column)
		table.Checks = append(table.Checks, checks...)
	}
	if model.PrimaryKey != nil {
		key := lowerKey(*model.PrimaryKey)
		table.PrimaryKey = &key
	}
	for _, key := range model.Uniques {
		table.Uniques = append(table.Uniques, lowerKey(key))
	}
	for _, check := range model.Checks {
		if check.Provider == ir.ProviderScopeSQLite {
			return physical.PhysicalTable{}, fmt.Errorf("check %s is scoped to SQLite", check.ID)
		}
		expression, err := lowerPredicate(check.Predicate)
		if err != nil {
			return physical.PhysicalTable{}, fmt.Errorf("check %s: %w", check.ID, err)
		}
		table.Checks = append(table.Checks, physical.PhysicalCheck{ID: check.ID, Name: physical.PhysicalName(check.PhysicalName), Expression: expression})
	}
	for _, index := range model.Indexes {
		if index.Provider == ir.ProviderScopeSQLite {
			return physical.PhysicalTable{}, fmt.Errorf("index %s is scoped to SQLite", index.ID)
		}
		lowered, err := lowerIndex(model.ID, index)
		if err != nil {
			return physical.PhysicalTable{}, err
		}
		table.Indexes = append(table.Indexes, lowered)
	}
	return table, nil
}

func lowerColumn(modelID ir.ModelID, field ir.FieldIR, enums map[ir.EnumID]ir.EnumIR) (physical.PhysicalColumn, []physical.PhysicalCheck, error) {
	scalar := field.Scalar
	if err := validateLogicalStorageType(scalar.Type, enums); err != nil {
		return physical.PhysicalColumn{}, nil, err
	}
	storage, err := postgresStorage(scalar.Type)
	if err != nil {
		return physical.PhysicalColumn{}, nil, err
	}
	column := physical.PhysicalColumn{ID: field.ID, Name: physical.PhysicalName(scalar.Column), Ordinal: field.DeclarationOrder, Storage: storage, Nullable: scalar.Nullable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}
	owner := physical.ObjectRef{Kind: ir.ObjectField, ModelID: modelID, FieldID: field.ID}
	if scalar.Type.Kind == ir.TypeJSON || scalar.Type.Kind == ir.TypeScalarList {
		column.RequiredCapabilities = append(column.RequiredCapabilities, physical.CapabilityRequirement{Capability: CapabilityJSONB, Owner: owner})
	}
	if scalar.Default != nil {
		column.Default, err = lowerDefault(*scalar.Default, storage)
		if err != nil {
			return physical.PhysicalColumn{}, nil, err
		}
	}
	if scalar.Generation != nil {
		if !scopeSupportsPostgreSQL(scalar.Generation.Provider) || scalar.Generation.Storage != ir.GeneratedStored {
			return physical.PhysicalColumn{}, nil, fmt.Errorf("generated column form is not supported by PostgreSQL 15 baseline")
		}
		expression, expressionErr := lowerExpression(scalar.Generation.Expr)
		if expressionErr != nil {
			return physical.PhysicalColumn{}, nil, expressionErr
		}
		column.Generated = &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: expression}
		column.RequiredCapabilities = append(column.RequiredCapabilities, physical.CapabilityRequirement{Capability: CapabilityGeneratedColumns, Owner: owner})
	}
	checks := syntheticChecks(modelID, field, enums, storage)
	return column, checks, nil
}

func postgresStorage(logical ir.LogicalTypeIR) (physical.StorageType, error) {
	return physical.ExpectedStorage(ir.PostgreSQL, logical)
}

func lowerDefault(value ir.DefaultIR, storage physical.StorageType) (physical.PhysicalDefault, error) {
	switch value.Kind {
	case ir.DefaultUUID, ir.DefaultNow:
		return physical.PhysicalDefault{Kind: physical.DefaultNone}, nil
	case ir.DefaultLiteral:
		if value.Producer != ir.ProducerDatabase || value.Literal == nil {
			return physical.PhysicalDefault{}, fmt.Errorf("literal default must be database-owned and typed")
		}
		literal, err := normalizePhysicalLiteral(*value.Literal, storage)
		if err != nil {
			return physical.PhysicalDefault{}, err
		}
		return physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &literal}, nil
	case ir.DefaultIdentity:
		if storage.Kind != physical.StoragePostgreSQLSmallInt && storage.Kind != physical.StoragePostgreSQLInteger && storage.Kind != physical.StoragePostgreSQLBigInt {
			return physical.PhysicalDefault{}, fmt.Errorf("PostgreSQL identity requires an integer storage type")
		}
		return physical.PhysicalDefault{Kind: physical.DefaultProvider, Expression: &physical.Expression{Kind: physical.ExpressionFunction, Type: storage, Symbol: postgresSymbol("golem.postgresql.default.identity.v1", ir.SchemaSymbolFunction)}}, nil
	case ir.DefaultProvider:
		return physical.PhysicalDefault{}, fmt.Errorf("unregistered PostgreSQL provider default")
	default:
		return physical.PhysicalDefault{}, fmt.Errorf("unsupported default kind %q", value.Kind)
	}
}

func lowerKey(value ir.KeyIR) physical.PhysicalKey {
	return physical.PhysicalKey{ID: value.ID, Name: physical.PhysicalName(value.PhysicalName), Columns: append([]ir.FieldID(nil), value.Fields...)}
}

func lowerIndex(modelID ir.ModelID, value ir.IndexIR) (physical.PhysicalIndex, error) {
	var method physical.IndexMethod
	switch value.Method {
	case ir.IndexBTree:
		method = physical.IndexBTree
	default:
		return physical.PhysicalIndex{}, fmt.Errorf("postgresql lower: unsupported index method %q", value.Method)
	}
	result := physical.PhysicalIndex{ID: value.ID, Name: physical.PhysicalName(value.PhysicalName), Unique: value.Unique, Method: method, CreationMode: physical.IndexTransactional, Include: append([]ir.FieldID(nil), value.Include...)}
	advanced := len(value.Include) > 0 || value.Predicate != nil
	for _, key := range value.Keys {
		item := physical.IndexKey{Column: cloneField(key.Column), Direction: key.Direction, Nulls: key.Nulls}
		if key.Expr != nil {
			expression, err := lowerExpression(*key.Expr)
			if err != nil {
				return physical.PhysicalIndex{}, err
			}
			item.Expression = &expression
			advanced = true
		}
		if key.Collation != nil || key.OpClass != nil {
			return physical.PhysicalIndex{}, fmt.Errorf("postgresql lower: index %s on model %s uses an unregistered collation/operator class extension", value.ID, modelID)
		}
		if key.Direction != ir.SortAsc || key.Nulls != ir.NullsDefault {
			advanced = true
		}
		result.Keys = append(result.Keys, item)
	}
	if value.Predicate != nil {
		predicate, err := lowerPredicate(*value.Predicate)
		if err != nil {
			return physical.PhysicalIndex{}, err
		}
		result.Predicate = &predicate
	}
	if advanced {
		owner := physical.ObjectRef{Kind: ir.ObjectIndex, ModelID: modelID, ObjectID: ir.ObjectID(value.ID)}
		result.RequiredCapabilities = []physical.CapabilityRequirement{{Capability: capabilityAdvancedIndexes, Owner: owner}}
	}
	return result, nil
}

func syntheticChecks(modelID ir.ModelID, field ir.FieldIR, enums map[ir.EnumID]ir.EnumIR, storage physical.StorageType) []physical.PhysicalCheck {
	logical := field.Scalar.Type
	var result []physical.PhysicalCheck
	add := func(kind string, expression physical.Expression) {
		id := stableID("check", string(modelID), string(field.ID), kind)
		result = append(result, physical.PhysicalCheck{ID: ir.CheckID(id), Name: generatedName("ck", kind, id), Expression: expression})
	}
	column := physical.Expression{Kind: physical.ExpressionColumn, Type: storage, Nullable: field.Scalar.Nullable, Column: cloneField(&field.ID)}
	if logical.Kind == ir.TypeEnum && logical.EnumID != nil {
		operands := []physical.Expression{column}
		for _, value := range enums[*logical.EnumID].Values {
			literal := ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: value.WireValue}
			operands = append(operands, physical.Expression{Kind: physical.ExpressionLiteral, Type: storage, Literal: &literal})
		}
		add("enum", operator(schemaexpr.In, operands...))
	}
	if logical.Kind == ir.TypeScalarList {
		literal := ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: "array"}
		typeof := physical.Expression{Kind: physical.ExpressionFunction, Type: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: field.Scalar.Nullable, Symbol: postgresSymbol("golem.postgresql.function.jsonb-typeof.v1", ir.SchemaSymbolFunction), Operands: []physical.Expression{column}}
		add("json_array", operator(schemaexpr.Equal, typeof, physical.Expression{Kind: physical.ExpressionLiteral, Type: typeof.Type, Literal: &literal}))
	}
	if logical.MaxLength != nil && (logical.Kind == ir.TypeString || logical.Kind == ir.TypeBytes) {
		literal := ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: fmt.Sprint(*logical.MaxLength)}
		function := "golem.schema.function.length.v1"
		if logical.Kind == ir.TypeBytes {
			function = "golem.postgresql.function.octet-length.v1"
		}
		length := physical.Expression{Kind: physical.ExpressionFunction, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}, Nullable: field.Scalar.Nullable, Symbol: semanticSymbol(function, ir.SchemaSymbolFunction), Operands: []physical.Expression{column}}
		add("max_length", operator(schemaexpr.LessEqual, length, physical.Expression{Kind: physical.ExpressionLiteral, Type: length.Type, Literal: &literal}))
	}
	return result
}

func lowerExpression(value ir.SchemaExprIR) (physical.Expression, error) {
	typeValue, err := postgresStorage(value.ResultType)
	if err != nil {
		return physical.Expression{}, err
	}
	result := physical.Expression{Type: typeValue, Nullable: value.Nullable}
	switch value.Kind {
	case ir.SchemaExprField:
		result.Kind, result.Column = physical.ExpressionColumn, cloneField(value.Field)
	case ir.SchemaExprLiteral:
		result.Kind = physical.ExpressionLiteral
		if value.Literal != nil {
			literal := *value.Literal
			result.Literal = &literal
		}
	case ir.SchemaExprOperator, ir.SchemaExprFunction, ir.SchemaExprCast:
		if value.Symbol == nil {
			return physical.Expression{}, fmt.Errorf("expression lacks registered symbol")
		}
		result.Kind = mapExpressionKind(value.Kind)
		result.Symbol = &physical.SemanticSymbol{Identity: value.Symbol.Identity, Kind: value.Symbol.Kind, Version: value.Symbol.Version, Provider: value.Symbol.Provider}
		for _, operand := range value.Operands {
			converted, convertErr := lowerExpression(operand)
			if convertErr != nil {
				return physical.Expression{}, convertErr
			}
			result.Operands = append(result.Operands, converted)
		}
	default:
		return physical.Expression{}, fmt.Errorf("unsupported expression kind %q", value.Kind)
	}
	return result, nil
}

func lowerPredicate(value ir.SchemaPredicateIR) (physical.Expression, error) {
	boolean := physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}
	switch value.Kind {
	case ir.SchemaPredicateConstant:
		if value.Constant == nil {
			return physical.Expression{}, fmt.Errorf("constant predicate lacks value")
		}
		literal := ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: fmt.Sprint(*value.Constant)}
		return physical.Expression{Kind: physical.ExpressionLiteral, Type: boolean, Literal: &literal}, nil
	case ir.SchemaPredicateOperator:
		if value.Symbol == nil {
			return physical.Expression{}, fmt.Errorf("operator predicate lacks symbol")
		}
		result := physical.Expression{Kind: physical.ExpressionOperator, Type: boolean, Nullable: value.Nullable, Symbol: &physical.SemanticSymbol{Identity: value.Symbol.Identity, Kind: value.Symbol.Kind, Version: value.Symbol.Version, Provider: value.Symbol.Provider}}
		for _, operand := range value.ExpressionOperands {
			converted, err := lowerExpression(operand)
			if err != nil {
				return physical.Expression{}, err
			}
			result.Operands = append(result.Operands, converted)
		}
		return result, nil
	case ir.SchemaPredicateAnd, ir.SchemaPredicateOr, ir.SchemaPredicateNot:
		identity := map[ir.SchemaPredicateKind]string{ir.SchemaPredicateAnd: "golem.schema.predicate.and.v1", ir.SchemaPredicateOr: "golem.schema.predicate.or.v1", ir.SchemaPredicateNot: "golem.schema.predicate.not.v1"}[value.Kind]
		result := physical.Expression{Kind: physical.ExpressionOperator, Type: boolean, Nullable: value.Nullable, Symbol: semanticSymbol(identity, ir.SchemaSymbolOperator)}
		for _, child := range value.Children {
			converted, err := lowerPredicate(child)
			if err != nil {
				return physical.Expression{}, err
			}
			result.Operands = append(result.Operands, converted)
		}
		return result, nil
	default:
		return physical.Expression{}, fmt.Errorf("unsupported predicate kind %q", value.Kind)
	}
}

func operator(identity string, operands ...physical.Expression) physical.Expression {
	return physical.Expression{Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}, Nullable: anyNullable(operands), Symbol: semanticSymbol(identity, ir.SchemaSymbolOperator), Operands: operands}
}

func anyNullable(values []physical.Expression) bool {
	for _, value := range values {
		if value.Nullable {
			return true
		}
	}
	return false
}
func postgresSymbol(identity string, kind ir.SchemaSymbolKind) *physical.SemanticSymbol {
	return &physical.SemanticSymbol{Identity: identity, Kind: kind, Version: 1, Provider: ir.ProviderScopePostgreSQL}
}
func semanticSymbol(identity string, kind ir.SchemaSymbolKind) *physical.SemanticSymbol {
	if strings.HasPrefix(identity, "golem.schema.") {
		return &physical.SemanticSymbol{Identity: identity, Kind: kind, Version: 1, Provider: ir.ProviderScopePortable}
	}
	return postgresSymbol(identity, kind)
}
func cloneField(value *ir.FieldID) *ir.FieldID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func mapExpressionKind(value ir.SchemaExprKind) physical.ExpressionKind {
	if value == ir.SchemaExprFunction {
		return physical.ExpressionFunction
	}
	if value == ir.SchemaExprCast {
		return physical.ExpressionCast
	}
	return physical.ExpressionOperator
}
func scopeSupportsPostgreSQL(value ir.ProviderScope) bool {
	return value == ir.ProviderScopePortable || value == ir.ProviderScopePostgreSQL
}

func stableID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte{0})
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}
func generatedName(prefix, kind, id string) physical.PhysicalName {
	name := prefix + "_" + kind + "_" + id[:12]
	return physical.PhysicalName(name)
}

func systemSchema() physical.SystemSchema {
	return physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "_golem"}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
	}}
}

func validListElement(kind ir.LogicalTypeKind) bool {
	switch kind {
	case ir.TypeString, ir.TypeBool, ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeFloat32, ir.TypeFloat64, ir.TypeDecimal, ir.TypeUUID, ir.TypeDate, ir.TypeTime, ir.TypeDateTime, ir.TypeEnum:
		return true
	default:
		return false
	}
}

const scalarListJSONCapability ir.CapabilityID = "scalar-list:json-array:v1"

func validateLogicalStorageType(logical ir.LogicalTypeIR, enums map[ir.EnumID]ir.EnumIR) error {
	if logical.Kind == ir.TypeEnum {
		if logical.EnumID == nil {
			return fmt.Errorf("enum storage requires an enum identity")
		}
		if _, exists := enums[*logical.EnumID]; !exists {
			return fmt.Errorf("enum storage references unknown enum %s", *logical.EnumID)
		}
	}
	if logical.Kind != ir.TypeScalarList {
		return nil
	}
	if logical.Capability == nil || *logical.Capability != scalarListJSONCapability {
		return fmt.Errorf("scalar list requires capability %q", scalarListJSONCapability)
	}
	if logical.Element == nil || logical.Element.Element != nil || !validListElement(logical.Element.Kind) {
		return fmt.Errorf("scalar list requires one accepted non-nested element type")
	}
	if logical.JSONSchemaID != nil || logical.Element.JSONSchemaID != nil || logical.Element.Capability != nil {
		return fmt.Errorf("scalar-list JSON schema/capability metadata is not valid on its element")
	}
	if logical.Element.Kind == ir.TypeEnum {
		if logical.Element.EnumID == nil {
			return fmt.Errorf("enum scalar-list element requires enum identity")
		}
		if _, exists := enums[*logical.Element.EnumID]; !exists {
			return fmt.Errorf("scalar-list element references unknown enum %s", *logical.Element.EnumID)
		}
	}
	if logical.Element.Kind == ir.TypeDecimal {
		if _, err := postgresStorage(*logical.Element); err != nil {
			return fmt.Errorf("scalar-list decimal element: %w", err)
		}
	}
	if logical.Element.Kind == ir.TypeTime || logical.Element.Kind == ir.TypeDateTime {
		if _, err := postgresStorage(*logical.Element); err != nil {
			return fmt.Errorf("scalar-list temporal element: %w", err)
		}
	}
	return nil
}

func normalizePhysicalLiteral(value ir.TypedLiteralIR, storage physical.StorageType) (ir.TypedLiteralIR, error) {
	value.Kind = literalKind(storage)
	if storage.Kind == physical.StoragePostgreSQLJSONB {
		canonical, err := scalar.CanonicalJSON([]byte(value.Canonical))
		if err != nil {
			return ir.TypedLiteralIR{}, fmt.Errorf("canonical PostgreSQL JSON literal: %w", err)
		}
		value.Canonical = string(canonical)
	}
	if _, err := renderLiteral(value); err != nil {
		return ir.TypedLiteralIR{}, fmt.Errorf("invalid PostgreSQL physical literal: %w", err)
	}
	return value, nil
}
