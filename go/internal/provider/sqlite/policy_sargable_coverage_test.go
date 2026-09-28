package sqlite

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/jmoiron/sqlx"
)

func TestPolicySQLiteSargableEquivalenceCoversCompositeValueKinds(t *testing.T) {
	ctx := context.Background()
	database, resolver, proof := newSargableCoverageFixture(t, ctx)
	conditions := sargableCoverageConditions(t, resolver)
	for index, condition := range conditions {
		assertSargableEquivalent(t, database, resolver, proof, -index-1, condition)
	}
	random := rand.New(rand.NewSource(19))
	for iteration := 0; iteration < 192; iteration++ {
		left := conditions[random.Intn(len(conditions))]
		right := conditions[random.Intn(len(conditions))]
		logical := ir.LogicalAnd
		if random.Intn(2) == 1 {
			logical = ir.LogicalOr
		}
		condition, err := ir.NewLogical(resolver.rootModel, logical, []ir.Condition{left, right})
		if err != nil {
			t.Fatal(err)
		}
		if random.Intn(2) == 1 {
			condition, err = ir.NewLogical(resolver.rootModel, ir.LogicalNot, []ir.Condition{condition})
			if err != nil {
				t.Fatal(err)
			}
		}
		assertSargableEquivalent(t, database, resolver, proof, iteration, condition)
	}
}

func TestPolicySQLiteSargableReadShapesUseIndexedSearch(t *testing.T) {
	ctx := context.Background()
	database, resolver, proof := newSargableCoverageFixture(t, ctx)
	one := sargableScalar(t, resolver, resolver.numberID, resolver.intType, ir.OperatorGreaterThanOrEqual, ir.ComparisonSensitive, sargableOne(t, sargableSigned(t, 128)))
	two := sargableScalar(t, resolver, resolver.numberID, resolver.intType, ir.OperatorLessThan, ir.ComparisonSensitive, sargableOne(t, sargableSigned(t, 192)))
	policyAndWhere, err := ir.NewLogical(resolver.rootModel, ir.LogicalAnd, []ir.Condition{one, two})
	if err != nil {
		t.Fatal(err)
	}
	in, err := ir.ManyOperand([]ir.Value{sargableSigned(t, 17), sargableSigned(t, 18), sargableSigned(t, 19)})
	if err != nil {
		t.Fatal(err)
	}
	findManyIn := sargableScalar(t, resolver, resolver.numberID, resolver.intType, ir.OperatorIn, ir.ComparisonSensitive, in)
	for _, test := range []struct {
		name      string
		condition ir.Condition
		statement func(policysql.Fragment) string
	}{
		{
			name:      "findMany range",
			condition: one,
			statement: func(fragment policysql.Fragment) string {
				return `SELECT "root"."id", "root"."name" FROM "sargable_root" AS "root" WHERE ` + fragment.SQL()
			},
		},
		{
			name:      "findMany in",
			condition: findManyIn,
			statement: func(fragment policysql.Fragment) string {
				return `SELECT "root"."id" FROM "sargable_root" AS "root" WHERE ` + fragment.SQL()
			},
		},
		{
			name:      "policy and where",
			condition: policyAndWhere,
			statement: func(fragment policysql.Fragment) string {
				return `SELECT "root"."id" FROM "sargable_root" AS "root" WHERE ` + fragment.SQL()
			},
		},
		{
			name:      "semantic candidate",
			condition: policyAndWhere,
			statement: func(fragment policysql.Fragment) string {
				return `SELECT "root"."id" AS "id" FROM "sargable_root" AS "root" WHERE ` + fragment.SQL()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fragment := compileSargableCoverage(t, resolver, proof, NewPolicyDialect(), test.condition, "root")
			plan := queryPlan(t, database, test.statement(fragment), fragment.Args())
			if !strings.Contains(plan, "SEARCH") || strings.Contains(plan, "SCAN sargable_root") {
				t.Fatalf("indexed read shape did not search: %s\nSQL: %s", plan, test.statement(fragment))
			}
		})
	}
}

func assertSargableEquivalent(t *testing.T, database *sqlx.DB, resolver *sargableCoverageResolver, proof policysql.CapabilityProof, iteration int, condition ir.Condition) {
	t.Helper()
	current := compileSargableCoverage(t, resolver, proof, NewPolicyDialect(), condition, "root")
	legacy := compileSargableCoverage(t, resolver, proof, legacySQLitePolicyDialect{}, condition, "root")
	for _, query := range []struct {
		name string
		expr func(string) string
	}{
		{name: "positive", expr: func(sql string) string { return "(" + sql + ")" }},
		{name: "complement", expr: func(sql string) string { return "NOT (" + sql + ")" }},
	} {
		var currentIDs, legacyIDs []int64
		currentSQL := `SELECT "root"."id" FROM "sargable_root" AS "root" WHERE ` + query.expr(current.SQL()) + ` ORDER BY "root"."id"`
		legacySQL := `SELECT "root"."id" FROM "sargable_root" AS "root" WHERE ` + query.expr(legacy.SQL()) + ` ORDER BY "root"."id"`
		if err := database.Select(&currentIDs, currentSQL, current.Args()...); err != nil {
			t.Fatalf("iteration %d %s current: %v\nSQL: %s", iteration, query.name, err, currentSQL)
		}
		if err := database.Select(&legacyIDs, legacySQL, legacy.Args()...); err != nil {
			t.Fatalf("iteration %d %s legacy: %v\nSQL: %s", iteration, query.name, err, legacySQL)
		}
		if !reflect.DeepEqual(currentIDs, legacyIDs) {
			t.Fatalf("iteration %d %s differs: current=%v legacy=%v\ncurrent SQL=%s\nlegacy SQL=%s", iteration, query.name, currentIDs, legacyIDs, current.SQL(), legacy.SQL())
		}
	}
	var unknown int
	if err := database.Get(&unknown, `SELECT count(*) FROM "sargable_root" AS "root" WHERE (`+current.SQL()+`) IS NULL`, current.Args()...); err != nil {
		t.Fatalf("iteration %d NULL probe: %v", iteration, err)
	}
	if unknown != 0 {
		t.Fatalf("iteration %d returned %d SQL-NULL rows: %s", iteration, unknown, current.SQL())
	}
}

func compileSargableCoverage(t *testing.T, resolver *sargableCoverageResolver, proof policysql.CapabilityProof, dialect policysql.Dialect, condition ir.Condition, alias string) policysql.Fragment {
	t.Helper()
	fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: dialect, Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: physical.PhysicalName(alias)})
	if err != nil {
		t.Fatal(err)
	}
	return fragment
}

func sargableCoverageConditions(t *testing.T, resolver *sargableCoverageResolver) []ir.Condition {
	t.Helper()
	conditions := make([]ir.Condition, 0, 48)
	text := func(value string) ir.Value {
		result, err := ir.StringValue(value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, entry := range operator.Entries() {
		if entry.NodeKind() != ir.ConditionScalar {
			continue
		}
		for _, mode := range []ir.ComparisonMode{ir.ComparisonSensitive, ir.ComparisonASCIIInsensitive} {
			if !entry.AcceptsMode(mode) {
				continue
			}
			operand := ir.NoOperand()
			switch {
			case entry.AcceptsOperand(ir.OperandOne):
				operand = sargableOne(t, text("alpha"))
			case entry.AcceptsOperand(ir.OperandMany):
				var err error
				operand, err = ir.ManyOperand([]ir.Value{text("alpha"), text("")})
				if err != nil {
					t.Fatal(err)
				}
			}
			conditions = append(conditions, sargableScalar(t, resolver, resolver.nameID, resolver.textType, entry.ID(), mode, operand))
		}
	}
	for _, value := range []int64{math.MinInt64, -1, 0, 9_007_199_254_740_993, math.MaxInt64} {
		conditions = append(conditions, sargableScalar(t, resolver, resolver.numberID, resolver.intType, ir.OperatorGreaterThanOrEqual, ir.ComparisonSensitive, sargableOne(t, sargableSigned(t, value))))
	}
	for _, value := range []int64{-999_999_999_999_999_999, -12_345, 0, 23_456, 999_999_999_999_999_999} {
		decimal, err := ir.NewDecimalValue(value, 2)
		if err != nil {
			t.Fatal(err)
		}
		conditions = append(conditions, sargableScalar(t, resolver, resolver.amountID, resolver.decimalType, ir.OperatorLessThanOrEqual, ir.ComparisonSensitive, sargableOne(t, decimal)))
	}
	goValue, dbValue := text("go"), text("db")
	list, err := ir.NewListValue([]ir.Value{goValue, dbValue})
	if err != nil {
		t.Fatal(err)
	}
	listOperands := []struct {
		operator ir.OperatorID
		operand  ir.Operand
	}{
		{ir.OperatorListEqual, sargableOne(t, list)},
		{ir.OperatorListHas, sargableOne(t, goValue)},
		{ir.OperatorListHasEvery, sargableMany(t, goValue, dbValue)},
		{ir.OperatorListHasSome, sargableMany(t, goValue, text("missing"))},
		{ir.OperatorListIsEmpty, sargableFlag(t, true)},
		{ir.OperatorListIsNull, ir.NoOperand()},
		{ir.OperatorListIsNotNull, ir.NoOperand()},
	}
	for _, test := range listOperands {
		conditions = append(conditions, sargableList(t, resolver, test.operator, test.operand))
	}
	number := sargableJSONNumber(t, "9007199254740993")
	word, err := ir.JSONStringValue("alpha")
	if err != nil {
		t.Fatal(err)
	}
	array, err := ir.JSONArrayValue([]ir.JSONValue{word})
	if err != nil {
		t.Fatal(err)
	}
	conditions = append(conditions,
		sargableJSON(t, resolver, ir.OperatorJSONEqual, ir.ComparisonSensitive, "n", sargableJSONOne(t, number)),
		sargableJSON(t, resolver, ir.OperatorJSONGreaterThan, ir.ComparisonSensitive, "n", sargableJSONOne(t, sargableJSONNumber(t, "0"))),
		sargableJSON(t, resolver, ir.OperatorJSONStringContains, ir.ComparisonASCIIInsensitive, "word", sargableJSONOne(t, word)),
		sargableJSON(t, resolver, ir.OperatorJSONArrayContains, ir.ComparisonSensitive, "items", sargableJSONOne(t, array)),
		sargableJSON(t, resolver, ir.OperatorJSONIsNull, ir.ComparisonSensitive, "", ir.NoOperand()),
		sargableJSON(t, resolver, ir.OperatorJSONIsNotNull, ir.ComparisonSensitive, "", ir.NoOperand()),
	)
	child := sargableScalarModel(t, resolver.childModel, resolver.childNameID, resolver.textType, ir.OperatorEqual, ir.ComparisonASCIIInsensitive, sargableOne(t, text("alpha")))
	for _, operatorID := range []ir.OperatorID{ir.OperatorRelationSome, ir.OperatorRelationEvery, ir.OperatorRelationNone} {
		conditions = append(conditions, sargableRelation(t, resolver, operatorID, child))
	}
	return conditions
}

func sargableScalar(t *testing.T, resolver *sargableCoverageResolver, field ir.FieldID, typ ir.TypeRef, operatorID ir.OperatorID, mode ir.ComparisonMode, operand ir.Operand) ir.Condition {
	t.Helper()
	return sargableScalarModel(t, resolver.rootModel, field, typ, operatorID, mode, operand)
}

func sargableScalarModel(t *testing.T, model ir.ModelID, field ir.FieldID, typ ir.TypeRef, operatorID ir.OperatorID, mode ir.ComparisonMode, operand ir.Operand) ir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionScalar, FieldType: typ, Operand: operand, Mode: mode, Providers: ir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewScalar(model, field, typ, operatorID, mode, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func sargableList(t *testing.T, resolver *sargableCoverageResolver, operatorID ir.OperatorID, operand ir.Operand) ir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionList, FieldType: resolver.listType, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewList(resolver.rootModel, resolver.tagsID, resolver.listType, operatorID, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func sargableJSON(t *testing.T, resolver *sargableCoverageResolver, operatorID ir.OperatorID, mode ir.ComparisonMode, key string, operand ir.Operand) ir.Condition {
	t.Helper()
	path, err := ir.NewJSONPath()
	if key != "" {
		segment, segmentErr := ir.JSONKeySegment(key)
		if segmentErr != nil {
			t.Fatal(segmentErr)
		}
		path, err = ir.NewJSONPath(segment)
	}
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionJSON, FieldType: resolver.jsonType, Operand: operand, Mode: mode, Path: path, Providers: ir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewJSON(resolver.rootModel, resolver.docID, resolver.jsonType, operatorID, mode, path, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func sargableRelation(t *testing.T, resolver *sargableCoverageResolver, operatorID ir.OperatorID, child ir.Condition) ir.Condition {
	t.Helper()
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: ir.RelationToMany, HasChild: true, Providers: ir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewRelation(resolver.rootModel, resolver.childrenID, resolver.relationID, resolver.childModel, ir.RelationToMany, operatorID, &child, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func sargableOne(t *testing.T, value ir.Value) ir.Operand {
	t.Helper()
	operand, err := ir.OneOperand(value)
	if err != nil {
		t.Fatal(err)
	}
	return operand
}

func sargableMany(t *testing.T, values ...ir.Value) ir.Operand {
	t.Helper()
	operand, err := ir.ManyOperand(values)
	if err != nil {
		t.Fatal(err)
	}
	return operand
}

func sargableFlag(t *testing.T, value bool) ir.Operand {
	t.Helper()
	return ir.FlagOperand(value)
}

func sargableSigned(t *testing.T, value int64) ir.Value {
	t.Helper()
	result, err := ir.SignedValue(ir.ValueInt64, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sargableJSONNumber(t *testing.T, value string) ir.JSONValue {
	t.Helper()
	number, err := ir.NewJSONNumber(strings.HasPrefix(value, "-"), []byte(strings.TrimPrefix(value, "-")), 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ir.JSONNumberValueOf(number)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sargableJSONOne(t *testing.T, value ir.JSONValue) ir.Operand {
	t.Helper()
	wrapper, err := ir.NewJSONValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return sargableOne(t, wrapper)
}

type sargableCoverageResolver struct {
	rootModel, childModel                                       ir.ModelID
	idID, nameID, numberID, amountID, tagsID, docID, childrenID ir.FieldID
	childID, childRootID, childNameID                           ir.FieldID
	relationID                                                  ir.RelationID
	intType, textType, decimalType, listType, jsonType          ir.TypeRef
	fingerprint                                                 [32]byte
}

func newSargableCoverageFixture(t *testing.T, ctx context.Context) (*sqlx.DB, *sargableCoverageResolver, policysql.CapabilityProof) {
	t.Helper()
	database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "sargable-coverage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Exec(`CREATE TABLE sargable_root (id INTEGER PRIMARY KEY, name TEXT, number INTEGER, amount INTEGER, tags TEXT, doc TEXT) STRICT;
CREATE INDEX sargable_root_number ON sargable_root(number);
CREATE TABLE sargable_child (id INTEGER PRIMARY KEY, root_id INTEGER NOT NULL, name TEXT) STRICT;
CREATE INDEX sargable_child_root_name ON sargable_child(root_id,name);`); err != nil {
		t.Fatal(err)
	}
	transaction, err := database.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	rootInsert, err := transaction.Preparex(`INSERT INTO sargable_root(id,name,number,amount,tags,doc) VALUES (?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	childInsert, err := transaction.Preparex(`INSERT INTO sargable_child(id,root_id,name) VALUES (?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	childID := int64(0)
	for id := int64(1); id <= 512; id++ {
		var name, number, amount, tags, doc any
		if id%11 != 0 {
			name = []string{"Alpha", "alpha", "BETA", "", fmt.Sprintf("value-%03d", id%37)}[id%5]
		}
		switch id {
		case 1:
			number, amount = int64(math.MinInt64), int64(math.MinInt64)
		case 2:
			number, amount = int64(math.MaxInt64), int64(math.MaxInt64)
		case 3:
			number, amount = int64(9_007_199_254_740_993), int64(12_345)
		default:
			if id%13 != 0 {
				number, amount = id-256, id*100-25_600
			}
		}
		switch id % 4 {
		case 0:
			tags = `[]`
		case 1:
			tags = `["go","db"]`
		case 2:
			tags = `["go"]`
		}
		if id%7 != 0 {
			doc = fmt.Sprintf(`{"n":%d,"word":%q,"items":["alpha","tail"]}`, id, []string{"Alpha", "alpha", "BETA"}[id%3])
		}
		if id == 3 {
			doc = `{"n":9007199254740993,"word":"Alpha","items":["alpha"]}`
		}
		if _, err := rootInsert.Exec(id, name, number, amount, tags, doc); err != nil {
			t.Fatal(err)
		}
		children := [][]any{nil, {"Alpha"}, {"other"}, {"Alpha", nil}, {"Alpha", "alpha"}}[id%5]
		for _, childName := range children {
			childID++
			if _, err := childInsert.Exec(childID, id, childName); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := rootInsert.Close(); err != nil {
		t.Fatal(err)
	}
	if err := childInsert.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	resolver := newSargableCoverageResolver(t)
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return database, resolver, proof
}

func newSargableCoverageResolver(t *testing.T) *sargableCoverageResolver {
	t.Helper()
	integer, err := ir.NewTypeRef(ir.ValueInt64, true, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	text, err := ir.NewTypeRef(ir.ValueString, true, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	decimal, err := ir.NewTypeRef(ir.ValueDecimal, true, 18, 2, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	element, err := ir.NewTypeRef(ir.ValueString, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	list, err := ir.NewTypeRef(ir.ValueScalarList, true, 0, 0, ir.EnumID{}, &element, ir.CapabilityScalarListJSON)
	if err != nil {
		t.Fatal(err)
	}
	jsonType, err := ir.NewTypeRef(ir.ValueJSON, true, 0, 0, ir.EnumID{}, nil, ir.CapabilityExactJSON)
	if err != nil {
		t.Fatal(err)
	}
	return &sargableCoverageResolver{
		rootModel: testModelID(21), childModel: testModelID(22),
		idID: testFieldID(21), nameID: testFieldID(22), numberID: testFieldID(23), amountID: testFieldID(24), tagsID: testFieldID(25), docID: testFieldID(26), childrenID: testFieldID(27),
		childID: testFieldID(31), childRootID: testFieldID(32), childNameID: testFieldID(33), relationID: testRelationID(21),
		intType: integer, textType: text, decimalType: decimal, listType: list, jsonType: jsonType, fingerprint: [32]byte{5, 3, 2},
	}
}

func (resolver *sargableCoverageResolver) Providers() ir.ProviderSet   { return ir.PortableProviders() }
func (resolver *sargableCoverageResolver) SchemaFingerprint() [32]byte { return resolver.fingerprint }
func (resolver *sargableCoverageResolver) Model(provider ir.Provider, model ir.ModelID) (policysql.Model, bool) {
	if provider != ir.ProviderSQLite {
		return policysql.Model{}, false
	}
	switch model {
	case resolver.rootModel:
		return policysql.Model{ID: model, Namespace: "main", Table: "sargable_root"}, true
	case resolver.childModel:
		return policysql.Model{ID: model, Namespace: "main", Table: "sargable_child"}, true
	default:
		return policysql.Model{}, false
	}
}

func (resolver *sargableCoverageResolver) Field(provider ir.Provider, model ir.ModelID, field ir.FieldID) (policysql.Field, bool) {
	if provider != ir.ProviderSQLite {
		return policysql.Field{}, false
	}
	if model == resolver.rootModel {
		for _, candidate := range []struct {
			id       ir.FieldID
			column   string
			typ      ir.TypeRef
			nullable bool
		}{
			{resolver.idID, "id", resolver.intType, false},
			{resolver.nameID, "name", resolver.textType, true},
			{resolver.numberID, "number", resolver.intType, true},
			{resolver.amountID, "amount", resolver.decimalType, true},
			{resolver.tagsID, "tags", resolver.listType, true},
			{resolver.docID, "doc", resolver.jsonType, true},
		} {
			if field == candidate.id {
				return policysql.Field{Model: model, ID: field, Column: physical.PhysicalName(candidate.column), Type: candidate.typ, Nullable: candidate.nullable}, true
			}
		}
	}
	if model == resolver.childModel {
		for _, candidate := range []struct {
			id       ir.FieldID
			column   string
			typ      ir.TypeRef
			nullable bool
		}{
			{resolver.childID, "id", resolver.intType, false},
			{resolver.childRootID, "root_id", resolver.intType, false},
			{resolver.childNameID, "name", resolver.textType, true},
		} {
			if field == candidate.id {
				return policysql.Field{Model: model, ID: field, Column: physical.PhysicalName(candidate.column), Type: candidate.typ, Nullable: candidate.nullable}, true
			}
		}
	}
	return policysql.Field{}, false
}

func (resolver *sargableCoverageResolver) Relation(model ir.ModelID, field ir.FieldID, relation ir.RelationID) (policysql.Relation, bool) {
	if model != resolver.rootModel || field != resolver.childrenID || relation != resolver.relationID {
		return policysql.Relation{}, false
	}
	return policysql.Relation{Model: model, Field: field, ID: relation, Target: resolver.childModel, Cardinality: ir.RelationToMany, Pairs: []policysql.Correlation{{Parent: resolver.idID, Child: resolver.childRootID}}}, true
}

func (*sargableCoverageResolver) EnumWire(ir.EnumID, ir.EnumValueID) (string, bool) { return "", false }
func (*sargableCoverageResolver) Capability(_ ir.Provider, capability ir.Capability) bool {
	return capability >= ir.CapabilityBinaryText && capability <= ir.CapabilityRelationCorrelation
}

func testRelationID(value byte) (result ir.RelationID) { result[len(result)-1] = value; return }

var _ policysql.Resolver = (*sargableCoverageResolver)(nil)
