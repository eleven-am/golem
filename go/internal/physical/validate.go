package physical

import (
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type ValidationCode string

const (
	CodeFormat             ValidationCode = "PHYSICAL_FORMAT"
	CodeProvider           ValidationCode = "PHYSICAL_PROVIDER"
	CodeIdentifier         ValidationCode = "PHYSICAL_IDENTIFIER"
	CodeDuplicate          ValidationCode = "PHYSICAL_DUPLICATE"
	CodeStableID           ValidationCode = "PHYSICAL_STABLE_ID"
	CodeStorage            ValidationCode = "PHYSICAL_STORAGE"
	CodeExpression         ValidationCode = "PHYSICAL_EXPRESSION"
	CodeConstraint         ValidationCode = "PHYSICAL_CONSTRAINT"
	CodeCapabilityMissing  ValidationCode = "PHYSICAL_CAPABILITY_MISSING"
	CodeCapabilityManifest ValidationCode = "PHYSICAL_CAPABILITY_MANIFEST"
	CodeExtension          ValidationCode = "PHYSICAL_EXTENSION"
)

type ValidationError struct {
	Code       ValidationCode
	Provider   ir.Provider
	Capability ir.CapabilityID
	Owner      ObjectRef
	Path       string
	Detail     string
}

func (e ValidationError) Error() string {
	location := e.Path
	if location == "" {
		location = "physical schema"
	}
	capability := ""
	if e.Capability != "" {
		capability = fmt.Sprintf(" capability=%s", e.Capability)
	}
	owner := ""
	if e.Owner != (ObjectRef{}) {
		owner = fmt.Sprintf(" owner={kind:%s model:%s field:%s object:%s}", e.Owner.Kind, e.Owner.ModelID, e.Owner.FieldID, e.Owner.ObjectID)
	}
	return fmt.Sprintf("%s: %s: provider=%s%s%s: %s", e.Code, location, e.Provider, capability, owner, e.Detail)
}

type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	parts := make([]string, len(e))
	for index, diagnostic := range e {
		parts[index] = diagnostic.Error()
	}
	return strings.Join(parts, "\n")
}

func (e ValidationErrors) Unwrap() []error {
	result := make([]error, len(e))
	for index := range e {
		result[index] = e[index]
	}
	return result
}

var (
	portableIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	systemIdentifier   = regexp.MustCompile(`^_?[a-z][a-z0-9_]*$`)
	semanticIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
)

type validator struct {
	schema       PhysicalSchema
	errors       ValidationErrors
	capabilities map[ir.CapabilityID]CapabilityFact
	tables       map[ir.ModelID]PhysicalTable
	objectNames  map[string]string
}

// Validate proves structural consistency, registered capability availability,
// portable naming, and cross-table key/FK invariants. It does not require input
// collections to already be in canonical order.
func Validate(schema PhysicalSchema) error {
	validation := &validator{
		schema:       schema,
		capabilities: make(map[ir.CapabilityID]CapabilityFact),
		tables:       make(map[ir.ModelID]PhysicalTable),
		objectNames:  make(map[string]string),
	}
	validation.validateHeader()
	validation.indexTables()
	for tableIndex := range schema.Tables {
		validation.validateTable(schema.Tables[tableIndex])
	}
	validation.validateExtensions()
	validation.validateSystem()
	validation.validateUnmanaged()
	if len(validation.errors) == 0 {
		return nil
	}
	sort.SliceStable(validation.errors, func(i, j int) bool {
		a, b := validation.errors[i], validation.errors[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Capability != b.Capability {
			return a.Capability < b.Capability
		}
		return a.Detail < b.Detail
	})
	return validation.errors
}

func (v *validator) add(code ValidationCode, path, detail string) {
	v.errors = append(v.errors, ValidationError{Code: code, Provider: v.schema.Provider.Provider, Path: path, Detail: detail})
}

func (v *validator) validateHeader() {
	if v.schema.Version != SchemaFormatVersion {
		v.add(CodeFormat, "version", fmt.Sprintf("got %d, want %d", v.schema.Version, SchemaFormatVersion))
	}
	if v.schema.CanonicalVersion != CanonicalFormatVersion {
		v.add(CodeFormat, "canonicalVersion", fmt.Sprintf("got %d, want %d", v.schema.CanonicalVersion, CanonicalFormatVersion))
	}
	manifest := v.schema.Provider
	switch manifest.Provider {
	case ir.SQLite:
		current := DriverIdentity{Module: "github.com/ncruces/go-sqlite3", Adapter: "sqlx"}
		historical := DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
		if manifest.Driver != current && manifest.Driver != historical {
			v.add(CodeProvider, "provider.driver", "SQLite driver is not a supported current or historical identity")
		}
		if manifest.MinimumVersion != (Version{Major: 3, Minor: 38}) {
			v.add(CodeProvider, "provider.minimumVersion", "SQLite semantic floor must be 3.38.0")
		}
		if v.schema.Namespace.Name != "main" {
			v.add(CodeProvider, "namespace", "SQLite application namespace must be main")
		}
	case ir.PostgreSQL:
		if manifest.Driver != (DriverIdentity{Module: "github.com/jackc/pgx/v5/stdlib", Adapter: "sqlx"}) {
			v.add(CodeProvider, "provider.driver", "PostgreSQL driver must be pgx/v5/stdlib through sqlx")
		}
		if manifest.MinimumVersion != (Version{Major: 15}) {
			v.add(CodeProvider, "provider.minimumVersion", "PostgreSQL semantic floor must be 15.0.0")
		}
	default:
		v.add(CodeProvider, "provider", fmt.Sprintf("unsupported provider %q", manifest.Provider))
	}
	v.validateName(v.schema.Namespace.Name, "namespace", false)
	for index, capability := range manifest.Capabilities {
		path := fmt.Sprintf("provider.capabilities[%d]", index)
		if !semanticIdentifier.MatchString(string(capability.ID)) {
			v.add(CodeCapabilityManifest, path, fmt.Sprintf("invalid capability ID %q", capability.ID))
		}
		if capability.Version == 0 {
			v.add(CodeCapabilityManifest, path, "capability version must be non-zero")
		}
		switch capability.Verification {
		case VerificationVersionFloor, VerificationRuntimeProbe, VerificationCompileProbe, VerificationExtension:
		default:
			v.add(CodeCapabilityManifest, path, fmt.Sprintf("invalid verification mechanism %q", capability.Verification))
		}
		if _, duplicate := v.capabilities[capability.ID]; duplicate {
			v.add(CodeDuplicate, path, fmt.Sprintf("duplicate capability %q", capability.ID))
		}
		v.capabilities[capability.ID] = capability
	}
}

func (v *validator) indexTables() {
	for index, table := range v.schema.Tables {
		path := fmt.Sprintf("tables[%d]", index)
		v.validateStableID(string(table.ID), path+".id")
		if _, duplicate := v.tables[table.ID]; duplicate {
			v.add(CodeDuplicate, path+".id", fmt.Sprintf("duplicate model ID %s", table.ID))
		}
		v.tables[table.ID] = table
		v.registerName(ir.ObjectModel, table.Name, path+".name")
	}
}

func (v *validator) validateTable(table PhysicalTable) {
	path := "table[" + string(table.ID) + "]"
	v.validateName(table.Name, path+".name", false)
	tableOwner := ObjectRef{Kind: ir.ObjectModel, ModelID: table.ID}
	v.validateRequirements(table.RequiredCapabilities, tableOwner, path)

	columns := make(map[ir.FieldID]PhysicalColumn, len(table.Columns))
	columnNames := make(map[string]ir.FieldID, len(table.Columns))
	ordinals := make(map[uint32]ir.FieldID, len(table.Columns))
	for _, column := range table.Columns {
		columnPath := path + ".column[" + string(column.ID) + "]"
		v.validateStableID(string(column.ID), columnPath+".id")
		if _, duplicate := columns[column.ID]; duplicate {
			v.add(CodeDuplicate, columnPath+".id", fmt.Sprintf("duplicate field ID %s", column.ID))
		}
		columns[column.ID] = column
		v.validateName(column.Name, columnPath+".name", false)
		folded := strings.ToLower(string(column.Name))
		if prior, duplicate := columnNames[folded]; duplicate {
			v.add(CodeDuplicate, columnPath+".name", fmt.Sprintf("column name collides with field %s", prior))
		}
		columnNames[folded] = column.ID
		if prior, duplicate := ordinals[column.Ordinal]; duplicate {
			v.add(CodeDuplicate, columnPath+".ordinal", fmt.Sprintf("ordinal %d also belongs to field %s", column.Ordinal, prior))
		}
		ordinals[column.Ordinal] = column.ID
		v.validateStorage(column.Storage, columnPath+".storage")
		owner := ObjectRef{Kind: ir.ObjectField, ModelID: table.ID, FieldID: column.ID}
		v.validateRequirements(column.RequiredCapabilities, owner, columnPath)
	}
	for _, column := range table.Columns {
		columnPath := path + ".column[" + string(column.ID) + "]"
		v.validateDefault(column.Default, columns, columnPath+".default")
		if column.Generated != nil {
			if column.Default.Kind != DefaultNone {
				v.add(CodeConstraint, columnPath, "generated column cannot also have a default")
			}
			if column.Generated.Kind != GeneratedStored && column.Generated.Kind != GeneratedVirtual {
				v.add(CodeExpression, columnPath+".generated.kind", fmt.Sprintf("invalid generated kind %q", column.Generated.Kind))
			}
			v.validateExpression(column.Generated.Expression, columns, columnPath+".generated.expression")
		}
		if column.Collation != nil {
			v.validateSymbol(*column.Collation, ir.SchemaSymbolFunction, columnPath+".collation")
		}
	}
	for ordinal := uint32(0); ordinal < uint32(len(table.Columns)); ordinal++ {
		if _, exists := ordinals[ordinal]; !exists {
			v.add(CodeConstraint, path+".columns", fmt.Sprintf("column ordinals must be contiguous from zero; missing %d", ordinal))
		}
	}

	keyColumns := make([][]ir.FieldID, 0, len(table.Uniques)+1)
	if table.PrimaryKey != nil {
		v.validateKey(table.ID, *table.PrimaryKey, columns, true, path+".primaryKey")
		keyColumns = append(keyColumns, table.PrimaryKey.Columns)
	}
	seenKeyIDs := make(map[ir.KeyID]struct{})
	if table.PrimaryKey != nil {
		seenKeyIDs[table.PrimaryKey.ID] = struct{}{}
	}
	for _, key := range table.Uniques {
		keyPath := path + ".unique[" + string(key.ID) + "]"
		if _, duplicate := seenKeyIDs[key.ID]; duplicate {
			v.add(CodeDuplicate, keyPath+".id", fmt.Sprintf("duplicate key ID %s", key.ID))
		}
		seenKeyIDs[key.ID] = struct{}{}
		v.validateKey(table.ID, key, columns, false, keyPath)
		keyColumns = append(keyColumns, key.Columns)
	}
	for _, foreignKey := range table.ForeignKeys {
		v.validateForeignKey(table, foreignKey, columns, path, keyColumns)
	}
	seenChecks := make(map[ir.CheckID]struct{})
	for _, check := range table.Checks {
		checkPath := path + ".check[" + string(check.ID) + "]"
		v.validateStableID(string(check.ID), checkPath+".id")
		if _, duplicate := seenChecks[check.ID]; duplicate {
			v.add(CodeDuplicate, checkPath+".id", fmt.Sprintf("duplicate check ID %s", check.ID))
		}
		seenChecks[check.ID] = struct{}{}
		v.validateName(check.Name, checkPath+".name", false)
		v.registerName(ir.ObjectCheck, check.Name, checkPath+".name")
		owner := ObjectRef{Kind: ir.ObjectCheck, ModelID: table.ID, ObjectID: ir.ObjectID(check.ID)}
		v.validateRequirements(check.RequiredCapabilities, owner, checkPath)
		v.validateExpression(check.Expression, columns, checkPath+".expression")
		if !v.booleanStorage(check.Expression.Type) {
			v.add(CodeExpression, checkPath+".expression.type", "check expression must have provider boolean storage")
		}
	}
	seenIndexes := make(map[ir.IndexID]struct{})
	for _, index := range table.Indexes {
		indexPath := path + ".index[" + string(index.ID) + "]"
		v.validateStableID(string(index.ID), indexPath+".id")
		if _, duplicate := seenIndexes[index.ID]; duplicate {
			v.add(CodeDuplicate, indexPath+".id", fmt.Sprintf("duplicate index ID %s", index.ID))
		}
		seenIndexes[index.ID] = struct{}{}
		v.validateIndex(table.ID, index, columns, indexPath)
	}
}

func (v *validator) validateKey(modelID ir.ModelID, key PhysicalKey, columns map[ir.FieldID]PhysicalColumn, primary bool, path string) {
	v.validateStableID(string(key.ID), path+".id")
	v.validateName(key.Name, path+".name", false)
	v.registerName(ir.ObjectKey, key.Name, path+".name")
	owner := ObjectRef{Kind: ir.ObjectKey, ModelID: modelID, ObjectID: ir.ObjectID(key.ID)}
	v.validateRequirements(key.RequiredCapabilities, owner, path)
	if len(key.Columns) == 0 {
		v.add(CodeConstraint, path+".columns", "key must contain at least one column")
	}
	seen := make(map[ir.FieldID]struct{}, len(key.Columns))
	for _, fieldID := range key.Columns {
		column, exists := columns[fieldID]
		if !exists {
			v.add(CodeConstraint, path+".columns", fmt.Sprintf("field %s does not belong to table", fieldID))
		}
		if _, duplicate := seen[fieldID]; duplicate {
			v.add(CodeDuplicate, path+".columns", fmt.Sprintf("field %s appears more than once", fieldID))
		}
		seen[fieldID] = struct{}{}
		if primary && exists && column.Nullable {
			v.add(CodeConstraint, path+".columns", fmt.Sprintf("primary-key field %s is nullable", fieldID))
		}
	}
}

func (v *validator) validateForeignKey(table PhysicalTable, foreignKey PhysicalForeignKey, columns map[ir.FieldID]PhysicalColumn, path string, _ [][]ir.FieldID) {
	foreignPath := path + ".foreignKey[" + string(foreignKey.ID) + "]"
	v.validateStableID(string(foreignKey.ID), foreignPath+".id")
	v.validateName(foreignKey.Name, foreignPath+".name", false)
	v.registerName(ir.ObjectForeignKey, foreignKey.Name, foreignPath+".name")
	owner := ObjectRef{Kind: ir.ObjectForeignKey, ModelID: table.ID, ObjectID: ir.ObjectID(foreignKey.ID)}
	v.validateRequirements(foreignKey.RequiredCapabilities, owner, foreignPath)
	if len(foreignKey.Columns) == 0 || len(foreignKey.Columns) != len(foreignKey.ReferencedColumns) {
		v.add(CodeConstraint, foreignPath, "foreign-key local/reference columns must have equal non-zero arity")
	}
	referenced, exists := v.tables[foreignKey.ReferencedTable]
	if !exists {
		v.add(CodeConstraint, foreignPath+".referencedTable", fmt.Sprintf("referenced model %s does not exist", foreignKey.ReferencedTable))
		return
	}
	referencedColumns := make(map[ir.FieldID]PhysicalColumn, len(referenced.Columns))
	for _, column := range referenced.Columns {
		referencedColumns[column.ID] = column
	}
	for index := 0; index < len(foreignKey.Columns) && index < len(foreignKey.ReferencedColumns); index++ {
		local, localExists := columns[foreignKey.Columns[index]]
		remote, remoteExists := referencedColumns[foreignKey.ReferencedColumns[index]]
		if !localExists {
			v.add(CodeConstraint, foreignPath+".columns", fmt.Sprintf("local field %s does not exist", foreignKey.Columns[index]))
		}
		if !remoteExists {
			v.add(CodeConstraint, foreignPath+".referencedColumns", fmt.Sprintf("referenced field %s does not exist", foreignKey.ReferencedColumns[index]))
		}
		if localExists && remoteExists && !reflect.DeepEqual(local.Storage, remote.Storage) {
			v.add(CodeConstraint, foreignPath, fmt.Sprintf("storage mismatch %s -> %s", foreignKey.Columns[index], foreignKey.ReferencedColumns[index]))
		}
	}
	if !tableHasKey(referenced, foreignKey.ReferencedColumns) {
		v.add(CodeConstraint, foreignPath+".referencedColumns", "referenced columns are not an ordered primary or unique key")
	}
	for _, action := range []struct {
		name  string
		value ir.ReferentialAction
	}{{"onUpdate", foreignKey.OnUpdate}, {"onDelete", foreignKey.OnDelete}} {
		switch action.value {
		case ir.ActionNoAction, ir.ActionRestrict, ir.ActionCascade, ir.ActionSetNull, ir.ActionSetDefault:
		default:
			v.add(CodeConstraint, foreignPath+"."+action.name, fmt.Sprintf("invalid action %q", action.value))
		}
		if action.value == ir.ActionSetNull {
			for _, fieldID := range foreignKey.Columns {
				if column, ok := columns[fieldID]; ok && !column.Nullable {
					v.add(CodeConstraint, foreignPath+"."+action.name, fmt.Sprintf("SetNull requires nullable local field %s", fieldID))
				}
			}
		}
		if action.value == ir.ActionSetDefault {
			for _, fieldID := range foreignKey.Columns {
				if column, ok := columns[fieldID]; ok && column.Default.Kind == DefaultNone {
					v.add(CodeConstraint, foreignPath+"."+action.name, fmt.Sprintf("SetDefault requires a database/provider default for local field %s", fieldID))
				}
			}
		}
	}
	switch foreignKey.Deferrable {
	case ir.NotDeferrable, ir.InitiallyImmediate, ir.InitiallyDeferred:
	default:
		v.add(CodeConstraint, foreignPath+".deferrable", fmt.Sprintf("invalid deferrability %q", foreignKey.Deferrable))
	}
	if foreignKey.Deferrable != ir.NotDeferrable && (foreignKey.OnUpdate == ir.ActionRestrict || foreignKey.OnDelete == ir.ActionRestrict) {
		v.add(CodeConstraint, foreignPath, "deferred foreign key cannot use Restrict")
	}
}

func tableHasKey(table PhysicalTable, fields []ir.FieldID) bool {
	if table.PrimaryKey != nil && reflect.DeepEqual(table.PrimaryKey.Columns, fields) {
		return true
	}
	for _, key := range table.Uniques {
		if reflect.DeepEqual(key.Columns, fields) {
			return true
		}
	}
	return false
}

func (v *validator) validateIndex(modelID ir.ModelID, index PhysicalIndex, columns map[ir.FieldID]PhysicalColumn, path string) {
	v.validateName(index.Name, path+".name", false)
	v.registerName(ir.ObjectIndex, index.Name, path+".name")
	owner := ObjectRef{Kind: ir.ObjectIndex, ModelID: modelID, ObjectID: ir.ObjectID(index.ID)}
	v.validateRequirements(index.RequiredCapabilities, owner, path)
	if len(index.Keys) == 0 {
		v.add(CodeConstraint, path+".keys", "index must contain at least one key")
	}
	advanced := index.Method != IndexBTree || len(index.Include) > 0 || index.Predicate != nil || index.CreationMode == IndexAutocommitOnly
	switch index.Method {
	case IndexBTree, IndexHash, IndexGIN, IndexGiST, IndexBRIN:
	default:
		v.add(CodeConstraint, path+".method", fmt.Sprintf("unregistered index method %q", index.Method))
	}
	switch index.CreationMode {
	case IndexTransactional, IndexAutocommitOnly:
	default:
		v.add(CodeConstraint, path+".creationMode", fmt.Sprintf("invalid creation mode %q", index.CreationMode))
	}
	for keyIndex, key := range index.Keys {
		keyPath := fmt.Sprintf("%s.keys[%d]", path, keyIndex)
		if (key.Column == nil) == (key.Expression == nil) {
			v.add(CodeConstraint, keyPath, "exactly one of column and expression is required")
		}
		if key.Column != nil {
			if _, exists := columns[*key.Column]; !exists {
				v.add(CodeConstraint, keyPath, fmt.Sprintf("field %s does not belong to table", *key.Column))
			}
		}
		if key.Expression != nil {
			advanced = true
			v.validateExpression(*key.Expression, columns, keyPath+".expression")
		}
		if key.Direction != ir.SortAsc && key.Direction != ir.SortDesc {
			v.add(CodeConstraint, keyPath+".direction", fmt.Sprintf("invalid direction %q", key.Direction))
		}
		if key.Nulls != ir.NullsDefault && key.Nulls != ir.NullsFirst && key.Nulls != ir.NullsLast {
			v.add(CodeConstraint, keyPath+".nulls", fmt.Sprintf("invalid null order %q", key.Nulls))
		}
		if key.Collation != nil {
			advanced = true
			v.validateSymbol(*key.Collation, ir.SchemaSymbolFunction, keyPath+".collation")
		}
		if key.OpClass != nil {
			advanced = true
			v.validateSymbol(*key.OpClass, ir.SchemaSymbolOperator, keyPath+".opClass")
		}
	}
	for _, fieldID := range index.Include {
		if _, exists := columns[fieldID]; !exists {
			v.add(CodeConstraint, path+".include", fmt.Sprintf("field %s does not belong to table", fieldID))
		}
	}
	if index.Predicate != nil {
		v.validateExpression(*index.Predicate, columns, path+".predicate")
		if !v.booleanStorage(index.Predicate.Type) {
			v.add(CodeExpression, path+".predicate.type", "index predicate must have provider boolean storage")
		}
	}
	if advanced && len(index.RequiredCapabilities) == 0 {
		v.add(CodeCapabilityMissing, path, "advanced index feature has no explicit capability requirement")
	}
}

func (v *validator) validateDefault(value PhysicalDefault, columns map[ir.FieldID]PhysicalColumn, path string) {
	switch value.Kind {
	case DefaultNone:
		if value.Literal != nil || value.Expression != nil {
			v.add(CodeExpression, path, "none default cannot contain a value")
		}
	case DefaultLiteral:
		if value.Literal == nil || value.Expression != nil {
			v.add(CodeExpression, path, "literal default requires only a typed literal")
		} else {
			v.validateLiteral(*value.Literal, path+".literal")
		}
	case DefaultProvider:
		if value.Expression == nil || value.Literal != nil {
			v.add(CodeExpression, path, "provider default requires only a semantic expression")
		} else {
			v.validateExpression(*value.Expression, columns, path+".expression")
		}
	default:
		v.add(CodeExpression, path+".kind", fmt.Sprintf("invalid default kind %q", value.Kind))
	}
}

func (v *validator) validateExpression(expression Expression, columns map[ir.FieldID]PhysicalColumn, path string) {
	v.validateStorage(expression.Type, path+".type")
	switch expression.Kind {
	case ExpressionColumn:
		if expression.Column == nil || expression.Literal != nil || expression.Symbol != nil || len(expression.Operands) != 0 {
			v.add(CodeExpression, path, "column expression must contain exactly one column reference")
		} else if _, exists := columns[*expression.Column]; !exists {
			v.add(CodeExpression, path+".column", fmt.Sprintf("field %s does not belong to table", *expression.Column))
		}
	case ExpressionLiteral:
		if expression.Literal == nil || expression.Column != nil || expression.Symbol != nil || len(expression.Operands) != 0 {
			v.add(CodeExpression, path, "literal expression must contain exactly one typed literal")
		} else {
			v.validateLiteral(*expression.Literal, path+".literal")
		}
	case ExpressionOperator, ExpressionFunction, ExpressionCast:
		if expression.Symbol == nil || expression.Column != nil || expression.Literal != nil {
			v.add(CodeExpression, path, "registered expression must contain exactly one semantic symbol")
		} else {
			expected := ir.SchemaSymbolOperator
			if expression.Kind == ExpressionFunction {
				expected = ir.SchemaSymbolFunction
			} else if expression.Kind == ExpressionCast {
				expected = ir.SchemaSymbolCast
			}
			v.validateSymbol(*expression.Symbol, expected, path+".symbol")
		}
		if expression.Kind == ExpressionOperator && len(expression.Operands) == 0 {
			v.add(CodeExpression, path+".operands", "operator requires at least one operand")
		}
		if expression.Kind == ExpressionCast && len(expression.Operands) != 1 {
			v.add(CodeExpression, path+".operands", "cast requires exactly one operand")
		}
		for index, operand := range expression.Operands {
			v.validateExpression(operand, columns, fmt.Sprintf("%s.operands[%d]", path, index))
		}
	default:
		v.add(CodeExpression, path+".kind", fmt.Sprintf("invalid expression kind %q", expression.Kind))
	}
}

func (v *validator) validateSymbol(symbol SemanticSymbol, expected ir.SchemaSymbolKind, path string) {
	if symbol.Identity == "" || !semanticIdentifier.MatchString(symbol.Identity) {
		v.add(CodeExpression, path+".identity", fmt.Sprintf("invalid registered identity %q", symbol.Identity))
	}
	if symbol.Kind != expected {
		v.add(CodeExpression, path+".kind", fmt.Sprintf("got %q, want %q", symbol.Kind, expected))
	}
	if symbol.Version == 0 {
		v.add(CodeExpression, path+".version", "symbol version must be non-zero")
	}
	switch symbol.Provider {
	case ir.ProviderScopePortable:
	case ir.ProviderScopeSQLite:
		if v.schema.Provider.Provider != ir.SQLite {
			v.add(CodeProvider, path+".provider", "SQLite symbol used in non-SQLite schema")
		}
	case ir.ProviderScopePostgreSQL:
		if v.schema.Provider.Provider != ir.PostgreSQL {
			v.add(CodeProvider, path+".provider", "PostgreSQL symbol used in non-PostgreSQL schema")
		}
	default:
		v.add(CodeProvider, path+".provider", fmt.Sprintf("invalid provider scope %q", symbol.Provider))
	}
}

func (v *validator) validateLiteral(literal ir.TypedLiteralIR, path string) {
	switch literal.Kind {
	case ir.LiteralBool, ir.LiteralInteger, ir.LiteralFloat, ir.LiteralDecimal, ir.LiteralString, ir.LiteralBytes, ir.LiteralUUID, ir.LiteralDate, ir.LiteralTime, ir.LiteralDateTime, ir.LiteralJSON, ir.LiteralEnum, ir.LiteralList:
	default:
		v.add(CodeExpression, path+".kind", fmt.Sprintf("invalid typed literal kind %q", literal.Kind))
	}
	if literal.Canonical == "" && literal.Kind != ir.LiteralString {
		v.add(CodeExpression, path+".canonical", "canonical literal must not be empty")
	}
}

func (v *validator) validateStorage(storage StorageType, path string) {
	valid := false
	switch v.schema.Provider.Provider {
	case ir.SQLite:
		valid = storage.Kind == StorageSQLiteInteger || storage.Kind == StorageSQLiteReal || storage.Kind == StorageSQLiteText || storage.Kind == StorageSQLiteBlob || storage.Kind == StorageProviderExtension
	case ir.PostgreSQL:
		switch storage.Kind {
		case StoragePostgreSQLBoolean, StoragePostgreSQLSmallInt, StoragePostgreSQLInteger, StoragePostgreSQLBigInt, StoragePostgreSQLReal, StoragePostgreSQLDouble, StoragePostgreSQLNumeric, StoragePostgreSQLText, StoragePostgreSQLBytea, StoragePostgreSQLUUID, StoragePostgreSQLDate, StoragePostgreSQLTime, StoragePostgreSQLTimestampTZ, StoragePostgreSQLJSONB, StorageProviderExtension:
			valid = true
		}
	}
	if !valid {
		v.add(CodeStorage, path+".kind", fmt.Sprintf("storage %q is invalid for provider %s", storage.Kind, v.schema.Provider.Provider))
	}
	if storage.Kind == StoragePostgreSQLNumeric {
		if storage.Precision == 0 || storage.Scale > storage.Precision {
			v.add(CodeStorage, path, "numeric requires precision > 0 and scale <= precision")
		}
	} else if storage.Precision != 0 || storage.Scale != 0 {
		v.add(CodeStorage, path, "precision/scale are only valid for PostgreSQL numeric storage")
	}
	if storage.Kind == StorageProviderExtension {
		if storage.Symbol == nil {
			v.add(CodeStorage, path+".symbol", "provider extension storage requires a registered symbol")
		} else {
			v.validateSymbol(*storage.Symbol, ir.SchemaSymbolCast, path+".symbol")
		}
	} else if storage.Symbol != nil {
		v.add(CodeStorage, path+".symbol", "built-in storage cannot contain an extension symbol")
	}
}

func (v *validator) booleanStorage(storage StorageType) bool {
	return (v.schema.Provider.Provider == ir.SQLite && storage.Kind == StorageSQLiteInteger) ||
		(v.schema.Provider.Provider == ir.PostgreSQL && storage.Kind == StoragePostgreSQLBoolean)
}

func (v *validator) validateRequirements(requirements []CapabilityRequirement, expected ObjectRef, path string) {
	seen := make(map[ir.CapabilityID]struct{}, len(requirements))
	for index, requirement := range requirements {
		requirementPath := fmt.Sprintf("%s.requiredCapabilities[%d]", path, index)
		if requirement.Owner != expected {
			v.errors = append(v.errors, ValidationError{Code: CodeCapabilityManifest, Provider: v.schema.Provider.Provider, Capability: requirement.Capability, Owner: requirement.Owner, Path: requirementPath, Detail: fmt.Sprintf("capability owner must be %#v", expected)})
		}
		if _, duplicate := seen[requirement.Capability]; duplicate {
			v.add(CodeDuplicate, requirementPath, fmt.Sprintf("duplicate capability requirement %s", requirement.Capability))
		}
		seen[requirement.Capability] = struct{}{}
		if _, available := v.capabilities[requirement.Capability]; !available {
			v.errors = append(v.errors, ValidationError{Code: CodeCapabilityMissing, Provider: v.schema.Provider.Provider, Capability: requirement.Capability, Owner: requirement.Owner, Path: requirementPath, Detail: "required capability is absent from provider manifest"})
		}
	}
}

func (v *validator) validateExtensions() {
	seen := make(map[ir.ExtensionID]struct{}, len(v.schema.Extensions))
	for _, extension := range v.schema.Extensions {
		path := "extension[" + string(extension.ID) + "]"
		v.validateStableID(string(extension.ID), path+".id")
		if _, duplicate := seen[extension.ID]; duplicate {
			v.add(CodeDuplicate, path+".id", fmt.Sprintf("duplicate extension ID %s", extension.ID))
		}
		seen[extension.ID] = struct{}{}
		if extension.Provider != v.schema.Provider.Provider {
			v.add(CodeProvider, path+".provider", fmt.Sprintf("extension provider %s does not match schema provider", extension.Provider))
		}
		if !semanticIdentifier.MatchString(extension.Kind) || extension.Version == 0 {
			v.add(CodeExtension, path, "extension requires a registered semantic kind and non-zero version")
		}
		v.validateObjectRef(extension.Owner, path+".owner")
		v.validateAttributes(extension.Attributes, path+".attributes")
		owner := ObjectRef{Kind: ir.ObjectExtension, ModelID: extension.Owner.ModelID, FieldID: extension.Owner.FieldID, ObjectID: ir.ObjectID(extension.ID)}
		v.validateRequirements(extension.RequiredCapabilities, owner, path)
	}
}

func (v *validator) validateSystem() {
	system := v.schema.System
	if system.Version == 0 && len(system.Objects) == 0 && system.Namespace.Name == "" {
		return
	}
	if system.Version == 0 {
		v.add(CodeFormat, "system.version", "non-empty system schema requires a non-zero version")
	}
	v.validateName(system.Namespace.Name, "system.namespace", true)
	seen := make(map[ir.ObjectID]struct{}, len(system.Objects))
	hasOutbox := false
	hasOutboxDelivery := false
	for _, object := range system.Objects {
		path := "system.object[" + string(object.ID) + "]"
		v.validateStableID(string(object.ID), path+".id")
		if _, duplicate := seen[object.ID]; duplicate {
			v.add(CodeDuplicate, path+".id", fmt.Sprintf("duplicate system object ID %s", object.ID))
		}
		seen[object.ID] = struct{}{}
		switch object.Kind {
		case SystemMigrationLedger, SystemMigrationLock:
		case SystemOutbox:
			if !IsOutboxSystemObjectV1(object) {
				v.add(CodeExtension, path, "outbox system object does not match the closed v1 registry entry")
			}
			hasOutbox = hasOutbox || IsOutboxSystemObjectV1(object)
		case SystemOutboxDelivery:
			if !IsOutboxDeliverySystemObjectV1(object) {
				v.add(CodeExtension, path, "outbox delivery system object does not match the closed v1 registry entry")
			}
			hasOutboxDelivery = hasOutboxDelivery || IsOutboxDeliverySystemObjectV1(object)
		case SystemUpsertGuard:
			if !IsUpsertGuardSystemObjectV1(object) {
				v.add(CodeExtension, path, "upsert guard system object does not match the closed v1 registry entry")
			}
		default:
			v.add(CodeExtension, path+".kind", fmt.Sprintf("unregistered system object kind %q", object.Kind))
		}
		if object.Version == 0 {
			v.add(CodeFormat, path+".version", "system object version must be non-zero")
		}
		v.validateName(object.Name, path+".name", true)
		v.validateAttributes(object.Attributes, path+".attributes")
		owner := ObjectRef{Kind: ir.ObjectKind("system"), ObjectID: object.ID}
		v.validateRequirements(object.RequiredCapabilities, owner, path)
	}
	if hasOutboxDelivery && !hasOutbox {
		v.add(CodeExtension, "system.objects", "outbox delivery v1 requires the immutable outbox v1 system object")
	}
}

func (v *validator) validateUnmanaged() {
	managed := make(map[string]struct{}, len(v.schema.Tables))
	for _, table := range v.schema.Tables {
		managed["table\x00"+strings.ToLower(string(table.Name))] = struct{}{}
	}
	seen := make(map[string]struct{}, len(v.schema.Unmanaged))
	for index, object := range v.schema.Unmanaged {
		path := fmt.Sprintf("unmanaged[%d]", index)
		if !semanticIdentifier.MatchString(object.Kind) {
			v.add(CodeIdentifier, path+".kind", fmt.Sprintf("invalid unmanaged object kind %q", object.Kind))
		}
		v.validateName(object.Name, path+".name", false)
		key := object.Kind + "\x00" + strings.ToLower(string(object.Name))
		if _, duplicate := seen[key]; duplicate {
			v.add(CodeDuplicate, path, "duplicate unmanaged object")
		}
		seen[key] = struct{}{}
		if _, collision := managed[key]; collision {
			v.add(CodeDuplicate, path, "unmanaged allowlist collides with a managed object")
		}
	}
}

func (v *validator) validateAttributes(attributes []Attribute, path string) {
	seen := make(map[string]struct{}, len(attributes))
	for index, attribute := range attributes {
		attributePath := fmt.Sprintf("%s[%d]", path, index)
		if !semanticIdentifier.MatchString(attribute.Name) {
			v.add(CodeExtension, attributePath+".name", fmt.Sprintf("invalid attribute name %q", attribute.Name))
		}
		if _, duplicate := seen[attribute.Name]; duplicate {
			v.add(CodeDuplicate, attributePath+".name", fmt.Sprintf("duplicate attribute %q", attribute.Name))
		}
		seen[attribute.Name] = struct{}{}
		v.validateSemanticValue(attribute.Value, attributePath+".value")
	}
}

func (v *validator) validateSemanticValue(value SemanticValue, path string) {
	switch value.Kind {
	case ValueBool:
		if value.Integer != 0 || value.String != "" || len(value.List) != 0 {
			v.add(CodeExtension, path, "bool semantic value contains fields for another kind")
		}
	case ValueInteger:
		if value.Bool || value.String != "" || len(value.List) != 0 {
			v.add(CodeExtension, path, "integer semantic value contains fields for another kind")
		}
	case ValueString:
		if value.Bool || value.Integer != 0 || len(value.List) != 0 {
			v.add(CodeExtension, path, "string semantic value contains fields for another kind")
		}
	case ValueID:
		if value.Bool || value.Integer != 0 || len(value.List) != 0 {
			v.add(CodeExtension, path, "ID semantic value contains fields for another kind")
		}
		v.validateStableID(value.String, path+".id")
	case ValueList:
		if value.Bool || value.Integer != 0 || value.String != "" {
			v.add(CodeExtension, path, "list semantic value contains fields for another kind")
		}
		for index, child := range value.List {
			v.validateSemanticValue(child, fmt.Sprintf("%s.list[%d]", path, index))
		}
	default:
		v.add(CodeExtension, path+".kind", fmt.Sprintf("invalid semantic value kind %q", value.Kind))
	}
}

func (v *validator) validateObjectRef(owner ObjectRef, path string) {
	if owner.Kind == "" {
		v.add(CodeStableID, path+".kind", "object owner kind is required")
	}
	if owner.ModelID != "" {
		v.validateStableID(string(owner.ModelID), path+".modelID")
	}
	if owner.FieldID != "" {
		v.validateStableID(string(owner.FieldID), path+".fieldID")
	}
	if owner.ObjectID != "" {
		v.validateStableID(string(owner.ObjectID), path+".objectID")
	}
	if owner.ModelID == "" && owner.FieldID == "" && owner.ObjectID == "" {
		v.add(CodeStableID, path, "object owner requires at least one stable ID")
	}
}

func (v *validator) validateName(name PhysicalName, path string, system bool) {
	pattern := portableIdentifier
	if system {
		pattern = systemIdentifier
	}
	if !pattern.MatchString(string(name)) || len([]byte(name)) > 63 {
		v.add(CodeIdentifier, path, fmt.Sprintf("invalid physical identifier %q", name))
	}
	if !system && strings.HasPrefix(string(name), "_golem_") {
		v.add(CodeIdentifier, path, "portable identifier uses reserved _golem_ prefix")
	}
}

func (v *validator) registerName(kind ir.ObjectKind, name PhysicalName, path string) {
	key := string(kind) + "\x00" + strings.ToLower(string(name))
	if prior, duplicate := v.objectNames[key]; duplicate {
		v.add(CodeDuplicate, path, fmt.Sprintf("name %q collides with %s in object kind %s", name, prior, kind))
	}
	v.objectNames[key] = path
}

func (v *validator) validateStableID(value, path string) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 || value != strings.ToLower(value) {
		v.add(CodeStableID, path, fmt.Sprintf("%q is not a canonical lowercase 128-bit hexadecimal ID", value))
	}
}

func IsValidationCode(err error, code ValidationCode) bool {
	var diagnostics ValidationErrors
	if errors.As(err, &diagnostics) {
		for _, item := range diagnostics {
			if item.Code == code {
				return true
			}
		}
	}
	var diagnostic ValidationError
	if errors.As(err, &diagnostic) {
		return diagnostic.Code == code
	}
	return false
}
