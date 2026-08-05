// Package keyindex resolves keys, indexes, checks, and generated-column
// assignments against an already resolved scalar ModelIR. It does not inspect
// Go packages, interpret GolemModel methods, resolve relations, or lower to a
// provider.
package keyindex

import "github.com/eleven-am/golem/go/internal/compiler/ir"

type AdvancedModelDeclarations struct {
	ModelID   ir.ModelID
	Keys      []KeyDeclaration
	Indexes   []IndexDeclaration
	Checks    []CheckDeclaration
	Generated []GeneratedDeclaration
}

// KeyDeclaration is the typed input produced by a future GolemModel static
// interpreter. Fields are stable IDs and their order is semantic.
type KeyDeclaration struct {
	Kind         ir.KeyKind
	PhysicalName ir.SQLIdentifier
	Fields       []ir.FieldID
	Span         ir.SourceSpan
}

type IndexDeclaration struct {
	PhysicalName ir.SQLIdentifier
	Unique       bool
	Method       ir.IndexMethod
	Keys         []ir.IndexKeyIR
	Include      []ir.FieldID
	Predicate    *ir.SchemaPredicateIR
	Provider     ir.ProviderScope
	Span         ir.SourceSpan
}

type CheckDeclaration struct {
	PhysicalName ir.SQLIdentifier
	Predicate    ir.SchemaPredicateIR
	Provider     ir.ProviderScope
	Span         ir.SourceSpan
}

type GeneratedDeclaration struct {
	FieldID                 ir.FieldID
	Generation              ir.GeneratedColumnIR
	ExpressionProvenNonNull bool
	Span                    ir.SourceSpan
}

type GeneratedAssignment struct {
	FieldID    ir.FieldID
	Generation ir.GeneratedColumnIR
}

type ModelFragment struct {
	ModelID         ir.ModelID
	PrimaryKey      *ir.KeyIR
	Uniques         []ir.KeyIR
	Indexes         []ir.IndexIR
	Checks          []ir.CheckIR
	Generated       []GeneratedAssignment
	Selectors       []ir.SelectorContractIR
	EqualityIndexes []ir.EqualityIndexIR
}

func (fragment ModelFragment) EqualityIndexed(fieldID ir.FieldID) bool {
	for _, entry := range fragment.EqualityIndexes {
		if entry.FieldID == fieldID {
			return true
		}
	}
	return false
}

type Result struct {
	Fragments   []ModelFragment
	Diagnostics []ir.Diagnostic
}
