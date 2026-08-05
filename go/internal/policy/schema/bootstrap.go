package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

// New validates and decodes a bundle exactly once, then publishes an immutable
// runtime registry. No partially built registry escapes on failure.
func New(bundle golem.SchemaBundle) (*Registry, error) {
	if bundle.FormatVersion() != golem.SchemaBundleFormatVersion {
		return nil, fail(CodeBundle, "formatVersion", "got %d, want %d", bundle.FormatVersion(), golem.SchemaBundleFormatVersion)
	}
	if bundle.GenerationDigest() == (golem.SchemaDigest{}) {
		return nil, fail(CodeBundle, "generationDigest", "digest must be non-zero")
	}
	if bundle.GeneratorVersion() == "" || bundle.TemplateABIVersion() == "" {
		return nil, fail(CodeBundle, "generator", "generator and template ABI versions must be non-empty")
	}

	modelDocument := bundle.Model()
	model, err := decodeModel(modelDocument)
	if err != nil {
		return nil, err
	}
	contractDocument := bundle.Contract()
	contract, err := decodeContract(contractDocument)
	if err != nil {
		return nil, err
	}

	builder := registryBuilder{
		registry: Registry{
			generationDigest: bundle.GenerationDigest(), modelFingerprint: modelDocument.Fingerprint(), contractFingerprint: contractDocument.Fingerprint(),
			models: make(map[golem.ModelID]Model), fields: make(map[golem.ModelID]map[golem.FieldID]Field), relations: make(map[relationKey]RelationEndpoint),
			enumValues: make(map[compilerir.EnumID]map[string]compilerir.EnumValueID), physicalModels: make(map[golem.Provider]map[golem.ModelID]PhysicalModel),
			physicalFields: make(map[golem.Provider]map[golem.ModelID]map[golem.FieldID]PhysicalField), capabilities: make(map[golem.Provider]map[compilerir.CapabilityID]physical.CapabilityFact),
		},
		logicalModels: make(map[compilerir.ModelID]compilerir.ModelDeclIR), logicalFields: make(map[compilerir.ModelID]map[compilerir.FieldID]compilerir.FieldIR),
	}
	if err := builder.indexLogical(model); err != nil {
		return nil, err
	}
	if err := builder.validateContract(contract); err != nil {
		return nil, err
	}
	if err := builder.indexProviders(model, bundle.Providers()); err != nil {
		return nil, err
	}
	return &builder.registry, nil
}

func decodeModel(document golem.SchemaDocument) (compilerir.ModelIR, error) {
	if document.FormatVersion() != uint32(compilerir.ModelFormatVersion) || document.CanonicalVersion() != uint32(compilerir.CanonicalFormatVersion) {
		return compilerir.ModelIR{}, fail(CodeDocument, "model", "unsupported format/canonical versions %d/%d", document.FormatVersion(), document.CanonicalVersion())
	}
	var model compilerir.ModelIR
	if err := decodeCanonicalJSON(document.Bytes(), &model); err != nil {
		return model, fail(CodeDocument, "model.payload", "%v", err)
	}
	canonical, err := compilerir.CanonicalModel(model)
	if err != nil {
		return model, fail(CodeDocument, "model.payload", "%v", err)
	}
	if !bytes.Equal(canonical, document.Bytes()) {
		return model, fail(CodeDocument, "model.payload", "document is not canonical")
	}
	fingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		return model, fail(CodeFingerprint, "model", "%v", err)
	}
	if fingerprint != compilerir.Fingerprint(document.Fingerprint().String()) {
		return model, fail(CodeFingerprint, "model", "fingerprint mismatch")
	}
	return model, nil
}

func decodeContract(document golem.SchemaDocument) (compilerir.ContractIR, error) {
	if document.FormatVersion() != uint32(compilerir.ContractFormatVersion) || document.CanonicalVersion() != uint32(compilerir.CanonicalFormatVersion) {
		return compilerir.ContractIR{}, fail(CodeDocument, "contract", "unsupported format/canonical versions %d/%d", document.FormatVersion(), document.CanonicalVersion())
	}
	var contract compilerir.ContractIR
	if err := decodeCanonicalJSON(document.Bytes(), &contract); err != nil {
		return contract, fail(CodeDocument, "contract.payload", "%v", err)
	}
	canonical, err := compilerir.CanonicalContract(contract)
	if err != nil {
		return contract, fail(CodeDocument, "contract.payload", "%v", err)
	}
	if !bytes.Equal(canonical, document.Bytes()) {
		return contract, fail(CodeDocument, "contract.payload", "document is not canonical")
	}
	fingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		return contract, fail(CodeFingerprint, "contract", "%v", err)
	}
	if fingerprint != compilerir.Fingerprint(document.Fingerprint().String()) {
		return contract, fail(CodeFingerprint, "contract", "fingerprint mismatch")
	}
	return contract, nil
}

func decodeCanonicalJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

type registryBuilder struct {
	registry      Registry
	logicalModels map[compilerir.ModelID]compilerir.ModelDeclIR
	logicalFields map[compilerir.ModelID]map[compilerir.FieldID]compilerir.FieldIR
}

func (builder *registryBuilder) indexLogical(model compilerir.ModelIR) error {
	declaredProviders := make(map[compilerir.Provider]bool, len(model.Providers))
	for index, provider := range model.Providers {
		if provider != compilerir.SQLite && provider != compilerir.PostgreSQL {
			return fail(CodeProvider, fmt.Sprintf("model.providers[%d]", index), "unsupported provider %q", provider)
		}
		if declaredProviders[provider] {
			return fail(CodeProvider, fmt.Sprintf("model.providers[%d]", index), "duplicate provider %q", provider)
		}
		declaredProviders[provider] = true
	}
	if len(declaredProviders) == 0 {
		return fail(CodeProvider, "model.providers", "at least one provider is required")
	}

	seenEnumIDs := map[compilerir.EnumID]bool{}
	seenEnumValueIDs := map[compilerir.EnumValueID]bool{}
	for enumIndex, enum := range model.Enums {
		path := fmt.Sprintf("model.enums[%d]", enumIndex)
		if _, err := fixedID(string(enum.ID)); err != nil {
			return fail(CodeIdentity, path+".id", "%v", err)
		}
		if seenEnumIDs[enum.ID] {
			return fail(CodeIdentity, path+".id", "duplicate enum ID %s", enum.ID)
		}
		seenEnumIDs[enum.ID] = true
		values := make(map[string]compilerir.EnumValueID, len(enum.Values))
		for valueIndex, value := range enum.Values {
			valuePath := fmt.Sprintf("%s.values[%d]", path, valueIndex)
			if _, err := fixedID(string(value.ID)); err != nil {
				return fail(CodeIdentity, valuePath+".id", "%v", err)
			}
			if seenEnumValueIDs[value.ID] {
				return fail(CodeIdentity, valuePath+".id", "duplicate enum value ID %s", value.ID)
			}
			if _, duplicate := values[value.WireValue]; duplicate {
				return fail(CodeIdentity, valuePath+".wireValue", "duplicate authored enum label %q", value.WireValue)
			}
			seenEnumValueIDs[value.ID] = true
			values[value.WireValue] = value.ID
		}
		builder.registry.enumValues[enum.ID] = values
	}

	globalFields := make(map[compilerir.FieldID]compilerir.ModelID)
	for modelIndex, logicalModel := range model.Models {
		path := fmt.Sprintf("model.models[%d]", modelIndex)
		mid, err := modelID(logicalModel.ID)
		if err != nil {
			return fail(CodeIdentity, path+".id", "%v", err)
		}
		if _, duplicate := builder.logicalModels[logicalModel.ID]; duplicate {
			return fail(CodeModel, path+".id", "duplicate model ID %s", logicalModel.ID)
		}
		builder.logicalModels[logicalModel.ID] = logicalModel
		builder.logicalFields[logicalModel.ID] = make(map[compilerir.FieldID]compilerir.FieldIR, len(logicalModel.Fields))
		builder.registry.models[mid] = Model{id: mid}
		builder.registry.fields[mid] = make(map[golem.FieldID]Field, len(logicalModel.Fields))
		for fieldIndex, logicalField := range logicalModel.Fields {
			fieldPath := fmt.Sprintf("%s.fields[%d]", path, fieldIndex)
			fid, fieldErr := fieldID(logicalField.ID)
			if fieldErr != nil {
				return fail(CodeIdentity, fieldPath+".id", "%v", fieldErr)
			}
			if owner, duplicate := globalFields[logicalField.ID]; duplicate {
				return fail(CodeField, fieldPath+".id", "field ID %s already belongs to model %s", logicalField.ID, owner)
			}
			globalFields[logicalField.ID] = logicalModel.ID
			if err := validateFieldShape(logicalField, seenEnumIDs); err != nil {
				return fail(CodeField, fieldPath, "%v", err)
			}
			builder.logicalFields[logicalModel.ID][logicalField.ID] = logicalField
			fact := Field{model: mid, id: fid, kind: logicalField.Kind}
			if logicalField.Scalar != nil {
				fact.logicalType = cloneLogicalType(logicalField.Scalar.Type)
				fact.nullable = logicalField.Scalar.Nullable
			}
			if logicalField.Relation != nil {
				rid, relationErr := relationID(logicalField.Relation.RelationID)
				if relationErr != nil {
					return fail(CodeIdentity, fieldPath+".relationId", "%v", relationErr)
				}
				fact.relation, fact.relationRole = rid, logicalField.Relation.Role
			}
			builder.registry.fields[mid][fid] = fact
		}
	}
	return builder.indexRelations(model.Relations)
}

func validateFieldShape(field compilerir.FieldIR, enums map[compilerir.EnumID]bool) error {
	switch field.Kind {
	case compilerir.FieldScalar, compilerir.FieldEnum, compilerir.FieldScalarList:
		if field.Scalar == nil || field.Relation != nil {
			return fmt.Errorf("scalar-like field must have exactly one scalar payload")
		}
		if field.Scalar.Type.Kind == compilerir.TypeEnum {
			if field.Scalar.Type.EnumID == nil || !enums[*field.Scalar.Type.EnumID] {
				return fmt.Errorf("enum field references an unknown enum")
			}
		} else if field.Scalar.Type.EnumID != nil {
			return fmt.Errorf("non-enum field carries enum identity")
		}
	case compilerir.FieldRelation:
		if field.Relation == nil || field.Scalar != nil {
			return fmt.Errorf("relation field must have exactly one relation payload")
		}
		if field.Relation.Role != compilerir.RelationSource && field.Relation.Role != compilerir.RelationInverse {
			return fmt.Errorf("invalid relation role %q", field.Relation.Role)
		}
		if field.Relation.Kind != compilerir.RelationBelongsTo && field.Relation.Kind != compilerir.RelationHasOne && field.Relation.Kind != compilerir.RelationHasMany {
			return fmt.Errorf("invalid relation kind %q", field.Relation.Kind)
		}
	default:
		return fmt.Errorf("unknown field kind %q", field.Kind)
	}
	return nil
}

func (builder *registryBuilder) indexRelations(relations []compilerir.RelationIR) error {
	seen := make(map[compilerir.RelationID]bool, len(relations))
	for index, relation := range relations {
		path := fmt.Sprintf("model.relations[%d]", index)
		rid, err := relationID(relation.ID)
		if err != nil {
			return fail(CodeIdentity, path+".id", "%v", err)
		}
		if seen[relation.ID] {
			return fail(CodeRelation, path+".id", "duplicate relation ID %s", relation.ID)
		}
		seen[relation.ID] = true
		sourceModel, sourceOK := builder.logicalModels[relation.SourceModel]
		targetModel, targetOK := builder.logicalModels[relation.TargetModel]
		if !sourceOK || !targetOK {
			return fail(CodeRelation, path, "relation references an unknown source or target model")
		}
		if relation.Cardinality != compilerir.RelationOne && relation.Cardinality != compilerir.RelationMany {
			return fail(CodeRelation, path+".cardinality", "invalid cardinality %q", relation.Cardinality)
		}
		if len(relation.LocalFields) == 0 || len(relation.LocalFields) != len(relation.RemoteFields) {
			return fail(CodeRelation, path, "correlation fields require equal non-zero arity")
		}
		sourceField, ok := builder.logicalFields[sourceModel.ID][relation.SourceField]
		if !ok || sourceField.Relation == nil || sourceField.Relation.RelationID != relation.ID || sourceField.Relation.Role != compilerir.RelationSource {
			return fail(CodeRelation, path+".sourceField", "source endpoint does not agree with relation field")
		}
		sourceEndpoint, endpointErr := builder.endpoint(sourceModel.ID, targetModel.ID, sourceField, rid, relation.LocalFields, relation.RemoteFields)
		if endpointErr != nil {
			return fail(CodeRelation, path, "%v", endpointErr)
		}
		if _, duplicate := builder.registry.relations[relationKey{sourceEndpoint.model, sourceEndpoint.field, rid}]; duplicate {
			return fail(CodeRelation, path, "duplicate source endpoint")
		}
		builder.registry.relations[relationKey{sourceEndpoint.model, sourceEndpoint.field, rid}] = sourceEndpoint
		if relation.InverseField != nil {
			inverseField, inverseOK := builder.logicalFields[targetModel.ID][*relation.InverseField]
			if !inverseOK || inverseField.Relation == nil || inverseField.Relation.RelationID != relation.ID || inverseField.Relation.Role != compilerir.RelationInverse {
				return fail(CodeRelation, path+".inverseField", "inverse endpoint does not agree with relation field")
			}
			inverseEndpoint, inverseErr := builder.endpoint(targetModel.ID, sourceModel.ID, inverseField, rid, relation.RemoteFields, relation.LocalFields)
			if inverseErr != nil {
				return fail(CodeRelation, path, "%v", inverseErr)
			}
			if _, duplicate := builder.registry.relations[relationKey{inverseEndpoint.model, inverseEndpoint.field, rid}]; duplicate {
				return fail(CodeRelation, path, "duplicate inverse endpoint")
			}
			builder.registry.relations[relationKey{inverseEndpoint.model, inverseEndpoint.field, rid}] = inverseEndpoint
		}
	}
	// Every logical relation field must be backed by exactly one endpoint.
	for modelIDValue, fields := range builder.logicalFields {
		mid, _ := modelID(modelIDValue)
		for _, field := range fields {
			if field.Relation == nil {
				continue
			}
			fid, _ := fieldID(field.ID)
			rid, _ := relationID(field.Relation.RelationID)
			if _, ok := builder.registry.relations[relationKey{mid, fid, rid}]; !ok {
				return fail(CodeRelation, "model.fields", "relation field %s has no normalized endpoint", field.ID)
			}
		}
	}
	return nil
}

func (builder *registryBuilder) endpoint(owner, target compilerir.ModelID, field compilerir.FieldIR, relation golem.RelationID, parents, children []compilerir.FieldID) (RelationEndpoint, error) {
	ownerID, _ := modelID(owner)
	targetID, _ := modelID(target)
	endpointField, _ := fieldID(field.ID)
	result := RelationEndpoint{model: ownerID, field: endpointField, relation: relation, target: targetID, role: field.Relation.Role, kind: field.Relation.Kind, cardinality: relationKindCardinality(field.Relation.Kind), correlation: make([]Correlation, len(parents))}
	for index := range parents {
		parent, parentOK := builder.logicalFields[owner][parents[index]]
		child, childOK := builder.logicalFields[target][children[index]]
		if !parentOK || !childOK || parent.Scalar == nil || child.Scalar == nil {
			return RelationEndpoint{}, fmt.Errorf("correlation pair %d must reference persisted scalar fields", index)
		}
		if !reflect.DeepEqual(parent.Scalar.Type, child.Scalar.Type) {
			return RelationEndpoint{}, fmt.Errorf("correlation pair %d has incompatible logical types", index)
		}
		parentID, _ := fieldID(parent.ID)
		childID, _ := fieldID(child.ID)
		result.correlation[index] = Correlation{parent: parentID, child: childID}
	}
	return result, nil
}

func relationKindCardinality(kind compilerir.RelationKind) compilerir.RelationCardinality {
	if kind == compilerir.RelationHasMany {
		return compilerir.RelationMany
	}
	return compilerir.RelationOne
}

func (builder *registryBuilder) validateContract(contract compilerir.ContractIR) error {
	if len(contract.Models) != len(builder.logicalModels) {
		return fail(CodeContract, "contract.models", "contract model inventory has %d entries, want %d", len(contract.Models), len(builder.logicalModels))
	}
	seenModels := make(map[compilerir.ModelID]bool, len(contract.Models))
	for modelIndex, model := range contract.Models {
		path := fmt.Sprintf("contract.models[%d]", modelIndex)
		if _, ok := builder.logicalModels[model.ModelID]; !ok {
			return fail(CodeContract, path+".modelId", "unknown model ID %s", model.ModelID)
		}
		if seenModels[model.ModelID] {
			return fail(CodeContract, path+".modelId", "duplicate model ID %s", model.ModelID)
		}
		seenModels[model.ModelID] = true
		if len(model.Fields) != len(builder.logicalFields[model.ModelID]) {
			return fail(CodeContract, path+".fields", "contract field inventory has %d entries, want %d", len(model.Fields), len(builder.logicalFields[model.ModelID]))
		}
		seenFields := make(map[compilerir.FieldID]bool, len(model.Fields))
		for fieldIndex, field := range model.Fields {
			fieldPath := fmt.Sprintf("%s.fields[%d]", path, fieldIndex)
			if _, ok := builder.logicalFields[model.ModelID][field.FieldID]; !ok {
				return fail(CodeContract, fieldPath+".fieldId", "field %s does not belong to model %s", field.FieldID, model.ModelID)
			}
			if seenFields[field.FieldID] {
				return fail(CodeContract, fieldPath+".fieldId", "duplicate field ID %s", field.FieldID)
			}
			seenFields[field.FieldID] = true
		}
	}
	if len(contract.Enums) != len(builder.registry.enumValues) {
		return fail(CodeContract, "contract.enums", "contract enum inventory has %d entries, want %d", len(contract.Enums), len(builder.registry.enumValues))
	}
	seenEnums := make(map[compilerir.EnumID]bool, len(contract.Enums))
	for index, enum := range contract.Enums {
		path := fmt.Sprintf("contract.enums[%d].enumId", index)
		if _, ok := builder.registry.enumValues[enum.EnumID]; !ok {
			return fail(CodeContract, path, "unknown enum ID %s", enum.EnumID)
		}
		if seenEnums[enum.EnumID] {
			return fail(CodeContract, path, "duplicate enum ID %s", enum.EnumID)
		}
		seenEnums[enum.EnumID] = true
	}
	return nil
}

func (builder *registryBuilder) indexProviders(model compilerir.ModelIR, documents []golem.ProviderSchemaDocument) error {
	declared := make(map[golem.Provider]bool, len(model.Providers))
	for _, provider := range model.Providers {
		declared[golem.Provider(provider)] = true
	}
	if len(documents) != len(declared) {
		return fail(CodeProvider, "providers", "provider document inventory has %d entries, want %d", len(documents), len(declared))
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].Provider() < documents[j].Provider() })
	seen := make(map[golem.Provider]bool, len(documents))
	for index, document := range documents {
		provider := document.Provider()
		path := fmt.Sprintf("providers[%d]", index)
		if !declared[provider] {
			return fail(CodeProvider, path, "provider %q is not declared by ModelIR", provider)
		}
		if seen[provider] {
			return fail(CodeProvider, path, "duplicate provider %q", provider)
		}
		seen[provider] = true
		schemaDocument := document.Schema()
		if schemaDocument.FormatVersion() != physical.SchemaFormatVersion || schemaDocument.CanonicalVersion() != physical.CanonicalFormatVersion {
			return fail(CodeDocument, path, "unsupported physical format/canonical versions %d/%d", schemaDocument.FormatVersion(), schemaDocument.CanonicalVersion())
		}
		decoded, err := physical.CanonicalDecodeVerified(schemaDocument.Bytes(), physical.Digest(schemaDocument.Fingerprint()), physical.Digest(document.SystemFingerprint()))
		if err != nil {
			return fail(CodePhysical, path, "%v", err)
		}
		if golem.Provider(decoded.Provider.Provider) != provider {
			return fail(CodeProvider, path, "outer provider %q disagrees with physical provider %q", provider, decoded.Provider.Provider)
		}
		if err := builder.indexPhysical(provider, decoded); err != nil {
			return err
		}
		builder.registry.providers = append(builder.registry.providers, provider)
	}
	return nil
}

func (builder *registryBuilder) indexPhysical(provider golem.Provider, schema physical.PhysicalSchema) error {
	path := "providers[" + string(provider) + "]"
	models := make(map[golem.ModelID]PhysicalModel, len(schema.Tables))
	fields := make(map[golem.ModelID]map[golem.FieldID]PhysicalField, len(schema.Tables))
	if len(schema.Tables) != len(builder.logicalModels) {
		return fail(CodePhysical, path+".tables", "physical table inventory has %d entries, want %d", len(schema.Tables), len(builder.logicalModels))
	}
	for _, table := range schema.Tables {
		logicalModel, ok := builder.logicalModels[table.ID]
		if !ok {
			return fail(CodePhysical, path+".tables", "unknown physical model ID %s", table.ID)
		}
		mid, _ := modelID(table.ID)
		models[mid] = PhysicalModel{provider: provider, model: mid, name: table.Name}
		persisted := make(map[compilerir.FieldID]compilerir.FieldIR)
		for _, logicalField := range logicalModel.Fields {
			if logicalField.Scalar != nil {
				persisted[logicalField.ID] = logicalField
			}
		}
		if len(table.Columns) != len(persisted) {
			return fail(CodePhysical, path+".table["+string(table.ID)+"].columns", "physical column inventory has %d entries, want %d", len(table.Columns), len(persisted))
		}
		fields[mid] = make(map[golem.FieldID]PhysicalField, len(table.Columns))
		for _, column := range table.Columns {
			logicalField, exists := persisted[column.ID]
			if !exists {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].columns", "unknown or non-persisted field ID %s", column.ID)
			}
			expectedStorage, err := physical.ExpectedStorage(compilerir.Provider(provider), logicalField.Scalar.Type)
			if err != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].column["+string(column.ID)+"].storage", "derive logical storage: %v", err)
			}
			if !physical.StorageEqual(column.Storage, expectedStorage) {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].column["+string(column.ID)+"].storage", "logical type %q requires %#v, got %#v", logicalField.Scalar.Type.Kind, expectedStorage, column.Storage)
			}
			if logicalField.Scalar.Nullable != column.Nullable {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].column["+string(column.ID)+"]", "logical and physical nullability disagree")
			}
			fid, _ := fieldID(column.ID)
			fields[mid][fid] = PhysicalField{provider: provider, model: mid, field: fid, table: table.Name, column: column.Name, storage: cloneStorage(column.Storage), nullable: column.Nullable, requiredCapabilities: append([]physical.CapabilityRequirement(nil), column.RequiredCapabilities...)}
		}
	}
	capabilities := make(map[compilerir.CapabilityID]physical.CapabilityFact, len(schema.Provider.Capabilities))
	for _, capability := range schema.Provider.Capabilities {
		capabilities[capability.ID] = capability
	}
	builder.registry.physicalModels[provider], builder.registry.physicalFields[provider], builder.registry.capabilities[provider] = models, fields, capabilities
	return nil
}
