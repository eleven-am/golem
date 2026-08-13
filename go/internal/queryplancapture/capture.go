// Package queryplancapture is the private, provider-neutral handoff between
// renderer-owned identity maps and provider plan parsers. It deliberately has
// no SQL/name getters and no public query-plan authority.
package queryplancapture

import (
	"errors"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/queryplanreport"
)

// ValidRenderedReadSQL is the final narrow guard before a renderer-owned read
// statement reaches provider EXPLAIN. Rendered reads contain no inline values,
// terminators, comments, or additional statements; values remain bound args.
func ValidRenderedReadSQL(statement string) bool {
	trimmed := strings.TrimSpace(statement)
	if trimmed == "" || strings.ContainsAny(trimmed, ";\x00") || strings.Contains(trimmed, "--") || strings.Contains(trimmed, "/*") {
		return false
	}
	upper := strings.ToUpper(trimmed)
	padded := " " + strings.Join(strings.Fields(upper), " ") + " "
	for _, mutation := range []string{" DELETE ", " INSERT ", " UPDATE ", " MERGE ", " CALL ", " COPY "} {
		if strings.Contains(padded, mutation) {
			return false
		}
	}
	return strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "SELECT\n") || strings.HasPrefix(upper, "WITH ") || strings.HasPrefix(upper, "WITH\n")
}

const (
	MaxRawBytes = 256 << 10
	MaxNodes    = 256
	MaxDepth    = 32
)

type ErrorCode uint8

const (
	ErrorUnavailable ErrorCode = iota + 1
	ErrorTooComplex
	ErrorInvalid
)

type captureError struct{ code ErrorCode }

func (failure *captureError) Error() string {
	if failure == nil {
		return "query plan is invalid"
	}
	switch failure.code {
	case ErrorUnavailable:
		return "query plan is unavailable"
	case ErrorTooComplex:
		return "query plan is too complex"
	default:
		return "query plan is invalid"
	}
}

func Refuse(code ErrorCode) error {
	switch code {
	case ErrorUnavailable, ErrorTooComplex, ErrorInvalid:
		return &captureError{code: code}
	default:
		return &captureError{code: ErrorInvalid}
	}
}

func CodeOf(err error) (ErrorCode, bool) {
	var failure *captureError
	if !errors.As(err, &failure) || failure == nil {
		return 0, false
	}
	return failure.code, true
}

// AliasRole is allocation provenance, not a provider-node classification.
// In particular, derived aliases may never be upgraded into physical access.
type AliasRole uint8

const (
	AliasPhysicalAccess AliasRole = iota + 1
	AliasCorrelatedRelation
	AliasAggregate
	AliasMaterialize
	AliasStructural
)

func validAliasRole(role AliasRole) bool {
	return role >= AliasPhysicalAccess && role <= AliasStructural
}

type aliasMatcher func(string) bool

// AliasFact retains equality authority plus stable identities only. The raw
// alias remains owned by the renderer fact captured by match.
type AliasFact struct {
	match      aliasMatcher
	modelID    golem.ModelID
	relationID golem.RelationID
	fieldIDs   []golem.FieldID
	role       AliasRole
}

func NewAliasFact(match func(string) bool, model golem.ModelID, relation golem.RelationID, fields []golem.FieldID, role AliasRole) (AliasFact, error) {
	if match == nil || model == (golem.ModelID{}) || !validAliasRole(role) {
		return AliasFact{}, Refuse(ErrorInvalid)
	}
	if role == AliasCorrelatedRelation && relation == (golem.RelationID{}) {
		return AliasFact{}, Refuse(ErrorInvalid)
	}
	if invalidFields(fields) {
		return AliasFact{}, Refuse(ErrorInvalid)
	}
	return AliasFact{match: match, modelID: model, relationID: relation, fieldIDs: append([]golem.FieldID(nil), fields...), role: role}, nil
}

type AliasMap struct{ facts []AliasFact }

func NewAliasMap(facts ...AliasFact) AliasMap {
	copyFacts := make([]AliasFact, len(facts))
	for index, fact := range facts {
		fact.fieldIDs = append([]golem.FieldID(nil), fact.fieldIDs...)
		copyFacts[index] = fact
	}
	return AliasMap{facts: copyFacts}
}

type MatchStatus uint8

const (
	MatchUnknown MatchStatus = iota
	MatchExact
	MatchAmbiguous
)

// AliasIdentity is a sanitized schema identity. It contains no alias, SQL, or
// provider object name and grants no matching authority.
type AliasIdentity struct {
	modelID    golem.ModelID
	relationID golem.RelationID
	fieldIDs   []golem.FieldID
	role       AliasRole
}

func PhysicalIdentity(model golem.ModelID) (AliasIdentity, bool) {
	if model == (golem.ModelID{}) {
		return AliasIdentity{}, false
	}
	return AliasIdentity{modelID: model, role: AliasPhysicalAccess}, true
}

func (value AliasIdentity) ModelID() golem.ModelID { return value.modelID }
func (value AliasIdentity) RelationID() (golem.RelationID, bool) {
	return value.relationID, value.relationID != (golem.RelationID{})
}
func (value AliasIdentity) FieldIDs() []golem.FieldID {
	return append([]golem.FieldID(nil), value.fieldIDs...)
}
func (value AliasIdentity) Role() AliasRole { return value.role }

// Resolve compares untrusted input with every renderer-owned matcher. More
// than one match is an ambiguity, even when the copied identities agree.
func (value AliasMap) Resolve(candidate string) (AliasIdentity, MatchStatus) {
	if candidate == "" {
		return AliasIdentity{}, MatchUnknown
	}
	var result AliasIdentity
	found := false
	for _, fact := range value.facts {
		if fact.match == nil || !fact.match(candidate) {
			continue
		}
		if found {
			return AliasIdentity{}, MatchAmbiguous
		}
		found = true
		result = AliasIdentity{modelID: fact.modelID, relationID: fact.relationID, fieldIDs: append([]golem.FieldID(nil), fact.fieldIDs...), role: fact.role}
	}
	if !found {
		return AliasIdentity{}, MatchUnknown
	}
	return result, MatchExact
}

type NodeKind uint8

const (
	NodeAccess NodeKind = iota + 1
	NodeJoin
	NodeSort
	NodeAggregate
	NodeMaterialize
	NodeCorrelatedRelation
	NodeConstant
	NodeUnknown
)

type AccessKind uint8

const (
	AccessNone AccessKind = iota + 1
	AccessPrimaryKey
	AccessUniqueIndex
	AccessIndex
	AccessBitmapIndex
	AccessFullScan
	AccessConstant
	AccessUnknown
)

type IndexID [16]byte

// Node is immutable sanitized provider structure. Provider strings are never
// retained. Children always returns a caller-owned copy.
type Node struct {
	kind     NodeKind
	access   AccessKind
	identity AliasIdentity
	hasModel bool
	indexID  IndexID
	hasIndex bool
	children []Node
}

func Unknown(children ...Node) Node {
	return Node{kind: NodeUnknown, access: AccessUnknown, children: cloneNodes(children)}
}

func Constant() Node { return Node{kind: NodeConstant, access: AccessConstant} }

func Structural(kind NodeKind, identity AliasIdentity, children ...Node) Node {
	return Node{kind: kind, access: AccessNone, identity: cloneIdentity(identity), hasModel: identity.modelID != (golem.ModelID{}), children: cloneNodes(children)}
}

func Access(access AccessKind, identity AliasIdentity, index IndexID, children ...Node) Node {
	return Node{kind: NodeAccess, access: access, identity: cloneIdentity(identity), hasModel: identity.modelID != (golem.ModelID{}), indexID: index, hasIndex: index != (IndexID{}), children: cloneNodes(children)}
}

func (value Node) Kind() NodeKind                       { return value.kind }
func (value Node) AccessKind() AccessKind               { return value.access }
func (value Node) ModelID() (golem.ModelID, bool)       { return value.identity.modelID, value.hasModel }
func (value Node) RelationID() (golem.RelationID, bool) { return value.identity.RelationID() }
func (value Node) FieldIDs() []golem.FieldID            { return value.identity.FieldIDs() }
func (value Node) IndexID() (IndexID, bool)             { return value.indexID, value.hasIndex }
func (value Node) Children() []Node                     { return cloneNodes(value.children) }

type Plan struct {
	root      Node
	nodeCount uint32
}

func NewPlan(root Node) (Plan, error) {
	if err := validate(root); err != nil {
		return Plan{}, err
	}
	count := uint32(0)
	stack := []Node{root}
	for len(stack) > 0 {
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		count++
		stack = append(stack, node.children...)
	}
	return Plan{root: cloneNode(root), nodeCount: count}, nil
}

func (value Plan) Root() Node        { return cloneNode(value.root) }
func (value Plan) NodeCount() uint32 { return value.nodeCount }

// RootInput returns a deep caller-owned copy suitable for the validated
// queryplanreport producer. Provider parsers cannot bypass that validation.
func (value Plan) RootInput() queryplanreport.NodeInput { return reportInput(value.root) }

func validate(root Node) error {
	type item struct {
		node  Node
		depth int
	}
	stack := []item{{node: root, depth: 1}}
	count := 0
	for len(stack) > 0 {
		last := len(stack) - 1
		entry := stack[last]
		stack = stack[:last]
		count++
		if entry.depth > MaxDepth || count > MaxNodes {
			return Refuse(ErrorTooComplex)
		}
		if !validNode(entry.node) {
			return Refuse(ErrorInvalid)
		}
		for _, child := range entry.node.children {
			stack = append(stack, item{node: child, depth: entry.depth + 1})
		}
	}
	return nil
}

func validNode(node Node) bool {
	switch node.kind {
	case NodeAccess:
		if node.access == AccessFullScan {
			return node.hasModel && !node.hasIndex
		}
		if node.access == AccessUnknown {
			return !node.hasModel && !node.hasIndex
		}
		return node.access >= AccessPrimaryKey && node.access <= AccessBitmapIndex && node.hasModel && node.hasIndex
	case NodeConstant:
		return node.access == AccessConstant && !node.hasModel && !node.hasIndex && len(node.children) == 0
	case NodeUnknown:
		return node.access == AccessUnknown && !node.hasModel && !node.hasIndex
	case NodeJoin, NodeSort, NodeAggregate, NodeMaterialize:
		return node.access == AccessNone && !node.hasIndex
	case NodeCorrelatedRelation:
		_, relation := node.identity.RelationID()
		return node.access == AccessNone && node.hasModel && relation && !node.hasIndex
	default:
		return false
	}
}

func reportInput(node Node) queryplanreport.NodeInput {
	result := queryplanreport.NodeInput{
		Kind:          nodeKindString(node.kind),
		Access:        accessKindString(node.access),
		ModelID:       node.identity.modelID,
		HasModelID:    node.hasModel,
		FieldIDs:      node.identity.FieldIDs(),
		RelationID:    node.identity.relationID,
		HasRelationID: node.identity.relationID != (golem.RelationID{}),
		IndexID:       queryplanreport.IndexID(node.indexID),
		HasIndexID:    node.hasIndex,
		Children:      make([]queryplanreport.NodeInput, len(node.children)),
	}
	for index, child := range node.children {
		result.Children[index] = reportInput(child)
	}
	return result
}

func nodeKindString(kind NodeKind) string {
	switch kind {
	case NodeAccess:
		return "access"
	case NodeJoin:
		return "join"
	case NodeSort:
		return "sort"
	case NodeAggregate:
		return "aggregate"
	case NodeMaterialize:
		return "materialize"
	case NodeCorrelatedRelation:
		return "correlatedRelation"
	case NodeConstant:
		return "constant"
	default:
		return "unknown"
	}
}

func accessKindString(kind AccessKind) string {
	switch kind {
	case AccessNone:
		return "none"
	case AccessPrimaryKey:
		return "primaryKey"
	case AccessUniqueIndex:
		return "uniqueIndex"
	case AccessIndex:
		return "index"
	case AccessBitmapIndex:
		return "bitmapIndex"
	case AccessFullScan:
		return "fullScan"
	case AccessConstant:
		return "constant"
	default:
		return "unknown"
	}
}

func invalidFields(fields []golem.FieldID) bool {
	if len(fields) > 64 {
		return true
	}
	seen := make(map[golem.FieldID]struct{}, len(fields))
	for _, field := range fields {
		if field == (golem.FieldID{}) {
			return true
		}
		if _, duplicate := seen[field]; duplicate {
			return true
		}
		seen[field] = struct{}{}
	}
	return false
}

func cloneIdentity(value AliasIdentity) AliasIdentity {
	value.fieldIDs = value.FieldIDs()
	return value
}

func cloneNode(value Node) Node {
	value.identity = cloneIdentity(value.identity)
	value.children = cloneNodes(value.children)
	return value
}

func cloneNodes(values []Node) []Node {
	result := make([]Node, len(values))
	for index, value := range values {
		result[index] = cloneNode(value)
	}
	return result
}
