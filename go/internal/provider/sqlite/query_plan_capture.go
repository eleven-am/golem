package sqlite

import (
	"context"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/jmoiron/sqlx"
)

const sqliteQueryPlanPrefix = "EXPLAIN QUERY PLAN "

type sqlitePlanRow struct {
	id       int64
	parent   int64
	detail   string
	children []int64
}

// CaptureQueryPlan owns and closes every EXPLAIN row before returning. The
// caller retains ownership of connection and must release it only after this
// function returns; this function never closes or returns provider authority.
func CaptureQueryPlan(ctx context.Context, connection *sqlx.Conn, statement string, arguments []any, registry *schema.Registry, aliases queryplancapture.AliasMap) (queryplancapture.Plan, error) {
	if connection == nil || registry == nil || !queryplancapture.ValidRenderedReadSQL(statement) {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorInvalid)
	}
	rows, err := connection.QueryxContext(ctx, sqliteQueryPlanPrefix+statement, arguments...)
	if err != nil {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	planRows, readErr := readSQLitePlanRows(rows)
	closeErr := rows.Close()
	if readErr != nil {
		return queryplancapture.Plan{}, readErr
	}
	if closeErr != nil {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	return sanitizeSQLitePlan(planRows, registry, aliases)
}

func readSQLitePlanRows(rows *sqlx.Rows) ([]sqlitePlanRow, error) {
	result := make([]sqlitePlanRow, 0, 8)
	rawBytes := 0
	for rows.Next() {
		if len(result) >= queryplancapture.MaxNodes {
			return nil, queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
		}
		var id, parent, unused int64
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return nil, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
		}
		rawBytes += len(detail)
		if rawBytes > queryplancapture.MaxRawBytes {
			return nil, queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
		}
		result = append(result, sqlitePlanRow{id: id, parent: parent, detail: detail})
	}
	if err := rows.Err(); err != nil {
		return nil, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	if len(result) == 0 {
		return nil, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	return result, nil
}

func sanitizeSQLitePlan(rows []sqlitePlanRow, registry *schema.Registry, aliases queryplancapture.AliasMap) (queryplancapture.Plan, error) {
	byID := make(map[int64]int, len(rows))
	for index, row := range rows {
		if row.id < 0 {
			return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
		}
		if _, duplicate := byID[row.id]; duplicate {
			return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
		}
		byID[row.id] = index
	}
	roots := make([]int64, 0, len(rows))
	for index := range rows {
		parentIndex, found := byID[rows[index].parent]
		if rows[index].parent == rows[index].id {
			return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
		}
		if !found {
			roots = append(roots, rows[index].id)
			continue
		}
		rows[parentIndex].children = append(rows[parentIndex].children, rows[index].id)
	}
	if len(roots) == 0 {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}

	converted := make(map[int64]queryplancapture.Node, len(rows))
	type frame struct {
		id       int64
		depth    int
		expanded bool
	}
	stack := make([]frame, 0, len(rows)*2)
	for index := len(roots) - 1; index >= 0; index-- {
		stack = append(stack, frame{id: roots[index], depth: 1})
	}
	visited := make(map[int64]bool, len(rows))
	for len(stack) > 0 {
		last := len(stack) - 1
		entry := stack[last]
		stack = stack[:last]
		if entry.depth > queryplancapture.MaxDepth {
			return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorTooComplex)
		}
		rowIndex, ok := byID[entry.id]
		if !ok {
			return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
		}
		row := rows[rowIndex]
		if !entry.expanded {
			if visited[entry.id] {
				return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
			}
			visited[entry.id] = true
			stack = append(stack, frame{id: entry.id, depth: entry.depth, expanded: true})
			for index := len(row.children) - 1; index >= 0; index-- {
				stack = append(stack, frame{id: row.children[index], depth: entry.depth + 1})
			}
			continue
		}
		children := make([]queryplancapture.Node, len(row.children))
		for index, child := range row.children {
			value, present := converted[child]
			if !present {
				return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
			}
			children[index] = value
		}
		converted[entry.id] = parseSQLitePlanDetail(row.detail, children, registry, aliases)
	}
	if len(visited) != len(rows) {
		return queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)
	}
	rootNodes := make([]queryplancapture.Node, len(roots))
	for index, root := range roots {
		rootNodes[index] = converted[root]
	}
	return queryplancapture.NewPlan(combineSQLiteRoots(rootNodes))
}

func parseSQLitePlanDetail(detail string, children []queryplancapture.Node, registry *schema.Registry, aliases queryplancapture.AliasMap) queryplancapture.Node {
	fields := strings.Fields(detail)
	if len(fields) == 0 {
		return queryplancapture.Unknown(children...)
	}
	if detail == "SCAN CONSTANT ROW" {
		return queryplancapture.Constant()
	}
	if strings.HasPrefix(detail, "USE TEMP B-TREE FOR ") {
		if strings.HasSuffix(detail, "GROUP BY") {
			return queryplancapture.Structural(queryplancapture.NodeAggregate, queryplancapture.AliasIdentity{}, children...)
		}
		return queryplancapture.Structural(queryplancapture.NodeSort, queryplancapture.AliasIdentity{}, children...)
	}
	if len(fields) >= 2 && (fields[0] == "MATERIALIZE" || fields[0] == "CO-ROUTINE") {
		identity, status := aliases.Resolve(fields[1])
		if status != queryplancapture.MatchExact {
			return queryplancapture.Unknown(children...)
		}
		if fields[0] == "MATERIALIZE" || identity.Role() == queryplancapture.AliasMaterialize {
			return queryplancapture.Structural(queryplancapture.NodeMaterialize, identity, children...)
		}
		return structuralForAlias(identity, children)
	}
	if len(fields) < 2 || fields[0] != "SCAN" && fields[0] != "SEARCH" {
		return queryplancapture.Unknown(children...)
	}
	identity, status := queryplancapture.ResolveCandidate(registry, golem.SQLite, aliases, fields[1])
	if status != queryplancapture.MatchExact {
		return queryplancapture.Unknown(children...)
	}
	if identity.Role() != queryplancapture.AliasPhysicalAccess && identity.Role() != queryplancapture.AliasCorrelatedRelation {
		return structuralForAlias(identity, children)
	}
	var accessNode queryplancapture.Node
	if fields[0] == "SCAN" {
		accessNode = queryplancapture.Access(queryplancapture.AccessFullScan, identity, queryplancapture.IndexID{}, children...)
	} else {
		access, indexID, ok := sqliteSearchAccess(detail, identity.ModelID(), registry)
		if !ok {
			return queryplancapture.Unknown(children...)
		}
		accessNode = queryplancapture.Access(access, identity, indexID, children...)
	}
	if identity.Role() == queryplancapture.AliasCorrelatedRelation {
		return queryplancapture.Structural(queryplancapture.NodeCorrelatedRelation, identity, accessNode)
	}
	return accessNode
}

func sqliteSearchAccess(detail string, model golem.ModelID, registry *schema.Registry) (queryplancapture.AccessKind, queryplancapture.IndexID, bool) {
	using := strings.Index(detail, " USING ")
	if using < 0 {
		return 0, queryplancapture.IndexID{}, false
	}
	strategy := detail[using+len(" USING "):]
	if strings.HasPrefix(strategy, "INTEGER PRIMARY KEY") {
		return queryplancapture.ResolveOnlyPrimaryKey(registry, golem.SQLite, model)
	}
	name := ""
	for _, prefix := range []string{"COVERING INDEX ", "INDEX "} {
		if strings.HasPrefix(strategy, prefix) {
			remainder := strategy[len(prefix):]
			if end := strings.IndexByte(remainder, ' '); end >= 0 {
				name = remainder[:end]
			} else {
				name = remainder
			}
			break
		}
	}
	if name != "" {
		if access, indexID, ok := queryplancapture.ResolvePhysicalAccess(registry, golem.SQLite, model, name, false); ok {
			return access, indexID, true
		}
	}
	columns := sqliteSearchColumns(strategy)
	fieldIDs, ok := queryplancapture.ResolvePhysicalFieldSequence(registry, golem.SQLite, model, columns)
	if !ok {
		return 0, queryplancapture.IndexID{}, false
	}
	primaryAccess, primaryID, primary := queryplancapture.ResolvePhysicalKeyByFields(registry, golem.SQLite, model, schema.PhysicalAccessPrimaryKey, fieldIDs)
	uniqueAccess, uniqueID, unique := queryplancapture.ResolvePhysicalKeyByFields(registry, golem.SQLite, model, schema.PhysicalAccessUniqueIndex, fieldIDs)
	if primary == unique {
		return 0, queryplancapture.IndexID{}, false
	}
	if primary {
		return primaryAccess, primaryID, true
	}
	return uniqueAccess, uniqueID, true
}

func sqliteSearchColumns(strategy string) []string {
	open := strings.LastIndexByte(strategy, '(')
	close := strings.LastIndexByte(strategy, ')')
	if open < 0 || close <= open+1 {
		return nil
	}
	terms := strings.Split(strategy[open+1:close], " AND ")
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		end := strings.IndexAny(term, "=<>! ")
		if end <= 0 {
			return nil
		}
		column := term[:end]
		if column == "rowid" || strings.ContainsAny(column, "()\"") {
			return nil
		}
		result = append(result, column)
	}
	return result
}

func structuralForAlias(identity queryplancapture.AliasIdentity, children []queryplancapture.Node) queryplancapture.Node {
	switch identity.Role() {
	case queryplancapture.AliasCorrelatedRelation:
		return queryplancapture.Structural(queryplancapture.NodeCorrelatedRelation, identity, children...)
	case queryplancapture.AliasAggregate:
		return queryplancapture.Structural(queryplancapture.NodeAggregate, identity, children...)
	case queryplancapture.AliasMaterialize:
		return queryplancapture.Structural(queryplancapture.NodeMaterialize, identity, children...)
	default:
		return queryplancapture.Unknown(children...)
	}
}

func combineSQLiteRoots(nodes []queryplancapture.Node) queryplancapture.Node {
	if len(nodes) == 1 {
		return nodes[0]
	}
	core := make([]queryplancapture.Node, 0, len(nodes))
	wrappers := make([]queryplancapture.NodeKind, 0, 2)
	for _, node := range nodes {
		if len(node.Children()) == 0 && (node.Kind() == queryplancapture.NodeSort || node.Kind() == queryplancapture.NodeAggregate) {
			wrappers = append(wrappers, node.Kind())
			continue
		}
		core = append(core, node)
	}
	var root queryplancapture.Node
	switch len(core) {
	case 0:
		root = queryplancapture.Unknown()
	case 1:
		root = core[0]
	default:
		root = queryplancapture.Structural(queryplancapture.NodeJoin, queryplancapture.AliasIdentity{}, core...)
	}
	for _, kind := range wrappers {
		root = queryplancapture.Structural(kind, queryplancapture.AliasIdentity{}, root)
	}
	return root
}
