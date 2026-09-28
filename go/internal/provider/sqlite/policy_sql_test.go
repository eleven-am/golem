package sqlite

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

func TestPolicyDialectSupportsClosedRegistryExactly(t *testing.T) {
	dialect := NewPolicyDialect()
	for _, entry := range operator.Entries() {
		if !dialect.Supports(entry.ID()) {
			t.Errorf("operator %d (%s) is not supported", entry.ID(), entry.Name())
		}
	}
	for _, unknown := range []ir.OperatorID{0, 14, 100, 108, 200, 215, 300, 308, 999} {
		if dialect.Supports(unknown) {
			t.Errorf("unknown operator %d is supported", unknown)
		}
	}
}

func TestPolicyFunctionsAreProbedOnTwoPooledConnectionsAndFingerprintBound(t *testing.T) {
	provider := New()
	database, report, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if !report.PolicyBinaryText || !report.PolicyASCIIText || !report.PolicyExactJSON || !report.PolicyScalarList || !report.PolicyRelation {
		t.Fatalf("incomplete policy capability report: %#v", report)
	}
	fingerprint := [32]byte{1, 2, 3}
	proof, err := provider.PolicyCapabilityProof(context.Background(), database, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Provider() != ir.ProviderSQLite || proof.SchemaFingerprint() != fingerprint {
		t.Fatalf("proof is not provider/fingerprint bound")
	}
	for _, capability := range []ir.Capability{ir.CapabilityBinaryText, ir.CapabilityASCIIInsensitiveText, ir.CapabilityExactJSON, ir.CapabilityScalarListJSON, ir.CapabilityRelationCorrelation} {
		if !proof.Has(capability) {
			t.Errorf("proof missing capability %d", capability)
		}
	}
	if _, err := provider.PolicyCapabilityProof(context.Background(), database, [32]byte{}); err == nil {
		t.Fatal("zero schema fingerprint was accepted")
	}
	manifest := provider.Manifest()
	want := map[string]bool{
		"policy.binary-text.v1":            false,
		"policy.ascii-insensitive-text.v1": false,
		"policy.exact-json.v1":             false,
		"scalar-list.json-array.v1":        false,
		"policy.relation-correlation.v1":   false,
	}
	for _, fact := range manifest.Capabilities {
		if _, ok := want[string(fact.ID)]; ok {
			want[string(fact.ID)] = true
		}
	}
	for capability, found := range want {
		if !found {
			t.Errorf("manifest missing %s", capability)
		}
	}
}

func TestPolicySQLiteNamedMutationSemanticsAndUnknownCount(t *testing.T) {
	database, _, err := New().Open(context.Background(), filepath.Join(t.TempDir(), "mutations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	queries := map[string]struct {
		sql  string
		want int64
	}{
		"adjacent_integer_above_2pow53":                    {`SELECT golem_policy_list('[9007199254740993]', '[9007199254740992]', '4:0:0', 101)`, 0},
		"list_equals_noncanonical_text":                    {`SELECT golem_policy_list('[1.0, 2e0]', '[1,2]', '7:18:2', 101)`, 1},
		"list_has_null_column_unknown_count":               {`SELECT golem_policy_list(NULL, '1', '4:0:0', 102)`, 0},
		"list_equals_malformed_null_element_is_two_valued": {`SELECT golem_policy_list('[null]', '[null]', '4:0:0', 101)`, 0},
		"list_has_wrong_type_is_two_valued":                {`SELECT golem_policy_list('["1"]', '1', '4:0:0', 102)`, 0},
		"list_has_every_empty_present":                     {`SELECT golem_policy_list('[]', '[]', '4:0:0', 103)`, 1},
		"list_has_some_empty":                              {`SELECT golem_policy_list('[1]', '[]', '4:0:0', 104)`, 0},
		"list_is_empty_null_column_unknown_count":          {`SELECT golem_policy_list(NULL, 'true', '4:0:0', 105)`, 0},
		"list_is_not_empty_counts_malformed_elements":      {`SELECT golem_policy_list('[null,"wrong"]', 'false', '4:0:0', 105)`, 1},
		"json_adjacent_integer_above_2pow53":               {`SELECT golem_policy_json('{"n":9007199254740993}', '[["k","n"]]', '9007199254740992', 203, 1, 2)`, 0},
		"json_missing_path_db_null":                        {`SELECT golem_policy_json('{"x":1}', '[["k","missing"]]', '1', 203, 1, 5)`, 1},
		"json_present_json_null":                           {`SELECT golem_policy_json('{"x":null}', '[["k","x"]]', '2', 203, 1, 5)`, 1},
		"json_wrong_type_ne_is_false":                      {`SELECT golem_policy_json('{"x":1}', '[["k","x"]]', '"x"', 204, 1, 2)`, 0},
		"json_structural_object_key_order":                 {`SELECT golem_policy_json('{"x":{"b":2,"a":1}}', '[["k","x"]]', '{"a":1,"b":2}', 203, 1, 2)`, 1},
		"json_array_contains_deep":                         {`SELECT golem_policy_json('{"x":[{"a":1,"b":2}]}', '[["k","x"]]', '[{"a":1}]', 212, 1, 2)`, 1},
		"literal_like_metacharacters_sensitive":            {`SELECT instr('a%_\\z', '%_\\') > 0`, 1},
		"non_ascii_uppercase_is_not_folded":                {`SELECT golem_policy_ascii_fold('ÅZ') = 'Åz'`, 1},
	}
	for name, test := range queries {
		t.Run(name, func(t *testing.T) {
			var got *int64
			if err := database.Get(&got, test.sql); err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatalf("predicate returned SQL NULL")
			}
			if *got != test.want {
				t.Fatalf("got %d want %d", *got, test.want)
			}
		})
	}
}

// TestPolicySQLiteNamedScalarMutationMatrix owns the scalar SQL-side witnesses
// for M2-M10 and M13.  These are execution assertions, not renderer-string
// snapshots: every fragment is compiled through the production compiler, run
// against nullable rows, and independently checked for SQL UNKNOWN.
func TestPolicySQLiteNamedScalarMutationMatrix(t *testing.T) {
	ctx := context.Background()
	database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "scalar-mutations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE probe (id INTEGER PRIMARY KEY, name TEXT) STRICT`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id   int
		name *string
	}{
		{1, pointer("100%_\\A")},
		{2, nil},
		{3, pointer("other")},
		{4, pointer("ÉCOLE")},
	}
	for _, row := range rows {
		if _, err := database.Exec(`INSERT INTO probe(id,name) VALUES (?,?)`, row.id, row.name); err != nil {
			t.Fatal(err)
		}
	}

	resolver := newSQLitePolicyTestResolver(t)
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	one := func(value string) ir.Operand {
		text, err := ir.StringValue(value)
		if err != nil {
			t.Fatal(err)
		}
		operand, err := ir.OneOperand(text)
		if err != nil {
			t.Fatal(err)
		}
		return operand
	}
	many := func(values ...string) ir.Operand {
		items := make([]ir.Value, len(values))
		for index, value := range values {
			items[index], err = ir.StringValue(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		operand, err := ir.ManyOperand(items)
		if err != nil {
			t.Fatal(err)
		}
		return operand
	}
	condition := func(operatorID ir.OperatorID, mode ir.ComparisonMode, operand ir.Operand) ir.Condition {
		requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.textType, Operand: operand, Mode: mode, Providers: ir.PortableProviders()})
		if err != nil {
			t.Fatal(err)
		}
		result, err := ir.NewScalar(resolver.modelID, resolver.nameID, resolver.textType, operatorID, mode, operand, requirements)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	tests := []struct {
		name      string
		condition ir.Condition
		want      []int
	}{
		{"M2_ordered_nullable_unknown_count", condition(ir.OperatorLessThan, ir.ComparisonSensitive, one("z")), []int{1, 3}},
		{"M3_equals_null_safe_unknown_count", condition(ir.OperatorEqual, ir.ComparisonSensitive, one("other")), []int{3}},
		{"M4_not_exact_complement_nullable", condition(ir.OperatorNotEqual, ir.ComparisonSensitive, one("other")), []int{1, 2, 4}},
		{"M5_in_nullable_unknown_count", condition(ir.OperatorIn, ir.ComparisonSensitive, many("other")), []int{3}},
		{"M6_not_in_includes_null_subject", condition(ir.OperatorNotIn, ir.ComparisonSensitive, many("other")), []int{1, 2, 4}},
		{"M7_in_empty_is_none", condition(ir.OperatorIn, ir.ComparisonSensitive, many()), nil},
		{"M8_not_in_empty_is_all", condition(ir.OperatorNotIn, ir.ComparisonSensitive, many()), []int{1, 2, 3, 4}},
		{"M9_literal_like_metacharacters_sensitive", condition(ir.OperatorContains, ir.ComparisonSensitive, one("%_\\")), []int{1}},
		{"M10_literal_like_metacharacters_insensitive", condition(ir.OperatorEqual, ir.ComparisonASCIIInsensitive, one("100%_\\a")), []int{1}},
		{"M13_empty_needle_excludes_null_subject", condition(ir.OperatorContains, ir.ComparisonSensitive, one("")), []int{1, 3, 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fragment, err := policysql.Compile(policysql.Request{Condition: test.condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
			if err != nil {
				t.Fatal(err)
			}
			var selected []int
			if err := database.Select(&selected, `SELECT id FROM probe AS "root" WHERE `+fragment.SQL()+` ORDER BY id`, fragment.Args()...); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(selected, test.want) {
				t.Fatalf("selected=%v want=%v SQL=%s args=%#v", selected, test.want, fragment.SQL(), fragment.Args())
			}
			var unknown int
			if err := database.Get(&unknown, `SELECT count(*) FROM probe AS "root" WHERE (`+fragment.SQL()+`) IS NULL`, fragment.Args()...); err != nil {
				t.Fatal(err)
			}
			if unknown != 0 {
				t.Fatalf("unknown count=%d", unknown)
			}
		})
	}
}

func TestPolicySQLiteEveryScalarOperatorIsNullClosedByConstruction(t *testing.T) {
	ctx := context.Background()
	database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "scalar-null-closure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	resolver := newSQLitePolicyTestResolver(t)
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	value, err := ir.StringValue("value")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range operator.Entries() {
		if entry.NodeKind() != ir.ConditionScalar {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			operand := ir.NoOperand()
			switch {
			case entry.AcceptsOperand(ir.OperandOne):
				operand, err = ir.OneOperand(value)
			case entry.AcceptsOperand(ir.OperandMany):
				operand, err = ir.ManyOperand([]ir.Value{value})
			}
			if err != nil {
				t.Fatal(err)
			}
			requirements, shapeErr := operator.ValidateShape(entry.ID(), operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.textType, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
			if shapeErr != nil {
				t.Fatal(shapeErr)
			}
			condition, conditionErr := ir.NewScalar(resolver.modelID, resolver.nameID, resolver.textType, entry.ID(), ir.ComparisonSensitive, operand, requirements)
			if conditionErr != nil {
				t.Fatal(conditionErr)
			}
			fragment, compileErr := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
			if compileErr != nil {
				t.Fatal(compileErr)
			}
			column := `"root"."name"`
			sql := fragment.SQL()
			switch entry.NullSubject() {
			case operator.NullSubjectNeverMatches:
				if !strings.Contains(sql, column+" IS NOT NULL") {
					t.Fatalf("nullable false-on-NULL operator bypassed closure: %s", sql)
				}
			case operator.NullSubjectAlwaysMatches:
				if !strings.Contains(sql, column+" IS NULL OR") {
					t.Fatalf("nullable true-on-NULL operator bypassed closure: %s", sql)
				}
			case operator.NullSubjectMatchesNull:
				if sql != "("+column+" IS NULL)" {
					t.Fatalf("nullable presence operator=%s", sql)
				}
			case operator.NullSubjectMatchesPresent:
				if sql != "("+column+" IS NOT NULL)" {
					t.Fatalf("nullable presence operator=%s", sql)
				}
			default:
				t.Fatalf("scalar operator has unsupported NULL behavior %d", entry.NullSubject())
			}
		})
	}
}

type legacySQLitePolicyDialect struct{ PolicyDialect }

func (legacySQLitePolicyDialect) RenderScalar(leaf policysql.ScalarLeaf, binder *policysql.Binder) (string, error) {
	predicate, err := (PolicyDialect{}).RenderScalar(leaf, binder)
	if err != nil {
		return "", err
	}
	if leaf.Operator == ir.OperatorEqual {
		column := policyColumn(leaf.Column)
		prefix := column + " IS NOT NULL AND ("
		if !strings.HasPrefix(predicate, prefix) || !strings.HasSuffix(predicate, ")") {
			return "", fmt.Errorf("legacy sqlite policy: unexpected equality predicate %q", predicate)
		}
		predicate = strings.TrimSuffix(strings.TrimPrefix(predicate, prefix), ")")
	}
	return "CASE WHEN (" + predicate + ") THEN 1 ELSE 0 END", nil
}

func (legacySQLitePolicyDialect) RenderList(leaf policysql.ListLeaf, binder *policysql.Binder) (string, error) {
	predicate, err := (PolicyDialect{}).RenderList(leaf, binder)
	if err != nil {
		return "", err
	}
	if leaf.Operator == ir.OperatorListIsNull || leaf.Operator == ir.OperatorListIsNotNull {
		return "CASE WHEN (" + predicate + ") THEN 1 ELSE 0 END", nil
	}
	return predicate, nil
}

func (legacySQLitePolicyDialect) RenderJSON(leaf policysql.JSONLeaf, binder *policysql.Binder) (string, error) {
	predicate, err := (PolicyDialect{}).RenderJSON(leaf, binder)
	if err != nil {
		return "", err
	}
	if leaf.Operator == ir.OperatorJSONIsNull || leaf.Operator == ir.OperatorJSONIsNotNull {
		return "CASE WHEN (" + predicate + ") THEN 1 ELSE 0 END", nil
	}
	return predicate, nil
}

func TestPolicySQLiteSargablePredicatesPreserveLegacyTruthSets(t *testing.T) {
	ctx := context.Background()
	database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "sargable-equivalence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE probe (id INTEGER PRIMARY KEY, name TEXT) STRICT`); err != nil {
		t.Fatal(err)
	}
	transaction, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := transaction.Prepare(`INSERT INTO probe(id,name) VALUES (?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(7))
	for id := 1; id <= 2048; id++ {
		var name any
		if id%7 != 0 {
			name = fmt.Sprintf("value-%03d-%c", random.Intn(128), 'a'+rune(random.Intn(6)))
		}
		if _, err := statement.Exec(id, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	resolver := newSQLitePolicyTestResolver(t)
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	operators := []ir.OperatorID{
		ir.OperatorEqual, ir.OperatorNotEqual, ir.OperatorIn, ir.OperatorNotIn,
		ir.OperatorLessThan, ir.OperatorLessThanOrEqual, ir.OperatorGreaterThan, ir.OperatorGreaterThanOrEqual,
		ir.OperatorContains, ir.OperatorStartsWith, ir.OperatorEndsWith, ir.OperatorIsNull, ir.OperatorIsNotNull,
	}
	leaf := func(operatorID ir.OperatorID) ir.Condition {
		operand := ir.NoOperand()
		if operatorID == ir.OperatorIn || operatorID == ir.OperatorNotIn {
			values := make([]ir.Value, 1+random.Intn(3))
			for index := range values {
				values[index], err = ir.StringValue(fmt.Sprintf("value-%03d-%c", random.Intn(128), 'a'+rune(random.Intn(6))))
				if err != nil {
					t.Fatal(err)
				}
			}
			operand, err = ir.ManyOperand(values)
		} else if operatorID != ir.OperatorIsNull && operatorID != ir.OperatorIsNotNull {
			value := "value-"
			if operatorID == ir.OperatorContains || operatorID == ir.OperatorStartsWith || operatorID == ir.OperatorEndsWith {
				value = []string{"value-", "-a", "-b", "037"}[random.Intn(4)]
			} else {
				value = fmt.Sprintf("value-%03d-%c", random.Intn(128), 'a'+rune(random.Intn(6)))
			}
			var wrapped ir.Value
			wrapped, err = ir.StringValue(value)
			if err == nil {
				operand, err = ir.OneOperand(wrapped)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.textType, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
		if err != nil {
			t.Fatal(err)
		}
		condition, err := ir.NewScalar(resolver.modelID, resolver.nameID, resolver.textType, operatorID, ir.ComparisonSensitive, operand, requirements)
		if err != nil {
			t.Fatal(err)
		}
		return condition
	}
	compile := func(condition ir.Condition, dialect policysql.Dialect) policysql.Fragment {
		fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: dialect, Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
		if err != nil {
			t.Fatal(err)
		}
		return fragment
	}
	for iteration := 0; iteration < 256; iteration++ {
		left := leaf(operators[random.Intn(len(operators))])
		right := leaf(operators[random.Intn(len(operators))])
		logical := ir.LogicalAnd
		if random.Intn(2) == 1 {
			logical = ir.LogicalOr
		}
		condition, err := ir.NewLogical(resolver.modelID, logical, []ir.Condition{left, right})
		if err != nil {
			t.Fatal(err)
		}
		if random.Intn(2) == 1 {
			condition, err = ir.NewLogical(resolver.modelID, ir.LogicalNot, []ir.Condition{condition})
			if err != nil {
				t.Fatal(err)
			}
		}
		current := compile(condition, NewPolicyDialect())
		legacy := compile(condition, legacySQLitePolicyDialect{})
		var currentIDs, legacyIDs []int
		if err := database.Select(&currentIDs, `SELECT id FROM probe AS "root" WHERE `+current.SQL()+` ORDER BY id`, current.Args()...); err != nil {
			t.Fatal(err)
		}
		if err := database.Select(&legacyIDs, `SELECT id FROM probe AS "root" WHERE `+legacy.SQL()+` ORDER BY id`, legacy.Args()...); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(currentIDs, legacyIDs) {
			t.Fatalf("iteration %d differs: current=%v legacy=%v\ncurrent SQL=%s\nlegacy SQL=%s", iteration, currentIDs, legacyIDs, current.SQL(), legacy.SQL())
		}
		var unknown int
		if err := database.Get(&unknown, `SELECT count(*) FROM probe AS "root" WHERE (`+current.SQL()+`) IS NULL`, current.Args()...); err != nil {
			t.Fatal(err)
		}
		if unknown != 0 {
			t.Fatalf("iteration %d returned %d unknown rows: %s", iteration, unknown, current.SQL())
		}
	}
}

func TestPolicySQLiteSargablePredicatesUseIndexes(t *testing.T) {
	ctx := context.Background()
	database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "sargable-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE probe (id INTEGER PRIMARY KEY, name TEXT) STRICT; CREATE UNIQUE INDEX probe_name ON probe(name)`); err != nil {
		t.Fatal(err)
	}
	resolver := newSQLitePolicyTestResolver(t)
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	stringValue, _ := ir.StringValue("value-001-a")
	stringOperand, _ := ir.OneOperand(stringValue)
	stringRequirements, err := operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.textType, Operand: stringOperand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	stringCondition, _ := ir.NewScalar(resolver.modelID, resolver.nameID, resolver.textType, ir.OperatorEqual, ir.ComparisonSensitive, stringOperand, stringRequirements)
	intValue, _ := ir.SignedValue(ir.ValueInt64, 7)
	intOperand, _ := ir.OneOperand(intValue)
	intRequirements, err := operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.intType, Operand: intOperand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	intCondition, _ := ir.NewScalar(resolver.modelID, resolver.idID, resolver.intType, ir.OperatorEqual, ir.ComparisonSensitive, intOperand, intRequirements)
	for name, condition := range map[string]ir.Condition{"primary_key": intCondition, "nullable_unique": stringCondition} {
		t.Run(name, func(t *testing.T) {
			fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(fragment.SQL(), "CASE WHEN") {
				t.Fatalf("policy predicate is not sargable: %s", fragment.SQL())
			}
			plan := queryPlan(t, database, `SELECT id FROM probe AS "root" WHERE `+fragment.SQL(), fragment.Args())
			if !strings.Contains(plan, "SEARCH") || strings.Contains(plan, "SCAN") {
				t.Fatalf("policy predicate did not use an index: %s\nSQL: %s", plan, fragment.SQL())
			}
		})
	}
}

func pointer(value string) *string { return &value }

func TestPolicySQLiteJSONAndListFunctionsNeverReturnUnknown(t *testing.T) {
	database, _, err := New().Open(context.Background(), filepath.Join(t.TempDir(), "unknown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE probe (list TEXT, doc TEXT) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO probe VALUES (NULL,NULL)`,
		`INSERT INTO probe VALUES ('[]','null')`,
		`INSERT INTO probe VALUES ('[null]','{"x":null}')`,
		`INSERT INTO probe VALUES ('["wrong"]','{"x":1}')`,
		`INSERT INTO probe VALUES ('not json','not json')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	probes := []string{
		`golem_policy_list(list, '[1]', '4:0:0', 101)`,
		`golem_policy_list(list, '1', '4:0:0', 102)`,
		`golem_policy_list(list, '[]', '4:0:0', 103)`,
		`golem_policy_list(list, '[]', '4:0:0', 104)`,
		`golem_policy_list(list, 'true', '4:0:0', 105)`,
		`golem_policy_json(doc, '[["k","x"]]', '1', 203, 1, 2)`,
		`golem_policy_json(doc, '[["k","x"]]', '1', 204, 1, 2)`,
		`golem_policy_json(doc, '[["k","x"]]', '3', 203, 1, 5)`,
	}
	for _, probe := range probes {
		var unknown int
		if err := database.Get(&unknown, `SELECT count(*) FROM probe WHERE (`+probe+`) IS NULL`); err != nil {
			t.Fatal(err)
		}
		if unknown != 0 {
			t.Errorf("unknown count=%d for %s", unknown, probe)
		}
	}
}

func TestPolicyDialectCompilesBoundFragmentsAndExecutesBeforeSelection(t *testing.T) {
	ctx := context.Background()
	database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "compiled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE probe (id INTEGER PRIMARY KEY, name TEXT, tags TEXT, doc TEXT) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO probe VALUES (1,'A%_\Z','["go","db"]','{"count":9007199254740993}')`,
		`INSERT INTO probe VALUES (2,NULL,NULL,NULL)`,
		`INSERT INTO probe VALUES (3,'other','[]','{"count":9007199254740992}')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	resolver := newSQLitePolicyTestResolver(t)
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	textValue, _ := ir.StringValue("%_\\")
	textOperand, _ := ir.OneOperand(textValue)
	textRequirements, _ := operator.ValidateShape(ir.OperatorContains, operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.textType, Operand: textOperand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
	textCondition, _ := ir.NewScalar(resolver.modelID, resolver.nameID, resolver.textType, ir.OperatorContains, ir.ComparisonSensitive, textOperand, textRequirements)

	goValue, _ := ir.StringValue("go")
	dbValue, _ := ir.StringValue("db")
	listValue, _ := ir.NewListValue([]ir.Value{goValue, dbValue})
	listOperand, _ := ir.OneOperand(listValue)
	listRequirements, _ := operator.ValidateShape(ir.OperatorListEqual, operator.Shape{Node: ir.ConditionList, FieldType: resolver.listType, Operand: listOperand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
	listCondition, _ := ir.NewList(resolver.modelID, resolver.tagsID, resolver.listType, ir.OperatorListEqual, listOperand, listRequirements)

	number, _ := ir.NewJSONNumber(false, []byte("9007199254740993"), 0)
	numberJSON, _ := ir.JSONNumberValueOf(number)
	wrapped, _ := ir.NewJSONValue(numberJSON)
	jsonOperand, _ := ir.OneOperand(wrapped)
	key, _ := ir.JSONKeySegment("count")
	path, _ := ir.NewJSONPath(key)
	jsonRequirements, _ := operator.ValidateShape(ir.OperatorJSONEqual, operator.Shape{Node: ir.ConditionJSON, FieldType: resolver.jsonType, Operand: jsonOperand, Mode: ir.ComparisonSensitive, Path: path, Providers: ir.PortableProviders()})
	jsonCondition, _ := ir.NewJSON(resolver.modelID, resolver.docID, resolver.jsonType, ir.OperatorJSONEqual, ir.ComparisonSensitive, path, jsonOperand, jsonRequirements)

	for name, condition := range map[string]ir.Condition{"literal_text": textCondition, "exact_list": listCondition, "exact_json": jsonCondition} {
		t.Run(name, func(t *testing.T) {
			fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(fragment.SQL(), "9007199254740993") || strings.Contains(fragment.SQL(), "%_\\") {
				t.Fatalf("operand leaked into SQL: %s", fragment.SQL())
			}
			var selected []int
			if err := database.Select(&selected, `SELECT id FROM probe AS "root" WHERE `+fragment.SQL()+` ORDER BY id`, fragment.Args()...); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(selected, []int{1}) {
				t.Fatalf("selected=%v SQL=%s args=%#v", selected, fragment.SQL(), fragment.Args())
			}
			var unknown int
			if err := database.Get(&unknown, `SELECT count(*) FROM probe AS "root" WHERE (`+fragment.SQL()+`) IS NULL`, fragment.Args()...); err != nil {
				t.Fatal(err)
			}
			if unknown != 0 {
				t.Fatalf("unknown count=%d", unknown)
			}
		})
	}
}

type sqlitePolicyTestResolver struct {
	modelID                     ir.ModelID
	idID, nameID, tagsID, docID ir.FieldID
	intType, textType           ir.TypeRef
	listType, jsonType          ir.TypeRef
	fingerprint                 [32]byte
}

func newSQLitePolicyTestResolver(t testing.TB) *sqlitePolicyTestResolver {
	t.Helper()
	text, err := ir.NewTypeRef(ir.ValueString, true, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	integer, err := ir.NewTypeRef(ir.ValueInt64, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	element, _ := ir.NewTypeRef(ir.ValueString, false, 0, 0, ir.EnumID{}, nil, 0)
	list, err := ir.NewTypeRef(ir.ValueScalarList, true, 0, 0, ir.EnumID{}, &element, ir.CapabilityScalarListJSON)
	if err != nil {
		t.Fatal(err)
	}
	jsonType, err := ir.NewTypeRef(ir.ValueJSON, true, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &sqlitePolicyTestResolver{modelID: testModelID(1), idID: testFieldID(1), nameID: testFieldID(2), tagsID: testFieldID(3), docID: testFieldID(4), intType: integer, textType: text, listType: list, jsonType: jsonType, fingerprint: [32]byte{9, 8, 7}}
}

func (resolver *sqlitePolicyTestResolver) Providers() ir.ProviderSet   { return ir.PortableProviders() }
func (resolver *sqlitePolicyTestResolver) SchemaFingerprint() [32]byte { return resolver.fingerprint }
func (resolver *sqlitePolicyTestResolver) Model(provider ir.Provider, model ir.ModelID) (policysql.Model, bool) {
	return policysql.Model{ID: model, Namespace: "main", Table: "probe"}, provider == ir.ProviderSQLite && model == resolver.modelID
}
func (resolver *sqlitePolicyTestResolver) Field(provider ir.Provider, model ir.ModelID, field ir.FieldID) (policysql.Field, bool) {
	if provider != ir.ProviderSQLite || model != resolver.modelID {
		return policysql.Field{}, false
	}
	switch field {
	case resolver.idID:
		return policysql.Field{Model: model, ID: field, Column: "id", Type: resolver.intType, Nullable: false}, true
	case resolver.nameID:
		return policysql.Field{Model: model, ID: field, Column: "name", Type: resolver.textType, Nullable: true}, true
	case resolver.tagsID:
		return policysql.Field{Model: model, ID: field, Column: "tags", Type: resolver.listType, Nullable: true}, true
	case resolver.docID:
		return policysql.Field{Model: model, ID: field, Column: "doc", Type: resolver.jsonType, Nullable: true}, true
	default:
		return policysql.Field{}, false
	}
}
func (*sqlitePolicyTestResolver) Relation(ir.ModelID, ir.FieldID, ir.RelationID) (policysql.Relation, bool) {
	return policysql.Relation{}, false
}
func (*sqlitePolicyTestResolver) EnumWire(ir.EnumID, ir.EnumValueID) (string, bool) { return "", false }
func (*sqlitePolicyTestResolver) Capability(_ ir.Provider, capability ir.Capability) bool {
	return capability >= ir.CapabilityBinaryText && capability <= ir.CapabilityRelationCorrelation
}

func testModelID(value byte) (result ir.ModelID) { result[len(result)-1] = value; return }
func testFieldID(value byte) (result ir.FieldID) { result[len(result)-1] = value; return }

var _ policysql.Resolver = (*sqlitePolicyTestResolver)(nil)
