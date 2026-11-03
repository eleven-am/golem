package schema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

var operationRoots = []string{"Query", "Mutation", "Subscription"}

var builtinScalars = map[string]bool{"Int": true, "Float": true, "String": true, "Boolean": true, "ID": true}

type reachabilityWalk struct {
	types      map[string]*ast.Definition
	directives map[string]*ast.DirectiveDefinition
	keptTypes  map[string]bool
	keptDirs   map[string]bool
	pending    []string
}

func pruneUnreachable(sdl string) (string, error) {
	source := &ast.Source{Name: "generated.graphql", Input: sdl}
	document, parseErr := parser.ParseSchema(source)
	if parseErr != nil {
		return "", fmt.Errorf("generated GraphQL schema does not parse: %w", parseErr)
	}
	if _, err := gqlparser.LoadSchema(source); err != nil {
		return "", fmt.Errorf("generated GraphQL schema is invalid before pruning: %w", err)
	}
	if document.Schema != nil || document.SchemaExtension != nil || len(document.Extensions) != 0 {
		return "", fmt.Errorf("generated GraphQL schema contains schema or extension definitions that pruning cannot split")
	}
	walk := &reachabilityWalk{
		types:      make(map[string]*ast.Definition, len(document.Definitions)),
		directives: make(map[string]*ast.DirectiveDefinition, len(document.Directives)),
		keptTypes:  map[string]bool{},
		keptDirs:   map[string]bool{},
	}
	for _, definition := range document.Definitions {
		walk.types[definition.Name] = definition
	}
	for _, directive := range document.Directives {
		walk.directives[directive.Name] = directive
	}
	for _, root := range operationRoots {
		if walk.types[root] != nil {
			walk.visitType(root)
		}
	}
	walk.drain()

	type span struct {
		line int
		kind string
		name string
	}
	var spans []span
	for _, definition := range document.Definitions {
		spans = append(spans, span{definition.Position.Line, "type", definition.Name})
	}
	for _, directive := range document.Directives {
		spans = append(spans, span{directive.Position.Line, "directive", directive.Name})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].line < spans[j].line })
	for index := 1; index < len(spans); index++ {
		if spans[index].line == spans[index-1].line {
			return "", fmt.Errorf("generated GraphQL definitions %s and %s share line %d", spans[index-1].name, spans[index].name, spans[index].line)
		}
	}
	lines := strings.Split(sdl, "\n")
	if len(spans) > 0 && strings.TrimSpace(strings.Join(lines[:spans[0].line-1], "\n")) != "" {
		return "", fmt.Errorf("generated GraphQL schema has text before its first definition")
	}
	var kept []string
	for index, current := range spans {
		end := len(lines)
		if index+1 < len(spans) {
			end = spans[index+1].line - 1
		}
		if current.kind == "type" && !walk.keptTypes[current.name] || current.kind == "directive" && !walk.keptDirs[current.name] {
			continue
		}
		kept = append(kept, strings.TrimRight(strings.Join(lines[current.line-1:end], "\n"), "\n "))
	}
	pruned := strings.Join(kept, "\n\n") + "\n"
	if _, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: pruned}); err != nil {
		return "", fmt.Errorf("pruned GraphQL schema is invalid: %w", err)
	}
	return pruned, nil
}

func (w *reachabilityWalk) visitType(name string) {
	if builtinScalars[name] || w.keptTypes[name] {
		return
	}
	w.keptTypes[name] = true
	w.pending = append(w.pending, name)
}

func (w *reachabilityWalk) visitDirectives(directives ast.DirectiveList) {
	for _, directive := range directives {
		if definition := w.directives[directive.Name]; definition != nil && !w.keptDirs[directive.Name] {
			w.keptDirs[directive.Name] = true
			w.visitArguments(definition.Arguments)
		}
	}
}

func (w *reachabilityWalk) visitArguments(arguments ast.ArgumentDefinitionList) {
	for _, argument := range arguments {
		w.visitType(argument.Type.Name())
		w.visitDirectives(argument.Directives)
	}
}

func (w *reachabilityWalk) drain() {
	for len(w.pending) > 0 {
		name := w.pending[len(w.pending)-1]
		w.pending = w.pending[:len(w.pending)-1]
		definition := w.types[name]
		w.visitDirectives(definition.Directives)
		for _, field := range definition.Fields {
			w.visitType(field.Type.Name())
			w.visitArguments(field.Arguments)
			w.visitDirectives(field.Directives)
		}
	}
}
