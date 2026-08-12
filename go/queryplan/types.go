package queryplan

import (
	"github.com/eleven-am/golem/go/golem"
	internalreport "github.com/eleven-am/golem/go/internal/queryplanreport"
)

// IndexID is the stable logical identity of a provider access object. It is a
// report identity only and grants no query or provider capability.
type IndexID [16]byte

type Operation string

const (
	OperationFindUnique      Operation = "findUnique"
	OperationFindFirst       Operation = "findFirst"
	OperationFindMany        Operation = "findMany"
	OperationCount           Operation = "count"
	OperationAggregate       Operation = "aggregate"
	OperationGroupBy         Operation = "groupBy"
	OperationRelationGroupBy Operation = "relationGroupBy"
	OperationScoped          Operation = "scoped"
)

type StatementPurpose string

const (
	StatementPurposeRoot            StatementPurpose = "root"
	StatementPurposeRelationBatch   StatementPurpose = "relationBatch"
	StatementPurposePolicyHydration StatementPurpose = "policyHydration"
	StatementPurposeAnalytics       StatementPurpose = "analytics"
	StatementPurposeScoped          StatementPurpose = "scoped"
)

type NodeKind string

const (
	NodeKindAccess             NodeKind = "access"
	NodeKindJoin               NodeKind = "join"
	NodeKindSort               NodeKind = "sort"
	NodeKindAggregate          NodeKind = "aggregate"
	NodeKindMaterialize        NodeKind = "materialize"
	NodeKindCorrelatedRelation NodeKind = "correlatedRelation"
	NodeKindDeferredBatch      NodeKind = "deferredBatch"
	NodeKindConstant           NodeKind = "constant"
	NodeKindUnknown            NodeKind = "unknown"
)

type AccessKind string

const (
	AccessKindNone        AccessKind = "none"
	AccessKindPrimaryKey  AccessKind = "primaryKey"
	AccessKindUniqueIndex AccessKind = "uniqueIndex"
	AccessKindIndex       AccessKind = "index"
	AccessKindBitmapIndex AccessKind = "bitmapIndex"
	AccessKindFullScan    AccessKind = "fullScan"
	AccessKindConstant    AccessKind = "constant"
	AccessKindUnknown     AccessKind = "unknown"
)

type Warning string

const (
	WarningFullScan            Warning = "FULL_SCAN"
	WarningTemporarySort       Warning = "TEMPORARY_SORT"
	WarningMaterialization     Warning = "MATERIALIZATION"
	WarningDeferredBatch       Warning = "DEFERRED_BATCH"
	WarningMultiStatement      Warning = "MULTI_STATEMENT"
	WarningUnknownProviderNode Warning = "UNKNOWN_PROVIDER_NODE"
)

// Report is an immutable closed query-plan diagnostic. Its zero value is the
// invalid report returned alongside a failed explain operation.
type Report internalreport.Report

func (report Report) FormatVersion() uint16    { return internalreport.Report(report).FormatVersion() }
func (report Report) Provider() golem.Provider { return internalreport.Report(report).Provider() }
func (report Report) Operation() Operation {
	return Operation(internalreport.Report(report).Operation())
}
func (report Report) RootModelID() golem.ModelID { return internalreport.Report(report).RootModelID() }
func (report Report) MinimumExecutionStatements() uint32 {
	return internalreport.Report(report).MinimumExecutionStatements()
}
func (report Report) MaximumExecutionStatements() uint32 {
	return internalreport.Report(report).MaximumExecutionStatements()
}
func (report Report) CanonicalDigest() [32]byte {
	return internalreport.Report(report).CanonicalDigest()
}

func (report Report) Statements() []Statement {
	values := internalreport.Report(report).Statements()
	result := make([]Statement, len(values))
	for index, value := range values {
		result[index] = Statement(value)
	}
	return result
}

func (report Report) Warnings() []Warning {
	values := internalreport.Report(report).Warnings()
	result := make([]Warning, len(values))
	for index, value := range values {
		result[index] = Warning(value)
	}
	return result
}

type Statement internalreport.Statement

func (statement Statement) Ordinal() uint32 { return internalreport.Statement(statement).Ordinal() }
func (statement Statement) Purpose() StatementPurpose {
	return StatementPurpose(internalreport.Statement(statement).Purpose())
}
func (statement Statement) Root() Node { return Node(internalreport.Statement(statement).Root()) }

type Node internalreport.Node

func (node Node) Kind() NodeKind                       { return NodeKind(internalreport.Node(node).Kind()) }
func (node Node) Access() AccessKind                   { return AccessKind(internalreport.Node(node).Access()) }
func (node Node) FieldIDs() []golem.FieldID            { return internalreport.Node(node).FieldIDs() }
func (node Node) ModelID() (golem.ModelID, bool)       { return internalreport.Node(node).ModelID() }
func (node Node) RelationID() (golem.RelationID, bool) { return internalreport.Node(node).RelationID() }
func (node Node) BatchCapacity() (uint32, bool)        { return internalreport.Node(node).BatchCapacity() }
func (node Node) MinimumExecutionStatements() (uint32, bool) {
	return internalreport.Node(node).MinimumExecutionStatements()
}
func (node Node) MaximumExecutionStatements() (uint32, bool) {
	return internalreport.Node(node).MaximumExecutionStatements()
}

func (node Node) IndexID() (IndexID, bool) {
	value, ok := internalreport.Node(node).IndexID()
	return IndexID(value), ok
}

func (node Node) Children() []Node {
	values := internalreport.Node(node).Children()
	result := make([]Node, len(values))
	for index, value := range values {
		result[index] = Node(value)
	}
	return result
}

func (node Node) Warnings() []Warning {
	values := internalreport.Node(node).Warnings()
	result := make([]Warning, len(values))
	for index, value := range values {
		result[index] = Warning(value)
	}
	return result
}
