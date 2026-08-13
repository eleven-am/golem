// Package queryplanreport owns the validated producer representation behind
// the public immutable queryplan report. It contains only sanitized, typed
// facts; provider diagnostic strings must be discarded before Build is called.
package queryplanreport

import "github.com/eleven-am/golem/go/golem"

const FormatVersion uint16 = 1

type IndexID [16]byte

type Input struct {
	Provider                   golem.Provider
	Operation                  string
	RootModelID                golem.ModelID
	Statements                 []StatementInput
	MinimumExecutionStatements uint32
	MaximumExecutionStatements uint32
}

type StatementInput struct {
	Ordinal uint32
	Purpose string
	Root    NodeInput
}

type NodeInput struct {
	Kind          string
	Access        string
	ModelID       golem.ModelID
	HasModelID    bool
	FieldIDs      []golem.FieldID
	RelationID    golem.RelationID
	HasRelationID bool
	IndexID       IndexID
	HasIndexID    bool
	BatchCapacity uint32
	BatchMinimum  uint32
	BatchMaximum  uint32
	HasBatch      bool
	Children      []NodeInput
}

type Report struct {
	formatVersion uint16
	provider      golem.Provider
	operation     string
	rootModelID   golem.ModelID
	statements    []Statement
	minimum       uint32
	maximum       uint32
	warnings      []string
	digest        [32]byte
}

func (value Report) FormatVersion() uint16              { return value.formatVersion }
func (value Report) Provider() golem.Provider           { return value.provider }
func (value Report) Operation() string                  { return value.operation }
func (value Report) RootModelID() golem.ModelID         { return value.rootModelID }
func (value Report) MinimumExecutionStatements() uint32 { return value.minimum }
func (value Report) MaximumExecutionStatements() uint32 { return value.maximum }
func (value Report) CanonicalDigest() [32]byte          { return value.digest }
func (value Report) Statements() []Statement            { return cloneStatements(value.statements) }
func (value Report) Warnings() []string                 { return append([]string(nil), value.warnings...) }

type Statement struct {
	ordinal uint32
	purpose string
	root    Node
}

func (value Statement) Ordinal() uint32 { return value.ordinal }
func (value Statement) Purpose() string { return value.purpose }
func (value Statement) Root() Node      { return cloneNode(value.root) }

type Node struct {
	kind          string
	access        string
	modelID       golem.ModelID
	hasModelID    bool
	fieldIDs      []golem.FieldID
	relationID    golem.RelationID
	hasRelationID bool
	indexID       IndexID
	hasIndexID    bool
	batchCapacity uint32
	batchMinimum  uint32
	batchMaximum  uint32
	hasBatch      bool
	children      []Node
	warnings      []string
}

func (value Node) Kind() string                         { return value.kind }
func (value Node) Access() string                       { return value.access }
func (value Node) FieldIDs() []golem.FieldID            { return append([]golem.FieldID(nil), value.fieldIDs...) }
func (value Node) Children() []Node                     { return cloneNodes(value.children) }
func (value Node) Warnings() []string                   { return append([]string(nil), value.warnings...) }
func (value Node) ModelID() (golem.ModelID, bool)       { return value.modelID, value.hasModelID }
func (value Node) RelationID() (golem.RelationID, bool) { return value.relationID, value.hasRelationID }
func (value Node) IndexID() (IndexID, bool)             { return value.indexID, value.hasIndexID }
func (value Node) BatchCapacity() (uint32, bool)        { return value.batchCapacity, value.hasBatch }
func (value Node) MinimumExecutionStatements() (uint32, bool) {
	return value.batchMinimum, value.hasBatch
}
func (value Node) MaximumExecutionStatements() (uint32, bool) {
	return value.batchMaximum, value.hasBatch
}

func cloneStatements(values []Statement) []Statement {
	result := make([]Statement, len(values))
	for index, value := range values {
		result[index] = Statement{ordinal: value.ordinal, purpose: value.purpose, root: cloneNode(value.root)}
	}
	return result
}

func cloneNodes(values []Node) []Node {
	result := make([]Node, len(values))
	for index, value := range values {
		result[index] = cloneNode(value)
	}
	return result
}

func cloneNode(value Node) Node {
	value.fieldIDs = append([]golem.FieldID(nil), value.fieldIDs...)
	value.children = cloneNodes(value.children)
	value.warnings = append([]string(nil), value.warnings...)
	return value
}
