package postgresql

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/jmoiron/sqlx"
)

const postgresQueryPlanPrefix = "EXPLAIN (FORMAT JSON, ANALYZE FALSE, VERBOSE FALSE, COSTS FALSE, SETTINGS FALSE, BUFFERS FALSE, WAL FALSE, SUMMARY FALSE) "

type postgresPlanDocument struct {
	Plan postgresPlanNode `json:"Plan"`
}

type postgresPlanNode struct {
	NodeType     string             `json:"Node Type"`
	RelationName string             `json:"Relation Name"`
	Alias        string             `json:"Alias"`
	IndexName    string             `json:"Index Name"`
	Plans        []postgresPlanNode `json:"Plans"`
}

// CaptureQueryPlan owns and closes the single EXPLAIN row before returning.
// The caller owns connection and releases it after this function returns.
func CaptureQueryPlan(ctx context.Context, connection *sqlx.Conn, statement string, arguments []any, registry *schema.Registry, aliases queryplancapture.AliasMap) (queryplancapture.Plan, error) {
	if connection == nil || registry == nil || !queryplancapture.ValidRenderedReadSQL(statement) {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorInvalid)
	}
	rows, err := connection.QueryxContext(ctx, postgresQueryPlanPrefix+statement, arguments...)
	if err != nil {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	raw, readErr := readPostgresPlanRow(rows)
	closeErr := rows.Close()
	if readErr != nil {
		return queryplancapture.Plan{}, readErr
	}
	if closeErr != nil {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	return sanitizePostgresPlan(raw, registry, aliases)
}

func readPostgresPlanRow(rows *sqlx.Rows) ([]byte, error) {
	if !rows.Next() {
		if rows.Err() != nil {
			return nil, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
		}
		return nil, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return nil, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	if len(raw) == 0 {
		return nil, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	if len(raw) > queryplancapture.MaxRawBytes {
		return nil, queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
	}
	if rows.Next() {
		return nil, queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
	}
	if rows.Err() != nil {
		return nil, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	return append([]byte(nil), raw...), nil
}

func sanitizePostgresPlan(raw []byte, registry *schema.Registry, aliases queryplancapture.AliasMap) (queryplancapture.Plan, error) {
	if err := validatePostgresJSONBounds(raw); err != nil {
		return queryplancapture.Plan{}, err
	}
	var documents []postgresPlanDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&documents); err != nil || len(documents) != 1 || strings.TrimSpace(documents[0].Plan.NodeType) == "" {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	root, err := sanitizePostgresNode(&documents[0].Plan, registry, aliases)
	if err != nil {
		return queryplancapture.Plan{}, err
	}
	return queryplancapture.NewPlan(root)
}

func validatePostgresJSONBounds(raw []byte) error {
	if len(raw) == 0 || len(raw) > queryplancapture.MaxRawBytes {
		return queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
	}
	depth := 0
	objects := 0
	inString := false
	escaped := false
	for _, value := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
				continue
			}
			if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if value == '{' {
				objects++
				if objects > queryplancapture.MaxNodes+2 {
					return queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
				}
			}
			// Array/document/Plans wrappers consume structural depth in
			// addition to provider nodes, so allow a fixed envelope of four.
			if depth > queryplancapture.MaxDepth*2+4 {
				return queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
			}
		}
	}
	if inString || escaped || depth != 0 {
		return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	// Tokenize iteratively before allocating the private decoded tree. This
	// bounds large scalar/array inventories that fit under the byte ceiling.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	type jsonFrame struct {
		object       bool
		expectingKey bool
		keys         map[string]struct{}
	}
	frames := make([]jsonFrame, 0, queryplancapture.MaxDepth*2+4)
	rootValues := 0
	consumeValue := func() error {
		if len(frames) == 0 {
			rootValues++
			if rootValues > 1 {
				return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
			}
			return nil
		}
		parent := &frames[len(frames)-1]
		if parent.object {
			if parent.expectingKey {
				return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
			}
			parent.expectingKey = true
		}
		return nil
	}
	for tokens := 0; ; tokens++ {
		if tokens > queryplancapture.MaxNodes*64 {
			return queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
		}
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				if err := consumeValue(); err != nil {
					return err
				}
				frames = append(frames, jsonFrame{object: true, expectingKey: true, keys: make(map[string]struct{})})
			case '[':
				if err := consumeValue(); err != nil {
					return err
				}
				frames = append(frames, jsonFrame{})
			case '}':
				if len(frames) == 0 || !frames[len(frames)-1].object || !frames[len(frames)-1].expectingKey {
					return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
				}
				frames = frames[:len(frames)-1]
			case ']':
				if len(frames) == 0 || frames[len(frames)-1].object {
					return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
				}
				frames = frames[:len(frames)-1]
			}
			continue
		}
		if len(frames) != 0 && frames[len(frames)-1].object && frames[len(frames)-1].expectingKey {
			key, ok := token.(string)
			if !ok {
				return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
			}
			frame := &frames[len(frames)-1]
			if _, duplicate := frame.keys[key]; duplicate {
				return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
			}
			frame.keys[key] = struct{}{}
			frame.expectingKey = false
			continue
		}
		if err := consumeValue(); err != nil {
			return err
		}
	}
	if len(frames) != 0 || rootValues != 1 {
		return queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	return nil
}

func sanitizePostgresNode(root *postgresPlanNode, registry *schema.Registry, aliases queryplancapture.AliasMap) (queryplancapture.Node, error) {
	type frame struct {
		node     *postgresPlanNode
		depth    int
		expanded bool
	}
	stack := []frame{{node: root, depth: 1}}
	converted := make(map[*postgresPlanNode]queryplancapture.Node)
	nodeCount := 0
	for len(stack) > 0 {
		last := len(stack) - 1
		entry := stack[last]
		stack = stack[:last]
		if entry.node == nil {
			return queryplancapture.Node{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
		}
		if !entry.expanded {
			nodeCount++
			if nodeCount > queryplancapture.MaxNodes || entry.depth > queryplancapture.MaxDepth {
				return queryplancapture.Node{}, queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
			}
			stack = append(stack, frame{node: entry.node, depth: entry.depth, expanded: true})
			for index := len(entry.node.Plans) - 1; index >= 0; index-- {
				stack = append(stack, frame{node: &entry.node.Plans[index], depth: entry.depth + 1})
			}
			continue
		}
		children := make([]queryplancapture.Node, len(entry.node.Plans))
		for index := range entry.node.Plans {
			child, ok := converted[&entry.node.Plans[index]]
			if !ok {
				return queryplancapture.Node{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
			}
			children[index] = child
		}
		converted[entry.node] = parsePostgresNode(*entry.node, children, registry, aliases)
	}
	return converted[root], nil
}

func parsePostgresNode(raw postgresPlanNode, children []queryplancapture.Node, registry *schema.Registry, aliases queryplancapture.AliasMap) queryplancapture.Node {
	identity, identityStatus := postgresNodeIdentity(raw, registry, aliases)
	switch raw.NodeType {
	case "Seq Scan", "Parallel Seq Scan":
		return postgresAccessNode(queryplancapture.AccessFullScan, queryplancapture.IndexID{}, identity, identityStatus, children)
	case "Index Scan", "Index Only Scan":
		if identityStatus != queryplancapture.MatchExact || identity.Role() != queryplancapture.AliasPhysicalAccess && identity.Role() != queryplancapture.AliasCorrelatedRelation {
			return postgresDerivedOrUnknown(identity, identityStatus, children)
		}
		access, indexID, ok := queryplancapture.ResolvePhysicalAccess(registry, golem.PostgreSQL, identity.ModelID(), raw.IndexName, false)
		if !ok {
			return queryplancapture.Unknown(children...)
		}
		return postgresAccessNode(access, indexID, identity, identityStatus, children)
	case "Bitmap Index Scan":
		mappedIdentity, access, indexID, ok := queryplancapture.ResolvePhysicalAccessAny(registry, golem.PostgreSQL, raw.IndexName, true)
		if !ok {
			return queryplancapture.Unknown(children...)
		}
		return queryplancapture.Access(access, mappedIdentity, indexID, children...)
	case "Bitmap Heap Scan":
		if identityStatus != queryplancapture.MatchExact || len(children) != 1 {
			return queryplancapture.Unknown(children...)
		}
		childModel, hasModel := children[0].ModelID()
		indexID, hasIndex := children[0].IndexID()
		if children[0].AccessKind() != queryplancapture.AccessBitmapIndex || !hasModel || !hasIndex || childModel != identity.ModelID() {
			return queryplancapture.Unknown(children...)
		}
		return postgresAccessNode(queryplancapture.AccessBitmapIndex, indexID, identity, identityStatus, children)
	case "Nested Loop", "Hash Join", "Merge Join":
		return queryplancapture.Structural(queryplancapture.NodeJoin, queryplancapture.AliasIdentity{}, children...)
	case "Sort", "Incremental Sort":
		return queryplancapture.Structural(queryplancapture.NodeSort, queryplancapture.AliasIdentity{}, children...)
	case "Aggregate", "Group", "GroupAggregate", "HashAggregate", "WindowAgg":
		return queryplancapture.Structural(queryplancapture.NodeAggregate, identity, children...)
	case "Materialize", "Memoize":
		return queryplancapture.Structural(queryplancapture.NodeMaterialize, identity, children...)
	case "CTE Scan", "Subquery Scan":
		return postgresDerivedOrUnknown(identity, identityStatus, children)
	case "Result":
		if len(children) == 0 {
			return queryplancapture.Constant()
		}
		return queryplancapture.Unknown(children...)
	default:
		return queryplancapture.Unknown(children...)
	}
}

func postgresNodeIdentity(raw postgresPlanNode, registry *schema.Registry, aliases queryplancapture.AliasMap) (queryplancapture.AliasIdentity, queryplancapture.MatchStatus) {
	if raw.Alias != "" {
		return aliases.Resolve(raw.Alias)
	}
	if raw.RelationName == "" {
		return queryplancapture.AliasIdentity{}, queryplancapture.MatchUnknown
	}
	return queryplancapture.ResolveCandidate(registry, golem.PostgreSQL, aliases, raw.RelationName)
}

func postgresAccessNode(access queryplancapture.AccessKind, indexID queryplancapture.IndexID, identity queryplancapture.AliasIdentity, status queryplancapture.MatchStatus, children []queryplancapture.Node) queryplancapture.Node {
	if status != queryplancapture.MatchExact {
		return queryplancapture.Unknown(children...)
	}
	if identity.Role() != queryplancapture.AliasPhysicalAccess && identity.Role() != queryplancapture.AliasCorrelatedRelation {
		return postgresDerivedOrUnknown(identity, status, children)
	}
	accessNode := queryplancapture.Access(access, identity, indexID, children...)
	if identity.Role() == queryplancapture.AliasCorrelatedRelation {
		return queryplancapture.Structural(queryplancapture.NodeCorrelatedRelation, identity, accessNode)
	}
	return accessNode
}

func postgresDerivedOrUnknown(identity queryplancapture.AliasIdentity, status queryplancapture.MatchStatus, children []queryplancapture.Node) queryplancapture.Node {
	if status != queryplancapture.MatchExact {
		return queryplancapture.Unknown(children...)
	}
	switch identity.Role() {
	case queryplancapture.AliasAggregate:
		return queryplancapture.Structural(queryplancapture.NodeAggregate, identity, children...)
	case queryplancapture.AliasMaterialize:
		return queryplancapture.Structural(queryplancapture.NodeMaterialize, identity, children...)
	case queryplancapture.AliasCorrelatedRelation:
		return queryplancapture.Structural(queryplancapture.NodeCorrelatedRelation, identity, children...)
	default:
		return queryplancapture.Unknown(children...)
	}
}
