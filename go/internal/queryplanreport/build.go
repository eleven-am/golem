package queryplanreport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"

	"github.com/eleven-am/golem/go/golem"
)

const (
	maxStatements   = 32
	maxNodes        = 256
	maxNodeDepth    = 32
	maxFields       = 64
	maxWarnings     = 64
	maxEncodedBytes = 256 << 10
)

const (
	operationFindUnique      = "findUnique"
	operationFindFirst       = "findFirst"
	operationFindMany        = "findMany"
	operationCount           = "count"
	operationAggregate       = "aggregate"
	operationGroupBy         = "groupBy"
	operationRelationGroupBy = "relationGroupBy"
	operationScoped          = "scoped"

	purposeRoot            = "root"
	purposeRelationBatch   = "relationBatch"
	purposePolicyHydration = "policyHydration"
	purposeAnalytics       = "analytics"
	purposeScoped          = "scoped"

	kindAccess             = "access"
	kindJoin               = "join"
	kindSort               = "sort"
	kindAggregate          = "aggregate"
	kindMaterialize        = "materialize"
	kindCorrelatedRelation = "correlatedRelation"
	kindDeferredBatch      = "deferredBatch"
	kindConstant           = "constant"
	kindUnknown            = "unknown"

	accessNone        = "none"
	accessPrimaryKey  = "primaryKey"
	accessUniqueIndex = "uniqueIndex"
	accessIndex       = "index"
	accessBitmapIndex = "bitmapIndex"
	accessFullScan    = "fullScan"
	accessConstant    = "constant"
	accessUnknown     = "unknown"

	warningFullScan            = "FULL_SCAN"
	warningTemporarySort       = "TEMPORARY_SORT"
	warningMaterialization     = "MATERIALIZATION"
	warningDeferredBatch       = "DEFERRED_BATCH"
	warningMultiStatement      = "MULTI_STATEMENT"
	warningUnknownProviderNode = "UNKNOWN_PROVIDER_NODE"
)

type buildCounts struct {
	nodes        int
	nodeWarnings int
	deferredMin  uint64
	deferredMax  uint64
	reportFlags  map[string]bool
}

// Build validates, canonicalizes, and deeply copies one sanitized report.
// Input contains no raw provider diagnostic vocabulary by construction.
func Build(input Input) (Report, error) {
	if !validProvider(input.Provider) || !validOperation(input.Operation) || input.RootModelID == (golem.ModelID{}) {
		return Report{}, fail(CodeInvalid)
	}
	if len(input.Statements) == 0 {
		return Report{}, fail(CodeInvalid)
	}
	if len(input.Statements) > maxStatements {
		return Report{}, fail(CodeTooComplex)
	}
	if input.MaximumExecutionStatements == math.MaxUint32 {
		return Report{}, fail(CodeUnavailable)
	}
	if input.MinimumExecutionStatements > input.MaximumExecutionStatements {
		return Report{}, fail(CodeInvalid)
	}

	counts := buildCounts{reportFlags: make(map[string]bool)}
	statements := make([]Statement, len(input.Statements))
	unconditional := uint64(0)
	primaryPurpose, ok := primaryStatementPurpose(input.Operation)
	if !ok {
		return Report{}, fail(CodeInvalid)
	}
	for index, candidate := range input.Statements {
		if candidate.Ordinal != uint32(index) || !validPurpose(candidate.Purpose) {
			return Report{}, fail(CodeInvalid)
		}
		if index == 0 {
			if candidate.Purpose != primaryPurpose {
				return Report{}, fail(CodeInvalid)
			}
		} else if candidate.Purpose != purposeRelationBatch && candidate.Purpose != purposePolicyHydration {
			return Report{}, fail(CodeInvalid)
		}
		root, err := buildNode(candidate.Root, 1, &counts)
		if err != nil {
			return Report{}, err
		}
		if index == 0 && root.kind == kindDeferredBatch {
			return Report{}, fail(CodeInvalid)
		}
		if candidate.Purpose == purposeRelationBatch && root.kind != kindDeferredBatch {
			return Report{}, fail(CodeInvalid)
		}
		if root.kind != kindDeferredBatch {
			unconditional++
		}
		statements[index] = Statement{ordinal: candidate.Ordinal, purpose: candidate.Purpose, root: root}
	}

	minimum := unconditional + counts.deferredMin
	maximum := unconditional + counts.deferredMax
	if minimum > math.MaxUint32 || maximum >= math.MaxUint32 {
		return Report{}, fail(CodeUnavailable)
	}
	if uint32(minimum) != input.MinimumExecutionStatements || uint32(maximum) != input.MaximumExecutionStatements {
		return Report{}, fail(CodeInvalid)
	}
	if maximum > 1 {
		counts.reportFlags[warningMultiStatement] = true
	}
	warnings := canonicalWarnings(counts.reportFlags)
	if counts.nodeWarnings+len(warnings) > maxWarnings {
		return Report{}, fail(CodeTooComplex)
	}

	report := Report{
		formatVersion: FormatVersion,
		provider:      input.Provider,
		operation:     input.Operation,
		rootModelID:   input.RootModelID,
		statements:    statements,
		minimum:       input.MinimumExecutionStatements,
		maximum:       input.MaximumExecutionStatements,
		warnings:      warnings,
	}
	encoded, err := canonicalBytes(report)
	if err != nil {
		return Report{}, err
	}
	if len(encoded) > maxEncodedBytes {
		return Report{}, fail(CodeTooComplex)
	}
	report.digest = sha256.Sum256(encoded)
	return report, nil
}

func buildNode(input NodeInput, depth int, counts *buildCounts) (Node, error) {
	if depth > maxNodeDepth || counts.nodes >= maxNodes {
		return Node{}, fail(CodeTooComplex)
	}
	counts.nodes++
	if !validNodeAccess(input.Kind, input.Access) {
		return Node{}, fail(CodeInvalid)
	}
	if len(input.FieldIDs) > maxFields {
		return Node{}, fail(CodeTooComplex)
	}
	if input.HasModelID != (input.ModelID != (golem.ModelID{})) || input.HasRelationID != (input.RelationID != (golem.RelationID{})) || input.HasIndexID != (input.IndexID != (IndexID{})) {
		return Node{}, fail(CodeInvalid)
	}
	if hasDuplicateOrZeroFields(input.FieldIDs) {
		return Node{}, fail(CodeInvalid)
	}
	if input.Kind == kindDeferredBatch && input.BatchMaximum == math.MaxUint32 {
		return Node{}, fail(CodeUnavailable)
	}
	if !validIdentityShape(input) || !validBatchShape(input) {
		return Node{}, fail(CodeInvalid)
	}

	warnings := nodeWarnings(input.Kind, input.Access)
	counts.nodeWarnings += len(warnings)
	for _, warning := range warnings {
		counts.reportFlags[warning] = true
	}
	if input.Kind == kindDeferredBatch {
		counts.deferredMin += uint64(input.BatchMinimum)
		counts.deferredMax += uint64(input.BatchMaximum)
		if counts.deferredMin > math.MaxUint32 || counts.deferredMax >= math.MaxUint32 {
			return Node{}, fail(CodeUnavailable)
		}
	}

	node := Node{
		kind:          input.Kind,
		access:        input.Access,
		modelID:       input.ModelID,
		hasModelID:    input.HasModelID,
		fieldIDs:      append([]golem.FieldID(nil), input.FieldIDs...),
		relationID:    input.RelationID,
		hasRelationID: input.HasRelationID,
		indexID:       input.IndexID,
		hasIndexID:    input.HasIndexID,
		batchCapacity: input.BatchCapacity,
		batchMinimum:  input.BatchMinimum,
		batchMaximum:  input.BatchMaximum,
		hasBatch:      input.HasBatch,
		warnings:      warnings,
	}
	node.children = make([]Node, len(input.Children))
	for index, child := range input.Children {
		value, err := buildNode(child, depth+1, counts)
		if err != nil {
			return Node{}, err
		}
		node.children[index] = value
	}
	return node, nil
}

func validIdentityShape(input NodeInput) bool {
	indexAccess := input.Access == accessPrimaryKey || input.Access == accessUniqueIndex || input.Access == accessIndex || input.Access == accessBitmapIndex
	if input.HasIndexID != indexAccess {
		return false
	}
	if (indexAccess || input.Access == accessFullScan) && !input.HasModelID {
		return false
	}
	switch input.Kind {
	case kindConstant:
		return !input.HasModelID && !input.HasRelationID && len(input.FieldIDs) == 0 && len(input.Children) == 0
	case kindCorrelatedRelation, kindDeferredBatch:
		return input.HasModelID && input.HasRelationID
	default:
		return true
	}
}

func validBatchShape(input NodeInput) bool {
	if input.Kind != kindDeferredBatch {
		return !input.HasBatch && input.BatchCapacity == 0 && input.BatchMinimum == 0 && input.BatchMaximum == 0
	}
	return input.HasBatch && input.BatchCapacity > 0 && input.BatchMaximum != math.MaxUint32 && input.BatchMinimum <= input.BatchMaximum && len(input.Children) == 0
}

func validNodeAccess(kind, access string) bool {
	switch kind {
	case kindAccess:
		switch access {
		case accessPrimaryKey, accessUniqueIndex, accessIndex, accessBitmapIndex, accessFullScan, accessUnknown:
			return true
		}
	case kindConstant:
		return access == accessConstant
	case kindUnknown:
		return access == accessUnknown
	case kindJoin, kindSort, kindAggregate, kindMaterialize, kindCorrelatedRelation, kindDeferredBatch:
		return access == accessNone
	}
	return false
}

func hasDuplicateOrZeroFields(values []golem.FieldID) bool {
	seen := make(map[golem.FieldID]struct{}, len(values))
	for _, value := range values {
		if value == (golem.FieldID{}) {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func nodeWarnings(kind, access string) []string {
	flags := make(map[string]bool)
	if access == accessFullScan {
		flags[warningFullScan] = true
	}
	if kind == kindSort {
		flags[warningTemporarySort] = true
	}
	if kind == kindMaterialize {
		flags[warningMaterialization] = true
	}
	if kind == kindDeferredBatch {
		flags[warningDeferredBatch] = true
	}
	if kind == kindUnknown || access == accessUnknown {
		flags[warningUnknownProviderNode] = true
	}
	return canonicalWarnings(flags)
}

func canonicalWarnings(flags map[string]bool) []string {
	order := [...]string{warningFullScan, warningTemporarySort, warningMaterialization, warningDeferredBatch, warningMultiStatement, warningUnknownProviderNode}
	result := make([]string, 0, len(order))
	for _, warning := range order {
		if flags[warning] {
			result = append(result, warning)
		}
	}
	return result
}

func validProvider(value golem.Provider) bool {
	return value == golem.SQLite || value == golem.PostgreSQL
}

func validOperation(value string) bool {
	switch value {
	case operationFindUnique, operationFindFirst, operationFindMany, operationCount, operationAggregate, operationGroupBy, operationRelationGroupBy, operationScoped:
		return true
	default:
		return false
	}
}

func validPurpose(value string) bool {
	switch value {
	case purposeRoot, purposeRelationBatch, purposePolicyHydration, purposeAnalytics, purposeScoped:
		return true
	default:
		return false
	}
}

func primaryStatementPurpose(operation string) (string, bool) {
	switch operation {
	case operationFindUnique, operationFindFirst, operationFindMany, operationCount:
		return purposeRoot, true
	case operationAggregate, operationGroupBy, operationRelationGroupBy:
		return purposeAnalytics, true
	case operationScoped:
		return purposeScoped, true
	default:
		return "", false
	}
}

type reportDocument struct {
	FormatVersion              uint16              `json:"formatVersion"`
	Provider                   golem.Provider      `json:"provider"`
	Operation                  string              `json:"operation"`
	RootModelID                string              `json:"rootModelId"`
	Statements                 []statementDocument `json:"statements"`
	MinimumExecutionStatements uint32              `json:"minimumExecutionStatements"`
	MaximumExecutionStatements uint32              `json:"maximumExecutionStatements"`
	Warnings                   []string            `json:"warnings"`
}

type statementDocument struct {
	Ordinal uint32       `json:"ordinal"`
	Purpose string       `json:"purpose"`
	Root    nodeDocument `json:"root"`
}

type nodeDocument struct {
	Kind          string         `json:"kind"`
	Access        string         `json:"access"`
	ModelID       *string        `json:"modelId,omitempty"`
	FieldIDs      []string       `json:"fieldIds"`
	RelationID    *string        `json:"relationId,omitempty"`
	IndexID       *string        `json:"indexId,omitempty"`
	BatchCapacity *uint32        `json:"batchCapacity,omitempty"`
	BatchMinimum  *uint32        `json:"minimumExecutionStatements,omitempty"`
	BatchMaximum  *uint32        `json:"maximumExecutionStatements,omitempty"`
	Children      []nodeDocument `json:"children"`
	Warnings      []string       `json:"warnings"`
}

func canonicalBytes(report Report) ([]byte, error) {
	document := reportDocument{
		FormatVersion: report.formatVersion, Provider: report.provider, Operation: report.operation,
		RootModelID: encodeID(report.rootModelID[:]), MinimumExecutionStatements: report.minimum,
		MaximumExecutionStatements: report.maximum, Warnings: append([]string(nil), report.warnings...),
		Statements: make([]statementDocument, len(report.statements)),
	}
	for index, statement := range report.statements {
		document.Statements[index] = statementDocument{Ordinal: statement.ordinal, Purpose: statement.purpose, Root: documentNode(statement.root)}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fail(CodeInvalid)
	}
	return encoded, nil
}

func documentNode(node Node) nodeDocument {
	document := nodeDocument{Kind: node.kind, Access: node.access, FieldIDs: make([]string, len(node.fieldIDs)), Children: make([]nodeDocument, len(node.children)), Warnings: append([]string(nil), node.warnings...)}
	for index, field := range node.fieldIDs {
		document.FieldIDs[index] = encodeID(field[:])
	}
	for index, child := range node.children {
		document.Children[index] = documentNode(child)
	}
	if node.hasModelID {
		value := encodeID(node.modelID[:])
		document.ModelID = &value
	}
	if node.hasRelationID {
		value := encodeID(node.relationID[:])
		document.RelationID = &value
	}
	if node.hasIndexID {
		value := encodeID(node.indexID[:])
		document.IndexID = &value
	}
	if node.hasBatch {
		capacity, minimum, maximum := node.batchCapacity, node.batchMinimum, node.batchMaximum
		document.BatchCapacity, document.BatchMinimum, document.BatchMaximum = &capacity, &minimum, &maximum
	}
	return document
}

func encodeID(value []byte) string { return hex.EncodeToString(value) }
