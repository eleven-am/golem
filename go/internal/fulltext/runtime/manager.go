package runtime

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextfolding "github.com/eleven-am/golem/go/internal/fulltext/folding"
	fulltextpostgresql "github.com/eleven-am/golem/go/internal/fulltext/postgresql"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
	"github.com/jmoiron/sqlx"
)

const (
	MaximumResults            = 1000
	sqliteDirectBranchMaximum = 32
)

type Index struct {
	Descriptor fulltextstorage.Descriptor
	Identity   []physical.PhysicalColumn
}

type Rank struct {
	Key      string
	Score    float64
	Identity []any
}

type Manager struct {
	database *sqlx.DB
	provider ir.Provider
	schema   physical.PhysicalSchema
	indexes  []Index
}

func NewManager(database *sqlx.DB, provider ir.Provider, schema physical.PhysicalSchema) (*Manager, error) {
	if database == nil || provider != ir.SQLite && provider != ir.PostgreSQL {
		return nil, fmt.Errorf("P9_FULLTEXT_RUNTIME: database and provider are required")
	}
	indexes := make([]Index, 0)
	seen := make(map[string]bool)
	for _, extension := range schema.Extensions {
		if extension.Kind != fulltextcontract.IndexKind {
			continue
		}
		descriptor, err := fulltextstorage.Decode(extension)
		if err != nil {
			return nil, fmt.Errorf("P9_FULLTEXT_SCHEMA: %w", err)
		}
		key := string(descriptor.ModelID) + "\x00" + descriptor.Index.Name
		if seen[key] {
			return nil, fmt.Errorf("P9_FULLTEXT_SCHEMA: duplicate full-text index")
		}
		seen[key] = true
		owner, ok := ownerTable(schema, descriptor.ModelID)
		if !ok || owner.PrimaryKey == nil || len(owner.PrimaryKey.Columns) == 0 {
			return nil, fmt.Errorf("P9_FULLTEXT_SCHEMA: indexed model identity is absent")
		}
		columns := make(map[ir.FieldID]physical.PhysicalColumn, len(owner.Columns))
		for _, column := range owner.Columns {
			columns[column.ID] = column
		}
		identity := make([]physical.PhysicalColumn, len(owner.PrimaryKey.Columns))
		for position, field := range owner.PrimaryKey.Columns {
			column, exists := columns[field]
			if !exists || column.Nullable {
				return nil, fmt.Errorf("P9_FULLTEXT_SCHEMA: indexed model identity is invalid")
			}
			identity[position] = column
		}
		indexes = append(indexes, Index{Descriptor: descriptor, Identity: identity})
	}
	sort.Slice(indexes, func(i, j int) bool {
		if indexes[i].Descriptor.ModelID != indexes[j].Descriptor.ModelID {
			return indexes[i].Descriptor.ModelID < indexes[j].Descriptor.ModelID
		}
		return indexes[i].Descriptor.Index.Name < indexes[j].Descriptor.Index.Name
	})
	return &Manager{database: database, provider: provider, schema: schema, indexes: indexes}, nil
}

func (manager *Manager) IndexFields(model ir.ModelID, name string) ([]ir.FieldID, bool) {
	index, ok := manager.index(model, name)
	if !ok {
		return nil, false
	}
	fields := make([]ir.FieldID, len(index.Descriptor.Index.Fields))
	for position, field := range index.Descriptor.Index.Fields {
		fields[position] = ir.FieldID(field.ID)
	}
	return fields, true
}

// QueryParameters returns the exact number of ranking-owned bind parameters
// for one query, including its result limit but excluding candidate binds.
func (manager *Manager) QueryParameters(model ir.ModelID, name, query string) (int, error) {
	if manager == nil {
		return 0, queryExecutionFailure("full-text execution is unavailable", nil)
	}
	index, ok := manager.index(model, name)
	if !ok {
		return 0, queryExecutionFailure("full-text index is unavailable", nil)
	}
	parsed, err := parse(query)
	if err != nil {
		return 0, err
	}
	if manager.provider == ir.PostgreSQL {
		return len(parsed) + 1, nil
	}
	if fulltextcontract.EffectiveRanking(index.Descriptor.Index) == fulltextcontract.RankingBM25 {
		return 2, nil
	}
	return len(sqliteRankBranches(index.Descriptor.Index, parsed)) + 1, nil
}

func (manager *Manager) Query(ctx context.Context, model ir.ModelID, name, query string, candidates semanticruntime.Candidates, take int) ([]Rank, error) {
	if manager == nil {
		return nil, queryExecutionFailure("full-text execution is unavailable", nil)
	}
	return manager.QueryOn(ctx, manager.database, model, name, query, candidates, take)
}

func (manager *Manager) QueryOn(ctx context.Context, queryer sqlx.QueryerContext, model ir.ModelID, name, query string, candidates semanticruntime.Candidates, take int) ([]Rank, error) {
	if manager == nil || ctx == nil || queryer == nil {
		return nil, queryExecutionFailure("full-text execution is unavailable", nil)
	}
	if take < 1 || take > MaximumResults {
		return nil, InvalidQuery("full-text result limit is %d, outside 1..%d", take, MaximumResults)
	}
	index, ok := manager.index(model, name)
	if !ok || candidates.NewScan == nil || len(candidates.Columns) != len(index.Identity) {
		return nil, queryExecutionFailure("full-text index or candidate identity is invalid", nil)
	}
	for position, column := range index.Identity {
		if candidates.Columns[position] != string(column.Name) {
			return nil, queryExecutionFailure("full-text candidate identity does not match the index", nil)
		}
	}
	parsed, err := parse(query)
	if err != nil {
		return nil, err
	}
	ranking := fulltextcontract.EffectiveRanking(index.Descriptor.Index)
	arguments := make([]any, 0, len(parsed)*len(index.Descriptor.Index.Fields)+len(candidates.Args)+1)
	statement := ""
	if manager.provider == ir.PostgreSQL {
		statement = manager.postgresqlStatement(index, parsed, candidates, ranking)
		for _, item := range parsed {
			value := item.value
			if index.Descriptor.Index.Folding == fulltextcontract.FoldingDiacritics {
				value = fulltextfolding.Diacritics(value)
			}
			arguments = append(arguments, value)
		}
	} else if ranking == fulltextcontract.RankingBM25 {
		statement = manager.sqliteBM25Statement(index, candidates)
		arguments = append(arguments, compileSQLite(parsed, index.Descriptor.Index.Folding))
	} else {
		branches := sqliteRankBranches(index.Descriptor.Index, parsed)
		statement = manager.sqliteStatement(index, candidates, branches)
		for _, branch := range branches {
			arguments = append(arguments, branch.expression)
		}
	}
	if err := readsql.ValidateStatementComplexity(candidates.Model, statement, candidates.MaxStatementBytes, candidates.MaxStatementAliases); err != nil {
		return nil, InvalidQuery("full-text ranking exceeds configured complexity")
	}
	arguments = append(arguments, candidates.Args...)
	arguments = append(arguments, take)
	if candidates.MaxStatementParameters < 1 || len(arguments) > candidates.MaxStatementParameters {
		return nil, InvalidQuery("full-text ranking exceeds configured parameter limit")
	}
	rows, err := queryer.QueryxContext(ctx, statement, arguments...)
	if err != nil {
		return nil, queryExecutionFailure("full-text ranking execution failed", err)
	}
	defer rows.Close()
	result := make([]Rank, 0, take)
	for rows.Next() {
		var score float64
		scan := candidates.NewScan()
		destinations := append([]any{&score}, scan.Destinations()...)
		if err := rows.Scan(destinations...); err != nil {
			return nil, queryExecutionFailure("full-text ranking decode failed", err)
		}
		identity := scan.RawValues()
		key, err := semantickey.Encode(identity)
		if err != nil || math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, queryExecutionFailure("full-text ranking row is invalid", err)
		}
		result = append(result, Rank{Key: key, Score: score, Identity: identity})
	}
	if err := rows.Err(); err != nil {
		return nil, queryExecutionFailure("full-text ranking stream failed", err)
	}
	return result, nil
}

func (manager *Manager) sqliteBM25Statement(index Index, candidates semanticruntime.Candidates) string {
	base := string(index.Descriptor.Storage)
	fts, keys := manager.quote(base+"_fts"), manager.quote(base+"_keys")
	identity, joins := manager.identitySQL(index, "golem_fk", "golem_fc", candidates)
	weights := make([]string, len(index.Descriptor.Index.Fields))
	for position, field := range index.Descriptor.Index.Fields {
		weights[position] = strconv.FormatFloat(field.Weight, 'g', -1, 64)
	}
	candidateSQL := policysql.RebasePlaceholders(candidates.SQL, 1, policyir.ProviderSQLite)
	limit := "?" + strconv.Itoa(len(candidates.Args)+2)
	return "SELECT -bm25(" + fts + "," + strings.Join(weights, ",") + ") AS score," + strings.Join(identity, ",") +
		" FROM " + fts + " AS golem_ff JOIN " + keys + " AS golem_fk ON golem_fk.docid=golem_ff.rowid" +
		" JOIN (" + candidateSQL + ") AS golem_fc ON " + strings.Join(joins, " AND ") +
		" WHERE " + fts + " MATCH ?1 ORDER BY score DESC," + strings.Join(identity, ",") + " LIMIT " + limit
}

type sqliteRankBranch struct {
	expression string
	weight     float64
}

func sqliteRankBranches(index fulltextcontract.Index, terms []term) []sqliteRankBranch {
	branches := make([]sqliteRankBranch, 0, len(index.Fields)*len(terms))
	seen := make(map[string]bool, cap(branches))
	for fieldPosition, field := range index.Fields {
		for _, item := range terms {
			expression := compileSQLiteField([]term{item}, index.Folding, fieldPosition)
			if seen[expression] {
				continue
			}
			seen[expression] = true
			branches = append(branches, sqliteRankBranch{expression: expression, weight: field.Weight})
		}
	}
	return branches
}

func (manager *Manager) sqliteStatement(index Index, candidates semanticruntime.Candidates, branches []sqliteRankBranch) string {
	base := string(index.Descriptor.Storage)
	fts, keys := manager.quote(base+"_fts"), manager.quote(base+"_keys")
	identity, joins := manager.identitySQL(index, "golem_fk", "golem_fc", candidates)
	candidateSQL := policysql.RebasePlaceholders(candidates.SQL, len(branches), policyir.ProviderSQLite)
	limit := "?" + strconv.Itoa(len(branches)+len(candidates.Args)+1)
	if len(branches) <= sqliteDirectBranchMaximum {
		ranks := make([]string, len(branches))
		for position, branch := range branches {
			ranks[position] = "SELECT golem_ff.rowid golem_fd," + strconv.FormatFloat(branch.weight, 'g', -1, 64) + " golem_fw FROM " + fts + " AS golem_ff" +
				" JOIN " + keys + " AS golem_fk ON golem_fk.docid=golem_ff.rowid JOIN (" + candidateSQL + ") AS golem_fc ON " + strings.Join(joins, " AND ") +
				" WHERE " + fts + " MATCH ?" + strconv.Itoa(position+1)
		}
		direct := "WITH golem_fr AS (" + strings.Join(ranks, " UNION ALL ") +
			"),golem_fs AS (SELECT golem_fd,sum(golem_fw) AS score FROM golem_fr GROUP BY golem_fd) SELECT golem_fs.score," + strings.Join(identity, ",") +
			" FROM golem_fs JOIN " + keys + " AS golem_fk ON golem_fk.docid=golem_fs.golem_fd" +
			" ORDER BY golem_fs.score DESC," + strings.Join(identity, ",") + " LIMIT " + limit
		if readsql.ValidateStatementComplexity(candidates.Model, direct, candidates.MaxStatementBytes, candidates.MaxStatementAliases) == nil {
			return direct
		}
	}
	values := make([]string, len(branches))
	for position, branch := range branches {
		values[position] = "(?" + strconv.Itoa(position+1) + "," + strconv.FormatFloat(branch.weight, 'g', -1, 64) + ")"
	}
	return "WITH golem_fq(golem_fe,golem_fw) AS (VALUES " + strings.Join(values, ",") + ") SELECT sum(golem_fq.golem_fw) AS score," + strings.Join(identity, ",") +
		" FROM golem_fq JOIN " + fts + " AS golem_ff ON " + fts + " MATCH golem_fq.golem_fe JOIN " + keys + " AS golem_fk ON golem_fk.docid=golem_ff.rowid" +
		" JOIN (" + candidateSQL + ") AS golem_fc ON " + strings.Join(joins, " AND ") +
		" GROUP BY golem_ff.rowid," + strings.Join(identity, ",") + " ORDER BY score DESC," + strings.Join(identity, ",") + " LIMIT " + limit
}

func (manager *Manager) postgresqlStatement(index Index, terms []term, candidates semanticruntime.Candidates, ranking string) string {
	table := manager.table(string(index.Descriptor.Storage) + "_fts")
	identity, joins := manager.identitySQL(index, "golem_ff", "golem_fc", candidates)
	candidateSQL := policysql.RebasePlaceholders(candidates.SQL, len(terms), policyir.ProviderPostgreSQL)
	queries := make([]string, len(terms))
	for position, item := range terms {
		queries[position] = fulltextpostgresql.PhraseQuery("$"+strconv.Itoa(position+1), fulltextcontract.FoldingNone, item.prefix)
	}
	query := fulltextpostgresql.JoinQueries(queries)
	limit := "$" + strconv.Itoa(len(terms)+len(candidates.Args)+1)
	normalization := ""
	if ranking == fulltextcontract.RankingBM25 {
		normalization = ",32"
	}
	return "SELECT ts_rank_cd(" + postgresqlWeights(index.Descriptor.Index) + ",golem_ff.document," + query + normalization + ")::double precision AS score," + strings.Join(identity, ",") +
		" FROM " + table + " AS golem_ff JOIN (" + candidateSQL + ") AS golem_fc ON " + strings.Join(joins, " AND ") +
		" WHERE golem_ff.document@@" + query + " ORDER BY score DESC," + strings.Join(identity, ",") + " LIMIT " + limit
}

func postgresqlWeights(index fulltextcontract.Index) string {
	values := make([]float64, 0, len(index.Fields))
	seen := make(map[float64]bool, len(index.Fields))
	for _, field := range index.Fields {
		if !seen[field.Weight] {
			seen[field.Weight] = true
			values = append(values, field.Weight)
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(values)))
	byClass := [4]float64{}
	for position, value := range values {
		byClass[position] = value / values[0]
	}
	return "ARRAY[" + strconv.FormatFloat(byClass[3], 'g', -1, 64) + "," + strconv.FormatFloat(byClass[2], 'g', -1, 64) + "," + strconv.FormatFloat(byClass[1], 'g', -1, 64) + "," + strconv.FormatFloat(byClass[0], 'g', -1, 64) + "]::real[]"
}

func (manager *Manager) identitySQL(index Index, storedAlias, candidateAlias string, candidates semanticruntime.Candidates) ([]string, []string) {
	identity := make([]string, len(index.Identity))
	joins := make([]string, len(index.Identity))
	for position, column := range index.Identity {
		identity[position] = storedAlias + "." + manager.quote(string(column.Name))
		joins[position] = candidateAlias + "." + manager.quote(candidates.Columns[position]) + "=" + identity[position]
	}
	return identity, joins
}

func (manager *Manager) index(model ir.ModelID, name string) (Index, bool) {
	if manager == nil {
		return Index{}, false
	}
	for _, index := range manager.indexes {
		if index.Descriptor.ModelID == model && index.Descriptor.Index.Name == name {
			return index, true
		}
	}
	return Index{}, false
}

func (manager *Manager) quote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (manager *Manager) table(value string) string {
	if manager.provider == ir.PostgreSQL {
		return manager.quote(string(manager.schema.Namespace.Name)) + "." + manager.quote(value)
	}
	return manager.quote(value)
}

func ownerTable(schema physical.PhysicalSchema, model ir.ModelID) (physical.PhysicalTable, bool) {
	for _, table := range schema.Tables {
		if table.ID == model {
			return table, true
		}
	}
	return physical.PhysicalTable{}, false
}

type term struct {
	value  string
	phrase bool
	prefix bool
}

func parse(input string) ([]term, error) {
	if input == "" {
		return nil, InvalidQuery("full-text query is empty")
	}
	if len(input) > 10_000 {
		return nil, InvalidQuery("full-text query exceeds the 10000-byte limit")
	}
	if !utf8.ValidString(input) {
		return nil, InvalidQuery("full-text query is not valid UTF-8")
	}
	if strings.IndexByte(input, 0) >= 0 {
		return nil, InvalidQuery("full-text query contains a NUL byte")
	}
	result := make([]term, 0, 8)
	for position := 0; position < len(input); {
		for position < len(input) {
			value, size := utf8.DecodeRuneInString(input[position:])
			if !unicode.IsSpace(value) {
				break
			}
			position += size
		}
		if position == len(input) {
			break
		}
		item := term{}
		if input[position] == '"' {
			item.phrase = true
			position++
			start := position
			for position < len(input) && input[position] != '"' {
				position++
			}
			if position == len(input) {
				return nil, InvalidQuery("full-text quoted phrase is not terminated")
			}
			item.value = strings.TrimSpace(input[start:position])
			position++
		} else {
			start := position
			for position < len(input) {
				value, size := utf8.DecodeRuneInString(input[position:])
				if unicode.IsSpace(value) {
					break
				}
				position += size
			}
			item.value = input[start:position]
			if strings.HasSuffix(item.value, "*") {
				item.prefix = true
				item.value = strings.TrimSuffix(item.value, "*")
				if prefixLexemeLength(item.value) < 2 {
					return nil, InvalidQuery("full-text prefix terms require at least two characters")
				}
			}
		}
		if item.value == "" {
			continue
		}
		result = append(result, item)
		if len(result) > 32 {
			return nil, InvalidQuery("full-text query exceeds the 32-term limit")
		}
	}
	if len(result) == 0 {
		return nil, InvalidQuery("full-text query has no terms")
	}
	return result, nil
}

func prefixLexemeLength(value string) int {
	length, latest := 0, 0
	for _, item := range value {
		switch {
		case unicode.IsLetter(item) || unicode.IsNumber(item):
			length++
			latest = length
		case unicode.IsMark(item):
		default:
			length = 0
		}
	}
	return latest
}

func compileSQLite(terms []term, folding string) string {
	parts := make([]string, len(terms))
	for position, item := range terms {
		value := item.value
		if folding == fulltextcontract.FoldingDiacritics {
			value = fulltextfolding.Diacritics(value)
		}
		parts[position] = `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
		if item.prefix {
			parts[position] += "*"
		}
	}
	return strings.Join(parts, " OR ")
}

func compileSQLiteField(terms []term, folding string, position int) string {
	return "_golem_field_" + strconv.Itoa(position) + " : (" + compileSQLite(terms, folding) + ")"
}
