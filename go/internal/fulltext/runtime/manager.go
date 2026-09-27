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

const MaximumResults = 1000

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

func (manager *Manager) Query(ctx context.Context, model ir.ModelID, name, query string, candidates semanticruntime.Candidates, take int) ([]Rank, error) {
	if manager == nil || ctx == nil || take < 1 || take > MaximumResults {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: context, manager, and a result limit in 1..%d are required", MaximumResults)
	}
	index, ok := manager.index(model, name)
	if !ok || candidates.NewScan == nil || len(candidates.Columns) != len(index.Identity) {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: index or candidate identity is invalid")
	}
	for position, column := range index.Identity {
		if candidates.Columns[position] != string(column.Name) {
			return nil, fmt.Errorf("P9_FULLTEXT_QUERY: candidate identity column does not match the index")
		}
	}
	parsed, err := parse(query)
	if err != nil {
		return nil, err
	}
	arguments := make([]any, 0, len(parsed)+len(index.Descriptor.Index.Fields)+len(candidates.Args)+1)
	statement := manager.sqliteStatement(index, candidates)
	if manager.provider == ir.PostgreSQL {
		statement = manager.postgresqlStatement(index, parsed, candidates)
		for _, item := range parsed {
			value := item.value
			if index.Descriptor.Index.Folding == fulltextcontract.FoldingDiacritics {
				value = fulltextfolding.Diacritics(value)
			}
			arguments = append(arguments, value)
		}
	} else {
		arguments = append(arguments, compileSQLite(parsed, index.Descriptor.Index.Folding))
		for position := range index.Descriptor.Index.Fields {
			arguments = append(arguments, compileSQLiteField(parsed, index.Descriptor.Index.Folding, position))
		}
	}
	if err := readsql.ValidateStatementComplexity(candidates.Model, statement, candidates.MaxStatementBytes, candidates.MaxStatementAliases); err != nil {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: ranking statement exceeds configured complexity")
	}
	arguments = append(arguments, candidates.Args...)
	arguments = append(arguments, take)
	if candidates.MaxStatementParameters < 1 || len(arguments) > candidates.MaxStatementParameters {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: ranking statement exceeds configured parameter limit")
	}
	rows, err := manager.database.QueryxContext(ctx, statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: ranking failed")
	}
	defer rows.Close()
	result := make([]Rank, 0, take)
	for rows.Next() {
		var score float64
		scan := candidates.NewScan()
		destinations := append([]any{&score}, scan.Destinations()...)
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("P9_FULLTEXT_QUERY: ranking decode failed")
		}
		identity := scan.RawValues()
		key, err := semantickey.Encode(identity)
		if err != nil || math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, fmt.Errorf("P9_FULLTEXT_QUERY: ranking row is invalid")
		}
		result = append(result, Rank{Key: key, Score: score, Identity: identity})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: ranking stream failed")
	}
	return result, nil
}

func (manager *Manager) sqliteStatement(index Index, candidates semanticruntime.Candidates) string {
	base := string(index.Descriptor.Storage)
	fts, keys := manager.quote(base+"_fts"), manager.quote(base+"_keys")
	identity, joins := manager.identitySQL(index, "golem_fk", "golem_fc", candidates)
	scores := make([]string, len(index.Descriptor.Index.Fields))
	for position, field := range index.Descriptor.Index.Fields {
		alias := "golem_fs" + strconv.Itoa(position)
		scores[position] = "CASE WHEN EXISTS(SELECT 1 FROM " + fts + " AS " + alias + " WHERE " + alias + ".rowid=golem_ff.rowid AND " + fts + " MATCH ?" + strconv.Itoa(position+2) + ") THEN " + strconv.FormatFloat(field.Weight, 'g', -1, 64) + " ELSE 0 END"
	}
	candidateSQL := policysql.RebasePlaceholders(candidates.SQL, len(index.Descriptor.Index.Fields)+1, policyir.ProviderSQLite)
	limit := "?" + strconv.Itoa(len(index.Descriptor.Index.Fields)+len(candidates.Args)+2)
	return "SELECT " + strings.Join(scores, "+") + " AS score," + strings.Join(identity, ",") +
		" FROM " + fts + " AS golem_ff JOIN " + keys + " AS golem_fk ON golem_fk.docid=golem_ff.rowid" +
		" JOIN (" + candidateSQL + ") AS golem_fc ON " + strings.Join(joins, " AND ") +
		" WHERE " + fts + " MATCH ?1 ORDER BY score DESC," + strings.Join(identity, ",") + " LIMIT " + limit
}

func (manager *Manager) postgresqlStatement(index Index, terms []term, candidates semanticruntime.Candidates) string {
	table := manager.table(string(index.Descriptor.Storage) + "_fts")
	identity, joins := manager.identitySQL(index, "golem_ff", "golem_fc", candidates)
	candidateSQL := policysql.RebasePlaceholders(candidates.SQL, len(terms), policyir.ProviderPostgreSQL)
	queries := make([]string, len(terms))
	for position, item := range terms {
		queries[position] = fulltextpostgresql.PhraseQuery("$"+strconv.Itoa(position+1), fulltextcontract.FoldingNone, item.prefix)
	}
	query := fulltextpostgresql.JoinQueries(queries)
	limit := "$" + strconv.Itoa(len(terms)+len(candidates.Args)+1)
	return "SELECT ts_rank_cd(" + postgresqlWeights(index.Descriptor.Index) + ",golem_ff.document," + query + ")::double precision AS score," + strings.Join(identity, ",") +
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
		byClass[position] = value
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
	if input == "" || len(input) > 10_000 || !utf8.ValidString(input) || strings.IndexByte(input, 0) >= 0 {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: query is empty or invalid")
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
				return nil, fmt.Errorf("P9_FULLTEXT_QUERY: quoted phrase is not terminated")
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
					return nil, fmt.Errorf("P9_FULLTEXT_QUERY: prefix terms require at least two characters")
				}
			}
		}
		if item.value == "" {
			continue
		}
		result = append(result, item)
		if len(result) > 32 {
			return nil, fmt.Errorf("P9_FULLTEXT_QUERY: query exceeds 32 terms")
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: query has no terms")
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
