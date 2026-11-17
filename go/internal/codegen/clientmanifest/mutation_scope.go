package clientmanifest

import (
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	MutationScopeModels   = "models"
	MutationScopeUnscoped = "unscoped"
)

// MutationScope bounds the models a mutation can change, independently of the
// response models in Root.Models. An absent scope is unscoped for clients.
type MutationScope struct {
	Kind   string   `json:"kind"`
	Models []string `json:"models,omitempty"`
}

// generatedMutationScopes uses the complete normalized relation graph, including
// internal endpoints, FK actions and through models. Its undirected closure
// deliberately overapproximates nested writes and inbound cascades/set-null.
// Hook bodies have arbitrary write reach, so a component containing a hook (or
// an unknown/private model) cannot claim a public model bound.
func generatedMutationScopes(compilation ir.CompilationIR, models map[ir.ModelID]ir.ModelDeclIR, contracts map[ir.ModelID]ir.ModelContractIR, relations map[ir.RelationID]ir.RelationIR) map[ir.ModelID]MutationScope {
	adjacent := make(map[ir.ModelID][]ir.ModelID, len(models))
	unsafe := make(map[ir.ModelID]bool)
	connect := func(left, right ir.ModelID) {
		adjacent[left] = append(adjacent[left], right)
		adjacent[right] = append(adjacent[right], left)
	}
	for _, relation := range relations {
		connect(relation.SourceModel, relation.TargetModel)
		if relation.Through != nil {
			connect(relation.SourceModel, relation.Through.ModelID)
		}
	}
	for id, model := range models {
		for _, field := range model.Fields {
			if field.Kind == ir.FieldRelation {
				if field.Relation == nil {
					unsafe[id] = true
				} else if _, ok := relations[field.Relation.RelationID]; !ok {
					unsafe[id] = true
				}
			}
		}
	}
	unknownHook := false
	for _, method := range compilation.Contract.Methods {
		if method.Kind != "hook" {
			continue
		}
		if method.ModelID == nil {
			unknownHook = true
		} else if _, ok := models[*method.ModelID]; !ok {
			unknownHook = true
		} else {
			unsafe[*method.ModelID] = true
		}
	}
	scopes := make(map[ir.ModelID]MutationScope, len(models))
	visited := make(map[ir.ModelID]bool, len(models))
	for start := range models {
		if visited[start] {
			continue
		}
		component := []ir.ModelID{start}
		visited[start] = true
		unscoped := unknownHook
		publicNames := make(map[string]bool)
		for index := 0; index < len(component); index++ {
			id := component[index]
			contract, hasContract := contracts[id]
			_, hasModel := models[id]
			if unsafe[id] || !hasModel || !hasContract || !contract.Exposed || contract.GraphQLName == "" {
				unscoped = true
			} else {
				publicNames[contract.GraphQLName] = true
			}
			for _, target := range adjacent[id] {
				if !visited[target] {
					visited[target] = true
					component = append(component, target)
				}
			}
		}
		scope := MutationScope{Kind: MutationScopeUnscoped}
		if !unscoped {
			names := make([]string, 0, len(publicNames))
			for name := range publicNames {
				names = append(names, name)
			}
			sort.Strings(names)
			scope = MutationScope{Kind: MutationScopeModels, Models: names}
		}
		for _, id := range component {
			scopes[id] = scope
		}
	}
	return scopes
}
