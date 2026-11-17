package ir

import (
	"fmt"
	"go/token"
	"strings"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/invalidationcontract"
)

type CustomInvalidationKind string

const (
	CustomInvalidationReadableRows CustomInvalidationKind = invalidationcontract.ReadableRows
	CustomInvalidationPolicyFacts  CustomInvalidationKind = invalidationcontract.PolicyFacts
)

type CustomInvalidationIR struct {
	SubscriptionField string                           `json:"subscriptionField"`
	Dependencies      []CustomInvalidationDependencyIR `json:"dependencies"`
}

type CustomInvalidationDependencyIR struct {
	ID              ExtensionID            `json:"id"`
	Name            string                 `json:"name"`
	Kind            CustomInvalidationKind `json:"kind"`
	ModelID         ModelID                `json:"modelId"`
	Binder          AttachedMethodIR       `json:"binder"`
	Fields          []FieldID              `json:"fields,omitempty"`
	ScopeTemplateID ScopeTemplateID        `json:"scopeTemplateId,omitempty"`
	FactTemplateIDs []FactTemplateID       `json:"factTemplateIds,omitempty"`
}

func CustomInvalidationDependencyID(operation ExtensionID, name string) ExtensionID {
	identity, _ := NewIDRegistry().Register(ObjectExtension, OwnedIdentity(string(operation), "graphql-invalidation\x00"+name), SourceSpan{})
	return ExtensionIDFrom(identity)
}

// InvalidationArguments excludes only compiler-owned page inputs. Ordinary
// Query arguments named take or after retain their application meaning.
func (operation CustomOperationContractIR) InvalidationArguments() []GraphQLArgumentContractIR {
	result := make([]GraphQLArgumentContractIR, 0, len(operation.Arguments))
	for _, argument := range operation.Arguments {
		if operation.Pagination != nil && (argument.Name == "take" || argument.Name == "after") {
			continue
		}
		result = append(result, argument)
	}
	return result
}

func (contract ContractIR) RequiredClientCapabilities() []string {
	var capabilities []string
	if contract.DataIncarnation != nil {
		capabilities = append(capabilities, dataincarnation.Capability)
	}
	for _, operation := range contract.CustomOperations {
		if operation.Invalidation != nil {
			capabilities = append(capabilities, invalidationcontract.Capability)
			break
		}
	}
	return capabilities
}

func ValidateGraphQLInvalidations(contract ContractIR) error {
	models := make(map[ModelID]ModelContractIR, len(contract.Models))
	roots := map[string]bool{}
	for _, model := range contract.Models {
		models[model.ModelID] = model
		if model.Roots.Events != "" {
			roots[model.Roots.Events] = true
		}
	}
	ids := map[ExtensionID]bool{}
	for _, operation := range contract.CustomOperations {
		if operation.Invalidation == nil {
			continue
		}
		invalidation := operation.Invalidation
		if operation.Operation != CustomOperationQuery || invalidation.SubscriptionField != operation.Name+"Changes" || !validGraphQLDefinitionName(invalidation.SubscriptionField) || roots[invalidation.SubscriptionField] {
			return fmt.Errorf("custom invalidation %s requires a unique generated query subscription root", operation.Name)
		}
		roots[invalidation.SubscriptionField] = true
		if len(invalidation.Dependencies) == 0 || len(invalidation.Dependencies) > invalidationcontract.MaxDependencies {
			return fmt.Errorf("custom invalidation %s requires 1..%d dependencies", operation.Name, invalidationcontract.MaxDependencies)
		}
		if err := ValidatePageArguments(operation.InvalidationArguments(), contract.GraphQLTypes); err != nil {
			return fmt.Errorf("custom invalidation %s arguments: %w", operation.Name, err)
		}
		names := map[string]bool{}
		for _, dependency := range invalidation.Dependencies {
			if operation.ExtensionID == "" || dependency.ID != CustomInvalidationDependencyID(operation.ExtensionID, dependency.Name) || ids[dependency.ID] || dependency.Name == "" || strings.TrimSpace(dependency.Name) != dependency.Name || len(dependency.Name) > 256 || !utf8.ValidString(dependency.Name) || names[dependency.Name] {
				return fmt.Errorf("custom invalidation %s has duplicate or invalid dependency identity", operation.Name)
			}
			ids[dependency.ID], names[dependency.Name] = true, true
			binder := dependency.Binder
			if binder.PackagePath == "" || binder.PackagePath != operation.Resolver.PackagePath || !token.IsIdentifier(binder.Name) || binder.Receiver != (GoNamedTypeIR{}) || binder.Kind != "invalidation" || binder.Actor == nil || binder.Actor.PackagePath == "" || !token.IsIdentifier(binder.Actor.Name) || binder.ModelID != nil {
				return fmt.Errorf("invalidation %s requires a named schema-package actor-bound binder", dependency.Name)
			}
			model, present := models[dependency.ModelID]
			if !present || !model.Subscriptions {
				return fmt.Errorf("invalidation %s source model requires subscriptions", dependency.Name)
			}
			switch dependency.Kind {
			case CustomInvalidationReadableRows:
				if len(dependency.Fields) == 0 || len(dependency.Fields) > invalidationcontract.MaxMembers || dependency.ScopeTemplateID != "" || len(dependency.FactTemplateIDs) != 0 {
					return fmt.Errorf("readable invalidation %s requires only bounded watched fields", dependency.Name)
				}
				seen := map[FieldID]bool{}
				for _, field := range dependency.Fields {
					known := false
					for _, candidate := range model.Fields {
						known = known || candidate.FieldID == field
					}
					if !known || seen[field] {
						return fmt.Errorf("readable invalidation %s has unknown or repeated source field", dependency.Name)
					}
					seen[field] = true
				}
			case CustomInvalidationPolicyFacts:
				if len(dependency.Fields) != 0 || dependency.ScopeTemplateID == "" || len(dependency.FactTemplateIDs) == 0 || len(dependency.FactTemplateIDs) > invalidationcontract.MaxMembers {
					return fmt.Errorf("fact invalidation %s requires only a scope and bounded fact inventory", dependency.Name)
				}
				knownScope := false
				for _, scope := range model.ScopeTemplates {
					knownScope = knownScope || scope.ID == dependency.ScopeTemplateID
				}
				if !knownScope {
					return fmt.Errorf("fact invalidation %s has an unknown source scope template", dependency.Name)
				}
				seen := map[FactTemplateID]bool{}
				for _, id := range dependency.FactTemplateIDs {
					known := false
					for _, fact := range model.FactTemplates {
						known = known || fact.ID == id && fact.ScopeTemplateID == dependency.ScopeTemplateID
					}
					if !known || seen[id] {
						return fmt.Errorf("fact invalidation %s has an unknown, repeated or differently scoped fact template", dependency.Name)
					}
					seen[id] = true
				}
			default:
				return fmt.Errorf("invalidation %s has unknown dependency kind", dependency.Name)
			}
		}
	}
	return nil
}
