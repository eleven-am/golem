package batch

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

type DependentsRequest struct {
	Registry      *schema.Registry
	Provider      policyir.Provider
	Parent        policyir.ModelID
	Parents       []mutationdecode.Row
	Effect        schema.DeleteEffect
	MaxRows       uint32
	MaxParameters uint32
}

type RowsRequest struct {
	Registry      *schema.Registry
	Provider      policyir.Provider
	Model         policyir.ModelID
	Rows          []mutationdecode.Row
	MaxParameters uint32
}

func LimitError(model policyir.ModelID, detail string) error {
	return fail(CodeLimit, model, policyir.FieldID{}, detail, nil)
}

func RenderDependents(request DependentsRequest) ([]Statement, error) {
	if request.MaxRows == 0 || request.MaxParameters == 0 || len(request.Parents) == 0 {
		return nil, fail(CodeInput, request.Parent, policyir.FieldID{}, "dependent capture requires parents and positive bounds", nil)
	}
	parent, err := standaloneContext(request.Registry, request.Provider, request.Parent, "golem_cp")
	if err != nil {
		return nil, err
	}
	child, err := standaloneContext(request.Registry, request.Provider, policyir.ModelID(request.Effect.SourceModelID()), "golem_cd")
	if err != nil {
		return nil, err
	}
	correlations := make([]string, 0, len(request.Effect.Correlation()))
	for _, pair := range request.Effect.Correlation() {
		parentField, parentOK := parent.resolver.Field(parent.provider, parent.logical, policyir.FieldID(pair.ParentFieldID()))
		childField, childOK := child.resolver.Field(child.provider, child.logical, policyir.FieldID(pair.ChildFieldID()))
		if !parentOK || !childOK {
			return nil, fail(CodeSchema, child.logical, policyir.FieldID(pair.ChildFieldID()), "dependent correlation field has no physical descriptor", nil)
		}
		correlations = append(correlations, parent.qualified(parentField.Column)+" = "+child.qualified(childField.Column))
	}
	fields, columns, err := child.completeColumns()
	if err != nil {
		return nil, err
	}
	chunk := int(request.MaxParameters) / len(parent.primary)
	if chunk < 1 {
		return nil, fail(CodeLimit, request.Parent, policyir.FieldID{}, "parameter bound cannot hold one parent identity", nil)
	}
	identities, err := parent.identities(request.Parents)
	if err != nil {
		return nil, err
	}
	var statements []Statement
	for start := 0; start < len(identities); start += chunk {
		end := min(start+chunk, len(identities))
		where, bindings, whereErr := parent.identitiesWhere(identities[start:end], 0)
		if whereErr != nil {
			return nil, whereErr
		}
		text := "SELECT " + strings.Join(fields, ", ") + " FROM " + child.dialect.Table(child.model) + " AS " + child.dialect.Quote(child.alias) +
			" WHERE EXISTS (SELECT 1 FROM " + parent.dialect.Table(parent.model) + " AS " + parent.dialect.Quote(parent.alias) + " WHERE (" + where + ") AND " + strings.Join(correlations, " AND ") + ")" +
			" ORDER BY " + child.orderBy() + fmt.Sprintf(" LIMIT %d", uint64(request.MaxRows)+1)
		statements = append(statements, Statement{role: CaptureDependents, text: text, bindings: bindings, columns: columns, cardinality: AtMostSentinelRows})
	}
	return statements, nil
}

func RenderRows(request RowsRequest) ([]Statement, error) {
	if request.MaxParameters == 0 || len(request.Rows) == 0 {
		return nil, fail(CodeInput, request.Model, policyir.FieldID{}, "row capture requires rows and a positive parameter bound", nil)
	}
	context, err := standaloneContext(request.Registry, request.Provider, request.Model, "golem_cr")
	if err != nil {
		return nil, err
	}
	fields, columns, err := context.completeColumns()
	if err != nil {
		return nil, err
	}
	chunk := int(request.MaxParameters) / len(context.primary)
	if chunk < 1 {
		return nil, fail(CodeLimit, request.Model, policyir.FieldID{}, "parameter bound cannot hold one identity", nil)
	}
	identities, err := context.identities(request.Rows)
	if err != nil {
		return nil, err
	}
	var statements []Statement
	for start := 0; start < len(identities); start += chunk {
		end := min(start+chunk, len(identities))
		where, bindings, whereErr := context.identitiesWhere(identities[start:end], 0)
		if whereErr != nil {
			return nil, whereErr
		}
		text := "SELECT " + strings.Join(fields, ", ") + " FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + where + " ORDER BY " + context.orderBy()
		statements = append(statements, Statement{role: CaptureDependents, text: text, bindings: bindings, columns: columns, cardinality: AtMostSentinelRows})
	}
	return statements, nil
}

func PrimaryKey(registry *schema.Registry, provider policyir.Provider, row mutationdecode.Row) (string, error) {
	context, err := standaloneContext(registry, provider, row.ModelID(), "golem_ck")
	if err != nil {
		return "", err
	}
	_, key, err := context.encodedPrimary(row)
	if err != nil {
		return "", err
	}
	model := row.ModelID()
	return string(model[:]) + key, nil
}

func standaloneContext(registry *schema.Registry, provider policyir.Provider, model policyir.ModelID, alias physical.PhysicalName) (renderContext, error) {
	if registry == nil {
		return renderContext{}, fail(CodeInput, model, policyir.FieldID{}, "active schema registry is required", nil)
	}
	dialect, _, err := providerDialect(provider)
	if err != nil {
		return renderContext{}, fail(CodeProvider, model, policyir.FieldID{}, "provider is unsupported", err)
	}
	resolver := policysql.SchemaResolver(registry)
	physicalModel, ok := resolver.Model(provider, model)
	if !ok {
		return renderContext{}, fail(CodeSchema, model, policyir.FieldID{}, "physical model is absent", nil)
	}
	logical, ok := registry.Model(golem.ModelID(model))
	if !ok || len(logical.PrimaryKey()) == 0 {
		return renderContext{}, fail(CodeSchema, model, policyir.FieldID{}, "cascaded model requires a declared primary key", nil)
	}
	return renderContext{registry: registry, provider: provider, dialect: dialect, resolver: resolver, model: physicalModel, alias: alias, primary: toPolicyFields(logical.PrimaryKey()), logical: model}, nil
}

func (context renderContext) identities(rows []mutationdecode.Row) ([][]any, error) {
	identities := make([][]any, len(rows))
	for index, row := range rows {
		values, _, err := context.encodedPrimary(row)
		if err != nil {
			return nil, err
		}
		identities[index] = values
	}
	return identities, nil
}
