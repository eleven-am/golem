package schema

import (
	"bytes"
	"crypto/sha256"
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
	return newRegistry(bundle, false)
}

// NewHistorical accepts only explicitly supported historical document
// versions after verifying their original canonical bytes and fingerprint.
// Active generated applications continue to use New and exact current
// versions; compatibility is never inferred from permissive JSON decoding.
func NewHistorical(bundle golem.SchemaBundle) (*Registry, error) {
	return newRegistry(bundle, true)
}

func newRegistry(bundle golem.SchemaBundle, historical bool) (*Registry, error) {
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
	model, err := decodeModel(modelDocument, historical)
	if err != nil {
		return nil, err
	}
	contractDocument := bundle.Contract()
	contract, err := decodeContract(contractDocument, historical)
	if err != nil {
		return nil, err
	}
	if err := physical.ValidateOptimisticConcurrencyLogical(model); err != nil {
		return nil, fail(CodeModel, "model.optimisticConcurrency", "%v", err)
	}

	builder := registryBuilder{
		registry: Registry{
			generationDigest: bundle.GenerationDigest(), modelFingerprint: modelDocument.Fingerprint(), contractFingerprint: contractDocument.Fingerprint(),
			models: make(map[golem.ModelID]Model), fields: make(map[golem.ModelID]map[golem.FieldID]Field), relations: make(map[relationKey]RelationEndpoint),
			enumValues: make(map[compilerir.EnumID]map[string]compilerir.EnumValueID), enumLabels: make(map[compilerir.EnumID]map[compilerir.EnumValueID]string), physicalModels: make(map[golem.Provider]map[golem.ModelID]PhysicalModel),
			physicalFields: make(map[golem.Provider]map[golem.ModelID]map[golem.FieldID]PhysicalField), capabilities: make(map[golem.Provider]map[compilerir.CapabilityID]physical.CapabilityFact),
			physicalModelNames:       make(map[golem.Provider]map[physical.PhysicalName]golem.ModelID),
			physicalAccessObjects:    make(map[golem.Provider]map[physical.PhysicalName]PhysicalAccessObject),
			physicalKeyAccessObjects: make(map[golem.Provider]map[golem.ModelID][]PhysicalAccessObject),
			physicalNamespaces:       make(map[golem.Provider]physical.PhysicalName),
			physicalSystemNamespaces: make(map[golem.Provider]physical.PhysicalName),
		},
		logicalModels: make(map[compilerir.ModelID]compilerir.ModelDeclIR), logicalFields: make(map[compilerir.ModelID]map[compilerir.FieldID]compilerir.FieldIR),
		logicalEnums: append([]compilerir.EnumIR(nil), model.Enums...),
	}
	if err := builder.indexLogical(model); err != nil {
		return nil, err
	}
	if err := builder.validateContract(contract); err != nil {
		return nil, err
	}
	if err := builder.indexProviders(model, bundle.Providers(), historical); err != nil {
		return nil, err
	}
	return &builder.registry, nil
}

func decodeModel(document golem.SchemaDocument, historical bool) (compilerir.ModelIR, error) {
	if historical && document.FormatVersion() == 1 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {
		model, err := compilerir.CanonicalDecodeModelV1(document.Bytes())
		if err != nil {
			return compilerir.ModelIR{}, fail(CodeDocument, "model.payload", "%v", err)
		}
		fingerprint, err := compilerir.ModelFingerprintV1(document.Bytes())
		if err != nil {
			return compilerir.ModelIR{}, fail(CodeFingerprint, "model", "%v", err)
		}
		if fingerprint != compilerir.Fingerprint(document.Fingerprint().String()) {
			return compilerir.ModelIR{}, fail(CodeFingerprint, "model", "fingerprint mismatch")
		}
		return model, nil
	}
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

func decodeContract(document golem.SchemaDocument, historical bool) (compilerir.ContractIR, error) {
	if historical && document.FormatVersion() == 4 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {
		return decodeHistoricalContractV4(document)
	}
	if historical && document.FormatVersion() == 5 && document.CanonicalVersion() == uint32(compilerir.CanonicalFormatVersion) {
		contract, err := compilerir.CanonicalDecodeContractV5(document.Bytes())
		if err != nil {
			return compilerir.ContractIR{}, fail(CodeDocument, "contract.payload", "%v", err)
		}
		fingerprint, err := compilerir.ContractFingerprintV5(document.Bytes())
		if err != nil {
			return compilerir.ContractIR{}, fail(CodeFingerprint, "contract", "%v", err)
		}
		if fingerprint != compilerir.Fingerprint(document.Fingerprint().String()) {
			return compilerir.ContractIR{}, fail(CodeFingerprint, "contract", "fingerprint mismatch")
		}
		return contract, nil
	}
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

type historicalContractV4 struct {
	FormatVersion     uint16                                 `json:"formatVersion"`
	GraphQLABIVersion uint16                                 `json:"graphqlAbiVersion"`
	Models            []historicalModelContractV4            `json:"models"`
	Enums             []compilerir.EnumContractIR            `json:"enums"`
	Methods           []compilerir.AttachedMethodIR          `json:"methods"`
	CustomOperations  []compilerir.CustomOperationContractIR `json:"customOperations"`
}

type historicalModelContractV4 struct {
	ModelID       compilerir.ModelID                   `json:"modelId"`
	GraphQLName   string                               `json:"graphqlName"`
	GraphQLPlural string                               `json:"graphqlPlural"`
	Roots         compilerir.GraphQLRootNamesIR        `json:"roots"`
	Fields        []compilerir.FieldContractIR         `json:"fields"`
	Selectors     []compilerir.SelectorContractIR      `json:"selectors"`
	Operations    []compilerir.Operation               `json:"operations"`
	Subscriptions bool                                 `json:"subscriptions"`
	Event         *compilerir.EventContractIR          `json:"event,omitempty"`
	Aggregation   *compilerir.AggregationContractIR    `json:"aggregation,omitempty"`
	ScopedReads   bool                                 `json:"scopedReads"`
	Limits        compilerir.LimitContractIR           `json:"limits"`
	Computed      []compilerir.ComputedFieldContractIR `json:"computed"`
	Exposed       bool                                 `json:"exposed"`
}

func decodeHistoricalContractV4(document golem.SchemaDocument) (compilerir.ContractIR, error) {
	var legacy historicalContractV4
	if err := decodeCanonicalJSON(document.Bytes(), &legacy); err != nil {
		return compilerir.ContractIR{}, fail(CodeDocument, "contract.payload", "%v", err)
	}
	if legacy.FormatVersion != 4 {
		return compilerir.ContractIR{}, fail(CodeDocument, "contract.payload", "historical format version changed during decode")
	}
	if legacy.GraphQLABIVersion != 3 {
		return compilerir.ContractIR{}, fail(CodeDocument, "contract.payload", "historical GraphQL ABI version %d is unsupported", legacy.GraphQLABIVersion)
	}
	current := contractV4ToCurrent(legacy)
	canonicalCurrent, err := compilerir.CanonicalContract(current)
	if err != nil {
		return compilerir.ContractIR{}, fail(CodeDocument, "contract.payload", "%v", err)
	}
	var normalized compilerir.ContractIR
	if err := json.Unmarshal(canonicalCurrent, &normalized); err != nil {
		return compilerir.ContractIR{}, fail(CodeDocument, "contract.payload", "%v", err)
	}
	canonicalLegacy, err := json.Marshal(contractCurrentToV4(normalized))
	if err != nil {
		return compilerir.ContractIR{}, fail(CodeDocument, "contract.payload", "%v", err)
	}
	if !bytes.Equal(canonicalLegacy, document.Bytes()) {
		return compilerir.ContractIR{}, fail(CodeDocument, "contract.payload", "document is not canonical")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:contract-fingerprint:v1"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonicalLegacy)
	if fmt.Sprintf("%x", hash.Sum(nil)) != document.Fingerprint().String() {
		return compilerir.ContractIR{}, fail(CodeFingerprint, "contract", "fingerprint mismatch")
	}
	return normalized, nil
}

func contractV4ToCurrent(input historicalContractV4) compilerir.ContractIR {
	result := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, GraphQLABIVersion: input.GraphQLABIVersion, Enums: input.Enums, Methods: input.Methods, CustomOperations: input.CustomOperations}
	result.Models = make([]compilerir.ModelContractIR, len(input.Models))
	for index, model := range input.Models {
		result.Models[index] = compilerir.ModelContractIR{ModelID: model.ModelID, GraphQLName: model.GraphQLName, GraphQLPlural: model.GraphQLPlural, Roots: model.Roots, Fields: model.Fields, HookOwnedCreateFields: []compilerir.FieldID{}, Selectors: model.Selectors, Operations: model.Operations, Subscriptions: model.Subscriptions, Event: model.Event, Aggregation: model.Aggregation, ScopedReads: model.ScopedReads, Limits: model.Limits, Computed: model.Computed, Exposed: model.Exposed}
	}
	return result
}

func contractCurrentToV4(input compilerir.ContractIR) historicalContractV4 {
	result := historicalContractV4{FormatVersion: 4, GraphQLABIVersion: input.GraphQLABIVersion, Enums: input.Enums, Methods: input.Methods, CustomOperations: input.CustomOperations}
	result.Models = make([]historicalModelContractV4, len(input.Models))
	for index, model := range input.Models {
		result.Models[index] = historicalModelContractV4{ModelID: model.ModelID, GraphQLName: model.GraphQLName, GraphQLPlural: model.GraphQLPlural, Roots: model.Roots, Fields: model.Fields, Selectors: model.Selectors, Operations: model.Operations, Subscriptions: model.Subscriptions, Event: model.Event, Aggregation: model.Aggregation, ScopedReads: model.ScopedReads, Limits: model.Limits, Computed: model.Computed, Exposed: model.Exposed}
	}
	return result
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
	logicalEnums  []compilerir.EnumIR
}

func equalOptionalCompilerFieldID(left, right *compilerir.FieldID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
		labels := make(map[compilerir.EnumValueID]string, len(enum.Values))
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
			labels[value.ID] = value.WireValue
		}
		builder.registry.enumValues[enum.ID] = values
		builder.registry.enumLabels[enum.ID] = labels
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
		modelFact := Model{id: mid, fields: make([]golem.FieldID, 0, len(logicalModel.Fields)), equality: make(map[golem.FieldID]struct{}, len(logicalModel.EqualityIndexes))}
		if logicalModel.OptimisticConcurrency != nil {
			field, fieldErr := fieldID(*logicalModel.OptimisticConcurrency)
			if fieldErr != nil {
				return fail(CodeIdentity, path+".optimisticConcurrency", "%v", fieldErr)
			}
			modelFact.optimisticConcurrency = &field
		}
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
				if logicalField.Scalar.Default != nil {
					value := cloneDefault(*logicalField.Scalar.Default)
					fact.defaultValue = &value
				}
				if logicalField.Scalar.Generation != nil {
					value := cloneGeneratedColumn(*logicalField.Scalar.Generation)
					fact.generation = &value
				}
				fact.updated = logicalField.Scalar.Updated
				fact.databaseReadOnly = logicalField.Scalar.DatabaseReadOnly
			}
			if logicalField.Relation != nil {
				rid, relationErr := relationID(logicalField.Relation.RelationID)
				if relationErr != nil {
					return fail(CodeIdentity, fieldPath+".relationId", "%v", relationErr)
				}
				fact.relation, fact.relationRole = rid, logicalField.Relation.Role
			}
			builder.registry.fields[mid][fid] = fact
			modelFact.fields = append(modelFact.fields, fid)
		}
		if logicalModel.PrimaryKey != nil {
			for _, keyField := range logicalModel.PrimaryKey.Fields {
				fid, fieldErr := fieldID(keyField)
				if fieldErr != nil {
					return fail(CodeIdentity, path+".primaryKey", "%v", fieldErr)
				}
				modelFact.primaryKey = append(modelFact.primaryKey, fid)
			}
			key, keyErr := keyID(logicalModel.PrimaryKey.ID)
			if keyErr != nil {
				return fail(CodeIdentity, path+".primaryKey.id", "%v", keyErr)
			}
			modelFact.identities = append(modelFact.identities, Identity{key: key, kind: logicalModel.PrimaryKey.Kind, fields: append([]golem.FieldID(nil), modelFact.primaryKey...)})
		}
		for uniqueIndex, unique := range logicalModel.Uniques {
			key, keyErr := keyID(unique.ID)
			if keyErr != nil {
				return fail(CodeIdentity, fmt.Sprintf("%s.uniques[%d].id", path, uniqueIndex), "%v", keyErr)
			}
			identity := Identity{key: key, kind: unique.Kind, fields: make([]golem.FieldID, len(unique.Fields))}
			for fieldIndex, uniqueField := range unique.Fields {
				fid, fieldErr := fieldID(uniqueField)
				if fieldErr != nil {
					return fail(CodeIdentity, fmt.Sprintf("%s.uniques[%d].fields[%d]", path, uniqueIndex, fieldIndex), "%v", fieldErr)
				}
				identity.fields[fieldIndex] = fid
			}
			modelFact.identities = append(modelFact.identities, identity)
		}
		for equalityIndex, equality := range logicalModel.EqualityIndexes {
			fid, fieldErr := fieldID(equality.FieldID)
			if fieldErr != nil {
				return fail(CodeIdentity, fmt.Sprintf("%s.equalityIndexes[%d].fieldId", path, equalityIndex), "%v", fieldErr)
			}
			if _, ok := builder.registry.fields[mid][fid]; !ok {
				return fail(CodeField, fmt.Sprintf("%s.equalityIndexes[%d].fieldId", path, equalityIndex), "field is absent from model")
			}
			modelFact.equality[fid] = struct{}{}
		}
		builder.registry.models[mid] = modelFact
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
		logicalModel := builder.logicalModels[model.ModelID]
		if !equalOptionalCompilerFieldID(logicalModel.OptimisticConcurrency, model.OptimisticConcurrency) {
			return fail(CodeContract, path+".optimisticConcurrency", "ModelIR and ContractIR optimistic-concurrency identities disagree")
		}
		if model.OptimisticConcurrency != nil {
			matches := 0
			for _, field := range model.Fields {
				if field.FieldID != *model.OptimisticConcurrency {
					continue
				}
				matches++
				if len(field.Modes) != 1 || field.Modes[0] != compilerir.ModeVisible {
					return fail(CodeContract, path+".optimisticConcurrency", "optimistic-concurrency field requires the exact ordinary visible contract mode")
				}
			}
			if matches != 1 {
				return fail(CodeContract, path+".optimisticConcurrency", "optimistic-concurrency field requires one exact contract field")
			}
		}
		mid, _ := modelID(model.ModelID)
		fact := builder.registry.models[mid]
		fact.maxTake = model.Limits.MaxTake
		fact.subscriptions = model.Subscriptions
		if model.Subscriptions {
			logical := builder.logicalModels[model.ModelID]
			if model.Event == nil {
				return fail(CodeContract, path+".event", "subscription-enabled model requires closed event metadata")
			}
			stored := make(map[compilerir.FieldID]bool, len(logical.Fields))
			for _, field := range logical.Fields {
				if field.Kind != compilerir.FieldRelation && field.Scalar != nil {
					stored[field.ID] = true
				}
			}
			snapshot := make([]compilerir.FieldID, len(model.Event.Schema.SnapshotFields))
			seenSnapshot := make(map[compilerir.FieldID]bool, len(snapshot))
			for index, field := range model.Event.Schema.SnapshotFields {
				if !stored[field.FieldID] || seenSnapshot[field.FieldID] {
					return fail(CodeContract, path+".event.schema", "event schema is not the complete normalized stored-scalar shape")
				}
				seenSnapshot[field.FieldID] = true
				snapshot[index] = field.FieldID
			}
			if len(seenSnapshot) != len(stored) {
				return fail(CodeContract, path+".event.schema", "event schema is not the complete normalized stored-scalar shape")
			}
			expected, err := compilerir.BuildEventSchemaShape(logical, builder.logicalEnums, snapshot)
			if err != nil {
				return fail(CodeContract, path+".event.schema", "%v", err)
			}
			if !reflect.DeepEqual(expected, model.Event.Schema) || !model.Event.DeleteSnapshotFull {
				return fail(CodeContract, path+".event.schema", "event schema is not the complete normalized stored-scalar shape")
			}
			fingerprint, err := compilerir.EventSchemaFingerprint(model.Event.Schema)
			if err != nil || fingerprint != model.Event.SchemaFingerprint {
				return fail(CodeContract, path+".event.schemaFingerprint", "event schema fingerprint mismatch")
			}
			fact.eventSchema = fingerprint
			fact.eventSnapshot = make([]golem.FieldID, len(snapshot))
			for index, field := range snapshot {
				fid, err := fieldID(field)
				if err != nil {
					return fail(CodeContract, path+".event.schema.snapshotFields", "%v", err)
				}
				fact.eventSnapshot[index] = fid
			}
		} else if model.Event != nil {
			return fail(CodeContract, path+".event", "subscription-disabled model cannot carry event metadata")
		}
		fact.scopedReads = model.ScopedReads
		fact.hookOwnedCreate = make([]golem.FieldID, len(model.HookOwnedCreateFields))
		for index, field := range model.HookOwnedCreateFields {
			value, err := fieldID(field)
			if err != nil {
				return fail(CodeContract, path+".hookOwnedCreateFields", "%v", err)
			}
			fact.hookOwnedCreate[index] = value
		}
		if model.Aggregation != nil {
			value := *model.Aggregation
			value.Dimensions = append([]compilerir.FieldID(nil), value.Dimensions...)
			value.Measures = append([]compilerir.FieldID(nil), value.Measures...)
			value.RelationDimensions = append([]compilerir.RelationDimensionContractIR(nil), value.RelationDimensions...)
			for index := range value.RelationDimensions {
				value.RelationDimensions[index].Path = append([]compilerir.RelationID(nil), value.RelationDimensions[index].Path...)
			}
			fact.analytics = &value
		}
		builder.registry.models[mid] = fact
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
			fid, _ := fieldID(field.FieldID)
			fieldFact := builder.registry.fields[mid][fid]
			fieldFact.graphqlName = field.GraphQLName
			fieldFact.modes = append([]compilerir.FieldMode(nil), field.Modes...)
			builder.registry.fields[mid][fid] = fieldFact
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

func (builder *registryBuilder) indexProviders(model compilerir.ModelIR, documents []golem.ProviderSchemaDocument, historical bool) error {
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
		decoded, err := decodeProviderSchemaDocument(schemaDocument, document.SystemFingerprint(), historical)
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

func decodeProviderSchemaDocument(document golem.SchemaDocument, system golem.SchemaDigest, historical bool) (physical.PhysicalSchema, error) {
	if !historical {
		if document.FormatVersion() != physical.SchemaFormatVersion || document.CanonicalVersion() != physical.CanonicalFormatVersion {
			return physical.PhysicalSchema{}, fmt.Errorf("unsupported physical format/canonical versions %d/%d", document.FormatVersion(), document.CanonicalVersion())
		}
		return physical.CanonicalDecodeVerified(document.Bytes(), physical.Digest(document.Fingerprint()), physical.Digest(system))
	}
	if document.FormatVersion() != document.CanonicalVersion() {
		return physical.PhysicalSchema{}, fmt.Errorf("unsupported historical physical format/canonical versions %d/%d", document.FormatVersion(), document.CanonicalVersion())
	}
	var (
		decoded physical.PhysicalSchema
		err     error
	)
	switch document.FormatVersion() {
	case 1:
		decoded, err = physical.CanonicalDecodeHistorical(document.Bytes())
	case 2:
		decoded, err = physical.CanonicalDecodeHistoricalV2(document.Bytes())
	case 3:
		decoded, err = physical.CanonicalDecodeHistoricalV3(document.Bytes())
	default:
		return physical.PhysicalSchema{}, fmt.Errorf("unsupported historical physical format/canonical versions %d/%d", document.FormatVersion(), document.CanonicalVersion())
	}
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	physicalFingerprint, err := physical.HistoricalPhysicalFingerprint(decoded)
	if err != nil {
		return physical.PhysicalSchema{}, fmt.Errorf("historical physical fingerprint: %w", err)
	}
	if physicalFingerprint != physical.Digest(document.Fingerprint()) {
		return physical.PhysicalSchema{}, fmt.Errorf("historical physical fingerprint mismatch: got %s want %s", physicalFingerprint, physical.Digest(document.Fingerprint()))
	}
	systemFingerprint, err := physical.HistoricalSystemFingerprint(decoded)
	if err != nil {
		return physical.PhysicalSchema{}, fmt.Errorf("historical system fingerprint: %w", err)
	}
	if systemFingerprint != physical.Digest(system) {
		return physical.PhysicalSchema{}, fmt.Errorf("historical system fingerprint mismatch: got %s want %s", systemFingerprint, physical.Digest(system))
	}
	return decoded, nil
}

func (builder *registryBuilder) indexPhysical(provider golem.Provider, schema physical.PhysicalSchema) error {
	path := "providers[" + string(provider) + "]"
	models := make(map[golem.ModelID]PhysicalModel, len(schema.Tables))
	fields := make(map[golem.ModelID]map[golem.FieldID]PhysicalField, len(schema.Tables))
	modelNames := make(map[physical.PhysicalName]golem.ModelID, len(schema.Tables))
	accessObjects := make(map[physical.PhysicalName]PhysicalAccessObject)
	keyAccessObjects := make(map[golem.ModelID][]PhysicalAccessObject, len(schema.Tables))
	if len(schema.Tables) != len(builder.logicalModels) {
		return fail(CodePhysical, path+".tables", "physical table inventory has %d entries, want %d", len(schema.Tables), len(builder.logicalModels))
	}
	for _, table := range schema.Tables {
		logicalModel, ok := builder.logicalModels[table.ID]
		if !ok {
			return fail(CodePhysical, path+".tables", "unknown physical model ID %s", table.ID)
		}
		mid, _ := modelID(table.ID)
		if !equalOptionalCompilerFieldID(logicalModel.OptimisticConcurrency, table.OptimisticConcurrency) {
			return fail(CodePhysical, path+".table["+string(table.ID)+"].optimisticConcurrency", "ModelIR and physical optimistic-concurrency identities disagree")
		}
		models[mid] = PhysicalModel{provider: provider, model: mid, name: table.Name}
		if _, duplicate := modelNames[table.Name]; duplicate {
			return fail(CodePhysical, path+".tables", "physical plan table lookup is ambiguous")
		}
		modelNames[table.Name] = mid
		if table.PrimaryKey != nil {
			id, err := keyID(table.PrimaryKey.ID)
			if err != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].primaryKey.id", "invalid stable key identity")
			}
			keyFields, fieldErr := physicalKeyFields(table.PrimaryKey.Columns)
			if fieldErr != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].primaryKey.columns", "invalid stable key field identity")
			}
			fact := PhysicalAccessObject{kind: PhysicalAccessPrimaryKey, model: mid, keyID: id, fields: keyFields}
			if err := registerPhysicalAccessObject(accessObjects, table.PrimaryKey.Name, fact); err != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].primaryKey", "%v", err)
			}
			keyAccessObjects[mid] = append(keyAccessObjects[mid], fact.clone())
		}
		for _, key := range table.Uniques {
			id, err := keyID(key.ID)
			if err != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].uniques", "invalid stable key identity")
			}
			keyFields, fieldErr := physicalKeyFields(key.Columns)
			if fieldErr != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].uniques", "invalid stable key field identity")
			}
			fact := PhysicalAccessObject{kind: PhysicalAccessUniqueIndex, model: mid, keyID: id, fields: keyFields}
			if err := registerPhysicalAccessObject(accessObjects, key.Name, fact); err != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].uniques", "%v", err)
			}
			keyAccessObjects[mid] = append(keyAccessObjects[mid], fact.clone())
		}
		for _, index := range table.Indexes {
			id, err := physicalIndexID(index.ID)
			if err != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].indexes", "invalid stable index identity")
			}
			kind := PhysicalAccessIndex
			if index.Unique {
				kind = PhysicalAccessUniqueIndex
			}
			if err := registerPhysicalAccessObject(accessObjects, index.Name, PhysicalAccessObject{kind: kind, model: mid, indexID: id}); err != nil {
				return fail(CodePhysical, path+".table["+string(table.ID)+"].indexes", "%v", err)
			}
		}
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
			if !physical.StorageEqual(column.Storage, expectedStorage) && !historicalV1PostgreSQLBoundedStringAgreement(schema, table, column, logicalField) {
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
	builder.registry.physicalModelNames[provider], builder.registry.physicalAccessObjects[provider] = modelNames, accessObjects
	builder.registry.physicalKeyAccessObjects[provider] = keyAccessObjects
	builder.registry.physicalNamespaces[provider] = schema.Namespace.Name
	builder.registry.physicalSystemNamespaces[provider] = schema.System.Namespace.Name
	return nil
}

func historicalV1PostgreSQLBoundedStringAgreement(schema physical.PhysicalSchema, table physical.PhysicalTable, column physical.PhysicalColumn, logical compilerir.FieldIR) bool {
	if schema.Version != 1 || schema.CanonicalVersion != 1 || schema.Provider.Provider != compilerir.PostgreSQL || logical.Scalar == nil || logical.Scalar.Type.Kind != compilerir.TypeString || logical.Scalar.Type.MaxLength == nil || *logical.Scalar.Type.MaxLength == 0 || column.Storage != (physical.StorageType{Kind: physical.StoragePostgreSQLText}) {
		return false
	}
	id, name := physical.HistoricalV1MaxLengthCheckIdentity(table.ID, column.ID)
	integer := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	boolean := physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}
	fieldID := column.ID
	literal := compilerir.TypedLiteralIR{Kind: compilerir.LiteralInteger, Canonical: fmt.Sprintf("%d", *logical.Scalar.Type.MaxLength)}
	want := physical.PhysicalCheck{ID: id, Name: name, Expression: physical.Expression{
		Kind: physical.ExpressionOperator, Type: boolean, Nullable: column.Nullable,
		Symbol: &physical.SemanticSymbol{Identity: "golem.schema.predicate.less-equal.v1", Kind: compilerir.SchemaSymbolOperator, Version: 1, Provider: compilerir.ProviderScopePortable},
		Operands: []physical.Expression{
			{Kind: physical.ExpressionFunction, Type: integer, Nullable: column.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.function.length.v1", Kind: compilerir.SchemaSymbolFunction, Version: 1, Provider: compilerir.ProviderScopePortable}, Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: column.Storage, Nullable: column.Nullable, Column: &fieldID, Operands: []physical.Expression{}}}},
			{Kind: physical.ExpressionLiteral, Type: integer, Literal: &literal, Operands: []physical.Expression{}},
		},
	}}
	found := 0
	for _, check := range table.Checks {
		if check.ID == id || check.Name == name {
			found++
			if check.ID != want.ID || check.Name != want.Name || len(check.RequiredCapabilities) != 0 || !equalHistoricalV1Expression(check.Expression, want.Expression) {
				return false
			}
		}
	}
	return found == 1
}

func equalHistoricalV1Expression(left, right physical.Expression) bool {
	if left.Kind != right.Kind || !reflect.DeepEqual(left.Type, right.Type) || left.Nullable != right.Nullable || !equalHistoricalV1Symbol(left.Symbol, right.Symbol) || !equalHistoricalV1Field(left.Column, right.Column) || !equalHistoricalV1Literal(left.Literal, right.Literal) || len(left.Operands) != len(right.Operands) {
		return false
	}
	for index := range left.Operands {
		if !equalHistoricalV1Expression(left.Operands[index], right.Operands[index]) {
			return false
		}
	}
	return true
}

func equalHistoricalV1Symbol(left, right *physical.SemanticSymbol) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalHistoricalV1Field(left, right *compilerir.FieldID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalHistoricalV1Literal(left, right *compilerir.TypedLiteralIR) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func registerPhysicalAccessObject(values map[physical.PhysicalName]PhysicalAccessObject, name physical.PhysicalName, value PhysicalAccessObject) error {
	if _, duplicate := values[name]; duplicate {
		// Names originate in untrusted provider plan/schema inputs. The private
		// canary must not cross even this bootstrap diagnostic boundary.
		return fmt.Errorf("physical plan access-object lookup is ambiguous")
	}
	hasKey, hasIndex := value.keyID != (golem.KeyID{}), value.indexID != (PhysicalIndexID{})
	if name == "" || value.kind == PhysicalAccessUnknown || value.model == (golem.ModelID{}) || hasKey == hasIndex || hasKey != validPhysicalKeyFields(value.fields) {
		return fmt.Errorf("physical plan access-object lookup fact is invalid")
	}
	values[name] = value.clone()
	return nil
}

func physicalKeyFields(values []compilerir.FieldID) ([]golem.FieldID, error) {
	result := make([]golem.FieldID, len(values))
	for index, value := range values {
		field, err := fieldID(value)
		if err != nil {
			return nil, err
		}
		result[index] = field
	}
	if !validPhysicalKeyFields(result) {
		return nil, fmt.Errorf("invalid physical key fields")
	}
	return result, nil
}
