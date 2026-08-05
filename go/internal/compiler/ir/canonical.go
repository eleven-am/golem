package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

type Fingerprint string

func CanonicalRaw(input RawDeclIR) ([]byte, error) {
	if input.FormatVersion != RawDeclFormatVersion {
		return nil, fmt.Errorf("raw declaration IR version %d is unsupported; expected %d", input.FormatVersion, RawDeclFormatVersion)
	}
	copy, err := cloneJSON(input)
	if err != nil {
		return nil, err
	}
	normalizeRaw(&copy)
	return json.Marshal(copy)
}

func CanonicalModel(input ModelIR) ([]byte, error) {
	if input.FormatVersion != ModelFormatVersion {
		return nil, fmt.Errorf("model IR version %d is unsupported; expected %d", input.FormatVersion, ModelFormatVersion)
	}
	copy, err := cloneJSON(input)
	if err != nil {
		return nil, err
	}
	normalizeModel(&copy)
	return json.Marshal(copy)
}

func CanonicalContract(input ContractIR) ([]byte, error) {
	if input.FormatVersion != ContractFormatVersion {
		return nil, fmt.Errorf("contract IR version %d is unsupported; expected %d", input.FormatVersion, ContractFormatVersion)
	}
	// Preserve bootstrap source compatibility even though the deprecated field
	// is deliberately excluded from serialized ContractIR.
	for index := range input.Models {
		if len(input.Models[index].Fields) == 0 && len(input.Models[index].FieldModes) != 0 {
			input.Models[index].Fields = append([]FieldContractIR(nil), input.Models[index].FieldModes...)
		}
		input.Models[index].FieldModes = nil
	}
	copy, err := cloneJSON(input)
	if err != nil {
		return nil, err
	}
	normalizeContract(&copy)
	return json.Marshal(copy)
}

func CanonicalRawGoType(input RawGoTypeRef) ([]byte, error) {
	copy, err := cloneJSON(input)
	if err != nil {
		return nil, err
	}
	normalizeRawGoType(&copy)
	return json.Marshal(copy)
}

func ModelFingerprint(input ModelIR) (Fingerprint, error) {
	encoded, err := CanonicalModel(input)
	if err != nil {
		return "", err
	}
	return fingerprint("golem:model-fingerprint:v1", encoded), nil
}

func ContractFingerprint(input ContractIR) (Fingerprint, error) {
	encoded, err := CanonicalContract(input)
	if err != nil {
		return "", err
	}
	return fingerprint("golem:contract-fingerprint:v1", encoded), nil
}

func fingerprint(domain string, encoded []byte) Fingerprint {
	hash := sha256.New()
	hash.Write([]byte(domain))
	hash.Write([]byte{0})
	hash.Write(encoded)
	return Fingerprint(hex.EncodeToString(hash.Sum(nil)))
}

func cloneJSON[T any](input T) (T, error) {
	var output T
	encoded, err := json.Marshal(input)
	if err != nil {
		return output, err
	}
	if err := json.Unmarshal(encoded, &output); err != nil {
		return output, err
	}
	return output, nil
}

func normalizeRaw(raw *RawDeclIR) {
	raw.Root.Span = NormalizeSourceSpan(raw.Root.Span)
	raw.Root.SchemaNameSpan = NormalizeSourceSpan(raw.Root.SchemaNameSpan)
	for i := range raw.Root.Models {
		raw.Root.Models[i].Span = NormalizeSourceSpan(raw.Root.Models[i].Span)
	}
	sort.Slice(raw.Root.Models, func(i, j int) bool {
		left, right := raw.Root.Models[i], raw.Root.Models[j]
		if left.PackagePath != right.PackagePath {
			return left.PackagePath < right.PackagePath
		}
		return left.GoName < right.GoName
	})
	for i := range raw.Root.Providers {
		raw.Root.Providers[i].Span = NormalizeSourceSpan(raw.Root.Providers[i].Span)
	}
	sort.Slice(raw.Root.Providers, func(i, j int) bool {
		return providerRank(raw.Root.Providers[i].Provider) < providerRank(raw.Root.Providers[j].Provider)
	})
	if raw.Root.Actor != nil {
		raw.Root.Actor.Span = NormalizeSourceSpan(raw.Root.Actor.Span)
	}
	for i := range raw.Models {
		model := &raw.Models[i]
		model.Span = NormalizeSourceSpan(model.Span)
		sortRawAttributes(model.Marker)
		for fieldIndex := range model.Fields {
			field := &model.Fields[fieldIndex]
			field.Span = NormalizeSourceSpan(field.Span)
			normalizeRawGoType(&field.GoType)
			sortRawAttributes(field.GolemAttrs)
		}
		for directiveIndex := range model.Directives {
			directive := &model.Directives[directiveIndex]
			directive.Span = NormalizeSourceSpan(directive.Span)
			sortRawAttributes(directive.Attributes)
		}
		sort.Slice(model.Directives, func(i, j int) bool {
			if model.Directives[i].Kind != model.Directives[j].Kind {
				return model.Directives[i].Kind < model.Directives[j].Kind
			}
			return model.Directives[i].Name < model.Directives[j].Name
		})
	}
	sort.Slice(raw.Models, func(i, j int) bool {
		if raw.Models[i].PackagePath != raw.Models[j].PackagePath {
			return raw.Models[i].PackagePath < raw.Models[j].PackagePath
		}
		return raw.Models[i].GoName < raw.Models[j].GoName
	})
	for i := range raw.Enums {
		raw.Enums[i].Span = NormalizeSourceSpan(raw.Enums[i].Span)
		raw.Enums[i].Method.Span = NormalizeSourceSpan(raw.Enums[i].Method.Span)
		for j := range raw.Enums[i].Values {
			raw.Enums[i].Values[j].Span = NormalizeSourceSpan(raw.Enums[i].Values[j].Span)
		}
		sort.SliceStable(raw.Enums[i].Values, func(a, b int) bool {
			return raw.Enums[i].Values[a].Ordinal < raw.Enums[i].Values[b].Ordinal
		})
	}
	sort.Slice(raw.Enums, func(i, j int) bool {
		if raw.Enums[i].PackagePath != raw.Enums[j].PackagePath {
			return raw.Enums[i].PackagePath < raw.Enums[j].PackagePath
		}
		return raw.Enums[i].GoName < raw.Enums[j].GoName
	})
	for i := range raw.Methods {
		raw.Methods[i].Span = NormalizeSourceSpan(raw.Methods[i].Span)
	}
	sort.Slice(raw.Methods, func(i, j int) bool {
		left, right := raw.Methods[i], raw.Methods[j]
		if left.ReceiverPackage != right.ReceiverPackage {
			return left.ReceiverPackage < right.ReceiverPackage
		}
		if left.ReceiverGoName != right.ReceiverGoName {
			return left.ReceiverGoName < right.ReceiverGoName
		}
		return left.Name < right.Name
	})
	forceRawSlices(raw)
}

func normalizeRawGoType(rawType *RawGoTypeRef) {
	rawType.Span = NormalizeSourceSpan(rawType.Span)
	for index := range rawType.Args {
		normalizeRawGoType(&rawType.Args[index])
	}
	if rawType.Args == nil {
		rawType.Args = []RawGoTypeRef{}
	}
}

func sortRawAttributes(attributes []RawAttribute) {
	for i := range attributes {
		attributes[i].Span = NormalizeSourceSpan(attributes[i].Span)
	}
	sort.Slice(attributes, func(i, j int) bool { return attributes[i].Name < attributes[j].Name })
}

func forceRawSlices(raw *RawDeclIR) {
	if raw.Root.Providers == nil {
		raw.Root.Providers = []RawProviderRef{}
	}
	if raw.Root.Models == nil {
		raw.Root.Models = []RawModelRef{}
	}
	if raw.Models == nil {
		raw.Models = []RawModelDecl{}
	}
	if raw.Enums == nil {
		raw.Enums = []RawEnumDecl{}
	}
	if raw.Methods == nil {
		raw.Methods = []RawMethodDecl{}
	}
	for i := range raw.Models {
		if raw.Models[i].Marker == nil {
			raw.Models[i].Marker = []RawAttribute{}
		}
		if raw.Models[i].Fields == nil {
			raw.Models[i].Fields = []RawFieldDecl{}
		}
		if raw.Models[i].Directives == nil {
			raw.Models[i].Directives = []RawDirectiveDecl{}
		}
		for fieldIndex := range raw.Models[i].Fields {
			if raw.Models[i].Fields[fieldIndex].GolemAttrs == nil {
				raw.Models[i].Fields[fieldIndex].GolemAttrs = []RawAttribute{}
			}
		}
		for directiveIndex := range raw.Models[i].Directives {
			directive := &raw.Models[i].Directives[directiveIndex]
			if directive.Components == nil {
				directive.Components = []string{}
			}
			if directive.Attributes == nil {
				directive.Attributes = []RawAttribute{}
			}
		}
	}
	for i := range raw.Enums {
		if raw.Enums[i].Values == nil {
			raw.Enums[i].Values = []RawEnumValue{}
		}
	}
}

func normalizeModel(model *ModelIR) {
	sort.Slice(model.Providers, func(i, j int) bool { return providerRank(model.Providers[i]) < providerRank(model.Providers[j]) })
	sort.Slice(model.Enums, func(i, j int) bool { return model.Enums[i].ID < model.Enums[j].ID })
	for i := range model.Enums {
		if model.Enums[i].Values == nil {
			model.Enums[i].Values = []EnumValueIR{}
		}
	}
	sort.Slice(model.Models, func(i, j int) bool { return model.Models[i].ID < model.Models[j].ID })
	for i := range model.Models {
		entry := &model.Models[i]
		sort.Slice(entry.Fields, func(a, b int) bool { return entry.Fields[a].ID < entry.Fields[b].ID })
		sort.Slice(entry.Uniques, func(a, b int) bool { return entry.Uniques[a].ID < entry.Uniques[b].ID })
		sort.Slice(entry.Indexes, func(a, b int) bool { return entry.Indexes[a].ID < entry.Indexes[b].ID })
		sort.Slice(entry.Checks, func(a, b int) bool { return entry.Checks[a].ID < entry.Checks[b].ID })
		sort.Slice(entry.EqualityIndexes, func(a, b int) bool {
			left, right := entry.EqualityIndexes[a], entry.EqualityIndexes[b]
			if left.FieldID != right.FieldID {
				return left.FieldID < right.FieldID
			}
			if left.Kind != right.Kind {
				return left.Kind < right.Kind
			}
			return equalityIndexIdentity(left) < equalityIndexIdentity(right)
		})
		if entry.Fields == nil {
			entry.Fields = []FieldIR{}
		}
		if entry.Uniques == nil {
			entry.Uniques = []KeyIR{}
		}
		if entry.Indexes == nil {
			entry.Indexes = []IndexIR{}
		}
		if entry.Checks == nil {
			entry.Checks = []CheckIR{}
		}
		if entry.EqualityIndexes == nil {
			entry.EqualityIndexes = []EqualityIndexIR{}
		}
		if entry.PrimaryKey != nil && entry.PrimaryKey.Fields == nil {
			entry.PrimaryKey.Fields = []FieldID{}
		}
		for index := range entry.Uniques {
			if entry.Uniques[index].Fields == nil {
				entry.Uniques[index].Fields = []FieldID{}
			}
		}
		for index := range entry.Indexes {
			if entry.Indexes[index].Keys == nil {
				entry.Indexes[index].Keys = []IndexKeyIR{}
			}
			if entry.Indexes[index].Include == nil {
				entry.Indexes[index].Include = []FieldID{}
			}
			for keyIndex := range entry.Indexes[index].Keys {
				if entry.Indexes[index].Keys[keyIndex].Expr != nil {
					normalizeSchemaExpr(entry.Indexes[index].Keys[keyIndex].Expr)
				}
			}
			if entry.Indexes[index].Predicate != nil {
				normalizeSchemaPredicate(entry.Indexes[index].Predicate)
			}
		}
		for fieldIndex := range entry.Fields {
			if entry.Fields[fieldIndex].Scalar != nil && entry.Fields[fieldIndex].Scalar.Generation != nil {
				normalizeSchemaExpr(&entry.Fields[fieldIndex].Scalar.Generation.Expr)
			}
		}
		for checkIndex := range entry.Checks {
			normalizeSchemaPredicate(&entry.Checks[checkIndex].Predicate)
		}
	}
	sort.Slice(model.Relations, func(i, j int) bool { return model.Relations[i].ID < model.Relations[j].ID })
	for i := range model.Relations {
		if model.Relations[i].LocalFields == nil {
			model.Relations[i].LocalFields = []FieldID{}
		}
		if model.Relations[i].RemoteFields == nil {
			model.Relations[i].RemoteFields = []FieldID{}
		}
	}
	sort.Slice(model.Extensions, func(i, j int) bool {
		left, right := model.Extensions[i], model.Extensions[j]
		if left.Provider != right.Provider {
			return providerRank(left.Provider) < providerRank(right.Provider)
		}
		if left.Owner != right.Owner {
			return left.Owner < right.Owner
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.ID < right.ID
	})
	if model.Providers == nil {
		model.Providers = []Provider{}
	}
	if model.Enums == nil {
		model.Enums = []EnumIR{}
	}
	if model.Models == nil {
		model.Models = []ModelDeclIR{}
	}
	if model.Relations == nil {
		model.Relations = []RelationIR{}
	}
	if model.Extensions == nil {
		model.Extensions = []ProviderExtensionIR{}
	}
}

func normalizeContract(contract *ContractIR) {
	sort.Slice(contract.Models, func(i, j int) bool { return contract.Models[i].ModelID < contract.Models[j].ModelID })
	for i := range contract.Models {
		model := &contract.Models[i]
		if len(model.Fields) == 0 && len(model.FieldModes) != 0 {
			model.Fields = append([]FieldContractIR(nil), model.FieldModes...)
		}
		model.FieldModes = nil
		sort.Slice(model.Fields, func(a, b int) bool { return model.Fields[a].FieldID < model.Fields[b].FieldID })
		for fieldIndex := range model.Fields {
			sort.Slice(model.Fields[fieldIndex].Modes, func(a, b int) bool {
				return model.Fields[fieldIndex].Modes[a] < model.Fields[fieldIndex].Modes[b]
			})
		}
		sort.Slice(model.Selectors, func(a, b int) bool {
			if model.Selectors[a].Name != model.Selectors[b].Name {
				return model.Selectors[a].Name < model.Selectors[b].Name
			}
			return model.Selectors[a].KeyID < model.Selectors[b].KeyID
		})
		for selectorIndex := range model.Selectors {
			if model.Selectors[selectorIndex].Fields == nil {
				model.Selectors[selectorIndex].Fields = []FieldID{}
			}
		}
		sort.Slice(model.Operations, func(a, b int) bool { return model.Operations[a] < model.Operations[b] })
		if model.Fields == nil {
			model.Fields = []FieldContractIR{}
		}
		if model.Selectors == nil {
			model.Selectors = []SelectorContractIR{}
		}
		if model.Operations == nil {
			model.Operations = []Operation{}
		}
		for fieldIndex := range model.Fields {
			if model.Fields[fieldIndex].Modes == nil {
				model.Fields[fieldIndex].Modes = []FieldMode{}
			}
		}
	}
	sort.Slice(contract.Enums, func(i, j int) bool { return contract.Enums[i].EnumID < contract.Enums[j].EnumID })
	sort.Slice(contract.Methods, func(i, j int) bool {
		left, right := contract.Methods[i], contract.Methods[j]
		if left.Receiver.PackagePath != right.Receiver.PackagePath {
			return left.Receiver.PackagePath < right.Receiver.PackagePath
		}
		if left.Receiver.Name != right.Receiver.Name {
			return left.Receiver.Name < right.Receiver.Name
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Name < right.Name
	})
	if contract.Models == nil {
		contract.Models = []ModelContractIR{}
	}
	if contract.Enums == nil {
		contract.Enums = []EnumContractIR{}
	}
	if contract.Methods == nil {
		contract.Methods = []AttachedMethodIR{}
	}
}

func normalizeSchemaExpr(expression *SchemaExprIR) {
	for index := range expression.Operands {
		normalizeSchemaExpr(&expression.Operands[index])
	}
	if expression.Operands == nil {
		expression.Operands = []SchemaExprIR{}
	}
	sort.Slice(expression.ReferencedFields, func(i, j int) bool { return expression.ReferencedFields[i] < expression.ReferencedFields[j] })
	if expression.ReferencedFields == nil {
		expression.ReferencedFields = []FieldID{}
	}
}

func normalizeSchemaPredicate(predicate *SchemaPredicateIR) {
	for index := range predicate.ExpressionOperands {
		normalizeSchemaExpr(&predicate.ExpressionOperands[index])
	}
	for index := range predicate.Children {
		normalizeSchemaPredicate(&predicate.Children[index])
	}
	if predicate.ExpressionOperands == nil {
		predicate.ExpressionOperands = []SchemaExprIR{}
	}
	if predicate.Children == nil {
		predicate.Children = []SchemaPredicateIR{}
	}
	sort.Slice(predicate.ReferencedFields, func(i, j int) bool { return predicate.ReferencedFields[i] < predicate.ReferencedFields[j] })
	if predicate.ReferencedFields == nil {
		predicate.ReferencedFields = []FieldID{}
	}
}

func equalityIndexIdentity(value EqualityIndexIR) string {
	if value.KeyID != nil {
		return string(*value.KeyID)
	}
	if value.IndexID != nil {
		return string(*value.IndexID)
	}
	return ""
}

func providerRank(provider Provider) int {
	switch provider {
	case SQLite:
		return 0
	case PostgreSQL:
		return 1
	default:
		return 100
	}
}
